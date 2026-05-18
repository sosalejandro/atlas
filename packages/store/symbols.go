package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
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
func (s *Store) Symbols() Symbols { return &symbolsStore{db: s} }

type symbolsStore struct{ db *Store }

const symbolsSelectCols = `id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at`

func scanSymbolRow(row interface{ Scan(...any) error }) (SymbolRow, error) {
	var (
		r       SymbolRow
		endLine sql.NullInt64
		pkg     sql.NullString
		bc      sql.NullString
		kind    string
	)
	if err := row.Scan(&r.ID, &r.QualifiedName, &kind, &r.FilePath, &r.Line, &endLine, &pkg, &bc, &r.CreatedAt); err != nil {
		return SymbolRow{}, err
	}
	r.Kind = shared.SymbolKind(kind)
	r.EndLine = ptrInt(endLine)
	r.Package = ptrString(pkg)
	r.BCPath = ptrString(bc)
	return r, nil
}

func (s *symbolsStore) Insert(ctx context.Context, sym SymbolRow) (int64, error) {
	if sym.QualifiedName == "" {
		return 0, fmt.Errorf("symbols insert: qualified_name required")
	}
	if sym.FilePath == "" {
		return 0, fmt.Errorf("symbols insert %q: file_path required", sym.QualifiedName)
	}

	kind := normalizeKind(sym.Kind)

	res, err := s.db.sqlDB().ExecContext(ctx, `
		INSERT OR IGNORE INTO symbols
		  (qualified_name, kind, file_path, line, end_line, package, bc_path)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		string(sym.QualifiedName), string(kind), sym.FilePath, sym.Line,
		nullInt(sym.EndLine), nullStringPtr(sym.Package), nullStringPtr(sym.BCPath),
	)
	if err != nil {
		return 0, fmt.Errorf("symbols insert %q: %w", sym.QualifiedName, err)
	}
	id, _ := res.LastInsertId()
	if id != 0 {
		return id, nil
	}
	// INSERT OR IGNORE collapsed to a no-op because the row already exists.
	// Look up the surrogate id so callers always get a usable value.
	var existing int64
	err = s.db.sqlDB().QueryRowContext(ctx,
		`SELECT id FROM symbols WHERE qualified_name = ?`, string(sym.QualifiedName)).Scan(&existing)
	if err != nil {
		return 0, fmt.Errorf("symbols insert %q (lookup existing): %w", sym.QualifiedName, err)
	}
	return existing, nil
}

func (s *symbolsStore) FindByQualifiedName(ctx context.Context, qn shared.SymbolID) (SymbolRow, error) {
	row := s.db.sqlDB().QueryRowContext(ctx,
		`SELECT `+symbolsSelectCols+` FROM symbols WHERE qualified_name = ?`, string(qn))
	r, err := scanSymbolRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SymbolRow{}, shared.ErrSymbolNotFound
	}
	if err != nil {
		return SymbolRow{}, fmt.Errorf("symbols find %q: %w", qn, err)
	}
	return r, nil
}

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

	q := `SELECT ` + symbolsSelectCols + ` FROM symbols`
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
		r, err := scanSymbolRow(rows)
		if err != nil {
			return nil, fmt.Errorf("symbols scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *symbolsStore) DeleteByFile(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fmt.Errorf("symbols delete-by-file: file_path required")
	}
	_, err := s.db.sqlDB().ExecContext(ctx, `DELETE FROM symbols WHERE file_path = ?`, filePath)
	if err != nil {
		return fmt.Errorf("symbols delete-by-file %q: %w", filePath, err)
	}
	return nil
}
