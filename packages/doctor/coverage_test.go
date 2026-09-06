package doctor

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

// seedRun inserts one coverage run finishing at finishedAt, carrying the
// attribution accounting the ingest would have written.
func (f *fixture) seedRun(t *testing.T, finishedAt time.Time, attributed, unattributed int) int64 {
	t.Helper()
	id, err := f.store.Coverage().InsertRun(context.Background(), store.CoverageRun{
		Framework:         store.FrameworkGoTest,
		StartedAt:         finishedAt.Add(-time.Minute),
		FinishedAt:        finishedAt,
		StmtsAttributed:   attributed,
		StmtsUnattributed: unattributed,
	})
	if err != nil {
		t.Fatalf("insert coverage run: %v", err)
	}
	return id
}

// The guard against the obvious lie: no coverage has ever been ingested,
// so the check has nothing to examine and must not claim health.
func TestCoverageFreshness_NoRuns_NotApplicable(t *testing.T) {
	f := newFixture(t)

	res := runCheck(t, coverageFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityNotApplicable)
	if res.Remediation == "" {
		t.Error("not-applicable must still tell the user how to make it applicable")
	}
}

func TestCoverageFreshness_RecentRun_OK(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-2*time.Hour), 100, 0)

	res := runCheck(t, coverageFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
}

func TestCoverageFreshness_OldRun_Warns(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-30*24*time.Hour), 100, 0)

	res := runCheck(t, coverageFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if got, _ := res.Details["age_hours"].(float64); got < 700 {
		t.Errorf("age_hours = %v, want ~720", res.Details["age_hours"])
	}
}

// Coverage measured before the last scan describes code atlas has since
// re-read: the two halves of the picture disagree about which repo they
// are about.
func TestCoverageFreshness_RunPredatesLastScan_Warns(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-2*time.Hour), 100, 0)
	f.recordHash(t, "pkg/a.go", "deadbeef", f.now.Add(-time.Minute))

	res := runCheck(t, coverageFreshness{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if res.Details["predates_index"] != true {
		t.Errorf("predates_index = %v, want true", res.Details["predates_index"])
	}
}

func TestCoverageAttribution_NoRuns_NotApplicable(t *testing.T) {
	f := newFixture(t)

	res := runCheck(t, coverageAttribution{}, f.env(t))

	assertSeverity(t, res, SeverityNotApplicable)
}

// A pass/fail framework, or any run written before schema 0011, records
// no accounting at all. Zero-of-zero is not perfect attribution, and
// reporting it as "ok" would advertise a blind spot as coverage.
func TestCoverageAttribution_NoAccountingRecorded_NotApplicable(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-time.Hour), 0, 0)

	res := runCheck(t, coverageAttribution{}, f.env(t))

	assertSeverity(t, res, SeverityNotApplicable)
	if res.Details["recorded"] != false {
		t.Errorf("recorded = %v, want false", res.Details["recorded"])
	}
}

func TestCoverageAttribution_FullyAttributed_OK(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-time.Hour), 1000, 0)

	res := runCheck(t, coverageAttribution{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
}

func TestCoverageAttribution_SmallBlindSpot_Warns(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-time.Hour), 850, 150) // 15%

	res := runCheck(t, coverageAttribution{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
}

func TestCoverageAttribution_LargeBlindSpot_Fails(t *testing.T) {
	f := newFixture(t)
	f.seedRun(t, f.now.Add(-time.Hour), 400, 600) // 60%

	res := runCheck(t, coverageAttribution{}, f.env(t))

	assertSeverity(t, res, SeverityFail)
	if got, _ := res.Details["unattributed_fraction"].(float64); got < 0.59 || got > 0.61 {
		t.Errorf("unattributed_fraction = %v, want 0.6", res.Details["unattributed_fraction"])
	}
	if res.Remediation == "" {
		t.Error("a blind spot must point at the command that enumerates it")
	}
}

// The frontier can span several runs of one CI build; the blind spot is
// the pooled one, not the newest run's.
func TestCoverageAttribution_SumsAcrossTheFrontier(t *testing.T) {
	f := newFixture(t)
	group := "build-42"
	ctx := context.Background()
	for _, pair := range [][2]int{{900, 100}, {100, 900}} {
		if _, err := f.store.Coverage().InsertRun(ctx, store.CoverageRun{
			Framework:         store.FrameworkGoTest,
			StartedAt:         f.now.Add(-time.Hour),
			FinishedAt:        f.now.Add(-time.Hour),
			StmtsAttributed:   pair[0],
			StmtsUnattributed: pair[1],
			RunGroup:          &group,
		}); err != nil {
			t.Fatalf("insert grouped run: %v", err)
		}
	}

	res := runCheck(t, coverageAttribution{}, f.env(t))

	if got := res.Details["stmts_unattributed"]; got != 1000 {
		t.Errorf("stmts_unattributed = %v, want 1000 (both runs pooled)", got)
	}
	assertSeverity(t, res, SeverityFail) // 1000 / 2000
}

func TestCoverageChecks_NoStore_NotApplicable(t *testing.T) {
	f := newFixture(t)
	for _, c := range []Check{coverageFreshness{}, coverageAttribution{}} {
		env := f.env(t)
		env.Store = nil
		res := runCheck(t, c, env)
		if res.Severity != SeverityNotApplicable {
			t.Errorf("%s severity = %q, want n/a", c.Name(), res.Severity)
		}
	}
}
