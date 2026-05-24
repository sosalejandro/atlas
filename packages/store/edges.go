package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// EdgeKind matches the CHECK constraint on `edges.kind`.
type EdgeKind string

const (
	EdgeKindCall      EdgeKind = "call"
	EdgeKindImplement EdgeKind = "implement"
	EdgeKindEmbed     EdgeKind = "embed"
	EdgeKindConstruct EdgeKind = "construct"
	// Python-specific kinds emitted by scanner.py. The schema CHECK
	// constraint was widened in migration 0007 to admit these.
	EdgeKindInheritance EdgeKind = "inheritance"
	EdgeKindDecorator   EdgeKind = "decorator"
	EdgeKindImport      EdgeKind = "import"
)

// IsValidEdgeKind reports whether kind is one of the closed set the
// store accepts. Callers should normalise via NormalizeEdgeKind before
// persistence rather than calling this directly.
func IsValidEdgeKind(kind EdgeKind) bool {
	switch kind {
	case EdgeKindCall, EdgeKindImplement, EdgeKindEmbed, EdgeKindConstruct,
		EdgeKindInheritance, EdgeKindDecorator, EdgeKindImport:
		return true
	}
	return false
}

// NormalizeEdgeKind maps a raw scanner-emitted kind string onto the closed
// EdgeKind enum. Unknown or empty inputs default to EdgeKindCall so
// upstream churn (a future scanner kind we haven't taught the store about
// yet) degrades gracefully rather than rejecting the edge.
func NormalizeEdgeKind(raw string) EdgeKind {
	k := EdgeKind(raw)
	if IsValidEdgeKind(k) {
		return k
	}
	return EdgeKindCall
}

// EdgeMeta canonical values. Today only Python `import` edges populate
// this column (issue #16); the vocabulary is the lexical-scope tag
// scanner.py computes for each import statement.
const (
	EdgeMetaImportScopeModule       = "module"
	EdgeMetaImportScopeFunction     = "function"
	EdgeMetaImportScopeConditional  = "conditional"
	EdgeMetaImportScopeTypeChecking = "type_checking"
	EdgeMetaImportScopeTryGuard     = "try_guard"
)

// IsValidEdgeMeta reports whether meta is an accepted qualifier for
// kind. Empty meta is always valid — the column is NULLable and
// non-import edges leave it unset.
//
// The validation lives in Go (not as a SQLite CHECK constraint) so the
// kind-scoped vocabulary can grow without re-migrating. SQLite CHECK
// constraints aren't ALTERable in place and we don't want to pay the
// table-rebuild cost every time a new scope-tagged edge kind joins the
// schema.
func IsValidEdgeMeta(kind EdgeKind, meta string) bool {
	if meta == "" {
		return true
	}
	if kind == EdgeKindImport {
		switch meta {
		case EdgeMetaImportScopeModule,
			EdgeMetaImportScopeFunction,
			EdgeMetaImportScopeConditional,
			EdgeMetaImportScopeTypeChecking,
			EdgeMetaImportScopeTryGuard:
			return true
		}
	}
	// No other kind has a defined meta vocabulary yet. Reject so a
	// scanner bug surfaces as a validation error rather than silently
	// landing junk in the column.
	return false
}

// NormalizeEdgeMeta sanitises a raw scanner-emitted meta string for the
// given kind. Unknown values become "" (the NULL marker) so an
// evolving scanner can't pollute the column with values the rest of
// the stack doesn't understand.
func NormalizeEdgeMeta(kind EdgeKind, raw string) string {
	if IsValidEdgeMeta(kind, raw) {
		return raw
	}
	return ""
}

// EdgeRow is one row of the `edges` table (docs/schema-v1.md §5.5).
//
// from/to are surrogate INTEGER FKs into symbols. Callers that want to
// work with qualified names use Edges.OutByName / Edges.InByName instead.
//
// Meta is the optional kind-specific qualifier (column edge_meta, added
// in migration 0008). For Python `import` edges this carries the scope
// the import was found in — issue #16. Empty string means no qualifier
// (NULL in SQLite). Callers should pass values that satisfy
// IsValidEdgeMeta(Kind, Meta); the Insert path normalises invalid
// values to "" rather than surfacing an error.
type EdgeRow struct {
	ID        int64     `json:"id"`
	FromID    int64     `json:"from_symbol_id"`
	ToID      int64     `json:"to_symbol_id"`
	Kind      EdgeKind  `json:"kind"`
	FilePath  string    `json:"file_path"`
	Line      int       `json:"line"`
	Meta      string    `json:"edge_meta,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// WalkResult is one node visited by Edges.Walk — produced by the recursive
// CTE in docs/schema-v1.md §7.2. Depth is 1-based (a direct callee of the
// root is depth 1).
type WalkResult struct {
	Depth    int             `json:"depth"`
	FromName shared.SymbolID `json:"from_qualified_name"`
	ToName   shared.SymbolID `json:"to_qualified_name"`
	Path     string          `json:"path"`
}

// ImportEdgeRow is one row of the import-graph projection emitted by
// Edges.ListImportEdges. It joins `edges` to `symbols` for both
// endpoints so every row carries the from-file and to-file paths
// already resolved — exactly the shape the SCC algorithm in
// packages/graph wants when it builds a file-to-file import graph.
//
// Scope is the edge_meta value normalised to one of the canonical
// EdgeMetaImportScope* constants (empty string when the column was
// NULL — older rows from before migration 0008). Line is the per-edge
// line number issue #17 wired through (defaults to 0 for rows that
// pre-date the fix).
//
// Both file paths are repo-relative, the same shape symbols.file_path
// stores. The CLI layer presents them verbatim without further
// normalisation.
type ImportEdgeRow struct {
	FromFile string `json:"from_file"`
	ToFile   string `json:"to_file"`
	Scope    string `json:"scope,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// ImportEdgeFilter narrows the ListImportEdges projection. Today
// callers only ever filter by scope (module / function / conditional
// / type_checking / try_guard / all-of-them); SymbolPrefix is a
// forward-looking knob for the `--scope <prefix>` flag the issue
// reserves but doesn't make load-bearing.
//
// An empty Scopes slice means "any scope, including NULL" — i.e.
// every import edge in the store, the `all` mode the verb's
// --scope-filter=all advertises. To request only module-scoped
// edges (the most common case — real load-time cycles), pass
// []string{EdgeMetaImportScopeModule}.
type ImportEdgeFilter struct {
	Scopes       []string
	SymbolPrefix string
}

// Edges is the narrow port for the `edges` table.
type Edges interface {
	// Insert upserts an edge (INSERT OR IGNORE against the composite
	// unique index). Returns the row's surrogate id on insert, or the
	// existing id when the row already exists.
	Insert(ctx context.Context, e EdgeRow) (int64, error)

	// Out returns every outgoing edge of fromID, ordered by line.
	Out(ctx context.Context, fromID int64) ([]EdgeRow, error)

	// In returns every incoming edge of toID, ordered by line.
	In(ctx context.Context, toID int64) ([]EdgeRow, error)

	// Walk traverses `call`-kind edges starting from fromID up to maxDepth
	// using the recursive CTE in docs/schema-v1.md §7.2. The application
	// layer is responsible for deduping cycles after the walk — the CTE
	// will happily revisit nodes; maxDepth is the only guardrail.
	Walk(ctx context.Context, fromID int64, maxDepth int) ([]WalkResult, error)

	// DeleteByFile removes every edge observed in filePath. Used by the
	// incremental scanner before re-emitting edges for a changed file.
	DeleteByFile(ctx context.Context, filePath string) error

	// ListImportEdges returns every `kind='import'` edge as a flat
	// (from_file, to_file, scope, line) projection, JOINed against
	// the `symbols` table for both endpoints. The result is the raw
	// material packages/graph.FindCycles consumes when looking for
	// circular imports — closes issue atlas-internal #14.
	//
	// Filter.Scopes narrows to a subset of the EdgeMetaImportScope*
	// values; an empty slice returns every import edge regardless of
	// scope (the `--scope-filter=all` mode). Filter.SymbolPrefix
	// narrows to symbols whose qualified_name starts with the given
	// string — useful for scoping the analysis to one package /
	// service in a monorepo. An empty prefix is the no-op default.
	//
	// Rows are ordered by (from_file, to_file, line) so downstream
	// consumers — and snapshot diffs — see stable output across
	// re-runs.
	ListImportEdges(ctx context.Context, f ImportEdgeFilter) ([]ImportEdgeRow, error)
}

var _ Edges = (*edgesStore)(nil)

// Edges returns the Store's Edges port.
func (s *Store) Edges() Edges { return &edgesStore{db: s, q: s.queries()} }

type edgesStore struct {
	db *Store
	q  *sqlc.Queries
}

// fromSQLCEdgeOut maps a generated ListEdgesOutRow into the public
// EdgeRow shape. The generator emits per-query row types (rather
// than re-using sqlc.Edge) because the SELECT column list now
// includes edge_meta and the model-vs-row split is sqlc's default
// for any custom projection. fromSQLCEdgeIn is its sibling for the
// In variant — the row shapes are byte-identical but distinct types
// so they can't unify without sqlc-side gymnastics.
func fromSQLCEdgeOut(r sqlc.ListEdgesOutRow) EdgeRow {
	return EdgeRow{
		ID:        r.ID,
		FromID:    r.FromSymbolID,
		ToID:      r.ToSymbolID,
		Kind:      EdgeKind(r.Kind),
		FilePath:  r.FilePath,
		Line:      int(r.Line),
		Meta:      derefString(r.EdgeMeta),
		CreatedAt: r.CreatedAt,
	}
}

func fromSQLCEdgeIn(r sqlc.ListEdgesInRow) EdgeRow {
	return EdgeRow{
		ID:        r.ID,
		FromID:    r.FromSymbolID,
		ToID:      r.ToSymbolID,
		Kind:      EdgeKind(r.Kind),
		FilePath:  r.FilePath,
		Line:      int(r.Line),
		Meta:      derefString(r.EdgeMeta),
		CreatedAt: r.CreatedAt,
	}
}

// derefString returns the pointed-to string or "" for nil. sqlc emits
// nullable TEXT columns as *string; the EdgeRow API exposes a plain
// string with "" as the NULL marker so callers don't have to guard
// against nil on every read.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// metaParam wraps a meta string for InsertEdgeParams.EdgeMeta (sqlc
// generates *string for NULLable TEXT). Returns nil for "" so the
// column stays NULL on insert — distinguishing "no qualifier" from
// "empty-string qualifier" matters for SQL filters like
// ``WHERE edge_meta IS NOT NULL``.
func metaParam(meta string) *string {
	if meta == "" {
		return nil
	}
	return &meta
}

func (s *edgesStore) Insert(ctx context.Context, e EdgeRow) (int64, error) {
	if e.FromID == 0 || e.ToID == 0 {
		return 0, fmt.Errorf("edges insert: from_symbol_id and to_symbol_id required")
	}
	if e.Kind == "" {
		e.Kind = EdgeKindCall
	}
	if e.FilePath == "" {
		return 0, fmt.Errorf("edges insert: file_path required")
	}
	// Defence-in-depth: a scanner-supplied Meta that doesn't match
	// the kind's allow-list lands as NULL rather than corrupting the
	// column. Tests cover both the happy path (valid scope tags
	// persisted) and the reject path (a fake "garbage" meta dropped).
	meta := NormalizeEdgeMeta(e.Kind, e.Meta)

	res, err := s.q.InsertEdge(ctx, sqlc.InsertEdgeParams{
		FromSymbolID: e.FromID,
		ToSymbolID:   e.ToID,
		Kind:         string(e.Kind),
		FilePath:     e.FilePath,
		Line:         int64(e.Line),
		EdgeMeta:     metaParam(meta),
	})
	if err != nil {
		return 0, fmt.Errorf("edges insert: %w", err)
	}
	id, _ := res.LastInsertId()
	if id != 0 {
		return id, nil
	}
	// Composite unique index already had this edge — look up the existing id.
	existing, err := s.q.GetEdgeID(ctx, sqlc.GetEdgeIDParams{
		FromSymbolID: e.FromID,
		ToSymbolID:   e.ToID,
		Kind:         string(e.Kind),
		FilePath:     e.FilePath,
		Line:         int64(e.Line),
	})
	if err != nil {
		return 0, fmt.Errorf("edges insert (lookup existing): %w", err)
	}
	return existing, nil
}

func (s *edgesStore) Out(ctx context.Context, fromID int64) ([]EdgeRow, error) {
	rows, err := s.q.ListEdgesOut(ctx, fromID)
	if err != nil {
		return nil, fmt.Errorf("edges out: %w", err)
	}
	out := make([]EdgeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCEdgeOut(r))
	}
	return out, nil
}

func (s *edgesStore) In(ctx context.Context, toID int64) ([]EdgeRow, error) {
	rows, err := s.q.ListEdgesIn(ctx, toID)
	if err != nil {
		return nil, fmt.Errorf("edges in: %w", err)
	}
	out := make([]EdgeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCEdgeIn(r))
	}
	return out, nil
}

// traceCallChainSQL is the recursive CTE that walks `call` edges depth-first
// from a root symbol up to maxDepth. It stays as raw SQL because sqlc's
// sqlite engine (as of v1.31.1) drops the column-name binding on
// `WITH RECURSIVE chain(...)` and rejects the recursive arm's references
// to those columns. See packages/store/queries/edges.sql for the note.
//
// We column-bind the qualified names into the CTE itself rather than
// resolving them via correlated subqueries in the outer SELECT. The CTE
// already JOINs `symbols` for the path string, so carrying the names
// forward as columns costs nothing extra. The alternative — two
// `(SELECT qualified_name FROM symbols WHERE id = chain.from_id)`
// subqueries in the final projection — issues a fresh lookup per chain row
// (N+1 against `symbols`), which gets expensive on call graphs with
// thousands of nodes.
const traceCallChainSQL = `
WITH RECURSIVE chain(from_id, to_id, from_name, to_name, depth, path) AS (
  SELECT e.from_symbol_id, e.to_symbol_id,
         s.qualified_name, t.qualified_name,
         1,
         s.qualified_name || ' -> ' || t.qualified_name
  FROM edges e
  JOIN symbols s ON s.id = e.from_symbol_id
  JOIN symbols t ON t.id = e.to_symbol_id
  WHERE e.from_symbol_id = ?
    AND e.kind = 'call'
  UNION ALL
  SELECT c.to_id, e.to_symbol_id,
         c.to_name, t.qualified_name,
         c.depth + 1,
         c.path || ' -> ' || t.qualified_name
  FROM chain c
  JOIN edges  e ON e.from_symbol_id = c.to_id AND e.kind = 'call'
  JOIN symbols t ON t.id = e.to_symbol_id
  WHERE c.depth < ?
)
SELECT depth, from_name, to_name, path
FROM chain ORDER BY depth, path
`

func (s *edgesStore) Walk(ctx context.Context, fromID int64, maxDepth int) ([]WalkResult, error) {
	if maxDepth <= 0 {
		maxDepth = 50 // sane default ceiling; callers can override
	}
	rows, err := s.db.sqlDB().QueryContext(ctx, traceCallChainSQL, fromID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("edges walk: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []WalkResult
	for rows.Next() {
		var w WalkResult
		var fromName, toName string
		if err := rows.Scan(&w.Depth, &fromName, &toName, &w.Path); err != nil {
			return nil, fmt.Errorf("edges walk scan: %w", err)
		}
		w.FromName = shared.SymbolID(fromName)
		w.ToName = shared.SymbolID(toName)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edges walk rows: %w", err)
	}
	return out, nil
}

func (s *edgesStore) DeleteByFile(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fmt.Errorf("edges delete-by-file: file_path required")
	}
	if err := s.q.DeleteEdgesByFile(ctx, filePath); err != nil {
		return fmt.Errorf("edges delete-by-file %q: %w", filePath, err)
	}
	return nil
}

// listImportEdgesBaseSQL is the file-to-file projection of `kind='import'`
// edges. We compose the WHERE clause dynamically to splice in optional
// filters (scope IN (...), qualified_name LIKE prefix%) — sqlc's
// sqlite engine can't bind variable-length IN lists cleanly so the raw-SQL
// path is the path of least resistance, same pattern as edges.Walk and
// symbols.FindByPattern. The JOIN to symbols-as-`from`/`to` resolves the
// surrogate ids to file paths in one round-trip; a correlated subquery
// would issue an N+1 lookup per row.
const listImportEdgesBaseSQL = `
SELECT
  fromsym.file_path AS from_file,
  tosym.file_path   AS to_file,
  e.edge_meta       AS scope,
  e.line            AS line
FROM edges e
JOIN symbols fromsym ON fromsym.id = e.from_symbol_id
JOIN symbols tosym   ON tosym.id   = e.to_symbol_id
WHERE e.kind = 'import'
  AND fromsym.file_path <> ''
  AND tosym.file_path   <> ''`

func (s *edgesStore) ListImportEdges(ctx context.Context, f ImportEdgeFilter) ([]ImportEdgeRow, error) {
	q := listImportEdgesBaseSQL
	args := []any{}

	// Scopes filter: validate each value against the canonical import
	// vocabulary before splicing into the IN clause. Anything outside
	// the allow-list is silently dropped — callers passing junk
	// scopes (e.g. via `--scope-filter foo`) get an empty result set,
	// not a SQL injection. The empty-result outcome is intentional:
	// the CLI surface validates flag values up-front so by the time
	// we're here, any junk represents a genuine programmer error
	// worth surfacing as "no cycles match" rather than swallowing
	// silently to "every cycle".
	if len(f.Scopes) > 0 {
		valid := make([]string, 0, len(f.Scopes))
		for _, scope := range f.Scopes {
			if IsValidEdgeMeta(EdgeKindImport, scope) && scope != "" {
				valid = append(valid, scope)
			}
		}
		if len(valid) == 0 {
			// Caller asked for scope filtering but none of
			// their values were valid — return an empty
			// projection rather than running an unfiltered
			// query.
			return []ImportEdgeRow{}, nil
		}
		placeholders := ""
		for i, v := range valid {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, v)
		}
		q += " AND e.edge_meta IN (" + placeholders + ")"
	}

	if f.SymbolPrefix != "" {
		// LIKE 'prefix%' on qualified_name. We anchor at the start
		// so the prefix is a real path-style scope (e.g.
		// "services.preprocessor") rather than a substring that
		// could match anywhere in a longer name.
		q += " AND (fromsym.qualified_name LIKE ? OR tosym.qualified_name LIKE ?)"
		needle := f.SymbolPrefix + "%"
		args = append(args, needle, needle)
	}

	q += " ORDER BY fromsym.file_path, tosym.file_path, e.line"

	rows, err := s.db.sqlDB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("edges list-import: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ImportEdgeRow
	for rows.Next() {
		var (
			fromFile string
			toFile   string
			scope    *string
			line     int64
		)
		if err := rows.Scan(&fromFile, &toFile, &scope, &line); err != nil {
			return nil, fmt.Errorf("edges list-import scan: %w", err)
		}
		row := ImportEdgeRow{
			FromFile: fromFile,
			ToFile:   toFile,
			Line:     int(line),
		}
		if scope != nil {
			row.Scope = *scope
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edges list-import rows: %w", err)
	}
	return out, nil
}
