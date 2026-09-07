package graph

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Issue #150: AddEdge used to rebuild the whole adjacency map on every
// call, so building an E-edge graph cost O(E^2) map inserts and threw away
// E maps for the collector to reclaim. The profile in docs/performance.md
// put 36.78% of scan CPU in hasPath, the single caller.
//
// These tests pin the two halves of the fix that can silently come undone:
// that the cost per edge no longer scales with the graph, and that the
// incrementally-maintained map still answers exactly what a from-scratch
// rebuild would. The second half matters more than the first — Edge.Cycle
// is serialised into the golden corpus, so a cache that drifts from
// g.Edges does not fail loudly, it fails as a diff nobody can explain.

// sinkID names a node with no outgoing edges. A cycle-check DFS starting at
// a sink terminates on its first step, so a graph built entirely out of
// into-the-sink edges isolates the cost of MAINTAINING the adjacency map
// from the cost of walking it. That separation is the point: the defect was
// in the map, not the walk.
func sinkID(i int) shared.SymbolID { return shared.SymbolID(fmt.Sprintf("sink%05d", i)) }

func srcID(i int) shared.SymbolID { return shared.SymbolID(fmt.Sprintf("src%05d", i)) }

// fanInGraph returns a graph of n edges, every one of them src_i -> sink_j
// with a distinct src per edge. No sink has an outgoing edge, so no edge in
// it is ever a cycle.
//
// Distinct sources on purpose: a rebuild allocates one adjacency slice per
// From key, so a graph that reuses a fixed handful of sources hides most of
// the quadratic cost behind Go's amortised slice growth. A real call graph
// has thousands of callers (4,999 on this repository), and this shape is
// what makes the rebuild visibly linear in E.
func fanInGraph(n int) *Graph {
	g := New()
	for i := 0; i < n; i++ {
		g.AddEdge(srcID(i), sinkID(i%64))
	}
	return g
}

// mallocsAddingEdges reports the mallocs charged to adding probe edges to a
// graph that already holds n, per edge added.
//
// Mallocs rather than wall time because the count is exact: it does not
// move with machine load, and it is the quantity the defect actually
// scaled — one fresh adjacency map plus one slice append per existing edge,
// on every call. A timing assertion here would be a flake generator.
func mallocsAddingEdges(t *testing.T, n, probe int) float64 {
	t.Helper()
	g := fanInGraph(n)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < probe; i++ {
		g.AddEdge(srcID(n+i), sinkID(i%64))
	}
	runtime.ReadMemStats(&after)

	return float64(after.Mallocs-before.Mallocs) / float64(probe)
}

// TestAddEdge_CostPerEdgeDoesNotScaleWithGraphSize is the O(E^2) guard.
//
// An eight-fold larger graph must not make one more edge eight times more
// expensive. Before the fix the per-edge malloc count tracked the edge
// count almost exactly; after it, it is flat.
func TestAddEdge_CostPerEdgeDoesNotScaleWithGraphSize(t *testing.T) {
	const probe = 200
	small := mallocsAddingEdges(t, 1_000, probe)
	large := mallocsAddingEdges(t, 8_000, probe)

	// The slack is deliberately generous: the claim under test is "flat,
	// not linear in E", and an 8x graph that clears a 2x+8 bar cannot be
	// linear in E.
	if limit := small*2 + 8; large > limit {
		t.Fatalf("per-edge mallocs scale with graph size: %.1f at 1k edges, %.1f at 8k (limit %.1f)\n"+
			"AddEdge is rebuilding the adjacency map instead of extending it (issue #150)",
			small, large, limit)
	}
}

// TestAddEdge_KeepsAdjacencyWarm states the mechanism directly. AddEdge
// used to call invalidateAdjacency and drop both maps on the floor, so the
// next walk paid for a full rebuild as well. The maps must survive an
// append, and they must already contain it.
func TestAddEdge_KeepsAdjacencyWarm(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddEdge("A", "B")
	if g.outgoing == nil || g.incoming == nil {
		t.Fatal("AddEdge dropped the adjacency maps; every later walk pays a rebuild")
	}
	g.AddEdge("B", "C")
	if got := g.outgoing["B"]; len(got) != 1 || got[0] != "C" {
		t.Fatalf("outgoing[B] = %v, want [C] — the append did not reach the cache", got)
	}
	if got := g.incoming["C"]; len(got) != 1 || got[0] != "B" {
		t.Fatalf("incoming[C] = %v, want [B]", got)
	}
}

// TestAddEdge_SelfEdgeOnFreshGraph pins the one path that skips the walk.
//
// hasPath short-circuits when src == dst, so a self-edge on a graph whose
// maps have never been built never reaches buildAdjacency through that
// route. AddEdge appends to both maps unconditionally afterwards, and
// appending to a nil map panics — which is why AddEdge warms them itself
// rather than relying on the walk to do it.
func TestAddEdge_SelfEdgeOnFreshGraph(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddEdge("A", "A")

	if len(g.Edges) != 1 || !g.Edges[0].Cycle {
		t.Fatalf("self-edge should be flagged as a cycle; got %+v", g.Edges)
	}
	if got := g.outgoing["A"]; len(got) != 1 || got[0] != "A" {
		t.Fatalf("outgoing[A] = %v, want [A]", got)
	}
	if got := g.incoming["A"]; len(got) != 1 || got[0] != "A" {
		t.Fatalf("incoming[A] = %v, want [A]", got)
	}
}

// TestAdjacency_MatchesFullRebuild is the drift detector.
//
// It drives a graph through every mutation the scanners actually perform —
// appends, a placeholder merge that retargets edges, and a prune that
// rewrites g.Edges directly — and after each one compares the live maps
// against what building from scratch would produce. A cache is only worth
// having if it is indistinguishable from the thing it replaces.
func TestAdjacency_MatchesFullRebuild(t *testing.T) {
	t.Parallel()

	assertMatches := func(t *testing.T, g *Graph, step string) {
		t.Helper()
		g.buildAdjacency()
		gotOut, gotIn := g.outgoing, g.incoming

		fresh := &Graph{Nodes: g.Nodes, Edges: g.Edges}
		fresh.buildAdjacency()

		if !sameAdjacency(gotOut, fresh.outgoing) {
			t.Fatalf("after %s: outgoing drifted\n got %v\nwant %v", step, gotOut, fresh.outgoing)
		}
		if !sameAdjacency(gotIn, fresh.incoming) {
			t.Fatalf("after %s: incoming drifted\n got %v\nwant %v", step, gotIn, fresh.incoming)
		}
	}

	g := New()
	for _, id := range []shared.SymbolID{"A", "B", "C", "D", "placeholder"} {
		g.AddNode(&Node{Symbol: shared.Symbol{ID: id}})
	}
	g.AddEdge("A", "B")
	g.AddEdge("B", "C")
	g.AddEdge("C", "placeholder")
	assertMatches(t, g, "appends")

	// The scanner's placeholder resolution: edges pointing at the
	// placeholder are retargeted in place.
	g.MergeNode("placeholder", &Node{Symbol: shared.Symbol{ID: "D"}})
	assertMatches(t, g, "MergeNode retarget")

	g.AddEdge("D", "A")
	assertMatches(t, g, "append after merge")

	// The scanner's prune: g.Edges is rewritten wholesale and the caller
	// invalidates. See codeindex/go/scanner.go removePlaceholder.
	g.Edges = g.Edges[:2]
	g.InvalidateAdjacency()
	assertMatches(t, g, "prune + InvalidateAdjacency")

	g.AddEdge("A", "D")
	assertMatches(t, g, "append after prune")
}

func sameAdjacency(a, b map[shared.SymbolID][]shared.SymbolID) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		// Order, not just membership: dfs walks these slices in order,
		// so two adjacency lists holding the same members in a different
		// order are two different cycle reports (issue #120).
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
	}
	return true
}

// TestAddEdge_CycleFlagSurvivesPruneWithoutInvalidate is the ugly case.
//
// The documented contract is that a caller who rewrites g.Edges by hand
// calls InvalidateAdjacency afterwards, and every caller in this repository
// does. But before #150 the cycle check read g.Edges directly on every
// call, so a caller who forgot still got a correct answer; caching without
// a staleness check would quietly turn that omission into a wrong
// Edge.Cycle — a golden-corpus diff with no visible cause.
//
// Length is the cheap tell, and it catches the prune shape, which is the
// only direct-mutation shape in the tree.
func TestAddEdge_CycleFlagSurvivesPruneWithoutInvalidate(t *testing.T) {
	t.Parallel()

	g := New()
	g.AddEdge("A", "B")
	g.AddEdge("B", "C")

	// Drop B->C by hand and neglect to invalidate.
	g.Edges = g.Edges[:1]

	// C->A closes nothing now: without B->C there is no path from C back
	// to A. A stale cache would still see one and flag the edge.
	g.AddEdge("C", "A")
	if g.Edges[len(g.Edges)-1].Cycle {
		t.Fatal("C->A flagged as a cycle from a stale adjacency map; " +
			"Edge.Cycle is serialised into the golden corpus")
	}
}
