package trend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// BackfillNotePrefix tags every point Backfill writes. A backfilled point is
// derived from a coverage run that was ingested before anyone ran
// `atlas trend record`, so it is a slightly coarser measurement than a
// recorded one (see Backfill), and a reader comparing the two must be able to
// tell which is which without guessing.
const BackfillNotePrefix = "backfilled from coverage run"

// SyntheticCommitPrefix names a point whose commit is unknown.
//
// `coverage_runs` has no commit column: a run knows its framework and when it
// finished, and (since #86) an optional free-text run_group that CI is
// encouraged to set to the commit sha. When the group is set, that IS the
// commit and the backfilled point is directly comparable with a recorded one.
// When it is not, the point is keyed by run id under this prefix — visible in
// the series, honest about what it is, and deliberately un-resolvable by
// `--compare-to <git-ref>`, because a git ref it cannot actually be matched
// against must never silently resolve to it.
const SyntheticCommitPrefix = "coverage-run:"

// MaxBackfillGroups bounds how many coverage run groups one Backfill will
// consider, newest first.
//
// Backfill runs on every `atlas trend`, and the "already recorded?" check is
// a query per group. Unbounded, a store with years of CI runs would pay
// thousands of round trips on every read of the series to discover there is
// nothing to do. 200 groups is more history than any trend line is read over,
// and the cap only ever hides the OLDEST derivable points — the ones a
// retention prune would have taken first anyway.
const MaxBackfillGroups = 200

// BackfillResult reports what a Backfill did.
type BackfillResult struct {
	// Added is the number of points written.
	Added int `json:"added"`
	// Skipped is the number of coverage runs whose commit already had a
	// recorded point. Backfill never overwrites one: a point written by
	// `atlas trend record` is the better measurement of the two.
	Skipped int `json:"skipped"`
	// Points are the points written, oldest first.
	Points []store.HistoryPoint `json:"points,omitempty"`
}

// Backfill derives history points from the coverage runs already in the
// store and records the ones the series does not have yet.
//
// It exists because issue #92 asked for a series over tables that already
// hold years of data, and `atlas trend record` only ever writes points from
// the moment a team adds a CI step. Without a backfill, `atlas trend` on a
// store full of coverage runs prints "no history recorded" — a trend command
// that needs a trend before it can say anything is not adoptable.
//
// What a backfilled point is, exactly: the statement-coverage fraction over
// each feature's linked impl symbols, as recorded by that run (or run group),
// with the statement total as the denominator — the same quantity and the
// same unit Collect records. What it is NOT: the audit's coverage component,
// which may widen a feature's surface via call-edge, dynamic or
// package-anchor derivation. Those derivations describe the code as it is
// NOW, and applying today's derivation to a year-old run would date-stamp a
// measurement that was never taken. A backfilled point is therefore the
// narrow, direct-link measurement, tagged as such in its note.
//
// Backfill is idempotent: a commit that already has a point is skipped, never
// replaced.
func Backfill(ctx context.Context, s *store.Store) (BackfillResult, error) {
	if s == nil {
		return BackfillResult{}, fmt.Errorf("trend backfill: store is required")
	}
	runs, err := s.Coverage().ListRuns(ctx, "")
	if err != nil {
		return BackfillResult{}, fmt.Errorf("trend backfill: list coverage runs: %w", err)
	}
	if len(runs) == 0 {
		return BackfillResult{}, nil
	}
	features, err := s.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		return BackfillResult{}, fmt.Errorf("trend backfill: list features: %w", err)
	}
	if len(features) == 0 {
		// Nothing to attribute coverage to. A point over zero features would
		// be an unmeasured row per run, which is noise, not history.
		return BackfillResult{}, nil
	}

	var out BackfillResult
	for _, g := range groupRuns(runs) {
		recorded, err := hasPoint(ctx, s, g.commitSHA)
		if err != nil {
			return BackfillResult{}, err
		}
		if recorded {
			out.Skipped++
			continue
		}
		point, err := backfillPoint(ctx, s, g, features)
		if err != nil {
			return BackfillResult{}, err
		}
		id, err := s.History().Record(ctx, point)
		if err != nil {
			return BackfillResult{}, fmt.Errorf("trend backfill: record %s: %w", g.commitSHA, err)
		}
		point.ID = id
		out.Added++
		out.Points = append(out.Points, point)
	}
	return out, nil
}

// runGroup is one logical measurement: every coverage run sharing a run_group
// (issue #86), or a single ungrouped run.
type runGroup struct {
	commitSHA  string
	group      *string
	runIDs     []int64
	runs       []store.CoverageRun
	measuredAt time.Time
}

// groupRuns folds the run list into logical measurements, oldest first. The
// frontier logic already treats a shared run_group as one measurement — a
// polyglot repo syncs go-cover and istanbul separately and means one number —
// so the series must too, or the same CI build lands as two points that each
// see half the repo.
func groupRuns(runs []store.CoverageRun) []runGroup {
	byKey := map[string]*runGroup{}
	order := make([]string, 0, len(runs))
	for _, r := range runs {
		key := SyntheticCommitPrefix + fmt.Sprint(r.ID)
		var group *string
		if r.RunGroup != nil && *r.RunGroup != "" {
			key = *r.RunGroup
			group = r.RunGroup
		}
		g, ok := byKey[key]
		if !ok {
			g = &runGroup{commitSHA: key, group: group}
			byKey[key] = g
			order = append(order, key)
		}
		g.runIDs = append(g.runIDs, r.ID)
		g.runs = append(g.runs, r)
		if r.FinishedAt.After(g.measuredAt) {
			// The group's timestamp is its LAST finished run: that is when
			// the measurement was complete.
			g.measuredAt = r.FinishedAt.UTC()
		}
	}
	out := make([]runGroup, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].measuredAt.Before(out[j].measuredAt) })
	if len(out) > MaxBackfillGroups {
		// Keep the NEWEST window — the oldest derivable points are the ones a
		// retention prune would have dropped first.
		out = out[len(out)-MaxBackfillGroups:]
	}
	return out
}

// hasPoint reports whether the series already holds a point for a commit.
func hasPoint(ctx context.Context, s *store.Store, commitSHA string) (bool, error) {
	_, err := s.History().Get(ctx, commitSHA)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, shared.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("trend backfill: look up %s: %w", commitSHA, err)
	}
}

// backfillPoint measures one run group against the feature set.
func backfillPoint(
	ctx context.Context,
	s *store.Store,
	g runGroup,
	features []store.Feature,
) (store.HistoryPoint, error) {
	results, err := groupResults(ctx, s, g)
	if err != nil {
		return store.HistoryPoint{}, err
	}
	index := indexStatements(results)

	measurements := make([]FeatureMeasurement, 0, len(features))
	for _, feat := range features {
		wanted, err := scoredSymbolIDs(ctx, s, feat.ID)
		if err != nil {
			return store.HistoryPoint{}, err
		}
		m := FeatureMeasurement{FeatureID: feat.ID, Denominator: int64(len(wanted))}
		if tally := sumStatements(index, wanted); tally.measured() {
			score := 100 * float64(tally.covered) / float64(tally.total)
			m.Score = &score
			m.Denominator = int64(tally.total)
		}
		measurements = append(measurements, m)
	}

	note := fmt.Sprintf("%s %s", BackfillNotePrefix, runIDList(g.runIDs))
	return Assemble(AssembleInput{
		CommitSHA:  g.commitSHA,
		MeasuredAt: g.measuredAt,
		Note:       &note,
		Features:   measurements,
	}), nil
}

// groupResults reads every result belonging to one logical measurement.
func groupResults(ctx context.Context, s *store.Store, g runGroup) ([]store.CoverageResult, error) {
	frontier := store.CoverageFrontier{Group: g.group, Runs: g.runs}
	results, err := s.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		return nil, fmt.Errorf("trend backfill: read results for %s: %w", g.commitSHA, err)
	}
	return results, nil
}

// runIDList renders the run ids behind a backfilled point so the note names
// exactly what the number came from.
func runIDList(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out := ""
	for i, id := range sorted {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprint(id)
	}
	return out
}
