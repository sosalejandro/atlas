// @testreg trace.graph-renderer
package adapters

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/internal/domain"
)

// --- fixtures ---

func newTestTraceResult() *domain.TraceResult {
	root := &domain.TraceNode{
		Node: &domain.Node{
			ID:   "handler.GetUser",
			Kind: domain.NodeHandler,
			File: "internal/handler/user.go",
			Line: 42,
		},
		Depth: 0,
		Children: []*domain.TraceNode{
			{
				Node: &domain.Node{
					ID:   "svc.UserService.Get",
					Kind: domain.NodeService,
					File: "internal/svc/user.go",
					Line: 15,
				},
				Depth: 1,
				Children: []*domain.TraceNode{
					{
						Node: &domain.Node{
							ID:   "repo.UserRepo.FindByID",
							Kind: domain.NodeRepository,
							File: "internal/repo/user.go",
							Line: 30,
						},
						Depth: 2,
					},
				},
			},
		},
	}

	return &domain.TraceResult{
		Root:       root,
		TotalNodes: 3,
		MaxDepth:   2,
		Confidence: 0.95,
	}
}

func newGraphRenderer() *GraphRenderer {
	return &GraphRenderer{color: false}
}

// --- RenderTrace (terminal tree) ---

func TestGraphRendererRenderTrace(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	result := newTestTraceResult()
	r.RenderTrace(result, &buf)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"root node", "handler.GetUser"},
		{"child node", "svc.UserService.Get"},
		{"leaf node", "repo.UserRepo.FindByID"},
		{"confidence", "95%"},
		{"nodes count", "Nodes: 3"},
		{"depth", "Depth: 2"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderTrace missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestGraphRendererRenderTraceNil(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	r.RenderTrace(nil, &buf)
	got := buf.String()
	if !strings.Contains(got, "(empty trace)") {
		t.Error("nil trace should render (empty trace)")
	}
}

func TestGraphRendererRenderTraceNilRoot(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	r.RenderTrace(&domain.TraceResult{}, &buf)
	got := buf.String()
	if !strings.Contains(got, "(empty trace)") {
		t.Error("nil root should render (empty trace)")
	}
}

// --- RenderDOT ---

func TestGraphRendererRenderDOT(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	result := newTestTraceResult()
	r.RenderDOT(result, &buf)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"digraph", "digraph trace {"},
		{"rankdir", "rankdir=TB"},
		{"root node", "handler.GetUser"},
		{"service node", "svc.UserService.Get"},
		{"repo node", "repo.UserRepo.FindByID"},
		{"edge arrow", "->"},
		{"closing brace", "}"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderDOT missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestGraphRendererRenderDOTEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	r.RenderDOT(nil, &buf)
	got := buf.String()
	if !strings.Contains(got, "digraph trace { }") {
		t.Error("empty DOT should render minimal digraph")
	}
}

// --- RenderMermaid ---

func TestGraphRendererRenderMermaid(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	result := newTestTraceResult()
	r.RenderMermaid(result, &buf)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"flowchart directive", "flowchart TD"},
		{"root node (safe id)", "handler_GetUser"},
		{"service node (safe id)", "svc_UserService_Get"},
		{"edge arrow", "-->"},
		{"class definitions", "classDef handler"},
		{"classDef service", "classDef service"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderMermaid missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestGraphRendererRenderMermaidEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	r.RenderMermaid(nil, &buf)
	got := buf.String()
	if !strings.Contains(got, "flowchart TD") {
		t.Error("empty Mermaid should render flowchart TD")
	}
}

// --- RenderJSON ---

func TestGraphRendererRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	result := newTestTraceResult()
	r.RenderJSON(result, &buf)

	raw := buf.Bytes()
	if !json.Valid(raw) {
		t.Error("output is not valid JSON")
	}

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"root id", "handler.GetUser"},
		{"child id", "svc.UserService.Get"},
		{"total_nodes", `"total_nodes": 3`},
		{"max_depth", `"max_depth": 2`},
		{"confidence", `"confidence": 0.95`},
		{"kind", `"kind": "handler"`},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderJSON missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestGraphRendererRenderJSONNil(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	r.RenderJSON(nil, &buf)
	got := strings.TrimSpace(buf.String())
	if got != "{}" {
		t.Errorf("nil result should render {}, got %q", got)
	}
}

// --- RenderDiagnosis ---

func TestGraphRendererRenderDiagnosis(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	rule := &domain.SymptomRule{
		Pattern:     `(?i)401`,
		Layer:       "backend-auth",
		Description: "Authentication failure",
		CheckOrder:  []string{"handler", "service"},
	}

	trace := newTestTraceResult()
	r.RenderDiagnosis("auth.login", "401 Unauthorized", rule, trace, &buf)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"report header", "Diagnosis Report"},
		{"feature", "auth.login"},
		{"symptom", "401 Unauthorized"},
		{"matched rule", "Best Match"},
		{"layer", "backend-auth"},
		{"description", "Authentication failure"},
		{"check order", "handler -> service"},
		{"dependency trace", "Dependency Trace"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderDiagnosis missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestGraphRendererRenderDiagnosisNoRule(t *testing.T) {
	var buf bytes.Buffer
	r := newGraphRenderer()

	r.RenderDiagnosis("auth.login", "unknown symptom", nil, nil, &buf)

	got := buf.String()
	if !strings.Contains(got, "No matching diagnostic rule") {
		t.Error("missing 'no matching rule' message")
	}
	if !strings.Contains(got, "No dependency trace available") {
		t.Error("missing 'no trace' message")
	}
}

// --- Helper: visibleLength ---

func TestVisibleLength(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello", 5},
		{"", 0},
		{"\033[31mred\033[0m", 3},
		{"\033[1m\033[32mgreen bold\033[0m\033[0m", 10},
		{"no color here", 13},
	}

	for _, tt := range tests {
		got := visibleLength(tt.input)
		if got != tt.want {
			t.Errorf("visibleLength(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

// --- Helper: mermaidSafe ---

func TestMermaidSafe(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"handler.GetUser", "handler_GetUser"},
		{"svc/user", "svc_user"},
		{"simple", "simple"},
		{"a.b-c:d", "a_b_c_d"},
	}

	for _, tt := range tests {
		got := mermaidSafe(tt.input)
		if got != tt.want {
			t.Errorf("mermaidSafe(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Helper: dotSafe ---

func TestDotSafe(t *testing.T) {
	got := dotSafe("handler.GetUser")
	if got != `"handler.GetUser"` {
		t.Errorf("dotSafe(%q) = %q, want quoted string", "handler.GetUser", got)
	}
}
