// @testreg trace.frontend-scanner
package adapters

import (
	"encoding/json"
	"testing"

	"github.com/sosalejandro/testreg/internal/domain"
)

// ---------------------------------------------------------------------------
// NewFrontendScanner
// ---------------------------------------------------------------------------

func TestNewFrontendScanner_NotNil(t *testing.T) {
	s := NewFrontendScanner()
	if s == nil {
		t.Fatal("NewFrontendScanner() returned nil")
	}
}

func TestNewFrontendScanner_EmptyCache(t *testing.T) {
	s := NewFrontendScanner()
	if s.cachedResult != nil {
		t.Fatal("expected nil cachedResult on fresh scanner")
	}
	if s.cachedFor != "" {
		t.Fatalf("expected empty cachedFor, got %q", s.cachedFor)
	}
}

// ---------------------------------------------------------------------------
// Scan – graceful failure when ts-scanner.ts is not available
// ---------------------------------------------------------------------------

func TestScan_MissingScanner(t *testing.T) {
	// Ensure the env var does not point to a real file.
	t.Setenv("TESTREG_TS_SCANNER", "/nonexistent/ts-scanner.ts")

	s := NewFrontendScanner()
	result, err := s.Scan("/some/project")
	if err == nil {
		t.Fatal("expected error when ts-scanner.ts is not found, got nil")
	}
	if result != nil {
		t.Fatal("expected nil result on scanner error")
	}
}

// ---------------------------------------------------------------------------
// JSON unmarshalling of FrontendScanResult
// ---------------------------------------------------------------------------

func TestFrontendScanResult_UnmarshalJSON(t *testing.T) {
	raw := `{
		"nodes": [
			{"id": "route:/login", "kind": "route", "file": "src/pages/Login.tsx", "line": 10},
			{"id": "LoginForm", "kind": "component", "file": "src/components/LoginForm.tsx", "line": 1, "doc": "Login form component"}
		],
		"edges": [
			{"from": "route:/login", "to": "LoginForm"}
		],
		"warnings": ["could not resolve dynamic import"],
		"stats": {
			"files_scanned": 42,
			"routes_found": 5,
			"api_calls_found": 12
		}
	}`

	var result FrontendScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Nodes
	if got := len(result.Nodes); got != 2 {
		t.Fatalf("expected 2 nodes, got %d", got)
	}
	if result.Nodes[0].ID != "route:/login" {
		t.Errorf("node[0].ID = %q, want %q", result.Nodes[0].ID, "route:/login")
	}
	if result.Nodes[0].Kind != "route" {
		t.Errorf("node[0].Kind = %q, want %q", result.Nodes[0].Kind, "route")
	}
	if result.Nodes[1].Doc != "Login form component" {
		t.Errorf("node[1].Doc = %q, want %q", result.Nodes[1].Doc, "Login form component")
	}

	// Edges
	if got := len(result.Edges); got != 1 {
		t.Fatalf("expected 1 edge, got %d", got)
	}
	if result.Edges[0].From != "route:/login" || result.Edges[0].To != "LoginForm" {
		t.Errorf("edge = %+v, want from=route:/login to=LoginForm", result.Edges[0])
	}

	// Warnings
	if got := len(result.Warnings); got != 1 {
		t.Fatalf("expected 1 warning, got %d", got)
	}

	// Stats
	if result.Stats.FilesScanned != 42 {
		t.Errorf("Stats.FilesScanned = %d, want 42", result.Stats.FilesScanned)
	}
	if result.Stats.RoutesFound != 5 {
		t.Errorf("Stats.RoutesFound = %d, want 5", result.Stats.RoutesFound)
	}
	if result.Stats.APICallsFound != 12 {
		t.Errorf("Stats.APICallsFound = %d, want 12", result.Stats.APICallsFound)
	}
}

func TestFrontendScanResult_UnmarshalEmptyJSON(t *testing.T) {
	raw := `{"nodes":[],"edges":[],"warnings":[],"stats":{"files_scanned":0,"routes_found":0,"api_calls_found":0}}`

	var result FrontendScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(result.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(result.Nodes))
	}
	if len(result.Edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(result.Edges))
	}
}

// ---------------------------------------------------------------------------
// MergeIntoGraph
// ---------------------------------------------------------------------------

func TestMergeIntoGraph_AddsNodesAndEdges(t *testing.T) {
	s := NewFrontendScanner()
	graph := domain.NewGraph()

	result := &FrontendScanResult{
		Nodes: []FrontendNode{
			{ID: "route:/dashboard", Kind: "route", File: "src/pages/Dashboard.tsx", Line: 5},
			{ID: "DashboardPanel", Kind: "component", File: "src/components/DashboardPanel.tsx", Line: 1},
			{ID: "useAuth", Kind: "hook", File: "src/hooks/useAuth.ts", Line: 3},
		},
		Edges: []FrontendEdge{
			{From: "route:/dashboard", To: "DashboardPanel"},
			{From: "DashboardPanel", To: "useAuth"},
		},
	}

	s.MergeIntoGraph(graph, result)

	// Verify all 3 nodes were added.
	if got := len(graph.Nodes); got != 3 {
		t.Fatalf("expected 3 nodes in graph, got %d", got)
	}

	// Verify node kinds are mapped correctly.
	wantKinds := map[string]domain.NodeKind{
		"route:/dashboard": domain.NodeEndpoint,
		"DashboardPanel":   domain.NodeComponent,
		"useAuth":          domain.NodeHook,
	}
	for id, wantKind := range wantKinds {
		node, ok := graph.Nodes[id]
		if !ok {
			t.Errorf("node %q not found in graph", id)
			continue
		}
		if node.Kind != wantKind {
			t.Errorf("node %q kind = %q, want %q", id, node.Kind, wantKind)
		}
	}

	// Verify edges were added.
	if got := len(graph.Edges); got != 2 {
		t.Fatalf("expected 2 edges, got %d", got)
	}
}

func TestMergeIntoGraph_SkipsDuplicateEndpoints(t *testing.T) {
	s := NewFrontendScanner()
	graph := domain.NewGraph()

	// Pre-populate backend endpoint node.
	graph.AddNode(&domain.Node{
		ID:   "POST /api/v1/auth/login",
		Kind: domain.NodeEndpoint,
		File: "internal/handlers/auth.go",
		Line: 42,
	})

	result := &FrontendScanResult{
		Nodes: []FrontendNode{
			// Frontend discovers the same endpoint — should be skipped.
			{ID: "POST /api/v1/auth/login", Kind: "endpoint", File: "src/api/auth.ts", Line: 10},
			// A new frontend-only component — should be added.
			{ID: "LoginButton", Kind: "component", File: "src/components/LoginButton.tsx", Line: 1},
		},
	}

	s.MergeIntoGraph(graph, result)

	// Endpoint node should still have the backend file/line info (not overwritten).
	endpointNode := graph.Nodes["POST /api/v1/auth/login"]
	if endpointNode == nil {
		t.Fatal("endpoint node missing from graph")
	}
	if endpointNode.File != "internal/handlers/auth.go" {
		t.Errorf("endpoint File = %q, want backend file %q", endpointNode.File, "internal/handlers/auth.go")
	}
	if endpointNode.Line != 42 {
		t.Errorf("endpoint Line = %d, want 42", endpointNode.Line)
	}

	// LoginButton should have been added.
	if _, ok := graph.Nodes["LoginButton"]; !ok {
		t.Error("LoginButton node not found in graph")
	}

	// Total: 2 nodes (backend endpoint preserved + new component).
	if got := len(graph.Nodes); got != 2 {
		t.Errorf("expected 2 nodes, got %d", got)
	}
}

func TestMergeIntoGraph_UnknownKindDefaultsToService(t *testing.T) {
	s := NewFrontendScanner()
	graph := domain.NewGraph()

	result := &FrontendScanResult{
		Nodes: []FrontendNode{
			{ID: "mystery-node", Kind: "unknown-kind", File: "src/mystery.ts", Line: 1},
		},
	}

	s.MergeIntoGraph(graph, result)

	node, ok := graph.Nodes["mystery-node"]
	if !ok {
		t.Fatal("mystery-node not found in graph")
	}
	if node.Kind != domain.NodeService {
		t.Errorf("unknown kind mapped to %q, want %q", node.Kind, domain.NodeService)
	}
}

func TestMergeIntoGraph_AllKindsMapped(t *testing.T) {
	s := NewFrontendScanner()
	graph := domain.NewGraph()

	result := &FrontendScanResult{
		Nodes: []FrontendNode{
			{ID: "n-route", Kind: "route", File: "a.ts", Line: 1},
			{ID: "n-component", Kind: "component", File: "b.ts", Line: 1},
			{ID: "n-hook", Kind: "hook", File: "c.ts", Line: 1},
			{ID: "n-api-service", Kind: "api-service", File: "d.ts", Line: 1},
			{ID: "n-endpoint", Kind: "endpoint", File: "e.ts", Line: 1},
		},
	}

	s.MergeIntoGraph(graph, result)

	expected := map[string]domain.NodeKind{
		"n-route":       domain.NodeEndpoint,
		"n-component":   domain.NodeComponent,
		"n-hook":        domain.NodeHook,
		"n-api-service": domain.NodeService,
		"n-endpoint":    domain.NodeEndpoint,
	}

	for id, wantKind := range expected {
		node, ok := graph.Nodes[id]
		if !ok {
			t.Errorf("node %q not found", id)
			continue
		}
		if node.Kind != wantKind {
			t.Errorf("node %q: kind = %q, want %q", id, node.Kind, wantKind)
		}
	}
}

func TestMergeIntoGraph_EmptyResult(t *testing.T) {
	s := NewFrontendScanner()
	graph := domain.NewGraph()

	result := &FrontendScanResult{}
	s.MergeIntoGraph(graph, result)

	if got := len(graph.Nodes); got != 0 {
		t.Errorf("expected 0 nodes after empty merge, got %d", got)
	}
	if got := len(graph.Edges); got != 0 {
		t.Errorf("expected 0 edges after empty merge, got %d", got)
	}
}
