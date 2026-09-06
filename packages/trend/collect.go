package trend

import (
	"context"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/store"
)

// Scorer is the slice of packages/audit that Collect needs. audit.Audit
// satisfies it; a test supplies a stub, which is what keeps the "what counts
// as evidence" rules below testable without standing up a coverage frontier.
type Scorer interface {
	ScoreAll(ctx context.Context) ([]audit.FeatureHealth, error)
}

// CollectOptions names the point being measured.
type CollectOptions struct {
	// CommitSHA identifies the point. Required: a history keyed by anything
	// less stable than a commit cannot answer "did THIS PR make it worse".
	CommitSHA string

	// MeasuredAt defaults to now (UTC) when zero.
	MeasuredAt time.Time

	// Note is optional free-form metadata carried on the row (a CI run id,
	// a branch name).
	Note *string
}

// Collect scores the project as it stands and folds the result into the
// history point for one commit. The caller persists it via
// store.History.Record.
//
// Two decisions live here, and both exist to keep the series honest:
//
//   - Evidence. A feature counts as MEASURED only when the audit produced a
//     coverage component for it. The audit deliberately re-normalises its
//     weighted blend over whatever signals are available, so a feature with
//     no coverage run still gets a respectable score out of pattern
//     compliance and contract freshness. Recording that as a point on a
//     coverage trend would make the line jump the day coverage first
//     arrives, with no change to the code — the exact false signal a trend
//     exists to avoid. Such features are recorded with a nil score.
//
//   - Denominator. The per-feature denominator is the count of symbols
//     linked to the feature in `feature_symbols` — the annotated surface,
//     which is the same set `atlas trace feature:<id>` walks. It is NOT the
//     audit's derived impl surface (dynamic / package-anchor / static),
//     because that derivation can change between releases of Atlas itself,
//     and a denominator that moves when the tool is upgraded would flag
//     every comparison across an upgrade as incomparable.
func Collect(ctx context.Context, s *store.Store, sc Scorer, opts CollectOptions) (store.HistoryPoint, error) {
	if s == nil {
		return store.HistoryPoint{}, fmt.Errorf("trend collect: store is required")
	}
	if sc == nil {
		return store.HistoryPoint{}, fmt.Errorf("trend collect: scorer is required")
	}
	if opts.CommitSHA == "" {
		return store.HistoryPoint{}, fmt.Errorf("trend collect: commit sha is required")
	}
	when := opts.MeasuredAt
	if when.IsZero() {
		when = time.Now().UTC()
	}

	healths, err := sc.ScoreAll(ctx)
	if err != nil {
		return store.HistoryPoint{}, fmt.Errorf("trend collect %s: score: %w", short(opts.CommitSHA), err)
	}

	measurements := make([]FeatureMeasurement, 0, len(healths))
	for _, h := range healths {
		denom, err := linkedSurfaceSize(ctx, s, h)
		if err != nil {
			return store.HistoryPoint{}, err
		}
		measurements = append(measurements, FeatureMeasurement{
			FeatureID:   h.FeatureID,
			Score:       coverageScore(h),
			Denominator: denom,
		})
	}

	return Assemble(AssembleInput{
		CommitSHA:  opts.CommitSHA,
		MeasuredAt: when.UTC(),
		Note:       opts.Note,
		Features:   measurements,
	}), nil
}

// coverageScore returns the feature's score when it rests on real coverage
// evidence, and nil otherwise. See Collect's "Evidence" note.
func coverageScore(h audit.FeatureHealth) *float64 {
	if _, ok := h.Components[audit.SignalCoverage]; !ok {
		return nil
	}
	score := h.Score
	return &score
}

func linkedSurfaceSize(ctx context.Context, s *store.Store, h audit.FeatureHealth) (int64, error) {
	links, err := s.FeatureSymbols().ListByFeature(ctx, h.FeatureID)
	if err != nil {
		return 0, fmt.Errorf("trend collect: feature %s surface: %w", h.FeatureID, err)
	}
	return int64(len(links)), nil
}
