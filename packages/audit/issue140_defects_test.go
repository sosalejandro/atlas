package audit

// issue140_defects_test.go — the defects an adversarial review found in the
// decision-coverage signal, each pinned by a test that fails without the fix.
//
// Two of them are the same mistake at different levels: a claim that is true
// of the ratio and was never made true of the weight, and a "both signals are
// scored over one surface" invariant that nothing checked.

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// TestDecisionCoverage_WeightScalesWithTheMeasuredSurface applies the
// availability rule to the WEIGHT rather than only to the ratio.
//
// Scoring the ratio over the measured symbols alone is right, but on its own it
// lets one measured symbol out of a hundred carry 0.24 of the 0.40 budget —
// 60% of everything the score says about testing — on evidence covering 1% of
// the feature. Here one of four symbols is measured, so decision coverage may
// draw only a quarter of its share (0.40*0.6*0.25 = 0.06) and statement
// coverage, which saw all four, keeps the rest (0.34):
//
//	(0.34*50 + 0.06*100) / 0.40 = 57.5
//
// An unscaled share gives 80 — the number a fully measured surface earns — so
// this test tells "measured everywhere" and "measured once" apart.
func TestDecisionCoverage_WeightScalesWithTheMeasuredSurface(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 4, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
		ids[2]: store.StatusFail, ids[3]: store.StatusFail,
	})
	// Exactly one of the four surface symbols has ever been through `atlas
	// flow`, and every outcome it has was taken.
	seedDecision(t, s, ids[0], 2, 2, 2)

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalCoverage]; v != 50 {
		t.Fatalf("statement coverage = %.4f, want 50", v)
	}
	if v := got.Components[SignalDecisionCoverage]; v != 100 {
		t.Fatalf("decision coverage = %.4f, want 100 (scored over the measured symbol only)", v)
	}
	if d := got.Decision; d == nil || d.SymbolsMeasured != 1 || d.SymbolsUnmeasured != 3 {
		t.Fatalf("Decision = %+v, want 1 measured / 3 unmeasured", d)
	}
	if !approxEqual(got.Score, 57.5) {
		t.Errorf("blended score = %.4f, want 57.5 — decision coverage's share must scale by the "+
			"1/4 of the surface it actually measured (80 means it drew the full share)", got.Score)
	}
}

// TestDecisionCoverage_FullyMeasuredSurfaceDrawsTheWholeShare is the other
// half of the scaling rule, and the reason it is proportional rather than a
// cutoff: at full measurement the factor is exactly 1 and the blend is
// untouched. Without this, "scale the weight" could satisfy the test above by
// shrinking decision coverage everywhere.
func TestDecisionCoverage_FullyMeasuredSurfaceDrawsTheWholeShare(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 4, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
		ids[2]: store.StatusFail, ids[3]: store.StatusFail,
	})
	for _, id := range ids {
		seedDecision(t, s, id, 2, 2, 2)
	}

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if !approxEqual(got.Score, 80) {
		t.Errorf("blended score = %.4f, want 80 (0.16*50 + 0.24*100 over a 0.40 budget)", got.Score)
	}
}

// TestDecisionCoverage_SurfaceSourceIsReportedWithNoCoverageRun closes a
// documented field that was blank in one case.
//
// docs/commands/audit.md promises that the tier the surface came from "is
// reported as surface_source". With an empty coverage frontier the statement
// signal never runs, so nothing else records the tier — and that is precisely
// the case where decision coverage is the ONLY number in the output and the
// reader most needs to know what it was computed over.
func TestDecisionCoverage_SurfaceSourceIsReportedWithNoCoverageRun(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 2, SymbolFile: "auth/login.go",
	})
	// No coverage run at all: `atlas flow measure` read a profile off disk.
	seedDecision(t, s, ids[0], 4, 4, 2)
	seedDecision(t, s, ids[1], 4, 4, 2)

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if _, ok := got.Components[SignalCoverage]; ok {
		t.Fatalf("statement coverage present with no coverage run")
	}
	if _, ok := got.Components[SignalDecisionCoverage]; !ok {
		t.Fatalf("decision coverage absent; the fixture is not exercising the path")
	}
	if got.SurfaceSource != SurfaceStatic {
		t.Errorf("surface_source = %q, want %q — decision coverage resolved the surface itself "+
			"and must record which tier answered", got.SurfaceSource, SurfaceStatic)
	}
}

// TestDecisionCoverage_SharedSurface_PackageAnchor pins half the mechanism the
// blend rests on: both signals are scored over the SAME symbols.
//
// The package-anchor tier (#84) is where they can diverge visibly. The
// feature's only link is a test stub, so the direct-link `wanted` set is EMPTY
// and the statement score comes from the production symbols co-located with
// the stub. Hand decision coverage anything but the anchor surface and it is
// scored over an empty set — the signal vanishes, and the operator loses a
// reading the store has the rows for.
func TestDecisionCoverage_SharedSurface_PackageAnchor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pkg := "measurements/application/services"

	_ = seedTestSymbolInPackage(t, s, "measurements.create", pkg)
	pSym1 := seedProductionSymbolInPackage(t, s, "services.Create", pkg,
		"src/contexts/measurements/application/services/service.go")
	pSym2 := seedProductionSymbolInPackage(t, s, "services.Validate", pkg,
		"src/contexts/measurements/application/services/service.go")
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		pSym1: store.StatusPass, pSym2: store.StatusPass,
	})
	seedDecision(t, s, pSym1, 2, 2, 1)
	seedDecision(t, s, pSym2, 2, 2, 2)

	got, err := New(s, Options{}).ScoreFeature(ctx, "measurements.create")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if got.SurfaceSource != SurfacePackageAnchor {
		t.Fatalf("surface_source = %q, want %q — the fixture is not on the anchor path",
			got.SurfaceSource, SurfacePackageAnchor)
	}
	d := got.Decision
	if d == nil {
		t.Fatal("Decision report is nil: the anchor surface was not handed to the decision signal, " +
			"so it scored over the (empty) direct-link set and disappeared")
	}
	if d.SymbolsMeasured != 2 || d.SymbolsUnmeasured != 0 {
		t.Errorf("symbols measured/unmeasured = %d/%d, want 2/0 — the decision surface is not the "+
			"surface the statement score was computed over", d.SymbolsMeasured, d.SymbolsUnmeasured)
	}
	if v := got.Components[SignalDecisionCoverage]; v != 75 {
		t.Errorf("decision coverage = %.4f, want 75 (3 of 4 decidable outcomes on the anchor symbols)", v)
	}
}

// TestDecisionCoverage_SharedSurface_CarriedRunStaysInBothDenominators pins
// the other half, on the tier where re-deriving the surface gives a DIFFERENT
// answer rather than an empty one.
//
// A build loses its Go job. Carryforward (#136) keeps the Go symbol in the
// statement denominator by resolving the surface against this build's runs
// PLUS the runs the carries came from. Decision coverage re-deriving the
// surface from the frontier alone would see only the front-end symbol and
// report 100% while half the feature's measured branches were never taken —
// the exact "a lost job makes the number go up" failure #136 exists to
// prevent, reappearing in the other signal.
func TestDecisionCoverage_SharedSurface_CarriedRunStaysInBothDenominators(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	goTest, feTest, goImpl, feImpl := seedDynamicFeature(t, s, feature)

	goRun := seedStmtRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute,
		map[int64]stmtRow{goImpl: {covered: 5, total: 100}})
	seedExecutions(t, s, goRun, []store.TestExecution{
		{TestSymbolID: goTest, SymbolID: goImpl, CoveredStmts: 5, TotalStmts: 100},
	})
	feRun := seedStmtRun(t, s, store.FrameworkVitest, "ci-1", 2*time.Minute,
		map[int64]stmtRow{feImpl: {covered: 10, total: 10}})
	seedExecutions(t, s, feRun, []store.TestExecution{
		{TestSymbolID: feTest, SymbolID: feImpl, CoveredStmts: 10, TotalStmts: 10},
	})

	// Build 2: the Go job crashed; only the front-end evidence is in the
	// frontier, and the Go result is carried into it.
	feRun2 := seedStmtRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute,
		map[int64]stmtRow{feImpl: {covered: 10, total: 10}})
	seedExecutions(t, s, feRun2, []store.TestExecution{
		{TestSymbolID: feTest, SymbolID: feImpl, CoveredStmts: 10, TotalStmts: 10},
	})

	// `atlas flow` measured both impl symbols. The Go one is untested.
	seedDecision(t, s, goImpl, 2, 2, 0)
	seedDecision(t, s, feImpl, 2, 2, 2)

	got, err := New(s, Options{}).ScoreFeature(context.Background(), feature)
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	d := got.Decision
	if d == nil {
		t.Fatal("Decision report is nil")
	}
	if d.SymbolsMeasured != 2 {
		t.Errorf("SymbolsMeasured = %d, want 2 — decision coverage must be scored over the surface "+
			"the statement signal used (this build's runs plus the carry sources), not over the "+
			"frontier's own runs", d.SymbolsMeasured)
	}
	if v := got.Components[SignalDecisionCoverage]; v != 50 {
		t.Errorf("decision coverage = %.4f, want 50 (2 of 4 decidable outcomes); 100 means the Go "+
			"symbol fell out of the decision denominator when its job died", v)
	}
}
