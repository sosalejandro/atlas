package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// ---------------------------------------------------------------------------
// coverage_history — the measurement series behind `atlas trend` (issue #92).
//
// Every other port here answers "what is true now". This one answers "is it
// getting better or worse", which is the question that makes a coverage tool
// adoptable on a legacy codebase: a team can gate on "this PR made it worse"
// without ever agreeing on an absolute threshold they would never reach.
// ---------------------------------------------------------------------------

// HistoryPoint is one row of `coverage_history` plus its per-feature
// breakdown.
//
// Score is a POINTER on purpose. nil means "coverage evidence was absent at
// this commit", which is a different fact from "the score was zero" — the
// docs-only PR that nobody ran the suite for, or the CI job that died before
// `cov sync`. Callers must skip a nil-score point rather than plot or compare
// it as a zero; doing otherwise draws cliffs that never happened and fails
// innocent builds.
//
// Denominator is the size of the surface the score was computed over. A
// delta between two points with different denominators is not a quality
// signal — deleting a thousand untested lines raises coverage without a
// single new test — so the denominator travels with the score and every
// comparison reports both.
type HistoryPoint struct {
	ID          int64                 `json:"id"`
	CommitSHA   string                `json:"commit_sha"`
	MeasuredAt  time.Time             `json:"measured_at"`
	Score       *float64              `json:"score"`
	Denominator int64                 `json:"denominator"`
	Note        *string               `json:"note,omitempty"`
	Features    []HistoryFeaturePoint `json:"features,omitempty"`
}

// Measured reports whether the point carries a usable score. The whole
// series contract hangs off this predicate, so it is a method rather than a
// `!= nil` check scattered across callers.
func (p HistoryPoint) Measured() bool { return p.Score != nil }

// HistoryFeaturePoint is one feature's slice of a HistoryPoint. Same
// nil-means-unmeasured rule as the parent.
type HistoryFeaturePoint struct {
	FeatureID   shared.FeatureID `json:"feature_id"`
	Score       *float64         `json:"score"`
	Denominator int64            `json:"denominator"`
}

// HistoryFilter narrows a series read. The zero value returns every point.
type HistoryFilter struct {
	// Since drops points measured strictly before this instant. Zero =
	// no lower bound.
	Since time.Time

	// Limit caps the result to the N most recent points (the returned slice
	// is still oldest-first). Zero or negative = no cap.
	Limit int
}

// defaultHistoryListLimit bounds an uncapped List. The series is retention-
// pruned rather than unbounded, but a store that has never been pruned
// should still not fault a decade of CI points into memory to print a table.
const defaultHistoryListLimit = 1000

// History is the narrow port for `coverage_history` +
// `coverage_history_features`.
type History interface {
	// Record writes a point and its per-feature breakdown in one
	// transaction, returning the point's surrogate id. Recording the same
	// commit twice REPLACES the earlier measurement (id and all) rather than
	// appending a second point: a CI retry is a correction, not a second
	// observation. Stale per-feature rows are removed as part of the
	// replacement, so a feature that disappeared does not linger.
	Record(ctx context.Context, p HistoryPoint) (int64, error)

	// Get returns the point for an exact commit sha, or shared.ErrNotFound.
	Get(ctx context.Context, commitSHA string) (HistoryPoint, error)

	// Resolve expands a commit-sha prefix to the single recorded sha it
	// matches. Returns shared.ErrNotFound when nothing matches and an
	// explicit ambiguity error when more than one does — silently picking
	// one would compare against the wrong baseline.
	Resolve(ctx context.Context, prefix string) (string, error)

	// List returns the series oldest-first, each point carrying its
	// per-feature breakdown.
	List(ctx context.Context, f HistoryFilter) ([]HistoryPoint, error)

	// Prune deletes every point measured strictly before `before` and
	// returns how many were removed. The per-feature rows go with them via
	// ON DELETE CASCADE.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

var _ History = (*historyStore)(nil)

// History returns the Store's History port.
func (s *Store) History() History { return &historyStore{db: s, q: s.queries()} }

type historyStore struct {
	db *Store
	q  *sqlc.Queries
}

func fromSQLCHistory(r sqlc.CoverageHistory) HistoryPoint {
	return HistoryPoint{
		ID:          r.ID,
		CommitSHA:   r.CommitSha,
		MeasuredAt:  r.MeasuredAt,
		Score:       r.Score,
		Denominator: r.Denominator,
		Note:        r.Note,
	}
}

func fromSQLCHistoryFeature(r sqlc.CoverageHistoryFeature) HistoryFeaturePoint {
	return HistoryFeaturePoint{
		FeatureID:   shared.FeatureID(r.FeatureID),
		Score:       r.Score,
		Denominator: r.Denominator,
	}
}

func (h *historyStore) Record(ctx context.Context, p HistoryPoint) (int64, error) {
	if p.CommitSHA == "" {
		return 0, fmt.Errorf("history record: commit_sha required")
	}
	if p.MeasuredAt.IsZero() {
		p.MeasuredAt = time.Now().UTC()
	}

	tx, err := h.db.sqlDB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("history record %s: begin: %w", p.CommitSHA, err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := h.q.WithTx(tx)
	if err := qtx.UpsertHistoryPoint(ctx, sqlc.UpsertHistoryPointParams{
		CommitSha:   p.CommitSHA,
		MeasuredAt:  p.MeasuredAt.UTC(),
		Score:       p.Score,
		Denominator: p.Denominator,
		Note:        p.Note,
	}); err != nil {
		return 0, fmt.Errorf("history record %s: upsert point: %w", p.CommitSHA, err)
	}
	id, err := qtx.HistoryPointIDByCommit(ctx, p.CommitSHA)
	if err != nil {
		return 0, fmt.Errorf("history record %s: read back id: %w", p.CommitSHA, err)
	}

	// Replace, don't merge. A re-measurement whose feature set shrank must
	// not leave the vanished features behind at their old scores — that is
	// exactly the stale point a trend reader would misread as "unchanged".
	if err := qtx.DeleteHistoryFeatures(ctx, id); err != nil {
		return 0, fmt.Errorf("history record %s: clear features: %w", p.CommitSHA, err)
	}
	for _, f := range p.Features {
		if f.FeatureID == "" {
			return 0, fmt.Errorf("history record %s: feature point with empty id", p.CommitSHA)
		}
		if err := qtx.InsertHistoryFeature(ctx, sqlc.InsertHistoryFeatureParams{
			HistoryID:   id,
			FeatureID:   string(f.FeatureID),
			Score:       f.Score,
			Denominator: f.Denominator,
		}); err != nil {
			return 0, fmt.Errorf("history record %s: feature %s: %w", p.CommitSHA, f.FeatureID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("history record %s: commit: %w", p.CommitSHA, err)
	}
	return id, nil
}

func (h *historyStore) Get(ctx context.Context, commitSHA string) (HistoryPoint, error) {
	row, err := h.q.GetHistoryPoint(ctx, commitSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return HistoryPoint{}, shared.ErrNotFound
	}
	if err != nil {
		return HistoryPoint{}, fmt.Errorf("history get %s: %w", commitSHA, err)
	}
	point := fromSQLCHistory(row)
	feats, err := h.features(ctx, row.ID)
	if err != nil {
		return HistoryPoint{}, err
	}
	point.Features = feats
	return point, nil
}

// resolveHistoryPrefixSQL is raw rather than sqlc-generated: sqlc's sqlite
// engine cannot infer a type for `length(?)` and emits an `interface{}`
// parameter for it. substr/length is used in preference to LIKE because the
// prefix is user input and LIKE would give `%` and `_` wildcard meaning
// inside it. LIMIT 2 is all that is needed to tell unique from ambiguous.
const resolveHistoryPrefixSQL = `SELECT commit_sha FROM coverage_history ` +
	`WHERE substr(commit_sha, 1, length(?)) = ? ORDER BY commit_sha LIMIT 2`

func (h *historyStore) Resolve(ctx context.Context, prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("history resolve: empty prefix")
	}
	rows, err := h.db.sqlDB().QueryContext(ctx, resolveHistoryPrefixSQL, prefix, prefix)
	if err != nil {
		return "", fmt.Errorf("history resolve %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()

	matches := make([]string, 0, 2)
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return "", fmt.Errorf("history resolve %q: scan: %w", prefix, err)
		}
		matches = append(matches, sha)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("history resolve %q: %w", prefix, err)
	}
	switch len(matches) {
	case 0:
		return "", shared.ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("history resolve %q: ambiguous, matches %s and %s",
			prefix, matches[0], matches[1])
	}
}

func (h *historyStore) List(ctx context.Context, f HistoryFilter) ([]HistoryPoint, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultHistoryListLimit
	}
	rows, err := h.q.ListHistoryPoints(ctx, sqlc.ListHistoryPointsParams{
		MeasuredAt: f.Since.UTC(),
		Limit:      int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("history list: %w", err)
	}

	// The query reads newest-first so a LIMIT keeps the most recent window;
	// a series reads oldest-first, so reverse in place here.
	out := make([]HistoryPoint, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		point := fromSQLCHistory(rows[i])
		feats, err := h.features(ctx, point.ID)
		if err != nil {
			return nil, err
		}
		point.Features = feats
		out = append(out, point)
	}
	return out, nil
}

func (h *historyStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	n, err := h.q.PruneHistoryBefore(ctx, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("history prune before %s: %w", before.Format(time.RFC3339), err)
	}
	return n, nil
}

// features loads one point's per-feature breakdown.
func (h *historyStore) features(ctx context.Context, historyID int64) ([]HistoryFeaturePoint, error) {
	rows, err := h.q.ListHistoryFeatures(ctx, historyID)
	if err != nil {
		return nil, fmt.Errorf("history features for point %d: %w", historyID, err)
	}
	out := make([]HistoryFeaturePoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromSQLCHistoryFeature(r))
	}
	return out, nil
}
