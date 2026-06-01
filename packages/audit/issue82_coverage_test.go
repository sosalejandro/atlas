package audit

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// seedTestRoleFeature links a feature to a single TEST-role symbol (the
// go-test reality: the annotation binds the test function; no impl symbol
// resolves). Returns the test symbol id.
func seedTestRoleFeature(t *testing.T, s *store.Store, fid shared.FeatureID, qn shared.SymbolID) int64 {
	t.Helper()
	ctx := context.Background()
	if err := s.Features().Upsert(ctx, store.Feature{ID: fid, Title: string(fid), Kind: store.FeatureKindFeature}); err != nil {
		t.Fatal(err)
	}
	sid, err := s.Symbols().Insert(ctx, store.SymbolRow{QualifiedName: qn, Kind: shared.KindFunc, FilePath: "nt/x_test.go", Line: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: fid, SymbolID: sid, Role: store.RoleTest, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatal(err)
	}
	return sid
}

// TestCoverageSignal_Issue82_TestRolePassCreditsFeature: a passing coverage
// result keyed to the feature's TEST symbol must credit the feature (score
// 100), even though no impl symbol is linked. This is the go-test bridge.
func TestCoverageSignal_Issue82_TestRolePassCreditsFeature(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tsid := seedTestRoleFeature(t, s, "meals.week-summary", "services.TestWeekSummary")
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{tsid: store.StatusPass})

	got, err := New(s, Options{}).ScoreFeature(ctx, "meals.week-summary")
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Components[SignalCoverage]; v != 100 {
		t.Errorf("coverage = %.1f, want 100 (passing test-role symbol must credit feature, #82)", v)
	}
}

// TestCoverageSignal_Issue82_TestRoleFailScoresZero: a failing annotated test
// (test-role symbol, only link) must surface 0% coverage, not "no signal".
func TestCoverageSignal_Issue82_TestRoleFailScoresZero(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tsid := seedTestRoleFeature(t, s, "meals.week-summary", "services.TestWeekSummary")
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{tsid: store.StatusFail})

	got, err := New(s, Options{}).ScoreFeature(ctx, "meals.week-summary")
	if err != nil {
		t.Fatal(err)
	}
	v, ok := got.Components[SignalCoverage]
	if !ok || v != 0 {
		t.Errorf("coverage = %.1f (present=%v), want 0 present (failing test must score 0, #82)", v, ok)
	}
}

// TestCoverageSignal_ImplSurface_ProfileExecutionCredits verifies the
// call-graph impl-surface model: a feature linked to a TEST symbol that calls
// a PRODUCTION symbol is scored by whether that production symbol executed
// (coverprofile), not by literal name match. See issue #82 / coverprofile work.
func TestCoverageSignal_ImplSurface_ProfileExecutionCredits(t *testing.T) {
	ctx := context.Background()

	run := func(implExecuted bool) float64 {
		s := openTestStore(t)
		// Feature → test symbol (role=test).
		tsid := seedTestRoleFeature(t, s, "billing.subscribe", "services.TestSubscribe")
		// Production impl symbol the test calls.
		endLine := 40
		isid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: "services.Subscribe", Kind: shared.KindFunc,
			FilePath: "src/contexts/billing/application/services/service.go", Line: 10, EndLine: &endLine,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Edges().Insert(ctx, store.EdgeRow{
			FromID: tsid, ToID: isid, Kind: store.EdgeKindCall,
			FilePath: "src/contexts/billing/application/services/service_test.go", Line: 12,
		}); err != nil {
			t.Fatal(err)
		}
		// Profile run: the impl symbol executed (or not).
		statuses := map[int64]store.CoverageStatus{}
		if implExecuted {
			statuses[isid] = store.StatusPass
		} else {
			// seed a different symbol so the run is non-empty but impl uncovered
			statuses[tsid] = store.StatusPass
		}
		seedCoverage(t, s, store.FrameworkGoTest, statuses)

		got, err := New(s, Options{}).ScoreFeature(ctx, "billing.subscribe")
		if err != nil {
			t.Fatal(err)
		}
		return got.Components[SignalCoverage]
	}

	if cov := run(true); cov != 100 {
		t.Errorf("impl executed: coverage = %.1f, want 100 (impl surface covered)", cov)
	}
	if cov := run(false); cov != 0 {
		t.Errorf("impl NOT executed: coverage = %.1f, want 0 (impl surface uncovered)", cov)
	}
}
