package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// SymbolRow is one row of the `symbols` table (docs/schema-v1.md §5.4).
//
// It is intentionally NOT shared.Symbol — the SQLite row carries the
// surrogate INTEGER PK plus a few persistence-only columns (end_line,
// bc_path, created_at) that the in-memory shared.Symbol does not need.
// Callers convert between the two via FromSharedSymbol / ToSharedSymbol.
type SymbolRow struct {
	ID            int64             `json:"id"`
	QualifiedName shared.SymbolID   `json:"qualified_name"`
	Kind          shared.SymbolKind `json:"kind"`
	FilePath      string            `json:"file_path"`
	Line          int               `json:"line"`
	EndLine       *int              `json:"end_line,omitempty"`
	Package       *string           `json:"package,omitempty"`
	BCPath        *string           `json:"bc_path,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

// schemaSymbolKinds is the closed set §5.4 accepts. The in-memory
// shared.SymbolKind enum is wider (audit-layer values like "handler",
// "service" etc.); the SQLite write path narrows everything that doesn't
// match to "func" so a CHECK constraint violation never reaches the user.
var schemaSymbolKinds = map[shared.SymbolKind]bool{
	shared.KindType:      true,
	shared.KindFunc:      true,
	shared.KindMethod:    true,
	shared.KindInterface: true,
	shared.KindVar:       true,
	shared.KindConst:     true,
}

// normalizeKind maps a shared.SymbolKind to the closed set the §5.4 CHECK
// constraint accepts. Audit-layer values (handler/service/repository/...)
// collapse to "func" because they're inferences over the call graph, not
// statements about the symbol's syntactic shape.
func normalizeKind(k shared.SymbolKind) shared.SymbolKind {
	if k == "" {
		return shared.KindFunc
	}
	if schemaSymbolKinds[k] {
		return k
	}
	return shared.KindFunc
}

// SymbolFilter narrows List queries.
type SymbolFilter struct {
	FilePath string
	Package  string
	BCPath   string
	Kind     shared.SymbolKind
}

// Symbols is the narrow port for the `symbols` table.
type Symbols interface {
	// Insert upserts the symbol (INSERT OR IGNORE on qualified_name) and
	// returns the row's surrogate id. If the symbol already exists, the
	// existing id is returned.
	Insert(ctx context.Context, sym SymbolRow) (int64, error)

	// FindByQualifiedName returns the SymbolRow for a qualified name, or
	// shared.ErrSymbolNotFound.
	FindByQualifiedName(ctx context.Context, qn shared.SymbolID) (SymbolRow, error)

	// List returns all rows that match the filter, ordered by file_path,line.
	List(ctx context.Context, f SymbolFilter) ([]SymbolRow, error)

	// DeleteByFile removes every symbol declared in filePath. Cascades to
	// edges + feature_symbols. Used by the incremental scanner before
	// re-inserting fresh rows for a changed file.
	DeleteByFile(ctx context.Context, filePath string) error
}

var _ Symbols = (*symbolsStore)(nil)

// Symbols returns the Store's Symbols port.
func (s *Store) Symbols() Symbols { return &symbolsStore{db: s, q: s.queries()} }

type symbolsStore struct {
	db *Store
	q  *sqlc.Queries
}

func fromSQLCSymbol(r sqlc.Symbol) SymbolRow {
	return SymbolRow{
		ID:            r.ID,
		QualifiedName: shared.SymbolID(r.QualifiedName),
		Kind:          shared.SymbolKind(r.Kind),
		FilePath:      r.FilePath,
		Line:          int(r.Line),
		EndLine:       int64PtrToIntPtr(r.EndLine),
		Package:       r.Package,
		BCPath:        r.BcPath,
		CreatedAt:     r.CreatedAt,
	}
}

func (s *symbolsStore) Insert(ctx context.Context, sym SymbolRow) (int64, error) {
	if sym.QualifiedName == "" {
		return 0, fmt.Errorf("symbols insert: qualified_name required")
	}
	if sym.FilePath == "" {
		return 0, fmt.Errorf("symbols insert %q: file_path required", sym.QualifiedName)
	}

	kind := normalizeKind(sym.Kind)

	res, err := s.q.InsertSymbol(ctx, sqlc.InsertSymbolParams{
		QualifiedName: string(sym.QualifiedName),
		Kind:          string(kind),
		FilePath:      sym.FilePath,
		Line:          int64(sym.Line),
		EndLine:       intPtrToInt64Ptr(sym.EndLine),
		Package:       sym.Package,
		BcPath:        sym.BCPath,
	})
	if err != nil {
		return 0, fmt.Errorf("symbols insert %q: %w", sym.QualifiedName, err)
	}
	id, _ := res.LastInsertId()
	if id != 0 {
		return id, nil
	}
	// INSERT OR IGNORE collapsed to a no-op because the row already exists.
	// Look up the surrogate id so callers always get a usable value.
	existing, err := s.q.GetSymbolIDByQualifiedName(ctx, string(sym.QualifiedName))
	if err != nil {
		return 0, fmt.Errorf("symbols insert %q (lookup existing): %w", sym.QualifiedName, err)
	}
	return existing, nil
}

func (s *symbolsStore) FindByQualifiedName(ctx context.Context, qn shared.SymbolID) (SymbolRow, error) {
	row, err := s.q.GetSymbolByQualifiedName(ctx, string(qn))
	if errors.Is(err, sql.ErrNoRows) {
		return SymbolRow{}, shared.ErrSymbolNotFound
	}
	if err != nil {
		return SymbolRow{}, fmt.Errorf("symbols find %q: %w", qn, err)
	}
	return fromSQLCSymbol(row), nil
}

// List uses raw SQL because the filter set is genuinely dynamic
// (file_path, package, bc_path, kind — each optional). sqlc's sqlite engine
// does not express conditional WHERE clauses well; the alternative is a 16x
// query-variant explosion. The raw query stays trivial: one prepared SELECT
// with a per-field placeholder.
func (s *symbolsStore) List(ctx context.Context, f SymbolFilter) ([]SymbolRow, error) {
	var (
		where []string
		args  []any
	)
	if f.FilePath != "" {
		where = append(where, "file_path = ?")
		args = append(args, f.FilePath)
	}
	if f.Package != "" {
		where = append(where, "package = ?")
		args = append(args, f.Package)
	}
	if f.BCPath != "" {
		where = append(where, "bc_path = ?")
		args = append(args, f.BCPath)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(normalizeKind(f.Kind)))
	}

	q := `SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at FROM symbols`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY file_path, line, qualified_name"

	rows, err := s.db.sqlDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("symbols list: %w", err)
	}
	defer rows.Close()

	var out []SymbolRow
	for rows.Next() {
		var (
			id        int64
			qn        string
			kind      string
			filePath  string
			line      int64
			endLine   sql.NullInt64
			pkg       sql.NullString
			bc        sql.NullString
			createdAt time.Time
		)
		if err := rows.Scan(&id, &qn, &kind, &filePath, &line, &endLine, &pkg, &bc, &createdAt); err != nil {
			return nil, fmt.Errorf("symbols scan: %w", err)
		}
		out = append(out, SymbolRow{
			ID:            id,
			QualifiedName: shared.SymbolID(qn),
			Kind:          shared.SymbolKind(kind),
			FilePath:      filePath,
			Line:          int(line),
			EndLine:       nullInt64ToIntPtr(endLine),
			Package:       nullStringToPtr(pkg),
			BCPath:        nullStringToPtr(bc),
			CreatedAt:     createdAt,
		})
	}
	return out, rows.Err()
}

func (s *symbolsStore) DeleteByFile(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fmt.Errorf("symbols delete-by-file: file_path required")
	}
	if err := s.q.DeleteSymbolsByFile(ctx, filePath); err != nil {
		return fmt.Errorf("symbols delete-by-file %q: %w", filePath, err)
	}
	return nil
}
