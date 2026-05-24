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
//
// Use this variant on the READ path (List filters etc.) where a collapse
// is benign — the caller is narrowing a search, not persisting a row.
// Use normalizeKindForWrite on the WRITE path so an unknown kind surfaces
// a parser-drift warning instead of silently masking the bug.
func normalizeKind(k shared.SymbolKind) shared.SymbolKind {
	out, _ := normalizeKindCollapsed(k)
	return out
}

// normalizeKindCollapsed returns the normalized kind plus whether the
// input was forced to KindFunc despite being non-empty and non-canonical
// (i.e. a real "I rewrote your kind" event the caller may want to log).
//
// The empty-string case is NOT a collapse — it's the explicit "no kind
// supplied" path and we default to func without warning.
func normalizeKindCollapsed(k shared.SymbolKind) (shared.SymbolKind, bool) {
	if k == "" {
		return shared.KindFunc, false
	}
	if schemaSymbolKinds[k] {
		return k, false
	}
	return shared.KindFunc, true
}

// normalizeKindForWrite is the write-path companion to normalizeKind. When
// the input is an unknown non-empty kind, it logs a Warn record at logger
// before returning the collapsed value so a real parser bug (a kind that
// silently rewrites to "func" forever) is visible in production logs.
//
// `where` is a short call-site tag ("symbols.Insert", "ingest.upsertSymbolTx")
// included in the log record so the operator can trace the source.
func normalizeKindForWrite(ctx context.Context, logger shared.Logger, where string, qn shared.SymbolID, k shared.SymbolKind) shared.SymbolKind {
	out, collapsed := normalizeKindCollapsed(k)
	if collapsed && logger != nil {
		logger.Warn(ctx, "symbol kind collapsed to func — possible parser drift",
			"where", where,
			"qualified_name", string(qn),
			"input_kind", string(k),
			"output_kind", string(out),
		)
	}
	return out
}

// SymbolFilter narrows List queries.
type SymbolFilter struct {
	FilePath string
	Package  string
	BCPath   string
	Kind     shared.SymbolKind
}

// DeadCodeFilter narrows FindDead queries. Every field is opt-in: leaving
// a field at its zero value disables that predicate. The defaults
// (EdgeKind empty → matches every edge kind; ScopeFilter empty → matches
// every meta scope) yield the loosest possible "has any incoming edge"
// check — the CLI layer applies stricter defaults (kind=import, scope=
// module+conditional) so the runtime semantics match the dead-code
// intent.
type DeadCodeFilter struct {
	// EdgeKind narrows the incoming-edge count to a single edge kind
	// (e.g. EdgeKindImport, EdgeKindCall). Empty means "any kind
	// counts" — equivalent to the CLI --kind=all flag.
	EdgeKind EdgeKind

	// ScopeFilter narrows EdgeKindImport edges to the listed scope tags
	// (per migration 0008 / issue #16). Empty disables the predicate
	// entirely (i.e. an import edge with NULL meta still counts). Only
	// meaningful when EdgeKind == EdgeKindImport.
	//
	// Use one of the EdgeMetaImportScope* constants; unknown values are
	// silently dropped so a typo can't accidentally produce a query
	// that matches nothing.
	ScopeFilter []string

	// PathPrefix restricts the candidate set to symbols whose file_path
	// starts with this prefix. Useful for narrowing a dead-code sweep
	// to a single service (e.g. "services/api"). Empty means no
	// filter — every internal symbol is a candidate.
	PathPrefix string

	// IncludeTests, when false, excludes test-file edges from the
	// incoming-edge count. A symbol whose only importer is a test
	// module is considered dead under the production-graph view this
	// default produces. Flip to true when you actually want to know
	// "is this used anywhere, tests included".
	//
	// Test patterns matched (case-sensitive, glob-style):
	//   - test_*.py
	//   - *_test.py
	//   - *_test.go
	//   - tests/...   (path prefix)
	//   - conftest.py
	IncludeTests bool
}

// DeadCodeCandidate is one row of the FindDead result set. It carries
// just enough context for the CLI to render the human-friendly output
// + the JSON envelope, plus the surrogate id so downstream tooling can
// chain into other store ports without a second lookup.
type DeadCodeCandidate struct {
	// Symbol is the candidate symbol that has zero qualifying incoming
	// edges. Carries the file path, kind, and qualified name.
	Symbol SymbolRow `json:"symbol"`

	// IncomingCount is always 0 for rows returned by FindDead — the
	// field is preserved on the type so future evolutions can return
	// "low-incoming" rather than "zero-incoming" without breaking the
	// JSON envelope shape.
	IncomingCount int `json:"incoming_count"`

	// EdgeKind echoes the filter used to compute IncomingCount. Useful
	// when the JSON consumer aggregates candidates across multiple
	// FindDead invocations (e.g. one per kind).
	EdgeKind EdgeKind `json:"edge_kind"`
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

	// FindDead returns the subset of internal symbols whose incoming-edge
	// count (filtered by DeadCodeFilter) is zero. Used by the
	// `atlas codebase dead` CLI verb to surface candidates that no first-
	// party module imports / calls / references.
	//
	// "Internal" excludes the `external:py` stubs the Python scanner emits
	// for unresolved import targets (stdlib, third-party, dynamic
	// imports) — those are never authored in the indexed codebase, so
	// flagging them as dead would be nonsense.
	//
	// The result is intentionally a candidate list, not a verdict.
	// Dynamic dispatch (Python getattr / importlib, Go reflect),
	// entry points, plugin registries, and re-export chains all show up
	// as zero-incoming-edge symbols in static analysis even when they're
	// live at runtime. Callers must treat the output as a triage starting
	// point, not a deletion list. The CLI layer surfaces these caveats
	// alongside the rows.
	FindDead(ctx context.Context, f DeadCodeFilter) ([]DeadCodeCandidate, error)
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

	kind := normalizeKindForWrite(ctx, s.db.Logger(), "symbols.Insert", sym.QualifiedName, sym.Kind)

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

// List delegates to the sqlc-generated ListSymbols query. Each filter
// field is opt-in via empty-string sentinels — the query short-circuits
// the predicate for any arg that's the zero value. See queries/symbols.sql
// for the rationale (TL;DR: sqlc v1.31.1 sqlite engine rejects sqlc.narg
// post-substitution, so we encode the IS-NULL-OR-EQUALS semantics with
// '' instead of NULL).
func (s *symbolsStore) List(ctx context.Context, f SymbolFilter) ([]SymbolRow, error) {
	kind := ""
	if f.Kind != "" {
		kind = string(normalizeKind(f.Kind))
	}
	rows, err := s.q.ListSymbols(ctx, sqlc.ListSymbolsParams{
		FilePath: f.FilePath,
		Package:  f.Package,
		BcPath:   f.BCPath,
		Kind:     kind,
	})
	if err != nil {
		return nil, fmt.Errorf("symbols list: %w", err)
	}
	out := make([]SymbolRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCSymbol(r))
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

// externalPyStubPath mirrors codeindex/py.externalPyStubPath — kept as
// a local sentinel here so the store package doesn't take a reverse
// import on codeindex/py. The two MUST stay in lockstep; the resolver
// owns the contract, the store owns the read-side filter that hides
// these stubs from "internal symbol" queries. A test in the dead-code
// suite locks in the value.
const externalPyStubPath = "external:py"

// IsTestPath reports whether filePath matches the conventional test
// patterns FindDead excludes by default. The same predicate is shared
// between the SQL builder (which encodes it as a chain of NOT LIKE
// clauses) and any caller that wants to apply the filter post-hoc.
//
// Patterns matched:
//   - Bases:  test_*.py, *_test.py, *_test.go, conftest.py
//   - Path segment: any segment named exactly "tests" (so
//     services/api/tests/foo.py and tests/unit/bar.py both match)
//
// The check is intentionally lexical — there's no AST-driven "is this
// file actually a test" signal because pytest / go test both happily
// discover tests by filename convention alone.
func IsTestPath(filePath string) bool {
	if filePath == "" {
		return false
	}
	// Path-segment check first: catches every nesting depth without
	// glob-matching every level.
	for _, seg := range strings.Split(filePath, "/") {
		if seg == "tests" {
			return true
		}
	}
	// Base-name patterns.
	last := filePath
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	if last == "conftest.py" {
		return true
	}
	if strings.HasPrefix(last, "test_") && strings.HasSuffix(last, ".py") {
		return true
	}
	if strings.HasSuffix(last, "_test.py") || strings.HasSuffix(last, "_test.go") {
		return true
	}
	return false
}

// deadCodeBaseSelect is the SELECT list FindDead uses to project a
// SymbolRow. Kept as a const so the column order stays in lockstep
// with scanSymbolRow above — if you reorder one, you MUST reorder
// the other.
const deadCodeBaseSelect = `SELECT s.id, s.qualified_name, s.kind, s.file_path, s.line, s.end_line, s.package, s.bc_path, s.created_at, s.pattern_matches`

// excludeTestPredicates is the NOT-LIKE chain that mirrors IsTestPath
// for the SQL builder. The fragment is appended to a query that has
// already established the table alias `e` for the edges row being
// counted, so test-file edges are dropped from the count rather than
// from the candidate set itself.
//
// SQLite LIKE is case-sensitive by default for ASCII inside the
// pragma-default collation Atlas uses, which matches the lexical
// convention pytest / go test enforce.
const excludeTestPredicates = `
  AND e.file_path NOT LIKE '%/tests/%'
  AND e.file_path NOT LIKE 'tests/%'
  AND e.file_path NOT LIKE '%/conftest.py'
  AND e.file_path != 'conftest.py'
  AND e.file_path NOT LIKE '%/test\_%' ESCAPE '\'
  AND e.file_path NOT LIKE 'test\_%' ESCAPE '\'
  AND e.file_path NOT LIKE '%\_test.py' ESCAPE '\'
  AND e.file_path NOT LIKE '%\_test.go' ESCAPE '\'`

// validImportScopes is the closed set of EdgeKindImport meta values
// FindDead accepts in ScopeFilter. Anything outside this set is silently
// dropped during normalisation — a typo can't accidentally produce a
// query whose IN-clause matches nothing.
var validImportScopes = map[string]struct{}{
	EdgeMetaImportScopeModule:       {},
	EdgeMetaImportScopeFunction:     {},
	EdgeMetaImportScopeConditional:  {},
	EdgeMetaImportScopeTypeChecking: {},
	EdgeMetaImportScopeTryGuard:     {},
}

// normalizeScopeFilter drops unknown entries and de-duplicates the
// remainder while preserving caller order (so the generated SQL is
// deterministic across runs). An empty slice means "no scope
// predicate" — used by callers that intentionally want every import
// edge to count regardless of meta.
func normalizeScopeFilter(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if _, ok := validImportScopes[s]; !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// FindDead returns every internal symbol whose qualifying incoming-edge
// count is zero. The query is built dynamically so the same code path
// serves all four "kind" modes (any / call / import / import+scope)
// without per-mode branching in the caller.
//
// Implementation notes:
//
//   - The candidate set is restricted to symbols whose file_path does
//     NOT start with the externalPyStubPath sentinel — those stubs
//     stand in for stdlib / third-party / dynamic imports and are not
//     authored in the indexed codebase.
//   - The incoming-edge count uses a correlated NOT EXISTS subquery
//     rather than a LEFT JOIN + COUNT(*), because correlated EXISTS
//     short-circuits on the first match and is materially faster on
//     dense graphs (the alternative scans every edge row per symbol).
//   - When IncludeTests is false, the EXISTS subquery's edges row is
//     filtered against the test-path predicates so a test-only
//     importer doesn't keep the symbol alive in the count.
//   - The result is ordered by file_path, line for deterministic
//     downstream diffs (smart commit / snapshot tooling relies on
//     stable iteration).
func (s *symbolsStore) FindDead(ctx context.Context, f DeadCodeFilter) ([]DeadCodeCandidate, error) {
	scopes := normalizeScopeFilter(f.ScopeFilter)

	// Build the EXISTS subquery's edge predicate. Each branch carries
	// its own bound-args so we can keep a single slice in step with
	// the placeholders the SQL emits.
	var (
		edgePredicates []string
		args           []any
	)
	if f.EdgeKind != "" {
		edgePredicates = append(edgePredicates, "e.kind = ?")
		args = append(args, string(f.EdgeKind))
	}
	if f.EdgeKind == EdgeKindImport && len(scopes) > 0 {
		// Use a Go-side comma-join for the IN clause; the inputs come
		// from the closed validImportScopes set so SQL-injection isn't
		// a concern, but we still bind the values rather than splicing
		// them so the prepared-statement plan stays cacheable.
		placeholders := make([]string, len(scopes))
		for i, sc := range scopes {
			placeholders[i] = "?"
			args = append(args, sc)
		}
		edgePredicates = append(edgePredicates,
			"e.edge_meta IN ("+strings.Join(placeholders, ", ")+")")
	}

	existsClause := `SELECT 1 FROM edges e WHERE e.to_symbol_id = s.id`
	for _, p := range edgePredicates {
		existsClause += " AND " + p
	}
	if !f.IncludeTests {
		existsClause += excludeTestPredicates
	}

	// Outer candidate predicate: exclude external stubs + optional
	// path-prefix filter. Bound args are appended AFTER the inner
	// EXISTS args because the EXISTS subquery is lexically inside the
	// outer SELECT but its placeholders are visited first.
	outerPredicates := []string{
		"s.file_path NOT LIKE ?",
	}
	outerArgs := []any{externalPyStubPath + "%"}

	if f.PathPrefix != "" {
		outerPredicates = append(outerPredicates, "s.file_path LIKE ?")
		outerArgs = append(outerArgs, f.PathPrefix+"%")
	}

	query := deadCodeBaseSelect + `
FROM symbols s
WHERE NOT EXISTS (` + existsClause + `)
  AND ` + strings.Join(outerPredicates, "\n  AND ") + `
ORDER BY s.file_path, s.line, s.qualified_name`

	// Final arg order: inner EXISTS placeholders first, then outer.
	finalArgs := make([]any, 0, len(args)+len(outerArgs))
	finalArgs = append(finalArgs, args...)
	finalArgs = append(finalArgs, outerArgs...)

	rows, err := s.db.sqlDB().QueryContext(ctx, query, finalArgs...)
	if err != nil {
		return nil, fmt.Errorf("symbols find-dead: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DeadCodeCandidate
	for rows.Next() {
		row, err := scanSymbolRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, DeadCodeCandidate{
			Symbol:        row,
			IncomingCount: 0,
			EdgeKind:      f.EdgeKind,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("symbols find-dead rows: %w", err)
	}
	return out, nil
}
