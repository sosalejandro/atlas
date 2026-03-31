// @testreg trace.graph-model
package domain

import (
	"regexp"
	"testing"
)

func makeNode(id string, kind NodeKind) *Node {
	return &Node{
		ID:   id,
		Kind: kind,
		File: id + ".go",
		Line: 1,
	}
}

func TestNewGraph(t *testing.T) {
	g := NewGraph()
	if g == nil {
		t.Fatal("NewGraph returned nil")
	}
	if len(g.Nodes) != 0 {
		t.Errorf("new graph should have 0 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 0 {
		t.Errorf("new graph should have 0 edges, got %d", len(g.Edges))
	}
}

func TestAddNode_Deduplication(t *testing.T) {
	g := NewGraph()

	n1 := makeNode("svc.Login", NodeService)
	n1.Doc = "first"

	n2 := makeNode("svc.Login", NodeService)
	n2.Doc = "second"

	g.AddNode(n1)
	g.AddNode(n2) // duplicate — should be ignored

	if len(g.Nodes) != 1 {
		t.Fatalf("expected 1 node after dedup, got %d", len(g.Nodes))
	}
	if g.Nodes["svc.Login"].Doc != "first" {
		t.Error("second AddNode should not overwrite the first")
	}
}

func TestAddNode_NilIgnored(t *testing.T) {
	g := NewGraph()
	g.AddNode(nil) // should not panic or add anything
	if len(g.Nodes) != 0 {
		t.Error("nil AddNode should not add a node")
	}
}

func TestAddEdge_Simple(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))

	g.AddEdge("A", "B")

	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	e := g.Edges[0]
	if e.From != "A" || e.To != "B" {
		t.Errorf("edge = %s->%s, want A->B", e.From, e.To)
	}
	if e.Cycle {
		t.Error("A->B should not be a cycle")
	}
}

func TestAddEdge_CycleDetection(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddNode(makeNode("C", NodeRepository))

	g.AddEdge("A", "B") // A -> B
	g.AddEdge("B", "C") // B -> C
	g.AddEdge("C", "A") // C -> A — creates cycle

	lastEdge := g.Edges[len(g.Edges)-1]
	if !lastEdge.Cycle {
		t.Error("C->A should be detected as a cycle (path A->B->C already exists)")
	}
}

func TestAddEdge_SelfLoop(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))

	g.AddEdge("A", "A") // self-loop

	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	if !g.Edges[0].Cycle {
		t.Error("self-loop should be marked as cycle")
	}
}

func TestAddAmbiguousEdge(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))

	g.AddAmbiguousEdge("A", "B")

	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	e := g.Edges[0]
	if !e.Ambiguous {
		t.Error("edge should be marked as ambiguous")
	}
	if e.Cycle {
		t.Error("non-cyclic ambiguous edge should not be a cycle")
	}
}

func TestCallees(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddNode(makeNode("C", NodeRepository))

	g.AddEdge("A", "B")
	g.AddEdge("A", "C")

	callees := g.Callees("A")
	if len(callees) != 2 {
		t.Fatalf("expected 2 callees, got %d", len(callees))
	}

	ids := make(map[string]bool)
	for _, n := range callees {
		ids[n.ID] = true
	}
	if !ids["B"] || !ids["C"] {
		t.Errorf("callees should contain B and C, got %v", ids)
	}
}

func TestCallees_Empty(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))

	callees := g.Callees("A")
	if len(callees) != 0 {
		t.Errorf("leaf node should have 0 callees, got %d", len(callees))
	}
}

func TestCallees_UnknownNode(t *testing.T) {
	g := NewGraph()
	callees := g.Callees("nonexistent")
	if len(callees) != 0 {
		t.Errorf("unknown node should have 0 callees, got %d", len(callees))
	}
}

func TestCallers(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddNode(makeNode("C", NodeRepository))

	g.AddEdge("A", "C")
	g.AddEdge("B", "C")

	callers := g.Callers("C")
	if len(callers) != 2 {
		t.Fatalf("expected 2 callers, got %d", len(callers))
	}

	ids := make(map[string]bool)
	for _, n := range callers {
		ids[n.ID] = true
	}
	if !ids["A"] || !ids["B"] {
		t.Errorf("callers should contain A and B, got %v", ids)
	}
}

func TestCallers_RootNode(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddEdge("A", "B")

	callers := g.Callers("A")
	if len(callers) != 0 {
		t.Errorf("root node should have 0 callers, got %d", len(callers))
	}
}

func TestTraceFrom_LinearChain(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("handler", NodeHandler))
	g.AddNode(makeNode("service", NodeService))
	g.AddNode(makeNode("repo", NodeRepository))

	g.AddEdge("handler", "service")
	g.AddEdge("service", "repo")

	result := g.TraceFrom("handler", 0)
	if result.Root == nil {
		t.Fatal("trace result root should not be nil")
	}
	if result.Root.Node.ID != "handler" {
		t.Errorf("root node = %s, want handler", result.Root.Node.ID)
	}
	if result.TotalNodes != 3 {
		t.Errorf("total nodes = %d, want 3", result.TotalNodes)
	}
	if result.MaxDepth != 2 {
		t.Errorf("max depth = %d, want 2", result.MaxDepth)
	}
	if len(result.Cycles) != 0 {
		t.Errorf("cycles = %d, want 0", len(result.Cycles))
	}
	if result.Confidence != 1.0 {
		t.Errorf("confidence = %f, want 1.0", result.Confidence)
	}
}

func TestTraceFrom_DepthLimit(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddNode(makeNode("C", NodeRepository))
	g.AddNode(makeNode("D", NodeQuery))

	g.AddEdge("A", "B")
	g.AddEdge("B", "C")
	g.AddEdge("C", "D")

	result := g.TraceFrom("A", 2)

	if result.MaxDepth != 2 {
		t.Errorf("max depth with limit 2 = %d, want 2", result.MaxDepth)
	}
	// Should include A (depth 0), B (depth 1), C (depth 2) but NOT D (depth 3).
	if result.TotalNodes != 3 {
		t.Errorf("total nodes with depth limit 2 = %d, want 3", result.TotalNodes)
	}
}

func TestTraceFrom_CycleHandling(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddNode(makeNode("C", NodeRepository))

	g.AddEdge("A", "B")
	g.AddEdge("B", "C")
	g.AddEdge("C", "A") // cycle back to root

	result := g.TraceFrom("A", 0)

	if len(result.Cycles) == 0 {
		t.Fatal("expected at least one cycle to be detected")
	}

	// Verify the cycle node is marked.
	found := false
	var checkCycle func(tn *TraceNode)
	checkCycle = func(tn *TraceNode) {
		if tn.IsCycle {
			found = true
		}
		for _, child := range tn.Children {
			checkCycle(child)
		}
	}
	checkCycle(result.Root)

	if !found {
		t.Error("expected at least one TraceNode with IsCycle=true")
	}
}

func TestTraceFrom_NonexistentRoot(t *testing.T) {
	g := NewGraph()

	result := g.TraceFrom("ghost", 0)

	if result.Root != nil {
		t.Error("trace from nonexistent root should have nil Root")
	}
	if result.Confidence != 0.0 {
		t.Errorf("confidence = %f, want 0.0 for missing root", result.Confidence)
	}
	if len(result.Warnings) == 0 {
		t.Error("expected a warning for missing root node")
	}
}

func TestTraceFrom_BranchingGraph(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("root", NodeHandler))
	g.AddNode(makeNode("left", NodeService))
	g.AddNode(makeNode("right", NodeService))
	g.AddNode(makeNode("leaf", NodeRepository))

	g.AddEdge("root", "left")
	g.AddEdge("root", "right")
	g.AddEdge("left", "leaf")
	g.AddEdge("right", "leaf")

	result := g.TraceFrom("root", 0)

	if result.Root == nil {
		t.Fatal("root should not be nil")
	}
	if len(result.Root.Children) != 2 {
		t.Errorf("root should have 2 children, got %d", len(result.Root.Children))
	}
	// "leaf" is visited via "left" first (alphabetical order).
	// When reached via "right", it should be marked as a cycle (already visited).
	if result.TotalNodes != 4 {
		t.Errorf("total nodes = %d, want 4", result.TotalNodes)
	}
}

func TestTraceFrom_UnknownEdgeTarget(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	// Edge to a node that is not in Nodes map.
	g.AddEdge("A", "ghost")

	result := g.TraceFrom("A", 0)

	if len(result.Warnings) == 0 {
		t.Error("expected warning about unknown node")
	}
	if result.Confidence >= 1.0 {
		t.Error("confidence should be reduced when edges reference unknown nodes")
	}
}

func TestAdjacencyCache_InvalidatedOnAddEdge(t *testing.T) {
	g := NewGraph()
	g.AddNode(makeNode("A", NodeHandler))
	g.AddNode(makeNode("B", NodeService))
	g.AddNode(makeNode("C", NodeRepository))

	g.AddEdge("A", "B")

	// Access callees to build cache.
	callees1 := g.Callees("A")
	if len(callees1) != 1 {
		t.Fatalf("expected 1 callee before second edge, got %d", len(callees1))
	}

	// Add another edge — cache should be invalidated.
	g.AddEdge("A", "C")

	callees2 := g.Callees("A")
	if len(callees2) != 2 {
		t.Errorf("expected 2 callees after second edge, got %d", len(callees2))
	}
}

// --- MatchSymptom tests ---

func TestMatchSymptom_401(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("got HTTP 401 Unauthorized from server", rules)
	if rule == nil {
		t.Fatal("expected match for 401")
	}
	if rule.Layer != "backend-auth" {
		t.Errorf("layer = %s, want backend-auth", rule.Layer)
	}
}

func TestMatchSymptom_403(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("403 Forbidden: permission denied", rules)
	if rule == nil {
		t.Fatal("expected match for 403")
	}
	if rule.Layer != "backend-auth" {
		t.Errorf("layer = %s, want backend-auth", rule.Layer)
	}
}

func TestMatchSymptom_404(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("Error: 404 Not Found", rules)
	if rule == nil {
		t.Fatal("expected match for 404")
	}
	if rule.Layer != "backend-routing" {
		t.Errorf("layer = %s, want backend-routing", rule.Layer)
	}
}

func TestMatchSymptom_500(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("500 Internal Server Error with panic in handler", rules)
	if rule == nil {
		t.Fatal("expected match for 500/panic")
	}
	if rule.Layer != "backend-bug" {
		t.Errorf("layer = %s, want backend-bug", rule.Layer)
	}
}

func TestMatchSymptom_SelectorNotFound(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("TestingLibraryElementError: getByTestId failed", rules)
	if rule == nil {
		t.Fatal("expected match for selector not found")
	}
	if rule.Layer != "frontend" {
		t.Errorf("layer = %s, want frontend", rule.Layer)
	}
}

func TestMatchSymptom_Timeout(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("context deadline exceeded after 30s", rules)
	if rule == nil {
		t.Fatal("expected match for timeout")
	}
	if rule.Layer != "infra" {
		t.Errorf("layer = %s, want infra", rule.Layer)
	}
}

func TestMatchSymptom_LoginFailed(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("Login failed: invalid credentials provided", rules)
	if rule == nil {
		t.Fatal("expected match for login failed")
	}
	if rule.Layer != "backend-auth" {
		t.Errorf("layer = %s, want backend-auth", rule.Layer)
	}
}

func TestMatchSymptom_ConnectionRefused(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("dial tcp 127.0.0.1:5432: connection refused", rules)
	if rule == nil {
		t.Fatal("expected match for connection refused")
	}
	if rule.Layer != "infra" {
		t.Errorf("layer = %s, want infra", rule.Layer)
	}
}

func TestMatchSymptom_EmptyResponse(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("unexpected empty response from API", rules)
	if rule == nil {
		t.Fatal("expected match for empty response")
	}
	if rule.Layer != "data" {
		t.Errorf("layer = %s, want data", rule.Layer)
	}
}

func TestMatchSymptom_NoMatch(t *testing.T) {
	rules := DefaultSymptomRules()
	rule := MatchSymptom("everything works perfectly", rules)
	if rule != nil {
		t.Errorf("expected nil for non-matching symptom, got layer %s", rule.Layer)
	}
}

func TestMatchSymptom_CaseInsensitive(t *testing.T) {
	rules := DefaultSymptomRules()

	tests := []struct {
		name    string
		symptom string
		layer   string
	}{
		{"uppercase UNAUTHORIZED", "HTTP UNAUTHORIZED", "backend-auth"},
		{"mixed case Forbidden", "Forbidden access", "backend-auth"},
		{"lowercase timeout", "request timed out", "infra"},
		{"uppercase ECONNREFUSED", "Error: ECONNREFUSED", "infra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := MatchSymptom(tt.symptom, rules)
			if rule == nil {
				t.Fatalf("expected match for %q", tt.symptom)
			}
			if rule.Layer != tt.layer {
				t.Errorf("layer = %s, want %s", rule.Layer, tt.layer)
			}
		})
	}
}

func TestMatchSymptom_MalformedPattern(t *testing.T) {
	rules := []SymptomRule{
		{Pattern: `[invalid`, Layer: "bad", Description: "broken regex"},
		{Pattern: `(?i)timeout`, Layer: "infra", Description: "timeout"},
	}

	rule := MatchSymptom("request timeout", rules)
	if rule == nil {
		t.Fatal("should skip malformed rule and match the second")
	}
	if rule.Layer != "infra" {
		t.Errorf("layer = %s, want infra", rule.Layer)
	}
}

func TestDefaultSymptomRules_AllPatternsCompile(t *testing.T) {
	rules := DefaultSymptomRules()
	for i, r := range rules {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			t.Errorf("rule[%d] pattern %q does not compile: %v", i, r.Pattern, err)
		}
	}
}
