package audit

// issue84_package_anchor_test.go — unit tests for the package-anchor fallback
// introduced in issue #84. The fallback fires in coverageSignal when:
//   (1) featureImplSurface yields no production symbols (empty call-edge graph
//       from the annotation roots — the "zero-edge stub" case), AND
//   (2) the direct-link wantedSymbolIDs model produces denom==0 (no linked
//       impl symbols).
//
// Under those conditions, coverageSignal falls back to all production symbols
// in the Go packages co-located with the feature's linked test symbols,
// gated by MaxPackageAnchorSymbols to exclude large shared packages.

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// seedTestSymbolInPackage creates a feature + one TEST-role symbol in the
// given package. Mirrors the real scenario: @atlas:feature annotation on
// a test stub function that the Go scanner emits with no call edges.
func seedTestSymbolInPackage(t *testing.T, s *store.Store, fid shared.FeatureID, pkg string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := s.Features().Upsert(ctx, store.Feature{
		ID: fid, Title: string(fid), Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatal(err)
	}
	qn := shared.SymbolID(string(fid) + ".TestStub")
	sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: qn,
		Kind:          shared.KindFunc,
		FilePath:      "src/contexts/measurements/application/services/x_test.go",
		Line:          10,
		Package:       &pkg,
	})
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

// seedProductionSymbolInPackage inserts a production symbol in the given package.
func seedProductionSymbolInPackage(t *testing.T, s *store.Store, qn shared.SymbolID, pkg, filePath string) int64 {
	t.Helper()
	sid, err := s.Symbols().Insert(context.Background(), store.SymbolRow{
		QualifiedName: qn,
		Kind:          shared.KindFunc,
		FilePath:      filePath,
		Line:          20,
		Package:       &pkg,
	})
	if err != nil {
		t.Fatalf("insert prod symbol %q: %v", qn, err)
	}
	return sid
}

// TestPackageAnchor_FallbackCreditsExecutedProductionSymbols is the primary
// regression test for issue #84.
//
// Scenario: feature annotated on a test stub (zero call edges), all production
// symbols live in the same Go package. The prod symbols ARE executed in the
// coverage run. Before the fix: score=0 (no impl surface, no direct-link hit).
// After the fix: score reflects the execution fraction of the package.
func TestPackageAnchor_FallbackCreditsExecutedProductionSymbols(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "measurements/application/services"

	// Test stub (annotation root) — zero call edges.
	_ = seedTestSymbolInPackage(t, s, "measurements-nutritionist.create", pkg)

	// Production symbols in the same package.
	pSym1 := seedProductionSymbolInPackage(t, s,
		"services.CreateMeasurement", pkg,
		"src/contexts/measurements/application/services/service.go")
	pSym2 := seedProductionSymbolInPackage(t, s,
		"services.ValidateMeasurement", pkg,
		"src/contexts/measurements/application/services/service.go")

	// Both production symbols executed.
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		pSym1: store.StatusPass,
		pSym2: store.StatusPass,
	})

	a := New(s, Options{MaxPackageAnchorSymbols: 200})
	got, err := a.ScoreFeature(ctx, "measurements-nutritionist.create")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalCoverage]; v != 100 {
		t.Errorf("coverage = %.1f, want 100 (package-anchor: 2/2 prod symbols executed, #84)", v)
	}
}

// TestPackageAnchor_PartialExecutionScoresPartial verifies proportional
// attribution: if only 1 of 2 production symbols executed, score = 50.
func TestPackageAnchor_PartialExecutionScoresPartial(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "measurements/application/services"

	_ = seedTestSymbolInPackage(t, s, "measurements-nutritionist.goal-complete", pkg)

	pSym1 := seedProductionSymbolInPackage(t, s,
		"services.CompleteGoal", pkg,
		"src/contexts/measurements/application/services/goal.go")
	pSym2 := seedProductionSymbolInPackage(t, s,
		"services.GoalHistory", pkg,
		"src/contexts/measurements/application/services/goal.go")

	// Only first symbol executed.
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		pSym1: store.StatusPass,
		pSym2: store.StatusFail,
	})

	a := New(s, Options{MaxPackageAnchorSymbols: 200})
	got, err := a.ScoreFeature(ctx, "measurements-nutritionist.goal-complete")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalCoverage]; v != 50 {
		t.Errorf("coverage = %.1f, want 50 (1/2 prod symbols in package executed, #84)", v)
	}
}

// TestPackageAnchor_GuardBlocksOversizedPackage verifies that the size guard
// prevents the fallback from firing for large shared packages (the handlers
// monolith scenario). With MaxPackageAnchorSymbols=2, a package with 3+ prod
// symbols must NOT produce a score — the feature stays at the annotation_presence
// floor instead.
func TestPackageAnchor_GuardBlocksOversizedPackage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "infrastructure/http/handlers"

	_ = seedTestSymbolInPackage(t, s, "measurements-nutritionist.list", pkg)

	// Seed 3 production symbols — over the guard of 2.
	p1 := seedProductionSymbolInPackage(t, s, "handlers.CreateMeasurement", pkg, "src/infrastructure/http/handlers/m1.go")
	p2 := seedProductionSymbolInPackage(t, s, "handlers.GetMeasurement", pkg, "src/infrastructure/http/handlers/m2.go")
	p3 := seedProductionSymbolInPackage(t, s, "handlers.ListMeasurements", pkg, "src/infrastructure/http/handlers/m3.go")

	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		p1: store.StatusPass, p2: store.StatusPass, p3: store.StatusPass,
	})

	// MaxPackageAnchorSymbols=2 → package with 3 symbols is blocked.
	a := New(s, Options{MaxPackageAnchorSymbols: 2})
	got, err := a.ScoreFeature(ctx, "measurements-nutritionist.list")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// Guard fired: no package-anchor coverage. Feature falls back to
	// annotation_presence floor (score = 10).
	if _, ok := got.Components[SignalCoverage]; ok {
		t.Errorf("coverage component present (%.1f) for oversized package — guard should block it (#84)", got.Components[SignalCoverage])
	}
}

// TestPackageAnchor_DisabledWhenMaxZero verifies that MaxPackageAnchorSymbols=0
// (or negative) completely disables the fallback, leaving the feature at the
// annotation_presence floor.
func TestPackageAnchor_DisabledWhenMaxZero(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "measurements/application/services"

	_ = seedTestSymbolInPackage(t, s, "measurements-nutritionist.delete", pkg)
	p1 := seedProductionSymbolInPackage(t, s, "services.DeleteMeasurement", pkg, "src/contexts/measurements/application/services/svc.go")

	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		p1: store.StatusPass,
	})

	// MaxPackageAnchorSymbols=-1 should disable the fallback.
	a := New(s, Options{MaxPackageAnchorSymbols: -1})
	got, err := a.ScoreFeature(ctx, "measurements-nutritionist.delete")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if _, ok := got.Components[SignalCoverage]; ok {
		t.Errorf("coverage component present (%.1f) when MaxPackageAnchorSymbols=-1 — fallback must be disabled", got.Components[SignalCoverage])
	}
}

// TestPackageAnchor_DoesNotFireWhenCallEdgeSurfacePresent ensures the fallback
// is strictly additive: when Tier 1 (call-edge surface) is non-empty, the
// package-anchor path must NOT be consulted. This prevents the fallback from
// widening the surface for features that already work correctly.
func TestPackageAnchor_DoesNotFireWhenCallEdgeSurfacePresent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "billing/application/services"

	// Test symbol (annotation root).
	testSym := seedTestSymbolInPackage(t, s, "billing.subscribe", pkg)

	// Production impl symbol the test calls (creates a call edge → tier 1 fires).
	endLine := 40
	implSym, err := s.Symbols().Insert(context.Background(), store.SymbolRow{
		QualifiedName: "services.Subscribe",
		Kind:          shared.KindFunc,
		FilePath:      "src/contexts/billing/application/services/service.go",
		Line:          10,
		EndLine:       &endLine,
		Package:       &pkg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edges().Insert(context.Background(), store.EdgeRow{
		Tier: graph.TierNameResolved, FromID: testSym, ToID: implSym, Kind: store.EdgeKindCall,
		FilePath: "src/contexts/billing/application/services/service_test.go", Line: 12,
	}); err != nil {
		t.Fatal(err)
	}

	// Seed a SECOND production symbol in the same package that is NOT executed.
	// If package-anchor fired, it would include this symbol and drag the score
	// below 100.
	_ = seedProductionSymbolInPackage(t, s, "services.BillingHelper", pkg, "src/contexts/billing/application/services/helper.go")

	// Only the impl symbol is covered.
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		implSym: store.StatusPass,
	})

	a := New(s, Options{MaxPackageAnchorSymbols: 200})
	got, err := a.ScoreFeature(ctx, "billing.subscribe")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// Tier 1 fires (call-edge surface = {implSym}). implSym is executed → 100%.
	// Package-anchor must NOT drag the score down by including BillingHelper.
	if v := got.Components[SignalCoverage]; v != 100 {
		t.Errorf("coverage = %.1f, want 100 (call-edge tier 1 active, package-anchor must not fire, #84)", v)
	}
}

// TestPackageAnchor_NoPackageFieldSkipsFallback verifies that when linked
// symbols have no Package field set (legacy scanner output), the fallback
// produces no surface (empty) and the feature scores via the annotation_presence
// floor rather than crashing.
func TestPackageAnchor_NoPackageFieldSkipsFallback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Test stub with no Package field.
	fid := shared.FeatureID("legacy.feature")
	if err := s.Features().Upsert(ctx, store.Feature{ID: fid, Title: "Legacy"}); err != nil {
		t.Fatal(err)
	}
	sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "legacy.TestFunc",
		Kind:          shared.KindFunc,
		FilePath:      "legacy/x_test.go",
		Line:          5,
		// Package: nil — intentionally absent.
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: fid, SymbolID: sid, Role: store.RoleTest, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatal(err)
	}

	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{})

	a := New(s, Options{MaxPackageAnchorSymbols: 200})
	got, err := a.ScoreFeature(ctx, fid)
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// No package field → package-anchor produces no surface → annotation_presence floor.
	if got.Score <= 0 {
		t.Errorf("Score = %.2f, want > 0 (annotation_presence floor must still apply)", got.Score)
	}
	if _, ok := got.Components[SignalCoverage]; ok {
		t.Errorf("coverage component present for no-package-field feature; want only annotation_presence")
	}
}

// TestPackageAnchor_ImplRoleTestStub covers the real-world case diagnosed in
// issue #84: the Go scanner links test stub functions (nopXxx, noopXxx) as
// RoleImpl rather than RoleTest. The symbol appears in wantedSymbolIDs, has
// no coverage result in the profile run (absent → denom=1, numer=0), and
// its impl surface from call-edge BFS is empty (zero edges). The package-anchor
// fallback must fire because allTestFileWanted returns true for the test-file
// symbol.
func TestPackageAnchor_ImplRoleTestStub(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "identity/application/services"

	// Impl-linked test stub (no call edges emitted by scanner).
	fid := shared.FeatureID("auth.mfa")
	if err := s.Features().Upsert(ctx, store.Feature{ID: fid, Title: "MFA"}); err != nil {
		t.Fatal(err)
	}
	stubID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "services.TestFormatRecoveryCode_ShortCode",
		Kind:          shared.KindFunc,
		FilePath:      "src/contexts/identity/application/services/mfa_service_test.go",
		Line:          34,
		Package:       &pkg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: fid, SymbolID: stubID, Role: store.RoleImpl, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatal(err)
	}
	// No call edges from stubID → impl surface is empty.

	// Production symbol in the same package (the actual implementation).
	prodID := seedProductionSymbolInPackage(t, s, "services.FormatRecoveryCode", pkg,
		"src/contexts/identity/application/services/mfa_service.go")
	// Production symbol executed in the coverage run.
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		prodID: store.StatusPass,
		// stubID intentionally absent — not in the profile
	})

	a := New(s, Options{MaxPackageAnchorSymbols: 200})
	got, err := a.ScoreFeature(ctx, fid)
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// Package-anchor fires: 1/1 prod symbol executed → coverage=100.
	if v := got.Components[SignalCoverage]; v != 100 {
		t.Errorf("coverage = %.1f, want 100 (impl-role test stub + package-anchor, #84)", v)
	}
}

// TestPackageAnchor_ImplRoleTestStub_ProdSymbolNotExecuted verifies that when
// the production symbol in the package was NOT executed, the package-anchor
// fallback correctly scores 0% (not absent) so the zero coverage is visible.
func TestPackageAnchor_ImplRoleTestStub_ProdSymbolNotExecuted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "identity/application/services"

	fid := shared.FeatureID("auth.mfa-unexecuted")
	if err := s.Features().Upsert(ctx, store.Feature{ID: fid, Title: "MFA unexecuted"}); err != nil {
		t.Fatal(err)
	}
	stubID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "services.TestNopMFAStub",
		Kind:          shared.KindFunc,
		FilePath:      "src/contexts/identity/application/services/mfa_nop_test.go",
		Line:          10,
		Package:       &pkg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: fid, SymbolID: stubID, Role: store.RoleImpl, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatal(err)
	}

	// Production symbol in same package but NOT executed.
	prodID := seedProductionSymbolInPackage(t, s, "services.MFAUnused", pkg,
		"src/contexts/identity/application/services/mfa_unused.go")
	// Seed a run that is non-empty but doesn't cover prodID.
	otherPkg := "other/pkg"
	otherID := seedProductionSymbolInPackage(t, s, "other.Func", otherPkg, "other/func.go")
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		otherID: store.StatusPass,
	})
	_ = prodID // referenced to satisfy linter

	a := New(s, Options{MaxPackageAnchorSymbols: 200})
	got, err := a.ScoreFeature(ctx, fid)
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// Package-anchor fires but prodID not in results → denom=1, numer=0 → coverage=0.
	// Coverage signal IS present (score=0), feature does not fall through to annotation_presence.
	v, ok := got.Components[SignalCoverage]
	if !ok {
		t.Errorf("coverage component absent; want coverage=0 (package-anchor fires, prod symbol uncovered, #84)")
	}
	if v != 0 {
		t.Errorf("coverage = %.1f, want 0 (prod symbol not executed)", v)
	}
}
