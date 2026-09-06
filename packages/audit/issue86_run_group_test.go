package audit

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// covRunBase anchors the seeded runs on a fixed clock so "which run is
// newest" is a property of the test table, never of scheduling.
var covRunBase = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// seedRun writes one coverage run finishing at covRunBase+offset, optionally
// tagged with a run group ("" = ungrouped).
func seedRun(
	t *testing.T,
	s *store.Store,
	framework store.Framework,
	group string,
	offset time.Duration,
	statuses map[int64]store.CoverageStatus,
) int64 {
	t.Helper()
	finished := covRunBase.Add(offset)
	run := store.CoverageRun{Framework: framework, StartedAt: finished, FinishedAt: finished}
	if group != "" {
		run.RunGroup = &group
	}
	results := make([]store.CoverageResult, 0, len(statuses))
	for sid, st := range statuses {
		v := sid
		results = append(results, store.CoverageResult{SymbolID: &v, Status: st})
	}
	id, err := s.Coverage().InsertRunWithResults(context.Background(), run, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults(%s, group=%q): %v", framework, group, err)
	}
	return id
}

// seedGoAndFEFeatures seeds one Go-backed feature and one frontend feature,
// each with a single impl symbol, and returns their symbol ids.
func seedGoAndFEFeatures(t *testing.T, s *store.Store) (goSym, feSym int64) {
	t.Helper()
	goIDs := seedFeature(t, s, seedSpec{
		FeatureID: "billing.charge", Title: "Charge", NumSymbols: 1,
		SymbolFile: "src/contexts/billing/application/services/charge.go",
	})
	feIDs := seedFeature(t, s, seedSpec{
		FeatureID: "billing.checkout-ui", Title: "Checkout UI", NumSymbols: 1,
		SymbolFile: "web/src/features/billing/checkout.ts",
	})
	return goIDs[0], feIDs[0]
}

func coverageOf(t *testing.T, s *store.Store, id shared.FeatureID) (float64, bool) {
	t.Helper()
	got, err := New(s, Options{}).ScoreFeature(context.Background(), id)
	if err != nil {
		t.Fatalf("ScoreFeature %q: %v", id, err)
	}
	v, ok := got.Components[SignalCoverage]
	return v, ok
}

// The #86 bug in one test: a Go sync followed by a frontend sync used to make
// every Go capability score as if the Go suite had never run. Sharing a run
// group makes the two syncs one frontier, so both languages keep their
// coverage no matter which one CI uploaded last.
func TestAudit_Issue86_GroupedRunsKeepBothLanguages(t *testing.T) {
	s := openTestStore(t)
	goSym, feSym := seedGoAndFEFeatures(t, s)

	seedRun(t, s, store.FrameworkGoTest, "ci-abc", time.Minute,
		map[int64]store.CoverageStatus{goSym: store.StatusPass})
	seedRun(t, s, store.FrameworkVitest, "ci-abc", 2*time.Minute,
		map[int64]store.CoverageStatus{feSym: store.StatusPass})

	if cov, ok := coverageOf(t, s, "billing.charge"); !ok || cov != 100 {
		t.Errorf("go feature coverage = %.1f (present=%v), want 100 — the frontend sync erased the Go run", cov, ok)
	}
	if cov, ok := coverageOf(t, s, "billing.checkout-ui"); !ok || cov != 100 {
		t.Errorf("fe feature coverage = %.1f (present=%v), want 100", cov, ok)
	}
}

// Ungrouped stores must behave exactly as they did before #86: the newest run
// alone is the frontier. This is the compatibility guarantee that lets the
// migration ship without re-ingesting anything.
func TestAudit_Issue86_UngroupedFallsBackToNewestRun(t *testing.T) {
	s := openTestStore(t)
	goSym, feSym := seedGoAndFEFeatures(t, s)

	seedRun(t, s, store.FrameworkGoTest, "", time.Minute,
		map[int64]store.CoverageStatus{goSym: store.StatusPass})
	seedRun(t, s, store.FrameworkVitest, "", 2*time.Minute,
		map[int64]store.CoverageStatus{feSym: store.StatusPass})

	if cov, _ := coverageOf(t, s, "billing.charge"); cov != 0 {
		t.Errorf("go feature coverage = %.1f, want 0 — ungrouped runs must not merge", cov)
	}
	if cov, ok := coverageOf(t, s, "billing.checkout-ui"); !ok || cov != 100 {
		t.Errorf("fe feature coverage = %.1f (present=%v), want 100 (newest run)", cov, ok)
	}
}

// A run that arrives ungrouped after a group supersedes it — the frontier is
// decided by the newest run, so an operator who forgets --run-group gets the
// old single-run semantics rather than a silent merge into a stale group.
func TestAudit_Issue86_UngroupedRunSupersedesOlderGroup(t *testing.T) {
	s := openTestStore(t)
	goSym, feSym := seedGoAndFEFeatures(t, s)

	seedRun(t, s, store.FrameworkGoTest, "ci-abc", time.Minute,
		map[int64]store.CoverageStatus{goSym: store.StatusPass})
	seedRun(t, s, store.FrameworkVitest, "", 2*time.Minute,
		map[int64]store.CoverageStatus{feSym: store.StatusPass})

	if cov, _ := coverageOf(t, s, "billing.charge"); cov != 0 {
		t.Errorf("go feature coverage = %.1f, want 0 (later ungrouped run is its own frontier)", cov)
	}
}

// Per-symbol conflicts inside one group resolve toward the higher covered
// fraction: a symbol the Go suite executed stays covered even when another
// framework in the same group reports it as not covered, and the outcome does
// not depend on which run happens to be newer.
func TestAudit_Issue86_ConflictPrefersCoveredResult(t *testing.T) {
	tests := []struct {
		name       string
		goStatus   store.CoverageStatus
		feStatus   store.CoverageStatus
		goOffset   time.Duration
		feOffset   time.Duration
		wantCovPct float64
	}{
		{
			name:     "pass first, uncovered report second",
			goStatus: store.StatusPass, feStatus: store.StatusFail,
			goOffset: time.Minute, feOffset: 2 * time.Minute,
			wantCovPct: 100,
		},
		{
			name:     "uncovered report first, pass second",
			goStatus: store.StatusFail, feStatus: store.StatusPass,
			goOffset: time.Minute, feOffset: 2 * time.Minute,
			wantCovPct: 100,
		},
		{
			name:     "skip never outweighs a pass",
			goStatus: store.StatusPass, feStatus: store.StatusSkip,
			goOffset: 2 * time.Minute, feOffset: time.Minute,
			wantCovPct: 100,
		},
		{
			name:     "nobody covers it",
			goStatus: store.StatusFail, feStatus: store.StatusFail,
			goOffset: time.Minute, feOffset: 2 * time.Minute,
			wantCovPct: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			ids := seedFeature(t, s, seedSpec{
				FeatureID: "billing.charge", Title: "Charge", NumSymbols: 1,
				SymbolFile: "src/contexts/billing/application/services/charge.go",
			})
			sym := ids[0]

			// The group has to stay the newest frontier regardless of which run
			// inside it finished last, so both runs share one group key.
			seedRun(t, s, store.FrameworkGoTest, "ci-abc", tc.goOffset,
				map[int64]store.CoverageStatus{sym: tc.goStatus})
			seedRun(t, s, store.FrameworkVitest, "ci-abc", tc.feOffset,
				map[int64]store.CoverageStatus{sym: tc.feStatus})

			cov, ok := coverageOf(t, s, "billing.charge")
			if !ok {
				t.Fatal("coverage signal absent, want present")
			}
			if cov != tc.wantCovPct {
				t.Errorf("coverage = %.1f, want %.1f", cov, tc.wantCovPct)
			}
		})
	}
}

// ScoreAll resolves the frontier once and shares it across features; that
// shortcut must produce the same numbers as the per-feature path.
func TestAudit_Issue86_ScoreAllUsesSameFrontier(t *testing.T) {
	s := openTestStore(t)
	goSym, feSym := seedGoAndFEFeatures(t, s)

	seedRun(t, s, store.FrameworkGoTest, "ci-abc", time.Minute,
		map[int64]store.CoverageStatus{goSym: store.StatusPass})
	seedRun(t, s, store.FrameworkVitest, "ci-abc", 2*time.Minute,
		map[int64]store.CoverageStatus{feSym: store.StatusPass})

	all, err := New(s, Options{}).ScoreAll(context.Background())
	if err != nil {
		t.Fatalf("ScoreAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(ScoreAll) = %d, want 2", len(all))
	}
	for _, h := range all {
		if h.Components[SignalCoverage] != 100 {
			t.Errorf("%s coverage = %.1f, want 100", h.FeatureID, h.Components[SignalCoverage])
		}
	}
}
