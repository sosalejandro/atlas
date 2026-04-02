// @testreg server.view-model-builders
package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/domain"
)

// writeTestSnapshot saves a pre-built snapshotFile to the server's snapshot dir.
func writeTestSnapshot(t *testing.T, srv *Server, name string, snap snapshotFile) {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.MkdirAll(srv.snapshotDir(), 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srv.snapshotDir(), name+".json"), raw, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// ─── featureHealth ────────────────────────────────────────────────────────────

func TestFeatureHealth_AllCovered(t *testing.T) {
	f := domain.Feature{
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusCovered},
				Web:     &domain.CoverageEntry{Status: domain.StatusCovered},
			},
			Integration: domain.IntegrationCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusCovered},
			},
			E2E: domain.E2ECoverage{
				Web: &domain.E2ECoverageEntry{Status: domain.StatusCovered},
			},
		},
	}
	got := featureHealth(f)
	if got != 1.0 {
		t.Errorf("featureHealth = %.2f, want 1.0", got)
	}
}

func TestFeatureHealth_AllMissing(t *testing.T) {
	f := domain.Feature{
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
			},
			Integration: domain.IntegrationCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
			},
		},
	}
	got := featureHealth(f)
	if got != 0.0 {
		t.Errorf("featureHealth = %.2f, want 0.0", got)
	}
}

func TestFeatureHealth_NoCoverage(t *testing.T) {
	got := featureHealth(domain.Feature{})
	if got != 0.0 {
		t.Errorf("featureHealth (no coverage) = %.2f, want 0.0", got)
	}
}

func TestFeatureHealth_PartialCoverage(t *testing.T) {
	f := domain.Feature{
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusCovered},
				Web:     &domain.CoverageEntry{Status: domain.StatusMissing},
			},
		},
	}
	got := featureHealth(f)
	if got != 0.5 {
		t.Errorf("featureHealth (half covered) = %.2f, want 0.5", got)
	}
}

func TestFeatureHealth_NotApplicable_CountsAsCovered(t *testing.T) {
	// StatusNotApplicable.IsMissing() == false → health counts it as "present"
	f := domain.Feature{
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusNotApplicable},
			},
		},
	}
	got := featureHealth(f)
	if got != 1.0 {
		t.Errorf("featureHealth (not-applicable) = %.2f, want 1.0", got)
	}
}

// ─── buildPriorityRings ───────────────────────────────────────────────────────

func TestBuildPriorityRings_FourRings(t *testing.T) {
	rings := buildPriorityRings(domain.Metrics{
		ByPriority: map[domain.Priority]domain.PriorityMetrics{},
	})
	if len(rings) != 4 {
		t.Fatalf("len(rings) = %d, want 4", len(rings))
	}
}

func TestBuildPriorityRings_CorrectPercentage(t *testing.T) {
	rings := buildPriorityRings(domain.Metrics{
		ByPriority: map[domain.Priority]domain.PriorityMetrics{
			domain.PriorityCritical: {Total: 10, CoveredUnit: 8},
		},
	})
	if rings[0].Pct != 80 {
		t.Errorf("critical ring Pct = %d, want 80", rings[0].Pct)
	}
}

func TestBuildPriorityRings_ZeroTotal_NoPanic(t *testing.T) {
	rings := buildPriorityRings(domain.Metrics{
		ByPriority: map[domain.Priority]domain.PriorityMetrics{},
	})
	for _, r := range rings {
		if r.Pct != 0 {
			t.Errorf("ring %q Pct = %d, want 0", r.Label, r.Pct)
		}
	}
}

func TestBuildPriorityRings_DashOffset_FullAndEmpty(t *testing.T) {
	rings := buildPriorityRings(domain.Metrics{
		ByPriority: map[domain.Priority]domain.PriorityMetrics{
			domain.PriorityCritical: {Total: 1, CoveredUnit: 1}, // 100%
		},
	})
	// 100% → dashOffset = 88 - (88*100/100) = 0
	if rings[0].DashOffset != 0 {
		t.Errorf("DashOffset at 100%% = %d, want 0", rings[0].DashOffset)
	}
	// 0% (high has no data) → dashOffset = 88
	if rings[1].DashOffset != 88 {
		t.Errorf("DashOffset at 0%% = %d, want 88", rings[1].DashOffset)
	}
}

// ─── buildCoverageBars ────────────────────────────────────────────────────────

func TestBuildCoverageBars_ThreeBars(t *testing.T) {
	bars := buildCoverageBars(domain.Metrics{TotalFeatures: 10, CoveredUnit: 7})
	if len(bars) != 3 {
		t.Fatalf("len(bars) = %d, want 3", len(bars))
	}
}

func TestBuildCoverageBars_Percentages(t *testing.T) {
	bars := buildCoverageBars(domain.Metrics{
		TotalFeatures: 10, CoveredUnit: 7, CoveredIntegration: 4, CoveredE2E: 1,
	})
	cases := []struct {
		label string
		pct   int
	}{
		{"Unit Tests", 70},
		{"Integration Tests", 40},
		{"E2E Tests", 10},
	}
	for i, c := range cases {
		if bars[i].Label != c.label {
			t.Errorf("bars[%d].Label = %q, want %q", i, bars[i].Label, c.label)
		}
		if bars[i].Pct != c.pct {
			t.Errorf("bars[%d].Pct = %d, want %d", i, bars[i].Pct, c.pct)
		}
	}
}

func TestBuildCoverageBars_ZeroTotal_NoPanic(t *testing.T) {
	bars := buildCoverageBars(domain.Metrics{TotalFeatures: 0})
	for _, b := range bars {
		if b.Pct != 0 {
			t.Errorf("bar %q Pct = %d, want 0", b.Label, b.Pct)
		}
	}
}

func TestBuildCoverageBars_ColorClasses(t *testing.T) {
	bars := buildCoverageBars(domain.Metrics{
		TotalFeatures: 10, CoveredUnit: 8, CoveredIntegration: 5, CoveredE2E: 2,
	})
	cases := []struct{ idx int; want string }{
		{0, "bg-emerald-500"}, // 80%
		{1, "bg-yellow-500"},  // 50%
		{2, "bg-red-500"},     // 20%
	}
	for _, c := range cases {
		if bars[c.idx].BarClass != c.want {
			t.Errorf("bars[%d].BarClass = %q, want %q", c.idx, bars[c.idx].BarClass, c.want)
		}
	}
}

// ─── featureToVM ─────────────────────────────────────────────────────────────

func TestFeatureToVM_CoveredStatus(t *testing.T) {
	f := domain.Feature{
		ID:       "auth.login",
		Priority: domain.PriorityCritical,
		Coverage: domain.Coverage{
			Unit:        domain.UnitCoverage{Backend: &domain.CoverageEntry{Status: domain.StatusCovered}},
			Integration: domain.IntegrationCoverage{Backend: &domain.CoverageEntry{Status: domain.StatusCovered}},
		},
	}
	vm := featureToVM(f)
	if vm.Status != "covered" {
		t.Errorf("status = %q, want covered", vm.Status)
	}
	if !vm.UnitCovered || !vm.IntegCovered {
		t.Error("expected UnitCovered and IntegCovered = true")
	}
}

func TestFeatureToVM_MissingStatus(t *testing.T) {
	vm := featureToVM(domain.Feature{ID: "billing.checkout", Priority: domain.PriorityHigh})
	if vm.Status != "missing" {
		t.Errorf("status = %q, want missing", vm.Status)
	}
	if vm.UnitCovered || vm.IntegCovered || vm.E2ECovered {
		t.Error("expected all coverage = false for empty feature")
	}
}

func TestFeatureToVM_PartialStatus(t *testing.T) {
	f := domain.Feature{
		Priority: domain.PriorityMedium,
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{Backend: &domain.CoverageEntry{Status: domain.StatusCovered}},
		},
	}
	vm := featureToVM(f)
	if vm.Status != "partial" {
		t.Errorf("status = %q, want partial", vm.Status)
	}
}

// ─── buildGraphVM ─────────────────────────────────────────────────────────────

func TestBuildGraphVM_EmptyTraces(t *testing.T) {
	vm := buildGraphVM(&app.TraceOutput{FeatureID: "auth.login", Priority: "critical"})
	if vm.FeatureID != "auth.login" {
		t.Errorf("FeatureID = %q, want auth.login", vm.FeatureID)
	}
	if len(vm.Traces) != 0 {
		t.Errorf("len(Traces) = %d, want 0", len(vm.Traces))
	}
	if vm.ConfidencePct != 0 {
		t.Errorf("ConfidencePct = %d, want 0", vm.ConfidencePct)
	}
}

func TestBuildGraphVM_SingleTrace(t *testing.T) {
	out := &app.TraceOutput{
		FeatureID: "auth.login",
		Traces: []*domain.TraceResult{
			{
				Root: &domain.TraceNode{
					Node: &domain.Node{ID: "AuthHandler.Login", Kind: domain.NodeHandler},
					Children: []*domain.TraceNode{
						{Node: &domain.Node{ID: "authService.Login", Kind: domain.NodeService}},
					},
				},
				TotalNodes: 2,
				MaxDepth:   1,
				Confidence: 0.9,
			},
		},
	}
	vm := buildGraphVM(out)
	if len(vm.Traces) != 1 {
		t.Fatalf("len(Traces) = %d, want 1", len(vm.Traces))
	}
	if vm.TotalNodes != 2 {
		t.Errorf("TotalNodes = %d, want 2", vm.TotalNodes)
	}
	if vm.ConfidencePct != 90 {
		t.Errorf("ConfidencePct = %d, want 90", vm.ConfidencePct)
	}
	root := vm.Traces[0].Root
	if root.Kind != "handler" {
		t.Errorf("root.Kind = %q, want handler", root.Kind)
	}
	if len(root.Children) != 1 || root.Children[0].Kind != "service" {
		t.Errorf("expected 1 service child, got %v", root.Children)
	}
}

func TestBuildGraphVM_PicksMaxConfidence(t *testing.T) {
	out := &app.TraceOutput{
		Traces: []*domain.TraceResult{
			{Root: &domain.TraceNode{Node: &domain.Node{ID: "a"}}, Confidence: 0.5},
			{Root: &domain.TraceNode{Node: &domain.Node{ID: "b"}}, Confidence: 0.95},
		},
	}
	vm := buildGraphVM(out)
	if vm.ConfidencePct != 95 {
		t.Errorf("ConfidencePct = %d, want 95", vm.ConfidencePct)
	}
}

// ─── traceNodeToVM ────────────────────────────────────────────────────────────

func TestTraceNodeToVM_NilNode(t *testing.T) {
	if got := traceNodeToVM(nil, 0); got != nil {
		t.Errorf("expected nil for nil TraceNode, got %+v", got)
	}
}

func TestTraceNodeToVM_NilInnerNode(t *testing.T) {
	if got := traceNodeToVM(&domain.TraceNode{Node: nil}, 0); got != nil {
		t.Errorf("expected nil for nil inner Node, got %+v", got)
	}
}

func TestTraceNodeToVM_DepthPassedToChildren(t *testing.T) {
	tn := &domain.TraceNode{
		Node: &domain.Node{ID: "parent", Kind: domain.NodeHandler},
		Children: []*domain.TraceNode{
			{Node: &domain.Node{ID: "child", Kind: domain.NodeService}},
		},
	}
	vm := traceNodeToVM(tn, 0)
	if vm.Depth != 0 {
		t.Errorf("parent depth = %d, want 0", vm.Depth)
	}
	if vm.Children[0].Depth != 1 {
		t.Errorf("child depth = %d, want 1", vm.Children[0].Depth)
	}
}

func TestTraceNodeToVM_NameFromLastSegment(t *testing.T) {
	tn := &domain.TraceNode{
		Node: &domain.Node{ID: "pkg.subpkg.FunctionName", Kind: domain.NodeService},
	}
	vm := traceNodeToVM(tn, 0)
	if vm.Name != "FunctionName" {
		t.Errorf("Name = %q, want FunctionName", vm.Name)
	}
}

// ─── computeDiff ──────────────────────────────────────────────────────────────

func TestComputeDiff_AllImproved(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	// Baseline: all features at 0% health.
	writeTestSnapshot(t, srv, "baseline", snapshotFile{
		Features: map[string]float64{
			"auth.login":       0.0,
			"billing.checkout": 0.0,
		},
	})

	result, err := srv.computeDiff("baseline", "current")
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	// auth.login has real coverage in fixture → health > 0 → improved.
	if result.Improved == 0 {
		t.Error("expected at least one improved feature")
	}
	if result.Regressed != 0 {
		t.Errorf("Regressed = %d, want 0", result.Regressed)
	}
}

func TestComputeDiff_Regressed(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	// Baseline: billing.checkout at 100%; fixture has it missing → regressed.
	writeTestSnapshot(t, srv, "baseline", snapshotFile{
		Features: map[string]float64{"billing.checkout": 1.0},
	})

	result, err := srv.computeDiff("baseline", "current")
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	if result.Regressed == 0 {
		t.Error("expected at least one regressed feature")
	}
}

func TestComputeDiff_NewFeatures(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	// Baseline has only one feature; current (fixture) has 5.
	writeTestSnapshot(t, srv, "baseline", snapshotFile{
		Features: map[string]float64{"auth.login": 0.5},
	})

	result, err := srv.computeDiff("baseline", "current")
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	if result.Added == 0 {
		t.Error("expected Added > 0 for features absent from baseline")
	}
}

func TestComputeDiff_Unchanged_ExcludedFromRows(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	// Save current health as baseline → all unchanged.
	if err := srv.saveSnapshot("baseline"); err != nil {
		t.Fatalf("saveSnapshot: %v", err)
	}
	result, err := srv.computeDiff("baseline", "current")
	if err != nil {
		t.Fatalf("computeDiff: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("rows = %d, want 0 (unchanged rows are excluded)", len(result.Rows))
	}
	if result.Unchanged == 0 {
		t.Error("expected Unchanged > 0")
	}
}

func TestComputeDiff_MissingFromSnapshot_ReturnsError(t *testing.T) {
	srv := newTestServer(t)
	srv.projectRoot = t.TempDir()

	_, err := srv.computeDiff("nonexistent", "current")
	if err == nil {
		t.Error("expected error for nonexistent snapshot, got nil")
	}
}

// ─── Priority / status helpers ────────────────────────────────────────────────

func TestPriorityDot(t *testing.T) {
	cases := []struct {
		p    domain.Priority
		want string
	}{
		{domain.PriorityCritical, "bg-red-500"},
		{domain.PriorityHigh, "bg-yellow-500"},
		{domain.PriorityMedium, "bg-emerald-500"},
		{domain.PriorityLow, "bg-slate-500"},
		{domain.Priority("unknown"), "bg-slate-500"},
	}
	for _, c := range cases {
		got := priorityDot(c.p)
		if got != c.want {
			t.Errorf("priorityDot(%q) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestStatusBadgeClass(t *testing.T) {
	cases := []struct{ s, want string }{
		{"covered", "bg-emerald-500/10 text-emerald-400"},
		{"partial", "bg-yellow-500/10 text-yellow-400"},
		{"missing", "bg-slate-800 text-slate-500"},
		{"failing", "bg-red-500/10 text-red-400"},
	}
	for _, c := range cases {
		got := statusBadgeClass(c.s)
		if got != c.want {
			t.Errorf("statusBadgeClass(%q) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestHealthBarClass(t *testing.T) {
	cases := []struct {
		h    float64
		want string
	}{
		{1.0, "bg-emerald-500"},
		{0.7, "bg-emerald-500"},
		{0.69, "bg-yellow-500"},
		{0.4, "bg-yellow-500"},
		{0.39, "bg-red-500"},
		{0.0, "bg-red-500"},
	}
	for _, c := range cases {
		got := healthBarClass(c.h)
		if got != c.want {
			t.Errorf("healthBarClass(%.2f) = %q, want %q", c.h, got, c.want)
		}
	}
}

func TestKindBadgeClass_AllKinds(t *testing.T) {
	kinds := []string{"handler", "endpoint", "service", "repository", "query", "component", "hook", "external", "unknown", ""}
	for _, k := range kinds {
		got := kindBadgeClass(k)
		if got == "" {
			t.Errorf("kindBadgeClass(%q) returned empty string", k)
		}
	}
}
