package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// MaxRunGapRows caps how many per-file gap rows a single coverage run
// persists. A pathological run — a repo scanned with the wrong root, so every
// file lands in the `no-indexed-symbol` bucket — would otherwise write one row
// per source file on every ingest, and the store is a cache that has to stay
// cheap to rebuild.
//
// The cap costs nothing in accuracy where it matters: `stmts_unattributed` on
// the run row is the exact total regardless of how the list was capped, and
// the rows that ARE kept are the largest losses. What the cap does cost is
// completeness of the enumeration, which is why the count of dropped files is
// persisted alongside it (see CoverageGaps.Insert).
const MaxRunGapRows = 500

// CoverageGap is one file's unattributed execution within a coverage run —
// one row of `coverage_run_gaps` (schema 0011).
//
// Path is the file as the coverage report named it (import-path-qualified for
// a Go coverprofile, absolute for an istanbul report), not the repo-relative
// atlas path: the point of the row is usually that atlas has no atlas path
// for it.
type CoverageGap struct {
	Path   string `json:"path"`
	Stmts  int    `json:"stmts"`
	Reason string `json:"reason"`
}

// CoverageGaps is the narrow port for `coverage_run_gaps`.
//
// It answers the question a UI, `atlas doctor` and a CI gate all need and
// which nothing could answer before issue #100: not "what does the coverage
// say", but "how much of what ran did atlas manage to see at all". The
// run-level totals live on CoverageRun; this port carries the per-file
// enumeration behind them.
type CoverageGaps interface {
	// Insert replaces a run's persisted gap list with `gaps`, keeping the
	// largest losses when there are more than MaxRunGapRows of them, and
	// returns how many files were dropped.
	//
	// The dropped count is also stamped onto the run's `gaps_truncated`
	// column in the same transaction, so the list and its completeness flag
	// can never disagree — a capped list that reports zero truncation is
	// indistinguishable from a complete one, which is the exact failure mode
	// this table exists to avoid.
	Insert(ctx context.Context, runID int64, gaps []CoverageGap) (int, error)

	// List returns a run's stored gaps, biggest loss first, ties broken by
	// path so the order is stable across reads.
	List(ctx context.Context, runID int64) ([]CoverageGap, error)
}

var _ CoverageGaps = (*coverageGapsStore)(nil)

// CoverageGaps returns the Store's CoverageGaps port.
func (s *Store) CoverageGaps() CoverageGaps {
	return &coverageGapsStore{db: s, q: s.queries()}
}

type coverageGapsStore struct {
	db *Store
	q  *sqlc.Queries
}

// capGaps orders gaps biggest-loss-first (ties by path, so the cut is
// deterministic rather than dependent on the caller's map iteration) and
// returns the rows that fit plus the number dropped.
func capGaps(gaps []CoverageGap) ([]CoverageGap, int) {
	ordered := make([]CoverageGap, len(gaps))
	copy(ordered, gaps)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Stmts != ordered[j].Stmts {
			return ordered[i].Stmts > ordered[j].Stmts
		}
		return ordered[i].Path < ordered[j].Path
	})
	if len(ordered) <= MaxRunGapRows {
		return ordered, 0
	}
	return ordered[:MaxRunGapRows], len(ordered) - MaxRunGapRows
}

func (g *coverageGapsStore) Insert(ctx context.Context, runID int64, gaps []CoverageGap) (int, error) {
	if runID == 0 {
		return 0, fmt.Errorf("coverage gaps insert: run_id required")
	}
	kept, dropped := capGaps(gaps)

	tx, err := g.db.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("coverage gaps insert: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := g.q.WithTx(tx)

	// Clear first: an empty list is a legitimate result (everything was
	// attributed) and must not leave a previous ingest's rows standing.
	if err := qtx.DeleteCoverageRunGaps(ctx, runID); err != nil {
		return 0, fmt.Errorf("coverage gaps insert: clear run %d: %w", runID, err)
	}
	for _, gap := range kept {
		if err := qtx.InsertCoverageRunGap(ctx, sqlc.InsertCoverageRunGapParams{
			RunID:  runID,
			Path:   gap.Path,
			Stmts:  int64(gap.Stmts),
			Reason: gap.Reason,
		}); err != nil {
			return 0, fmt.Errorf("coverage gaps insert (run %d, %s): %w", runID, gap.Path, err)
		}
	}
	if err := qtx.SetCoverageRunGapsTruncated(ctx, sqlc.SetCoverageRunGapsTruncatedParams{
		GapsTruncated: int64(dropped),
		ID:            runID,
	}); err != nil {
		return 0, fmt.Errorf("coverage gaps insert: record truncation for run %d: %w", runID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("coverage gaps insert: commit: %w", err)
	}
	return dropped, nil
}

func (g *coverageGapsStore) List(ctx context.Context, runID int64) ([]CoverageGap, error) {
	rows, err := g.q.ListCoverageRunGaps(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("coverage gaps list run %d: %w", runID, err)
	}
	out := make([]CoverageGap, 0, len(rows))
	for _, r := range rows {
		out = append(out, CoverageGap{Path: r.Path, Stmts: int(r.Stmts), Reason: r.Reason})
	}
	return out, nil
}
