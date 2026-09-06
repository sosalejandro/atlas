package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// ---------------------------------------------------------------------------
// Regression tests for the confirmed defects in `atlas trend` (issue #92).
// ---------------------------------------------------------------------------

// --- Finding 3 -------------------------------------------------------------

// The innocent case (nobody ever measured the baseline) and the suspicious
// case (this commit produced no measurement) are different events. Treating
// them alike lets a PR that breaks measurement outright sail through: delete
// the coverage step, every scope reports "no evidence", gate passes.
func TestTrend_HeadThatLostItsMeasurementFailsTheGate(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: nil, Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111")
	if err == nil {
		t.Fatalf("the commit under test produced no measurement and the gate passed:\n%s", stdout)
	}
	if !strings.Contains(err.Error(), "measurement") {
		t.Errorf("the error did not say the measurement was lost: %v", err)
	}
	if !strings.Contains(stdout, "gate: FAIL") {
		t.Errorf("the report did not print a failing gate:\n%s", stdout)
	}
}

// The mirror case must still pass: failing a build because nobody ever ran
// the suite on the baseline punishes the wrong PR.
func TestTrend_UnmeasuredBaselineStillPasses(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: nil, Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(75), Denominator: 100})

	if _, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111"); err != nil {
		t.Fatalf("an unmeasured baseline failed the gate: %v", err)
	}
}

// --- Finding 4 -------------------------------------------------------------

// `atlas trend --compare-to origin/main` must gate THIS checkout. Taking the
// head side as the most recently RECORDED point means a CI runner sharing a
// store gates whatever another branch wrote last.
func TestTrend_CompareToGatesTheCommitUnderTestNotTheNewestPoint(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 100})
	// The commit under test regressed badly...
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(40), Denominator: 100})
	// ...and then another branch's healthy point landed in the shared store
	// afterwards. It is the newest RECORDED point and is nothing to do with
	// this PR.
	fix.record(t, store.HistoryPoint{CommitSHA: "other333", MeasuredAt: trendDay(2), Score: ptr(80), Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111")
	if err == nil {
		t.Fatalf("the gate passed on another branch's newer point:\n%s", stdout)
	}
	if strings.Contains(stdout, "compare base1111 -> other333") {
		t.Errorf("the head side was the newest recorded point, not the commit under test:\n%s", stdout)
	}
	if !strings.Contains(stdout, "compare base1111 -> head2222") {
		t.Errorf("the report did not name the commit under test as the head side:\n%s", stdout)
	}
}

// A commit that recorded no point of its own must fail loudly. Silently
// gating some other commit's number is worse than not gating, because it
// looks like it worked.
func TestTrend_CompareToFailsWhenTheCommitUnderTestHasNoPoint(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "other333", MeasuredAt: trendDay(2), Score: ptr(80), Denominator: 100})

	_, _, err := runTrendCmd(t, fix, "--head", "unrecorded9", "--compare-to", "base1111")
	if err == nil {
		t.Fatal("gating a commit with no recorded point succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "commit under test") {
		t.Errorf("the error did not explain which side was missing: %v", err)
	}
}

// --- Finding 5 -------------------------------------------------------------

// Only `atlas trend record` writes the series, so a repo that has been
// ingesting coverage for a year prints "no history recorded" on first run.
// The points are derivable from coverage_runs, so derive them.
func TestTrend_BackfillsTheSeriesFromExistingCoverageRuns(t *testing.T) {
	fix := newTrendFixture(t)
	fix.coverageRun(t, "sha-bf01", trendDay(0), 30, 100)

	stdout, _, err := runTrendCmd(t, fix)
	if err != nil {
		t.Fatalf("trend: %v", err)
	}
	if strings.Contains(stdout, "no history recorded") {
		t.Fatalf("a store holding a coverage run still reported an empty series:\n%s", stdout)
	}
	if !strings.Contains(stdout, "sha-bf01") {
		t.Errorf("the backfilled point is not in the series:\n%s", stdout)
	}
	if !strings.Contains(stdout, "30.00") {
		t.Errorf("the backfilled score (30 of 100 statements) is missing:\n%s", stdout)
	}
	// A backfilled point is a coarser measurement than a recorded one; the
	// reader has to be told which they are looking at.
	if !strings.Contains(stdout, "backfilled") {
		t.Errorf("the backfill was not disclosed:\n%s", stdout)
	}
}

// The derivation is opt-out: a team that wants only what `record` wrote must
// be able to say so.
func TestTrend_NoBackfillLeavesTheSeriesAlone(t *testing.T) {
	fix := newTrendFixture(t)
	fix.coverageRun(t, "sha-bf01", trendDay(0), 30, 100)

	stdout, _, err := runTrendCmd(t, fix, "--no-backfill")
	if err != nil {
		t.Fatalf("trend --no-backfill: %v", err)
	}
	if !strings.Contains(stdout, "no history recorded") {
		t.Errorf("--no-backfill still derived points:\n%s", stdout)
	}
}

// --- Finding 6 -------------------------------------------------------------

// A capped read that presents its page as the whole series is undetectable
// from the output. Say so.
func TestTrend_TruncatedSeriesSaysSo(t *testing.T) {
	fix := newTrendFixture(t)
	for i, sha := range []string{"cap0", "cap1", "cap2"} {
		fix.record(t, store.HistoryPoint{
			CommitSHA: sha, MeasuredAt: trendDay(i), Score: ptr(50), Denominator: 10,
		})
	}

	stdout, _, err := runTrendCmd(t, fix, "--limit", "2")
	if err != nil {
		t.Fatalf("trend --limit 2: %v", err)
	}
	if !strings.Contains(stdout, "truncat") {
		t.Errorf("a 2-of-3 window did not disclose the truncation:\n%s", stdout)
	}
	if strings.Contains(stdout, "cap0") {
		t.Errorf("--limit 2 kept the oldest point:\n%s", stdout)
	}

	jsonOut, _, err := runTrendCmd(t, fix, "--limit", "2", "--json")
	if err != nil {
		t.Fatalf("trend --limit 2 --json: %v", err)
	}
	var env struct {
		Result struct {
			Series struct {
				Truncated bool `json:"truncated"`
			} `json:"series"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, jsonOut)
	}
	if !env.Result.Series.Truncated {
		t.Errorf("JSON carried a truncated series without the flag:\n%s", jsonOut)
	}
}

// --- Finding 7 -------------------------------------------------------------

// --since and --limit shape what a human READS. A gate that a display flag
// can quietly narrow is worse than no gate, so pin them the way --feature is
// pinned.
func TestTrend_LimitDoesNotNarrowTheGate(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{
		CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 200,
		Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.a", Score: ptr(80), Denominator: 100},
			{FeatureID: "f.b", Score: ptr(80), Denominator: 100},
		},
	})
	fix.record(t, store.HistoryPoint{
		CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(80), Denominator: 200,
		Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.a", Score: ptr(120), Denominator: 100},
			{FeatureID: "f.b", Score: ptr(40), Denominator: 100},
		},
	})

	// --limit 1 leaves only the head point in the window a human reads. The
	// baseline must still be found, and every feature still evaluated.
	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--limit", "1", "--compare-to", "base1111")
	if err == nil {
		t.Fatalf("--limit 1 narrowed the gate away from a feature that fell 80 -> 40:\n%s", stdout)
	}
	// It has to fail as a REGRESSION, not because the windowed read could no
	// longer find the baseline: an error either way would hide the narrowing.
	if !strings.Contains(err.Error(), "regression gate failed") {
		t.Fatalf("the gate did not evaluate the comparison; it failed with: %v", err)
	}
	if !strings.Contains(stdout, "f.b") {
		t.Errorf("the regressing feature was not named under --limit:\n%s", stdout)
	}
}

func TestTrend_SinceDoesNotNarrowTheGate(t *testing.T) {
	fix := newTrendFixture(t)
	old := time.Now().UTC().AddDate(0, 0, -60)
	recent := time.Now().UTC().Add(-time.Hour)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: old, Score: ptr(80), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: recent, Score: ptr(50), Denominator: 100})

	// The window excludes the baseline entirely; the gate must not.
	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--since", "24h", "--compare-to", "base1111")
	if err == nil {
		t.Fatalf("--since 24h moved the gate off a baseline outside the window:\n%s", stdout)
	}
	// A "cannot find the baseline" error is the failure mode this pins, not
	// an acceptable way to fail: the gate must have found it and judged it.
	if !strings.Contains(err.Error(), "regression gate failed") {
		t.Fatalf("the baseline was looked up in the windowed series; gate failed with: %v", err)
	}
	if !strings.Contains(stdout, "base1111") {
		t.Errorf("the out-of-window baseline was not compared against:\n%s", stdout)
	}
}

// --- Finding 8 -------------------------------------------------------------

// An unknown id renders as a real series of gaps — indistinguishable from a
// feature that exists and has never been measured. A typo must be an error.
func TestTrend_UnknownFeatureIDIsAnError(t *testing.T) {
	fix := newTrendFixture(t)
	fix.feature(t, "f.real")
	fix.record(t, store.HistoryPoint{
		CommitSHA: "aaa1", MeasuredAt: trendDay(0), Score: ptr(70), Denominator: 100,
		Features: []store.HistoryFeaturePoint{{FeatureID: "f.real", Score: ptr(70), Denominator: 100}},
	})

	stdout, _, err := runTrendCmd(t, fix, "--feature", "f.typo")
	if err == nil {
		t.Fatalf("an unknown feature id rendered as a series:\n%s", stdout)
	}
	if !strings.Contains(err.Error(), "f.typo") {
		t.Errorf("the error did not name the unknown id: %v", err)
	}

	// A real id still works.
	if _, _, err := runTrendCmd(t, fix, "--feature", "f.real"); err != nil {
		t.Fatalf("a known feature id was rejected: %v", err)
	}
}

// coverageRun seeds one feature, one linked impl symbol and a coverage run
// carrying statement counts for it, so `atlas trend` has something to
// backfill from.
func (f *trendFixture) coverageRun(t *testing.T, group string, finished time.Time, covered, total int) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Features().Upsert(ctx, store.Feature{
		ID: "f.covered", Title: "f.covered", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	symID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID("f.covered.impl"),
		Kind:          shared.KindFunc,
		FilePath:      "covered.go",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: "f.covered", SymbolID: symID,
		Role: store.RoleImpl, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("link symbol: %v", err)
	}
	if _, err := s.Coverage().InsertRunWithResults(ctx,
		store.CoverageRun{
			Framework:  store.FrameworkGoTest,
			StartedAt:  finished.Add(-time.Minute),
			FinishedAt: finished,
			RunGroup:   &group,
		},
		[]store.CoverageResult{{
			SymbolID:     &symID,
			Status:       store.StatusPass,
			CoveredStmts: covered,
			TotalStmts:   total,
		}}); err != nil {
		t.Fatalf("insert coverage run: %v", err)
	}
}
