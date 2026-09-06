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
// and, more sharply, the index can move underneath it: when the index
// holds file content NEWER than the coverage run, atlas is scoring code
// that run never executed, so the two halves of the picture are about
// different repos.
//
// "Newer" is measured against indexed content, not against scan time.
// A scan is not evidence that anything changed (see
// newestIndexedContent), and a check that fired every time someone ran
// `atlas scan` would be indistinguishable from noise.
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
	newestContent, err := newestIndexedContent(ctx, env)
	if err != nil {
		return Result{}, err
	}

	// Three-state, never a bool. With nothing indexed there is no signal
	// to compare the frontier against, and rendering that unknown as
	// `false` would report a question doctor never got to ask as a
	// question it answered "no".
	predates := predatesUnknown
	switch {
	case newestContent.IsZero():
		// No hashed file anywhere: nothing to date the index from, so
		// the answer stays unknown.
	case measured.Before(newestContent):
		predates = predatesYes
	default:
		predates = predatesNo
	}

	details := map[string]any{
		"run_ids":        frontier.RunIDs(),
		"measured_at":    measured.UTC().Format(time.RFC3339),
		"age_hours":      age.Hours(),
		"max_age_hours":  env.CoverageMaxAge.Hours(),
		"predates_index": predates,
	}
	if !newestContent.IsZero() {
		details["newest_indexed_content"] = newestContent.UTC().Format(time.RFC3339)
	}
	return coverageFreshnessVerdict(frontier, measured, age, newestContent, predates, env, details), nil
}

// The three states of the "did the index move under this frontier?"
// half of the check. Strings rather than a bool because the third one
// has to be representable in the JSON details.
const (
	predatesYes     = "yes"
	predatesNo      = "no"
	predatesUnknown = "unknown"
)

// coverageFreshnessVerdict scores the two independent halves: how old the
// frontier is, and whether the indexed content moved past it.
func coverageFreshnessVerdict(
	frontier store.CoverageFrontier,
	measured time.Time,
	age time.Duration,
	newestContent time.Time,
	predates string,
	env *Env,
	details map[string]any,
) Result {
	// Both complaints are reported when both hold: they have different
	// remedies in the user's head ("re-run the suite" vs "the code moved"),
	// and collapsing them to the first would hide the sharper one.
	var complaints []string
	if predates == predatesYes {
		complaints = append(complaints, fmt.Sprintf(
			"it was measured %s before the newest file content in the index, so the index "+
				"holds code the coverage run never executed",
			roundDuration(newestContent.Sub(measured))))
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
		}
	}
	if predates == predatesUnknown {
		// The age half passed, but the half this check is named for could
		// not run at all. Reporting that as "ok" would be the same lie as
		// reporting an unread file as matching -- so it is n/a, with the
		// half that DID run stated so the reader knows what was covered.
		return Result{
			Severity: SeverityNotApplicable,
			Finding: fmt.Sprintf(
				"the coverage frontier (%d run(s)) is %s old, but whether the index moved under "+
					"it could not be determined: the store records no file hashes to date its "+
					"indexed content from",
				len(frontier.Runs), roundDuration(age)),
			Remediation: "atlas scan --hash-files",
			Details:     details,
		}
	}
	return Result{
		Severity: SeverityOK,
		Finding: fmt.Sprintf(
			"the coverage frontier (%d run(s)) is %s old and postdates the newest file content in the index",
			len(frontier.Runs), roundDuration(age)),
		Details: details,
	}
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

// newestIndexedContent is the mtime of the newest file content atlas
// holds in its index: max(file_hashes.mtime). Zero when nothing has been
// hashed.
//
// Explicitly NOT max(file_hashes.last_scanned), which is what this used
// to be and what made the signal useless. store.Ingest refreshes
// last_scanned for EVERY file on EVERY scan, unchanged ones included --
// "always, even unchanged files get last_scanned refreshed so the cache
// TTL stays warm" (packages/store/ingest.go, step 5). So the newest
// last_scanned moved whenever anyone ran `atlas scan`, and any frontier
// older than the last scan was accused of describing "code atlas has
// since re-read" even when that scan re-read byte-identical files. The
// check fired on essentially every scan, which is how a check gets muted.
//
// mtime is the file's own recorded modification time, so it moves only
// when the CONTENT the index holds moved. A frontier measured after the
// newest indexed mtime covered every file the index knows about; one
// measured before it did not. That is the claim the finding makes.
//
// The bound is one-sided on purpose. A file whose bytes changed while
// its mtime did not (a restore from an archive, a deliberate touch
// backwards) is invisible here -- index.freshness catches that case by
// re-hashing, and it fails rather than warns, so nothing is lost.
func newestIndexedContent(ctx context.Context, env *Env) (time.Time, error) {
	rows, err := env.Store.FileHashes().List(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("doctor coverage: list file hashes: %w", err)
	}
	var newest time.Time
	for _, r := range rows {
		if r.ModTime.After(newest) {
			newest = r.ModTime
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
