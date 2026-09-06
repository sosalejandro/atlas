package audit

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// seedDynamic wires a feature whose annotation sits on a TEST symbol with no
// call edges at all — the #84 shape — plus per-test execution evidence showing
// what that test really ran.
type dynamicSeed struct {
	feature   shared.FeatureID
	testName  string
	implNames []string
	// noise symbols are executed by every test in the run: the logger, the DI
	// container, the middleware chain.
	noiseNames []string
	otherTests int // how many unrelated tests also ran, each touching the noise
}

type dynamicIDs struct {
	runID  int64
	test   int64
	impl   []int64
	noise  []int64
	symIDs map[string]int64
}

func seedDynamic(t *testing.T, s *store.Store, spec dynamicSeed) dynamicIDs {
	t.Helper()
	ctx := context.Background()
	out := dynamicIDs{symIDs: map[string]int64{}}

	if err := s.Features().Upsert(ctx, store.Feature{
		ID: spec.feature, Title: string(spec.feature), Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("Upsert feature: %v", err)
	}

	insert := func(name, file string, line int) int64 {
		id, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(name), Kind: shared.KindFunc,
			FilePath: file, Line: line,
		})
		if err != nil {
			t.Fatalf("Insert %s: %v", name, err)
		}
		out.symIDs[name] = id
		return id
	}

	out.test = insert(spec.testName, "src/feature_test.go", 10)
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: spec.feature, SymbolID: out.test,
		Role: store.RoleTest, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("Link test: %v", err)
	}
	for i, n := range spec.implNames {
		out.impl = append(out.impl, insert(n, "src/impl.go", 20+i*10))
	}
	for i, n := range spec.noiseNames {
		out.noise = append(out.noise, insert(n, "src/runtime.go", 30+i*10))
	}

	// The run itself: union results, exactly as the per-test ingest writes.
	now := time.Now().UTC()
	// Impl symbols are well covered (8/10); shared runtime is barely covered
	// (2/10). The gap is deliberate: if noise leaks into a feature's surface
	// it drags the score down measurably, so these tests can tell the two
	// outcomes apart instead of passing either way.
	results := make([]store.CoverageResult, 0, len(out.impl)+len(out.noise))
	for _, sid := range out.impl {
		v := sid
		results = append(results, store.CoverageResult{
			SymbolID: &v, Status: store.StatusPass, CoveredStmts: 8, TotalStmts: 10,
		})
	}
	for _, sid := range out.noise {
		v := sid
		results = append(results, store.CoverageResult{
			SymbolID: &v, Status: store.StatusPass, CoveredStmts: 2, TotalStmts: 10,
		})
	}
	runID, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}
	out.runID = runID

	rows := []store.TestExecution{}
	for _, sid := range out.impl {
		rows = append(rows, store.TestExecution{
			TestSymbolID: out.test, SymbolID: sid, CoveredStmts: 8, TotalStmts: 10,
		})
	}
	for _, sid := range out.noise {
		rows = append(rows, store.TestExecution{
			TestSymbolID: out.test, SymbolID: sid, CoveredStmts: 2, TotalStmts: 10,
		})
	}
	// Unrelated tests: each runs only the noise symbols, which is what makes
	// them noise.
	for i := 0; i < spec.otherTests; i++ {
		other := insert("other.Test"+string(rune('A'+i)), "src/other_test.go", 100+i)
		for _, sid := range out.noise {
			rows = append(rows, store.TestExecution{
				TestSymbolID: other, SymbolID: sid, CoveredStmts: 2, TotalStmts: 10,
			})
		}
	}
	if err := s.TestCoverage().Insert(ctx, runID, rows); err != nil {
		t.Fatalf("TestCoverage.Insert: %v", err)
	}
	return out
}

// The #84 shape, solved: the feature's only annotation is on a test symbol
// with zero outgoing call edges, so the static walk finds nothing — but the
// execution evidence knows exactly what that test ran.
func TestDynamicSurface_CreditsWhatTheTestActuallyRan(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ids := seedDynamic(t, s, dynamicSeed{
		feature: "measurements.log-entry", testName: "measurements.TestLogEntry",
		implNames: []string{"LogService.Record", "LogService.validate", "logRepository.Insert"},
	})
	_ = ids

	got, err := New(s, Options{}).ScoreFeature(ctx, "measurements.log-entry")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if got.SurfaceSource != SurfaceDynamic {
		t.Fatalf("surface_source = %q, want %q — the static walk should not have won", got.SurfaceSource, SurfaceDynamic)
	}
	if v := got.Components[SignalCoverage]; v < 79 || v > 81 {
		t.Errorf("coverage = %.1f, want ~80 (24 of 30 statements over the three symbols the test ran)", v)
	}
}

// Shared runtime must not land in every feature's surface. With a suite large
// enough for the ratio to mean something, a symbol executed by most tests is
// dropped.
func TestDynamicSurface_DropsUbiquitousSymbols(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedDynamic(t, s, dynamicSeed{
		feature: "billing.checkout", testName: "billing.TestCheckout",
		implNames:  []string{"Checkout.Charge"},
		noiseNames: []string{"log.Info", "di.Resolve"},
		otherTests: 9, // 10 tests total, noise runs in all of them
	})

	got, err := New(s, Options{}).ScoreFeature(ctx, "billing.checkout")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if got.SurfaceSource != SurfaceDynamic {
		t.Fatalf("surface_source = %q, want dynamic", got.SurfaceSource)
	}
	// Only Checkout.Charge should be in the denominator: 8/10 = 80. If the
	// noise leaked in, the score would be dragged toward (8+2+2)/30 = 40.
	if v := got.Components[SignalCoverage]; v < 79 || v > 81 {
		t.Errorf("coverage = %.1f, want ~80 — shared runtime leaked into the surface", v)
	}
}

// A small suite must NOT apply the ratio: with three tests, "executed by more
// than half" describes a shared domain service, not framework plumbing.
func TestDynamicSurface_SmallSuiteKeepsSharedSymbols(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedDynamic(t, s, dynamicSeed{
		feature: "orders.place", testName: "orders.TestPlace",
		implNames:  []string{"Order.Place"},
		noiseNames: []string{"Money.Add"}, // shared with one other test only
		otherTests: 2,
	})

	got, err := New(s, Options{}).ScoreFeature(ctx, "orders.place")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// Both symbols count: (8 + 2) / 20 = 50.
	if v := got.Components[SignalCoverage]; v < 49 || v > 51 {
		t.Errorf("coverage = %.1f, want ~50 (a 3-test suite must not treat a shared helper as runtime)", v)
	}
}

// A store with no per-test evidence must behave exactly as before.
func TestDynamicSurface_FallsBackWhenNoEvidence(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 2, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
	})

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if got.SurfaceSource == SurfaceDynamic {
		t.Error("claimed a dynamic surface with no per-test evidence in the run")
	}
	if v := got.Components[SignalCoverage]; v != 100 {
		t.Errorf("coverage = %.1f, want 100 — the pre-0010 path regressed", v)
	}
}
