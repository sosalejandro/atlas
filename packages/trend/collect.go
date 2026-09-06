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
//   - What is recorded. The recorded score is the audit's COVERAGE COMPONENT,
//     not its overall FeatureHealth.Score. The overall score is a
//     re-normalised blend of statement coverage, decision coverage,
//     annotation freshness, pattern
//     compliance and contract drift; recording that while calling the series
//     a coverage trend would make the gate fire on an annotation going stale,
//     and would let a real coverage drop hide behind another component
//     rising. A feature counts as MEASURED only when the audit produced a
//     coverage component for it; the rest are recorded with a nil score,
//     because the blend jumping the day coverage first arrives is the exact
//     false signal a trend exists to avoid.
//
//   - Denominator. The denominator is in the SAME UNIT as the score, which
//     for the Tier B coverage signal is STATEMENTS: the total statement count
//     the current coverage frontier reports for the feature's linked impl
//     symbols. A denominator counted in symbols cannot see a statement-level
//     deletion, so the "deleting a thousand untested lines raises the number"
//     guard would never fire — which is the whole reason the denominator is
//     recorded. When no statement data exists anywhere for the feature (the
//     gotest pass/fail model, playwright, maestro) the coverage signal is
//     itself a fraction of SYMBOLS, and the denominator falls back to the
//     count of scored (non-test-role) linked symbols so the unit still
//     matches the score.
//
// The surface counted is the feature's linked impl symbols, NOT the audit's
// derived impl surface (dynamic / package-anchor / static): that derivation
// can change between releases of Atlas itself, and a denominator that moves
// on a tool upgrade would flag every comparison across the upgrade as
// incomparable.
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

	// The frontier and its results are read ONCE for the whole point: the
	// statement denominator is a per-feature slice of the same pool, and a
	// query per feature would multiply out across a large repo.
	surface, err := newSurfaceSizer(ctx, s)
	if err != nil {
		return store.HistoryPoint{}, err
	}

	measurements := make([]FeatureMeasurement, 0, len(healths))
	for _, h := range healths {
		denom, err := surface.forFeature(ctx, h.FeatureID)
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

// coverageScore returns the feature's COVERAGE COMPONENT when the audit
// produced one, and nil otherwise. See Collect's "What is recorded" note: the
// blended FeatureHealth.Score is deliberately not what lands in the series.
func coverageScore(h audit.FeatureHealth) *float64 {
	score, ok := h.Components[audit.SignalVerification]
	if !ok {
		return nil
	}
	return &score
}
