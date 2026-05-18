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

	// PatternMatches is the JSON-encoded []patterns.Match record set
	// produced by codeindex/patterns recognisers (Phase 6f). Nil if the
	// symbol has no recogniser hits — the common case for non-aggregate,
	// non-service symbols.
	//
	// The string form is preserved as-is so this layer does NOT take a
	// build-time dependency on the patterns package. Consumers that need
	// typed access decode with json.Unmarshal([]byte(*p), &out).
	PatternMatches *string `json:"pattern_matches,omitempty"`
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

	// LookupAtPosition resolves a (file, line) pair to the symbol it
	// attaches to. Used by the ingest feature-materialization pass: an
	// annotation parsed at file:line is mapped to the symbol whose
	// declaration line is at or just after the annotation (the standard
	// "doc-comment above the func decl" shape).
	//
	// Returns shared.ErrSymbolNotFound when no symbol exists within the
	// search horizon (default 30 lines forward). The error is the natural
	// "orphan annotation" signal — callers that want to silently ignore
	// orphans should check for this error and skip.
	LookupAtPosition(ctx context.Context, filePath string, line int) (SymbolRow, error)

	// List returns all rows that match the filter, ordered by file_path,line.
	List(ctx context.Context, f SymbolFilter) ([]SymbolRow, error)

	// DeleteByFile removes every symbol declared in filePath. Cascades to
	// edges + feature_symbols. Used by the incremental scanner before
	// re-inserting fresh rows for a changed file.
	DeleteByFile(ctx context.Context, filePath string) error

	// SetPatternMatches replaces the JSON-encoded pattern_matches column
	// for one symbol identified by qualified name. Passing an empty
	// string (or "null") clears any previously-stored matches.
	//
	// Used by codeindex.IndexProject after running patterns.MatchAllFiles
	// to persist recogniser hits alongside symbol metadata. Other consumers
	// (audit/, diagnose/) should treat pattern_matches as READ-ONLY.
	SetPatternMatches(ctx context.Context, qn shared.SymbolID, jsonValue string) error

	// FindByPattern returns every symbol whose pattern_matches column
	// contains a Match record for the given pattern name. Backed by a
	// substring match against the stored JSON — patterns.Match.Pattern is
	// emitted as `"pattern":"<name>"` so the substring `"pattern":"<name>"`
	// uniquely identifies the recogniser kind.
	//
	// Returns an empty slice when no symbol has been recognised under that
	// pattern; never returns an error for "no rows".
	FindByPattern(ctx context.Context, pattern string) ([]SymbolRow, error)
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
		ID:             r.ID,
		QualifiedName:  shared.SymbolID(r.QualifiedName),
		Kind:           shared.SymbolKind(r.Kind),
		FilePath:       r.FilePath,
		Line:           int(r.Line),
		EndLine:        int64PtrToIntPtr(r.EndLine),
		Package:        r.Package,
		BCPath:         r.BcPath,
		CreatedAt:      r.CreatedAt,
		PatternMatches: r.PatternMatches,
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

// defaultPositionLookahead is the line window LookupAtPosition uses when
// searching forward from an annotation to find the symbol it documents.
// Tuned for the realistic upper bound on a doc-comment block in Go/TS
// source — annotations more than ~30 lines from their target are almost
// always orphans on markdown or end-of-file comments.
const defaultPositionLookahead int64 = 30

func (s *symbolsStore) LookupAtPosition(ctx context.Context, filePath string, line int) (SymbolRow, error) {
	if filePath == "" {
		return SymbolRow{}, fmt.Errorf("symbols lookup-at-position: file_path required")
	}
	if line <= 0 {
		return SymbolRow{}, fmt.Errorf("symbols lookup-at-position: line must be > 0")
	}
	row, err := s.q.LookupSymbolAtOrAfterLine(ctx, sqlc.LookupSymbolAtOrAfterLineParams{
		FilePath:     filePath,
		Line:         int64(line),
		MaxLookahead: defaultPositionLookahead,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return SymbolRow{}, shared.ErrSymbolNotFound
	}
	if err != nil {
		return SymbolRow{}, fmt.Errorf("symbols lookup-at-position %q L%d: %w", filePath, line, err)
	}
	return fromSQLCSymbol(row), nil
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

	q := `SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at, pattern_matches FROM symbols`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY file_path, line, qualified_name"

	rows, err := s.db.sqlDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("symbols list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SymbolRow
	for rows.Next() {
		row, err := scanSymbolRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("symbols rows: %w", err)
	}
	return out, nil
}

// scanSymbolRow extracts a SymbolRow from a *sql.Rows positioned at a row
// whose columns match the canonical 10-column SELECT used by List and
// FindByPattern. Centralising it here keeps the two raw-SQL readers in sync.
func scanSymbolRow(rows *sql.Rows) (SymbolRow, error) {
	var (
		id             int64
		qn             string
		kind           string
		filePath       string
		line           int64
		endLine        sql.NullInt64
		pkg            sql.NullString
		bc             sql.NullString
		createdAt      time.Time
		patternMatches sql.NullString
	)
	if err := rows.Scan(&id, &qn, &kind, &filePath, &line, &endLine, &pkg, &bc, &createdAt, &patternMatches); err != nil {
		return SymbolRow{}, fmt.Errorf("symbols scan: %w", err)
	}
	return SymbolRow{
		ID:             id,
		QualifiedName:  shared.SymbolID(qn),
		Kind:           shared.SymbolKind(kind),
		FilePath:       filePath,
		Line:           int(line),
		EndLine:        nullInt64ToIntPtr(endLine),
		Package:        nullStringToPtr(pkg),
		BCPath:         nullStringToPtr(bc),
		CreatedAt:      createdAt,
		PatternMatches: nullStringToPtr(patternMatches),
	}, nil
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

// SetPatternMatches persists the JSON-encoded recogniser hit set for a
// symbol identified by qualified name. Empty input clears the column.
//
// Idempotent: re-running with the same JSON is a no-op at the row level
// (still touches the column, but pattern_matches has no audit columns of
// its own — last writer wins).
func (s *symbolsStore) SetPatternMatches(ctx context.Context, qn shared.SymbolID, jsonValue string) error {
	if qn == "" {
		return fmt.Errorf("symbols set-pattern-matches: qualified_name required")
	}
	var v *string
	if jsonValue != "" {
		// Defensive: treat the literal "null" as "clear" so callers that
		// json.Marshal a nil slice (→ "null") get the natural meaning.
		if jsonValue != "null" {
			v = &jsonValue
		}
	}
	if err := s.q.SetSymbolPatternMatchesByQualifiedName(ctx, sqlc.SetSymbolPatternMatchesByQualifiedNameParams{
		PatternMatches: v,
		QualifiedName:  string(qn),
	}); err != nil {
		return fmt.Errorf("symbols set-pattern-matches %q: %w", qn, err)
	}
	return nil
}

// FindByPattern returns every symbol whose pattern_matches column contains
// a Match record for the given pattern name. Uses a LIKE substring match —
// patterns.Match.Pattern is the only JSON value that contains the
// `"pattern":"<name>"` token, so collisions with other JSON fields are
// impossible by construction.
//
// Returns rows ordered by file_path, line, qualified_name for deterministic
// downstream diffs (audit/, diagnose/ rely on stable iteration).
func (s *symbolsStore) FindByPattern(ctx context.Context, pattern string) ([]SymbolRow, error) {
	if pattern == "" {
		return nil, fmt.Errorf("symbols find-by-pattern: pattern required")
	}
	// The needle includes the JSON quote on both sides so we never match a
	// pattern whose name is a substring of another (e.g. "outbox-append"
	// would never match "outbox-append-extended" if such a name existed).
	needle := `"pattern":"` + pattern + `"`
	q := `SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at, pattern_matches
FROM symbols
WHERE pattern_matches IS NOT NULL AND pattern_matches LIKE ?
ORDER BY file_path, line, qualified_name`

	rows, err := s.db.sqlDB().QueryContext(ctx, q, "%"+needle+"%")
	if err != nil {
		return nil, fmt.Errorf("symbols find-by-pattern %q: %w", pattern, err)
	}
	defer func() { _ = rows.Close() }()

	var out []SymbolRow
	for rows.Next() {
		row, err := scanSymbolRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("symbols find-by-pattern %q rows: %w", pattern, err)
	}
	return out, nil
}
