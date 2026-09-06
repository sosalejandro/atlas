package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

// noCoverageIngested is the shared not-applicable Result for the two
// coverage checks. It is not-applicable rather than a failure because a
// repo that has not wired `cov sync` into CI yet is not broken -- but it
// is emphatically not "ok" either: reporting healthy coverage hygiene for
// a store with no coverage in it is the exact confident-but-empty answer
// doctor exists to prevent.
func noCoverageIngested() Result {
	return Result{
		Severity:    SeverityNotApplicable,
		Finding:     "no coverage run has been ingested, so there is nothing to assess",
		Remediation: "atlas cov sync --framework go-cover --input coverage.out",
	}
}

// coverageFreshness asks whether the coverage numbers still describe the
// code in the working tree.
//
// Two ways they stop doing so, and both are silent. The frontier simply
// ages -- a percentage from six weeks ago is about six-week-old code --
// and, more sharply, the index can move underneath it: a scan after the
// last coverage sync means atlas has re-read files the coverage run never
// executed, so the two halves of the picture are about different repos.
type coverageFreshness struct{}

func (coverageFreshness) Name() string { return "coverage.freshness" }

func (coverageFreshness) Examines() string {
	return "how old the current coverage frontier is, and whether the index moved under it"
}

func (c coverageFreshness) Run(ctx context.Context, env *Env) (Result, error) {
	if res, ok := env.requireStore(); !ok {
		return res, nil
	}
	frontier, err := env.Store.Coverage().LatestFrontier(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("doctor coverage: latest frontier: %w", err)
	}
	if frontier.Empty() {
		return noCoverageIngested(), nil
	}

	measured := frontierFinishedAt(frontier)
	age := env.Now().Sub(measured)
	lastScan, err := lastIndexWrite(ctx, env)
	if err != nil {
		return Result{}, err
	}
	predatesIndex := !lastScan.IsZero() && measured.Before(lastScan)

	details := map[string]any{
		"run_ids":        frontier.RunIDs(),
		"measured_at":    measured.UTC().Format(time.RFC3339),
		"age_hours":      age.Hours(),
		"max_age_hours":  env.CoverageMaxAge.Hours(),
		"predates_index": predatesIndex,
	}
	if !lastScan.IsZero() {
		details["last_index_write"] = lastScan.UTC().Format(time.RFC3339)
	}

	// Both complaints are reported when both hold: they have different
	// remedies in the user's head ("re-run the suite" vs "the code moved"),
	// and collapsing them to the first would hide the sharper one.
	var complaints []string
	if predatesIndex {
		complaints = append(complaints, fmt.Sprintf(
			"it was measured %s before the last index write, so it describes code atlas has since re-read",
			roundDuration(lastScan.Sub(measured))))
	}
	if age > env.CoverageMaxAge {
		complaints = append(complaints, fmt.Sprintf("it is %s old", roundDuration(age)))
	}
	if len(complaints) > 0 {
		return Result{
			Severity: SeverityWarn,
			Finding: "the coverage frontier no longer tracks the working tree: " +
				strings.Join(complaints, "; "),
			Remediation: "atlas cov sync --framework go-cover --input coverage.out",
			Details:     details,
		}, nil
	}
	return Result{
		Severity: SeverityOK,
		Finding: fmt.Sprintf("the coverage frontier (%d run(s)) is %s old and postdates the last index write",
			len(frontier.Runs), roundDuration(age)),
		Details: details,
	}, nil
}

// frontierFinishedAt is when the frontier finished measuring: the latest
// finish across its runs. A frontier spanning a polyglot build is only as
// fresh as its youngest member, and taking the oldest would nag about a
// build that in fact just ran.
func frontierFinishedAt(f store.CoverageFrontier) time.Time {
	var newest time.Time
	for _, r := range f.Runs {
		if r.FinishedAt.After(newest) {
			newest = r.FinishedAt
		}
	}
	return newest
}

// lastIndexWrite is the most recent file_hashes.last_scanned, i.e. when
// atlas last re-read the tree. Zero when nothing has been scanned.
func lastIndexWrite(ctx context.Context, env *Env) (time.Time, error) {
	rows, err := env.Store.FileHashes().List(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("doctor coverage: list file hashes: %w", err)
	}
	var newest time.Time
	for _, r := range rows {
		if r.LastScanned.After(newest) {
			newest = r.LastScanned
		}
	}
	return newest, nil
}

// roundDuration renders an age at a granularity a human reads without
// counting digits. Sub-day ages keep their hour; anything older is a
// whole number of days, because "43 days" and "43.7 days" call for the
// same action.
func roundDuration(d time.Duration) string {
	if d < 24*time.Hour {
		return d.Round(time.Hour).String()
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// coverageAttribution measures atlas's blind spot: the share of executed
// statements the ingest could not charge to any symbol.
//
// It matters because the blind spot is invisible in the direction that
// flatters nobody. Unattributed statements are dropped from the numerator
// AND the denominator of every per-feature figure, so a repo whose
// coverprofile paths do not reconcile shows plausible-looking percentages
// computed over a fraction of what actually ran (issues #85 / #100).
type coverageAttribution struct{}

func (coverageAttribution) Name() string { return "coverage.attribution" }

func (coverageAttribution) Examines() string {
	return "the share of executed statements the ingest could not charge to a symbol"
}

func (c coverageAttribution) Run(ctx context.Context, env *Env) (Result, error) {
	if res, ok := env.requireStore(); !ok {
		return res, nil
	}
	frontier, err := env.Store.Coverage().LatestFrontier(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("doctor attribution: latest frontier: %w", err)
	}
	if frontier.Empty() {
		return noCoverageIngested(), nil
	}

	// Pooled across the frontier, not read off the newest run: one CI
	// build's Go and istanbul syncs are one measurement, and scoring only
	// the last one to land would rate the build by whichever framework
	// happened to finish last.
	var attributed, unattributed, filesUnmatched int
	for _, r := range frontier.Runs {
		attributed += r.StmtsAttributed
		unattributed += r.StmtsUnattributed
		filesUnmatched += r.FilesUnmatched
	}
	total := attributed + unattributed
	details := map[string]any{
		"run_ids":            frontier.RunIDs(),
		"recorded":           total > 0,
		"stmts_attributed":   attributed,
		"stmts_unattributed": unattributed,
		"files_unmatched":    filesUnmatched,
	}

	// Zero of zero is not perfect attribution. Pass/fail frameworks carry
	// no statement counts at all, and every run written before schema 0011
	// left these columns at zero, so an all-zero set means "nothing was
	// measured" -- reporting it as 100% attributed would advertise a blind
	// spot as coverage.
	if total == 0 {
		return Result{
			Severity: SeverityNotApplicable,
			Finding: "the frontier's runs recorded no attribution accounting " +
				"(a pass/fail framework, or an ingest predating schema 0011)",
			Remediation: "atlas cov sync --framework go-cover --input coverage.out",
			Details:     details,
		}, nil
	}

	frac := float64(unattributed) / float64(total)
	details["unattributed_fraction"] = frac
	details["warn_above"] = env.UnattributedWarn
	details["fail_above"] = env.UnattributedFail

	finding := fmt.Sprintf(
		"%.1f%% of executed statements (%d of %d) could not be charged to a symbol, "+
			"so every coverage figure atlas reports is understated by that much",
		frac*100, unattributed, total)
	switch {
	case frac >= env.UnattributedFail:
		return Result{
			Severity:    SeverityFail,
			Finding:     finding,
			Remediation: "atlas cov status --gaps",
			Details:     details,
		}, nil
	case frac >= env.UnattributedWarn:
		return Result{
			Severity:    SeverityWarn,
			Finding:     finding,
			Remediation: "atlas cov status --gaps",
			Details:     details,
		}, nil
	default:
		return Result{
			Severity: SeverityOK,
			Finding: fmt.Sprintf("%d of %d executed statements (%.1f%%) were charged to a symbol",
				attributed, total, (1-frac)*100),
			Details: details,
		}, nil
	}
}
