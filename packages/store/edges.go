package store

import (
	"context"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/graph"
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
//
// Tier and Ambiguous are the provenance pair added in migration 0018
// (issue #146). Unlike Meta they are NOT normalised away on a bad
// value: Insert refuses the row. Meta is a nice-to-have qualifier and
// dropping a junk one loses nothing, whereas an edge that reaches the
// table with no stated mechanism is indistinguishable from one a type
// checker vouched for -- which is the exact confusion the column
// exists to end.
type EdgeRow struct {
	ID        int64     `json:"id"`
	FromID    int64     `json:"from_symbol_id"`
	ToID      int64     `json:"to_symbol_id"`
	Kind      EdgeKind  `json:"kind"`
	FilePath  string    `json:"file_path"`
	Line      int       `json:"line"`
	Meta      string    `json:"edge_meta,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Tier is which mechanism resolved this edge. Required on insert;
	// see graph.ResolutionTier for what each value claims.
	//
	// It carries a JSON tag with no omitempty so anything serialising
	// an EdgeRow states the provenance rather than dropping it when it
	// is inconvenient. Note that packages/mcp projects edges into its
	// own `neighbour` shape and does NOT yet forward this, so #103's
	// consumers cannot weigh an answer by it until that projection
	// widens — that is a change to packages/mcp, not to this field.
	Tier graph.ResolutionTier `json:"resolution_tier"`

	// Ambiguous is graph.Edge.Ambiguous, persisted rather than
	// recomputed: the resolver saw more than one candidate and picked.
	// It is orthogonal to Tier -- a name_resolved edge can be
	// ambiguous (two packages declare the short name) and a syntactic
	// one can be unambiguous (one substring matched, still a guess).
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// TierBucket is one cell of the per-language tier histogram: how many
// edges in this language were produced by this mechanism, and how many
// of those the resolver had to choose between candidates for.
//
// This is the shape #87's review reads. Issue #146 replaces "symbol and
// edge counts are unchanged +/- a delta" with a histogram comparison
// precisely because a total can hold steady while the composition rots
// -- which is what a resolver migration does when it goes wrong.
type TierBucket struct {
	// Lang is derived from the edge's file extension, not stored. See
	// tierHistogramSQL for why that derivation is the honest one.
	Lang  string               `json:"lang"`
	Tier  graph.ResolutionTier `json:"resolution_tier"`
	Edges int                  `json:"edges"`

	// Ambiguous counts the subset of Edges the resolver flagged. It is
	// reported beside the tier rather than folded into it because the
	// two answer different questions, and a reader watching a
	// migration wants both.
	Ambiguous int `json:"ambiguous"`
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

	// CallAdjacency returns the entire `call`-kind edge set as an adjacency
	// map (from_symbol_id → []to_symbol_id), loaded in one query. Intended
	// for callers running many in-memory reachability walks (e.g. the
	// audit's per-feature impl-surface derivation) without N round-trips.
	CallAdjacency(ctx context.Context) (map[int64][]int64, error)

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

	// TierHistogram returns the (language, resolution_tier) tally over
	// the whole edge set — the report issue #146 exists to make
	// possible, and the one #87's acceptance criteria read instead of
	// an edge count.
	//
	// Buckets with zero edges are omitted. Order is language
	// ascending, then tier strongest-first, so two runs of the same
	// store — or the same repo before and after a resolver change —
	// produce line-comparable output.
	TierHistogram(ctx context.Context) ([]TierBucket, error)
}

var _ Edges = (*edgesStore)(nil)

// Edges returns the Store's Edges port.
func (s *Store) Edges() Edges { return &edgesStore{db: s, q: s.queries()} }

type edgesStore struct {
	db *Store
	q  *sqlc.Queries
}

// fromSQLCEdge maps a generated sqlc.Edge into the public EdgeRow
// shape.
//
// Out and In share this one mapper because the ListEdges* SELECT lists
// are now in table-declaration order. sqlc emits a per-query row type
// for any projection whose column order differs from the table's, and
// it used to do that here — two byte-identical structs that could not
// unify. Keeping the SELECT in declaration order costs nothing and
// collapses them back onto sqlc.Edge.
func fromSQLCEdge(r sqlc.Edge) EdgeRow {
	return EdgeRow{
		ID:        r.ID,
		FromID:    r.FromSymbolID,
		ToID:      r.ToSymbolID,
		Kind:      EdgeKind(r.Kind),
		FilePath:  r.FilePath,
		Line:      int(r.Line),
		Meta:      derefString(r.EdgeMeta),
		CreatedAt: r.CreatedAt,
		Tier:      graph.ResolutionTier(r.ResolutionTier),
		Ambiguous: r.Ambiguous != 0,
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
// “WHERE edge_meta IS NOT NULL“.
func metaParam(meta string) *string {
	if meta == "" {
		return nil
	}
	return &meta
}

// requireTier is the guard every edge write passes through.
//
// It is the whole point of issue #146 in one function: an edge with no
// stated mechanism must not reach the table, because once it is there
// nothing distinguishes it from one a type checker resolved. The
// database's CHECK enforces the same rule; this exists so the failure
// arrives with the from/to pair in it rather than as a bare
// SQLITE_CONSTRAINT_CHECK, and so it arrives from the Go path the test
// suite actually exercises.
//
// The error names the column so a caller reading a scanner's failure
// can grep straight to the migration that explains why.
func requireTier(fromID, toID int64, tier graph.ResolutionTier) error {
	if graph.IsValidTier(tier) {
		return nil
	}
	if tier == graph.TierUnset {
		return fmt.Errorf(
			"edge %d->%d: resolution_tier is required and was not set; "+
				"the scanner that produced this edge must say which mechanism resolved it "+
				"(one of %v) - see packages/graph/tier.go",
			fromID, toID, graph.AllTiers())
	}
	return fmt.Errorf(
		"edge %d->%d: resolution_tier %q is not one of %v; "+
			"the tier vocabulary is closed on purpose so histograms stay comparable "+
			"across scanners - see packages/graph/tier.go",
		fromID, toID, tier, graph.AllTiers())
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
	// Tier gets no such mercy — see requireTier.
	if err := requireTier(e.FromID, e.ToID, e.Tier); err != nil {
		return 0, fmt.Errorf("edges insert: %w", err)
	}

	res, err := s.q.InsertEdge(ctx, sqlc.InsertEdgeParams{
		FromSymbolID:   e.FromID,
		ToSymbolID:     e.ToID,
		Kind:           string(e.Kind),
		FilePath:       e.FilePath,
		Line:           int64(e.Line),
		EdgeMeta:       metaParam(meta),
		ResolutionTier: string(e.Tier),
		Ambiguous:      boolToInt(e.Ambiguous),
	})
	if err != nil {
		return 0, fmt.Errorf("edges insert: %w", err)
	}
	// RowsAffected, not LastInsertId -- see the note on symbols.Insert. A
	// skipped INSERT OR IGNORE leaves last_insert_rowid pointing at the
	// previous write, so the old `id != 0` guard handed back another EDGE's
	// surrogate id every time a re-scan re-wrote an edge it already had.
	// Found by TestProperty_Edges_EndpointsSurvivePersistence.
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("edges insert: rows affected: %w", err)
	}
	if affected > 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("edges insert: last insert id: %w", err)
		}
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
		out = append(out, fromSQLCEdge(r))
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
		out = append(out, fromSQLCEdge(r))
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

func (s *edgesStore) CallAdjacency(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.sqlDB().QueryContext(ctx,
		`SELECT from_symbol_id, to_symbol_id FROM edges WHERE kind = 'call'`)
	if err != nil {
		return nil, fmt.Errorf("edges call-adjacency: %w", err)
	}
	defer func() { _ = rows.Close() }()
	adj := map[int64][]int64{}
	for rows.Next() {
		var from, to int64
		if err := rows.Scan(&from, &to); err != nil {
			return nil, fmt.Errorf("edges call-adjacency scan: %w", err)
		}
		adj[from] = append(adj[from], to)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edges call-adjacency rows: %w", err)
	}
	return adj, nil
}

// tierHistogramSQL tallies edges by language and resolution tier.
//
// The language is derived from the edge's file extension rather than
// read from a column, because there is no language column: `symbols`
// and `edges` have never carried one, and adding one to serve a
// diagnostic would mean every scanner suddenly owed a second
// classification it does not currently make. The extension is a fact
// about the file, produced by nobody and therefore not something a
// scanner can get wrong — which for a check whose job is to report what
// the scanners did is the right kind of independent.
//
// Files that match nothing bucket as "other" rather than being dropped.
// A tier histogram that quietly omitted a third of the edge set would
// be the same lie by omission the tier column exists to prevent.
//
// The CASE lives here rather than in queries/edges.sql because which
// extension belongs to which scanner is a policy decision that wants
// this comment beside it, and because a bucketing expression in the
// GROUP BY is the shape sqlc's sqlite engine handles least gracefully —
// same reason Walk and ListImportEdges are raw (see their notes).
const tierHistogramSQL = `
SELECT
  CASE
    WHEN file_path LIKE '%.go'  THEN 'go'
    WHEN file_path LIKE '%.ts'  OR file_path LIKE '%.tsx'
      OR file_path LIKE '%.js'  OR file_path LIKE '%.jsx'
      OR file_path LIKE '%.mts' OR file_path LIKE '%.cts'
      OR file_path LIKE '%.mjs' OR file_path LIKE '%.cjs' THEN 'ts'
    WHEN file_path LIKE '%.py'  THEN 'py'
    ELSE 'other'
  END AS lang,
  resolution_tier,
  COUNT(*)       AS edges,
  SUM(ambiguous) AS ambiguous
FROM edges
GROUP BY lang, resolution_tier
ORDER BY lang,
  CASE resolution_tier
    WHEN 'typed'         THEN 0
    WHEN 'name_resolved' THEN 1
    WHEN 'syntactic'     THEN 2
    WHEN 'imported'      THEN 3
    ELSE 4
  END`

func (s *edgesStore) TierHistogram(ctx context.Context) ([]TierBucket, error) {
	rows, err := s.db.sqlDB().QueryContext(ctx, tierHistogramSQL)
	if err != nil {
		return nil, fmt.Errorf("edges tier-histogram: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]TierBucket, 0, 8)
	for rows.Next() {
		var b TierBucket
		var tier string
		if err := rows.Scan(&b.Lang, &tier, &b.Edges, &b.Ambiguous); err != nil {
			return nil, fmt.Errorf("edges tier-histogram scan: %w", err)
		}
		// Not validated against IsValidTier on the way out. The CHECK
		// on the column already closed the vocabulary, and a reader
		// that silently dropped a value it did not recognise would
		// under-report the total — which is exactly the failure mode
		// this histogram exists to expose.
		b.Tier = graph.ResolutionTier(tier)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edges tier-histogram rows: %w", err)
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
