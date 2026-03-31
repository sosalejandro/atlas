// @testreg audit.health-report
package app

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mockGraphBuilder returns a pre-built graph from its stored Graph field.
// This avoids depending on the StubGraphBuilder's stdout output in tests.
type mockGraphBuilder struct {
	graph *domain.Graph
}

func (m *mockGraphBuilder) Build(_ string, _ ports.GraphConfig) (*domain.Graph, error) {
	return m.graph, nil
}

func (m *mockGraphBuilder) BuildFrom(_ string, entryPoints []string, _ ports.GraphConfig) (*domain.Graph, error) {
	if m.graph != nil {
		return m.graph, nil
	}
	// Fallback: return an empty graph with endpoint nodes.
	g := domain.NewGraph()
	for _, ep := range entryPoints {
		g.AddNode(&domain.Node{
			ID:   ep,
			Kind: domain.NodeEndpoint,
		})
	}
	return g, nil
}

// buildAuditTestGraph constructs a graph with a handler -> service -> repository -> query chain.
func buildAuditTestGraph() *domain.Graph {
	g := domain.NewGraph()

	handler := &domain.Node{ID: "POST /api/v1/auth/login", Kind: domain.NodeHandler, File: "internal/handler/auth.go", Line: 10}
	service := &domain.Node{ID: "AuthService.Login", Kind: domain.NodeService, File: "internal/service/auth.go", Line: 20}
	repo := &domain.Node{ID: "UserRepo.FindByEmail", Kind: domain.NodeRepository, File: "internal/repository/user.go", Line: 30}
	query := &domain.Node{ID: "sql:GetUserByEmail", Kind: domain.NodeQuery, File: "", Line: 0}

	g.AddNode(handler)
	g.AddNode(service)
	g.AddNode(repo)
	g.AddNode(query)

	g.AddEdge("POST /api/v1/auth/login", "AuthService.Login")
	g.AddEdge("AuthService.Login", "UserRepo.FindByEmail")
	g.AddEdge("UserRepo.FindByEmail", "sql:GetUserByEmail")

	return g
}

// setupRegistryDir creates a temp registry directory using the init use case.
func setupRegistryDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}
	return registryDir
}

// defaultConfig returns a GraphConfig suitable for testing.
func defaultConfig() ports.GraphConfig {
	return ports.GraphConfig{MaxDepth: 10}
}

// ---------------------------------------------------------------------------
// Unit tests for pure functions
// ---------------------------------------------------------------------------

func TestAuditGapSeverity(t *testing.T) {
	tests := []struct {
		kind     string
		expected string
	}{
		{"handler", "critical"},
		{"service", "critical"},
		{"repository", "high"},
		{"query", "medium"},
		{"component", "low"},
		{"hook", "low"},
		{"external", "low"},
		{"unknown", "low"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got := gapSeverity(tt.kind)
			if got != tt.expected {
				t.Errorf("gapSeverity(%q) = %q, want %q", tt.kind, got, tt.expected)
			}
		})
	}
}

func TestAuditGapReason(t *testing.T) {
	tests := []struct {
		kind       string
		testStatus string
		wantPrefix string
	}{
		{"handler", "untested", "no unit test for handler"},
		{"handler", "partial", "incomplete unit test for handler"},
		{"service", "untested", "no unit test for service method"},
		{"service", "partial", "incomplete unit test for service method"},
		{"repository", "untested", "no integration test for repository"},
		{"repository", "partial", "incomplete integration test for repository"},
		{"query", "untested", "no test coverage for SQL query"},
		{"query", "partial", "incomplete test coverage for SQL query"},
		{"component", "untested", "no test for component"},
		{"hook", "untested", "no test for hook"},
		{"other", "untested", "no test coverage"},
		{"other", "partial", "incomplete test coverage"},
	}

	for _, tt := range tests {
		t.Run(tt.kind+"_"+tt.testStatus, func(t *testing.T) {
			got := gapReason(tt.kind, tt.testStatus)
			if got != tt.wantPrefix {
				t.Errorf("gapReason(%q, %q) = %q, want %q", tt.kind, tt.testStatus, got, tt.wantPrefix)
			}
		})
	}
}

func TestAuditDeduplicateNodes(t *testing.T) {
	nodes := []domain.AnnotatedNode{
		{NodeID: "A", Kind: "handler", TestStatus: "tested"},
		{NodeID: "B", Kind: "service", TestStatus: "untested"},
		{NodeID: "A", Kind: "handler", TestStatus: "untested"}, // duplicate
		{NodeID: "C", Kind: "repository", TestStatus: "partial"},
		{NodeID: "B", Kind: "service", TestStatus: "tested"}, // duplicate
	}

	result := deduplicateNodes(nodes)

	if len(result) != 3 {
		t.Fatalf("deduplicateNodes: got %d nodes, want 3", len(result))
	}

	// Verify first occurrence is kept.
	idMap := make(map[string]string)
	for _, n := range result {
		idMap[n.NodeID] = n.TestStatus
	}

	if idMap["A"] != "tested" {
		t.Errorf("node A should keep first occurrence (tested), got %q", idMap["A"])
	}
	if idMap["B"] != "untested" {
		t.Errorf("node B should keep first occurrence (untested), got %q", idMap["B"])
	}
	if idMap["C"] != "partial" {
		t.Errorf("node C should be partial, got %q", idMap["C"])
	}
}

func TestAuditDeduplicateNodes_Empty(t *testing.T) {
	result := deduplicateNodes(nil)
	if len(result) != 0 {
		t.Errorf("deduplicateNodes(nil) should return empty, got %d", len(result))
	}
}

func TestAuditDeduplicateNodes_NoDuplicates(t *testing.T) {
	nodes := []domain.AnnotatedNode{
		{NodeID: "X", Kind: "handler"},
		{NodeID: "Y", Kind: "service"},
	}

	result := deduplicateNodes(nodes)
	if len(result) != 2 {
		t.Errorf("deduplicateNodes with no duplicates: got %d, want 2", len(result))
	}
}

func TestAuditCalculateHealthScore(t *testing.T) {
	tests := []struct {
		name   string
		layers []domain.LayerCoverage
		want   float64
	}{
		{
			name:   "empty layers",
			layers: nil,
			want:   0.0,
		},
		{
			name: "all layers 100%",
			layers: []domain.LayerCoverage{
				{Layer: "handler", Tested: 2, Total: 2, Percentage: 100.0},
				{Layer: "service", Tested: 3, Total: 3, Percentage: 100.0},
				{Layer: "repository", Tested: 1, Total: 1, Percentage: 100.0},
				{Layer: "query", Tested: 2, Total: 2, Percentage: 100.0},
			},
			want: 1.0,
		},
		{
			name: "all layers 0%",
			layers: []domain.LayerCoverage{
				{Layer: "handler", Tested: 0, Total: 2, Percentage: 0.0},
				{Layer: "service", Tested: 0, Total: 3, Percentage: 0.0},
				{Layer: "repository", Tested: 0, Total: 1, Percentage: 0.0},
				{Layer: "query", Tested: 0, Total: 2, Percentage: 0.0},
			},
			want: 0.0,
		},
		{
			name: "mixed coverage",
			layers: []domain.LayerCoverage{
				{Layer: "handler", Tested: 1, Total: 2, Percentage: 50.0},
				{Layer: "service", Tested: 3, Total: 3, Percentage: 100.0},
			},
			// handler weight=0.30, service weight=0.30
			// weighted = 0.30*(50/100) + 0.30*(100/100) = 0.15 + 0.30 = 0.45
			// totalWeight = 0.60
			// score = 0.45 / 0.60 = 0.75
			want: 0.75,
		},
		{
			name: "single handler layer at 50%",
			layers: []domain.LayerCoverage{
				{Layer: "handler", Tested: 1, Total: 2, Percentage: 50.0},
			},
			// handler weight=0.30, weighted=0.30*0.5=0.15, score=0.15/0.30=0.5
			want: 0.5,
		},
		{
			name: "unknown layer gets default weight",
			layers: []domain.LayerCoverage{
				{Layer: "handler", Tested: 2, Total: 2, Percentage: 100.0},
				{Layer: "component", Tested: 1, Total: 2, Percentage: 50.0},
			},
			// handler weight=0.30, component weight=0.05 (default)
			// weighted = 0.30*1.0 + 0.05*0.5 = 0.30 + 0.025 = 0.325
			// totalWeight = 0.35
			// score = 0.325 / 0.35 ~= 0.9286
			want: 0.325 / 0.35,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateHealthScore(tt.layers)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("calculateHealthScore() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestAuditIdentifyGaps(t *testing.T) {
	nodes := []domain.AnnotatedNode{
		{NodeID: "handler1", Kind: "handler", File: "h.go", Line: 1, TestStatus: "tested"},
		{NodeID: "handler2", Kind: "handler", File: "h2.go", Line: 2, TestStatus: "untested"},
		{NodeID: "svc1", Kind: "service", File: "s.go", Line: 10, TestStatus: "partial"},
		{NodeID: "repo1", Kind: "repository", File: "r.go", Line: 20, TestStatus: "untested"},
		{NodeID: "q1", Kind: "query", File: "", Line: 0, TestStatus: "untested"},
		{NodeID: "comp1", Kind: "component", File: "c.tsx", Line: 5, TestStatus: "untested"},
	}

	gaps := identifyGaps(nodes)

	// "tested" nodes should be excluded, so we expect 5 gaps.
	if len(gaps) != 5 {
		t.Fatalf("identifyGaps: got %d gaps, want 5", len(gaps))
	}

	// Verify ordering: critical first, then high, medium, low.
	expectedOrder := []string{"critical", "critical", "high", "medium", "low"}
	for i, expected := range expectedOrder {
		if gaps[i].Severity != expected {
			t.Errorf("gaps[%d].Severity = %q, want %q (NodeID=%s)", i, gaps[i].Severity, expected, gaps[i].NodeID)
		}
	}

	// Verify critical gaps are handler2 and svc1 (both critical).
	criticalIDs := make(map[string]bool)
	for _, g := range gaps {
		if g.Severity == "critical" {
			criticalIDs[g.NodeID] = true
		}
	}
	if !criticalIDs["handler2"] {
		t.Error("expected handler2 in critical gaps")
	}
	if !criticalIDs["svc1"] {
		t.Error("expected svc1 in critical gaps")
	}
}

func TestAuditIdentifyGaps_AllTested(t *testing.T) {
	nodes := []domain.AnnotatedNode{
		{NodeID: "A", Kind: "handler", TestStatus: "tested"},
		{NodeID: "B", Kind: "service", TestStatus: "tested"},
	}

	gaps := identifyGaps(nodes)
	if len(gaps) != 0 {
		t.Errorf("identifyGaps with all tested: got %d gaps, want 0", len(gaps))
	}
}

func TestAuditIdentifyGaps_Empty(t *testing.T) {
	gaps := identifyGaps(nil)
	if len(gaps) != 0 {
		t.Errorf("identifyGaps(nil): got %d gaps, want 0", len(gaps))
	}
}

// ---------------------------------------------------------------------------
// Integration tests using real YAML store + mock graph builder
// ---------------------------------------------------------------------------

func TestAuditExecute_SingleFeature(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Verify basic fields.
	if output.FeatureID != "auth.login" {
		t.Errorf("FeatureID = %q, want %q", output.FeatureID, "auth.login")
	}
	if output.FeatureName != "User Login" {
		t.Errorf("FeatureName = %q, want %q", output.FeatureName, "User Login")
	}
	if output.Priority != "critical" {
		t.Errorf("Priority = %q, want %q", output.Priority, "critical")
	}

	// Should have trace results.
	if len(output.TraceResults) == 0 {
		t.Error("Expected non-empty TraceResults")
	}

	// With a full graph and no test files, all nodes should be untested.
	// So we should have gaps.
	if len(output.Gaps) == 0 {
		t.Error("Expected non-empty Gaps (no test files configured)")
	}

	// Should have actions generated from gaps.
	if len(output.Actions) == 0 {
		t.Error("Expected non-empty Actions")
	}

	// Health score should be between 0 and 1.
	if output.HealthScore < 0 || output.HealthScore > 1 {
		t.Errorf("HealthScore = %f, want 0.0-1.0", output.HealthScore)
	}

	// Layer coverage should exist for layers present in graph.
	if len(output.LayerCoverage) == 0 {
		t.Error("Expected non-empty LayerCoverage")
	}
}

func TestAuditExecute_FeatureNotFound(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	builder := &mockGraphBuilder{graph: domain.NewGraph()}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	_, err := auditUC.Execute(registryDir, "nonexistent.feature", defaultConfig())
	if err == nil {
		t.Error("Execute() with nonexistent feature should return error")
	}
}

func TestAuditExecute_VerifyLayerCoverage(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Build a map of layer -> coverage for easier verification.
	layerMap := make(map[string]domain.LayerCoverage)
	for _, lc := range output.LayerCoverage {
		layerMap[lc.Layer] = lc
	}

	// Our test graph has: 1 handler, 1 service, 1 repository, 1 query.
	// All should be untested since the feature has no test files with coverage.
	for _, layer := range []string{"handler", "service", "repository"} {
		lc, ok := layerMap[layer]
		if !ok {
			t.Errorf("Expected layer coverage for %q", layer)
			continue
		}
		if lc.Total < 1 {
			t.Errorf("Layer %q: Total = %d, want >= 1", layer, lc.Total)
		}
	}
}

func TestAuditExecute_VerifyGapSeverities(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Gaps should be sorted by severity (critical first).
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	for i := 1; i < len(output.Gaps); i++ {
		prev := severityOrder[output.Gaps[i-1].Severity]
		curr := severityOrder[output.Gaps[i].Severity]
		if prev > curr {
			t.Errorf("Gaps not sorted by severity: gaps[%d].Severity=%q before gaps[%d].Severity=%q",
				i-1, output.Gaps[i-1].Severity, i, output.Gaps[i].Severity)
		}
	}
}

func TestAuditExecute_VerifyActions(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Actions should have incrementing priorities.
	for i, action := range output.Actions {
		if action.Priority != i+1 {
			t.Errorf("Actions[%d].Priority = %d, want %d", i, action.Priority, i+1)
		}
		if action.Description == "" {
			t.Errorf("Actions[%d].Description is empty", i)
		}
		if action.TestType == "" {
			t.Errorf("Actions[%d].TestType is empty", i)
		}
	}

	// auth.login has web and mobile surfaces with missing E2E, so actions should
	// include E2E suggestions.
	hasE2EAction := false
	for _, action := range output.Actions {
		if action.TestType == "e2e" {
			hasE2EAction = true
			break
		}
	}
	if !hasE2EAction {
		t.Error("Expected at least one E2E action for auth.login (has web and mobile surfaces)")
	}
}

func TestAuditExecuteAll(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	results, err := auditUC.ExecuteAll(registryDir, defaultConfig())
	if err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	// The template registry has features across auth, meals, profile domains.
	if len(results) == 0 {
		t.Fatal("ExecuteAll() returned empty results")
	}

	// Results should be sorted by health score ascending (worst first).
	for i := 1; i < len(results); i++ {
		if results[i-1].HealthScore > results[i].HealthScore {
			t.Errorf("Results not sorted by HealthScore ascending: [%d]=%f > [%d]=%f",
				i-1, results[i-1].HealthScore, i, results[i].HealthScore)
		}
	}

	// Every result should have a valid FeatureID.
	for i, r := range results {
		if r.FeatureID == "" {
			t.Errorf("results[%d].FeatureID is empty", i)
		}
	}
}

func TestAuditExecuteAll_SortOrder(t *testing.T) {
	// Create a registry with two features. Give one feature a graph that
	// produces some coverage (higher health) and another with no graph nodes
	// (lower health).
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()

	// Empty graph: features with API surfaces will have no traced nodes,
	// so health score will be 0.0 for all features (no layer coverage).
	builder := &mockGraphBuilder{graph: domain.NewGraph()}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	results, err := auditUC.ExecuteAll(registryDir, defaultConfig())
	if err != nil {
		t.Fatalf("ExecuteAll() error = %v", err)
	}

	if len(results) == 0 {
		t.Fatal("ExecuteAll() returned empty results")
	}

	// With an empty graph, all features should have 0.0 health score.
	for _, r := range results {
		if r.HealthScore != 0.0 {
			t.Errorf("Feature %q: HealthScore = %f, want 0.0 (empty graph)", r.FeatureID, r.HealthScore)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests for computeLayerCoverage
// ---------------------------------------------------------------------------

func TestAuditComputeLayerCoverage(t *testing.T) {
	nodes := []domain.AnnotatedNode{
		{NodeID: "h1", Kind: "handler", TestStatus: "tested"},
		{NodeID: "h2", Kind: "handler", TestStatus: "untested"},
		{NodeID: "s1", Kind: "service", TestStatus: "tested"},
		{NodeID: "s2", Kind: "service", TestStatus: "tested"},
		{NodeID: "s3", Kind: "service", TestStatus: "partial"},
		{NodeID: "r1", Kind: "repository", TestStatus: "untested"},
		{NodeID: "q1", Kind: "query", TestStatus: "untested"},
	}

	coverage := computeLayerCoverage(nodes)

	coverageMap := make(map[string]domain.LayerCoverage)
	for _, lc := range coverage {
		coverageMap[lc.Layer] = lc
	}

	// Handler: 1 tested, 1 untested -> 50%
	if h, ok := coverageMap["handler"]; ok {
		if h.Tested != 1 || h.Total != 2 {
			t.Errorf("handler: Tested=%d, Total=%d, want 1, 2", h.Tested, h.Total)
		}
		if math.Abs(h.Percentage-50.0) > 0.01 {
			t.Errorf("handler: Percentage=%f, want 50.0", h.Percentage)
		}
	} else {
		t.Error("missing handler layer coverage")
	}

	// Service: 2 tested + 1 partial (counts as tested) -> 3/3 = 100%
	if s, ok := coverageMap["service"]; ok {
		if s.Tested != 3 || s.Total != 3 {
			t.Errorf("service: Tested=%d, Total=%d, want 3, 3", s.Tested, s.Total)
		}
		if math.Abs(s.Percentage-100.0) > 0.01 {
			t.Errorf("service: Percentage=%f, want 100.0", s.Percentage)
		}
	} else {
		t.Error("missing service layer coverage")
	}

	// Repository: 0/1 -> 0%
	if r, ok := coverageMap["repository"]; ok {
		if r.Tested != 0 || r.Total != 1 {
			t.Errorf("repository: Tested=%d, Total=%d, want 0, 1", r.Tested, r.Total)
		}
		if r.Percentage != 0.0 {
			t.Errorf("repository: Percentage=%f, want 0.0", r.Percentage)
		}
	} else {
		t.Error("missing repository layer coverage")
	}

	// Query: 0/1 -> 0%
	if q, ok := coverageMap["query"]; ok {
		if q.Tested != 0 || q.Total != 1 {
			t.Errorf("query: Tested=%d, Total=%d, want 0, 1", q.Tested, q.Total)
		}
	} else {
		t.Error("missing query layer coverage")
	}
}

func TestAuditComputeLayerCoverage_UnknownKindSkipped(t *testing.T) {
	nodes := []domain.AnnotatedNode{
		{NodeID: "x1", Kind: "external", TestStatus: "untested"},
		{NodeID: "x2", Kind: "endpoint", TestStatus: "untested"},
	}

	coverage := computeLayerCoverage(nodes)
	if len(coverage) != 0 {
		t.Errorf("computeLayerCoverage with unknown kinds: got %d layers, want 0", len(coverage))
	}
}

// ---------------------------------------------------------------------------
// Tests for helper functions
// ---------------------------------------------------------------------------

func TestAuditSuggestTestFile(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"internal/handler/auth.go", "internal/handler/auth_test.go"},
		{"src/Login.tsx", "src/Login.test.tsx"},
		{"src/Login.ts", "src/Login.test.ts"},
		{"src/utils.js", "src/utils.test.js"},
		{"src/app.jsx", "src/app.test.jsx"},
		{"src/main.py", "src/main_test.py"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := suggestTestFile(tt.input)
			if got != tt.want {
				t.Errorf("suggestTestFile(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAuditExtractFuncName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"RecipeHandler.ListRecipes", "ListRecipes"},
		{"handler:ListRecipes", "ListRecipes"},
		{"SimpleName", "SimpleName"},
		{"pkg.Type.Method", "Method"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractFuncName(tt.input)
			if got != tt.want {
				t.Errorf("extractFuncName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAuditSuggestTestFileName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"internal/handler/auth.go", "auth_test.go"},
		{"", "<test_file>"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := suggestTestFileName(tt.input)
			if got != tt.want {
				t.Errorf("suggestTestFileName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAuditSuggestPackageDir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"internal/handler/auth.go", "internal/handler"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := suggestPackageDir(tt.input)
			if got != tt.want {
				t.Errorf("suggestPackageDir(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAuditPerfBenchmarkReason(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"handler", "handler on hot path, no benchmark"},
		{"service", "service method, no benchmark"},
		{"repository", "repository, no benchmark"},
		{"other", "no benchmark"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			got := perfBenchmarkReason(tt.kind)
			if got != tt.want {
				t.Errorf("perfBenchmarkReason(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

func TestAuditExecute_PerfScore(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// PerfScore should be non-nil.
	if output.PerfScore == nil {
		t.Fatal("PerfScore should not be nil")
	}

	// With no actual test files on disk, benchmark and race coverage should be 0.
	if output.PerfScore.BenchmarkCoverage != 0.0 {
		t.Errorf("BenchmarkCoverage = %f, want 0.0", output.PerfScore.BenchmarkCoverage)
	}
	if output.PerfScore.RaceTestCoverage != 0.0 {
		t.Errorf("RaceTestCoverage = %f, want 0.0", output.PerfScore.RaceTestCoverage)
	}
	if output.PerfScore.Overall != 0.0 {
		t.Errorf("Overall = %f, want 0.0", output.PerfScore.Overall)
	}

	// BenchmarkableNodes should count handler + service + repository = 3
	if output.PerfScore.BenchmarkableNodes != 3 {
		t.Errorf("BenchmarkableNodes = %d, want 3", output.PerfScore.BenchmarkableNodes)
	}
	// ConcurrentNodes should count handler + service = 2
	if output.PerfScore.ConcurrentNodes != 2 {
		t.Errorf("ConcurrentNodes = %d, want 2", output.PerfScore.ConcurrentNodes)
	}
}

func TestAuditExecute_PerfGaps(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()
	graph := buildAuditTestGraph()

	builder := &mockGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should have performance gaps for untested nodes.
	if len(output.PerfGaps) == 0 {
		t.Error("Expected non-empty PerfGaps")
	}

	// PerfGaps should be sorted by severity.
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	for i := 1; i < len(output.PerfGaps); i++ {
		prev := severityOrder[output.PerfGaps[i-1].Severity]
		curr := severityOrder[output.PerfGaps[i].Severity]
		if prev > curr {
			t.Errorf("PerfGaps not sorted: [%d].Severity=%q before [%d].Severity=%q",
				i-1, output.PerfGaps[i-1].Severity, i, output.PerfGaps[i].Severity)
		}
	}

	// Verify gap types include no-benchmark and no-race-test.
	gapTypes := make(map[string]bool)
	for _, g := range output.PerfGaps {
		gapTypes[g.GapType] = true
	}
	if !gapTypes["no-benchmark"] {
		t.Error("Expected no-benchmark gap type in PerfGaps")
	}
	if !gapTypes["no-race-test"] {
		t.Error("Expected no-race-test gap type in PerfGaps")
	}
}

func TestAuditExecute_EmptyGraph(t *testing.T) {
	registryDir := setupRegistryDir(t)
	store := adapters.NewYAMLStore()

	builder := &mockGraphBuilder{graph: nil}
	traceUC := NewTraceFeatureUseCase(store, builder)
	auditUC := NewAuditFeatureUseCase(traceUC, store)

	output, err := auditUC.Execute(registryDir, "auth.login", defaultConfig())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// With no graph nodes, health score should be 0.
	if output.HealthScore != 0.0 {
		t.Errorf("HealthScore with empty graph = %f, want 0.0", output.HealthScore)
	}

	// Layer coverage should be empty.
	if len(output.LayerCoverage) != 0 {
		t.Errorf("LayerCoverage with empty graph: got %d, want 0", len(output.LayerCoverage))
	}
}
