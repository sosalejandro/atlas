package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// EdgeKind matches the CHECK constraint on `edges.kind`.
type EdgeKind string

const (
	EdgeKindCall      EdgeKind = "call"
	EdgeKindImplement EdgeKind = "implement"
	EdgeKindEmbed     EdgeKind = "embed"
	EdgeKindConstruct EdgeKind = "construct"
)

// EdgeRow is one row of the `edges` table (docs/schema-v1.md §5.5).
//
// from/to are surrogate INTEGER FKs into symbols. Callers that want to
// work with qualified names use Edges.OutByName / Edges.InByName instead.
type EdgeRow struct {
	ID         int64     `json:"id"`
	FromID     int64     `json:"from_symbol_id"`
	ToID       int64     `json:"to_symbol_id"`
	Kind       EdgeKind  `json:"kind"`
	FilePath   string    `json:"file_path"`
	Line       int       `json:"line"`
	CreatedAt  time.Time `json:"created_at"`
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
func (s *Store) Edges() Edges { return &edgesStore{db: s} }

type edgesStore struct{ db *Store }

const edgesSelectCols = `id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at`

func scanEdgeRow(row interface{ Scan(...any) error }) (EdgeRow, error) {
	var (
		r    EdgeRow
		kind string
	)
	if err := row.Scan(&r.ID, &r.FromID, &r.ToID, &kind, &r.FilePath, &r.Line, &r.CreatedAt); err != nil {
		return EdgeRow{}, err
	}
	r.Kind = EdgeKind(kind)
	return r, nil
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

	res, err := s.db.sqlDB().ExecContext(ctx, `
		INSERT OR IGNORE INTO edges
		  (from_symbol_id, to_symbol_id, kind, file_path, line)
		VALUES (?, ?, ?, ?, ?)
	`, e.FromID, e.ToID, string(e.Kind), e.FilePath, e.Line)
	if err != nil {
		return 0, fmt.Errorf("edges insert: %w", err)
	}
	id, _ := res.LastInsertId()
	if id != 0 {
		return id, nil
	}
	// Composite unique index already had this edge — look up the existing id.
	var existing int64
	err = s.db.sqlDB().QueryRowContext(ctx, `
		SELECT id FROM edges
		WHERE from_symbol_id = ? AND to_symbol_id = ? AND kind = ?
		  AND file_path = ? AND line = ?
	`, e.FromID, e.ToID, string(e.Kind), e.FilePath, e.Line).Scan(&existing)
	if err != nil {
		return 0, fmt.Errorf("edges insert (lookup existing): %w", err)
	}
	return existing, nil
}

func (s *edgesStore) Out(ctx context.Context, fromID int64) ([]EdgeRow, error) {
	rows, err := s.db.sqlDB().QueryContext(ctx,
		`SELECT `+edgesSelectCols+` FROM edges WHERE from_symbol_id = ? ORDER BY file_path, line`, fromID)
	if err != nil {
		return nil, fmt.Errorf("edges out: %w", err)
	}
	defer rows.Close()
	return collectEdgeRows(rows)
}

func (s *edgesStore) In(ctx context.Context, toID int64) ([]EdgeRow, error) {
	rows, err := s.db.sqlDB().QueryContext(ctx,
		`SELECT `+edgesSelectCols+` FROM edges WHERE to_symbol_id = ? ORDER BY file_path, line`, toID)
	if err != nil {
		return nil, fmt.Errorf("edges in: %w", err)
	}
	defer rows.Close()
	return collectEdgeRows(rows)
}

func collectEdgeRows(rows *sql.Rows) ([]EdgeRow, error) {
	var out []EdgeRow
	for rows.Next() {
		r, err := scanEdgeRow(rows)
		if err != nil {
			return nil, fmt.Errorf("edges scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *edgesStore) Walk(ctx context.Context, fromID int64, maxDepth int) ([]WalkResult, error) {
	if maxDepth <= 0 {
		maxDepth = 50 // sane default ceiling; callers can override
	}
	const q = `
WITH RECURSIVE chain(from_id, to_id, depth, path) AS (
  SELECT e.from_symbol_id, e.to_symbol_id, 1,
         s.qualified_name || ' -> ' || t.qualified_name
  FROM edges e
  JOIN symbols s ON s.id = e.from_symbol_id
  JOIN symbols t ON t.id = e.to_symbol_id
  WHERE e.from_symbol_id = ?
    AND e.kind = 'call'
  UNION ALL
  SELECT c.to_id, e.to_symbol_id, c.depth + 1,
         c.path || ' -> ' || t.qualified_name
  FROM chain c
  JOIN edges  e ON e.from_symbol_id = c.to_id AND e.kind = 'call'
  JOIN symbols t ON t.id = e.to_symbol_id
  WHERE c.depth < ?
)
SELECT depth,
       (SELECT qualified_name FROM symbols WHERE id = chain.from_id),
       (SELECT qualified_name FROM symbols WHERE id = chain.to_id),
       path
FROM chain ORDER BY depth, path
`
	rows, err := s.db.sqlDB().QueryContext(ctx, q, fromID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("edges walk: %w", err)
	}
	defer rows.Close()

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
	return out, rows.Err()
}

func (s *edgesStore) DeleteByFile(ctx context.Context, filePath string) error {
	if filePath == "" {
		return fmt.Errorf("edges delete-by-file: file_path required")
	}
	_, err := s.db.sqlDB().ExecContext(ctx, `DELETE FROM edges WHERE file_path = ?`, filePath)
	if err != nil {
		return fmt.Errorf("edges delete-by-file %q: %w", filePath, err)
	}
	return nil
}
