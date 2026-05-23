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

// EdgeRow is one row of the `edges` table (docs/schema-v1.md §5.5).
//
// from/to are surrogate INTEGER FKs into symbols. Callers that want to
// work with qualified names use Edges.OutByName / Edges.InByName instead.
type EdgeRow struct {
	ID        int64     `json:"id"`
	FromID    int64     `json:"from_symbol_id"`
	ToID      int64     `json:"to_symbol_id"`
	Kind      EdgeKind  `json:"kind"`
	FilePath  string    `json:"file_path"`
	Line      int       `json:"line"`
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
}

var _ Edges = (*edgesStore)(nil)

// Edges returns the Store's Edges port.
func (s *Store) Edges() Edges { return &edgesStore{db: s, q: s.queries()} }

type edgesStore struct {
	db *Store
	q  *sqlc.Queries
}

func fromSQLCEdge(r sqlc.Edge) EdgeRow {
	return EdgeRow{
		ID:        r.ID,
		FromID:    r.FromSymbolID,
		ToID:      r.ToSymbolID,
		Kind:      EdgeKind(r.Kind),
		FilePath:  r.FilePath,
		Line:      int(r.Line),
		CreatedAt: r.CreatedAt,
	}
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

	res, err := s.q.InsertEdge(ctx, sqlc.InsertEdgeParams{
		FromSymbolID: e.FromID,
		ToSymbolID:   e.ToID,
		Kind:         string(e.Kind),
		FilePath:     e.FilePath,
		Line:         int64(e.Line),
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

func (s *edgesStore) DeleteByFile(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fmt.Errorf("edges delete-by-file: file_path required")
	}
	if err := s.q.DeleteEdgesByFile(ctx, filePath); err != nil {
		return fmt.Errorf("edges delete-by-file %q: %w", filePath, err)
	}
	return nil
}
