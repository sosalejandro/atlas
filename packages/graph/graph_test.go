package graph

import (
	"encoding/json"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func newNode(id shared.SymbolID, kind shared.SymbolKind) *Node {
	return &Node{
		Symbol: shared.Symbol{
			ID:       id,
			Kind:     kind,
			Position: shared.FilePosition{Path: string(id) + ".go", Line: 1},
		},
	}
}

func TestAddNode_Dedupes(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddNode(newNode("A", shared.KindHandler))
	g.AddNode(newNode("A", shared.KindService)) // duplicate ID; ignored
	g.AddNode(nil)
	g.AddNode(&Node{}) // empty ID; ignored

	if got := len(g.Nodes); got != 1 {
		t.Fatalf("expected 1 node, got %d", got)
	}
	if g.Nodes["A"].Kind != shared.KindHandler {
		t.Fatalf("first-write-wins violated; got kind=%s", g.Nodes["A"].Kind)
	}
}

func TestAddEdge_CycleDetected(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddNode(newNode("A", shared.KindHandler))
	g.AddNode(newNode("B", shared.KindService))
	g.AddNode(newNode("C", shared.KindRepository))

	g.AddEdge("A", "B")
	g.AddEdge("B", "C")
	g.AddEdge("C", "A") // closes A→B→C→A

	if len(g.Edges) != 3 {
		t.Fatalf("expected 3 edges, got %d", len(g.Edges))
	}
	last := g.Edges[2]
	if !last.Cycle {
		t.Fatalf("expected C→A to be marked Cycle; got %+v", last)
	}
	// Earlier edges should not be marked cycle.
	if g.Edges[0].Cycle || g.Edges[1].Cycle {
		t.Fatalf("non-cycle edges incorrectly flagged: %+v", g.Edges)
	}
}

func TestAddAmbiguousEdge(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddNode(newNode("X", shared.KindHandler))
	g.AddNode(newNode("Y", shared.KindService))
	g.AddAmbiguousEdge("X", "Y")

	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	if !g.Edges[0].Ambiguous {
		t.Fatalf("expected Ambiguous=true; got %+v", g.Edges[0])
	}
}

func TestOutIn_ReturnEdges(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddNode(newNode("A", shared.KindHandler))
	g.AddNode(newNode("B", shared.KindService))
	g.AddNode(newNode("C", shared.KindRepository))
	g.AddEdge("A", "B")
	g.AddEdge("A", "C")
	g.AddEdge("B", "C")

	if got := g.Out("A"); len(got) != 2 {
		t.Fatalf("expected 2 outgoing from A, got %d", len(got))
	}
	if got := g.In("C"); len(got) != 2 {
		t.Fatalf("expected 2 incoming to C, got %d", len(got))
	}
	if got := g.Out("Z"); got != nil {
		t.Fatalf("expected nil for unknown node, got %v", got)
	}
}

func TestTraceFrom_DepthAndCycleHandling(t *testing.T) {
	t.Parallel()

	g := New()
	for _, id := range []shared.SymbolID{"A", "B", "C", "D"} {
		g.AddNode(newNode(id, shared.KindService))
	}
	g.AddEdge("A", "B")
	g.AddEdge("B", "C")
	g.AddEdge("C", "D")
	g.AddEdge("D", "B") // cycle B→C→D→B

	res := g.TraceFrom("A", 0)
	if res.Root == nil || res.Root.Node.ID != "A" {
		t.Fatalf("expected root A; got %+v", res.Root)
	}
	if res.TotalNodes != 4 {
		t.Fatalf("expected 4 visited, got %d", res.TotalNodes)
	}
	if res.Confidence != 1.0 {
		t.Fatalf("expected confidence 1.0 with all known nodes; got %f", res.Confidence)
	}
}

func TestTraceFrom_UnknownRoot(t *testing.T) {
	t.Parallel()

	g := New()
	res := g.TraceFrom("missing", 0)
	if res.Root != nil {
		t.Fatalf("expected nil root for missing node")
	}
	if res.Confidence != 0 {
		t.Fatalf("expected zero confidence; got %f", res.Confidence)
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected warning for missing root")
	}
}

func TestFindPathTo_PrefersRouteHint(t *testing.T) {
	t.Parallel()

	g := New()
	for _, id := range []shared.SymbolID{
		"route:GET /a", "route:GET /b", "Handler.A", "Handler.B", "Svc.Do",
	} {
		g.AddNode(newNode(id, shared.KindEndpoint))
	}
	g.AddEdge("route:GET /a", "Handler.A")
	g.AddEdge("route:GET /b", "Handler.B")
	g.AddEdge("Handler.A", "Svc.Do")
	g.AddEdge("Handler.B", "Svc.Do")

	path := g.FindPathTo("Svc.Do", "/b", 10)
	if len(path) == 0 {
		t.Fatalf("expected non-empty path")
	}
	if path[0] != "route:GET /b" {
		t.Fatalf("expected route hint /b to win; got root=%s", path[0])
	}
}

func TestTraceCallersFrom(t *testing.T) {
	t.Parallel()

	g := New()
	for _, id := range []shared.SymbolID{"Root", "Mid", "Leaf"} {
		g.AddNode(newNode(id, shared.KindService))
	}
	g.AddEdge("Root", "Mid")
	g.AddEdge("Mid", "Leaf")

	chains := g.TraceCallersFrom("Leaf", 10)
	if len(chains) == 0 {
		t.Fatalf("expected at least one chain")
	}
}

func TestMergeNode_RetargetsEdges(t *testing.T) {
	t.Parallel()

	g := New()
	placeholder := newNode("placeholder.Login", shared.KindHandler)
	placeholder.Position = shared.FilePosition{} // unresolved
	g.AddNode(placeholder)
	g.AddNode(newNode("Endpoint", shared.KindEndpoint))
	g.AddEdge("Endpoint", "placeholder.Login")

	resolved := newNode("AuthHandler.Login", shared.KindHandler)
	resolved.Position = shared.FilePosition{Path: "auth/handler.go", Line: 42}
	g.MergeNode("placeholder.Login", resolved)

	if _, ok := g.Nodes["placeholder.Login"]; ok {
		t.Fatalf("placeholder should be deleted")
	}
	if _, ok := g.Nodes["AuthHandler.Login"]; !ok {
		t.Fatalf("resolved node missing")
	}
	if g.Edges[0].To != "AuthHandler.Login" {
		t.Fatalf("expected edge retargeted; got %+v", g.Edges[0])
	}
}

func TestGraph_JSONShape(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddNode(newNode("A", shared.KindHandler))
	g.AddNode(newNode("B", shared.KindService))
	g.AddEdge("A", "B")

	data, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var roundTrip struct {
		Nodes map[string]json.RawMessage `json:"nodes"`
		Edges []Edge                     `json:"edges"`
	}
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(roundTrip.Nodes) != 2 || len(roundTrip.Edges) != 1 {
		t.Fatalf("unexpected JSON shape: %s", string(data))
	}
}

func TestInvalidateAdjacency_RebuildsOnNextWalk(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddNode(newNode("A", shared.KindHandler))
	g.AddNode(newNode("B", shared.KindService))
	g.AddEdge("A", "B")
	_ = g.Out("A") // builds adjacency

	// Mutate edges directly and invalidate.
	g.Edges = nil
	g.InvalidateAdjacency()
	if got := g.Out("A"); got != nil {
		t.Fatalf("expected nil after invalidation+empty edges; got %v", got)
	}
}
