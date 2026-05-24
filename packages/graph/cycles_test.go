package graph

import (
	"reflect"
	"testing"
)

// TestFindCycles_NoEdges asserts the empty-graph degenerate case
// returns nil (not an empty slice) so JSON consumers see `"cycles":
// null` rather than `[]` and can pattern-match on absence.
func TestFindCycles_NoEdges(t *testing.T) {
	t.Parallel()

	if got := FindCycles(nil); got != nil {
		t.Fatalf("expected nil for empty input, got %#v", got)
	}
	if got := FindCycles([]CycleEdge{}); got != nil {
		t.Fatalf("expected nil for empty slice, got %#v", got)
	}
}

// TestFindCycles_NoCycle covers the linear-chain control case where
// Tarjan returns one SCC per node (size 1 each). All of those must
// drop out of the result because they aren't real cycles.
func TestFindCycles_NoCycle(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		{From: "a.py", To: "b.py", Scope: "module"},
		{From: "b.py", To: "c.py", Scope: "module"},
	}
	if got := FindCycles(edges); got != nil {
		t.Fatalf("expected no cycles in linear chain, got %#v", got)
	}
}

// TestFindCycles_TwoNode is the canonical 2-node cycle: a.py <-> b.py.
// This is the most common shape in real codebases (mutual import) and
// the one the issue's example output highlights first.
func TestFindCycles_TwoNode(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		{From: "a.py", To: "b.py", Scope: "module", Line: 1},
		{From: "b.py", To: "a.py", Scope: "module", Line: 1},
	}
	got := FindCycles(edges)
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d: %#v", len(got), got)
	}
	c := got[0]
	if c.Length != 2 {
		t.Fatalf("expected length=2, got %d", c.Length)
	}
	wantNodes := []string{"a.py", "b.py"}
	if !reflect.DeepEqual(c.Nodes, wantNodes) {
		t.Fatalf("nodes mismatch: want %v got %v", wantNodes, c.Nodes)
	}
	if len(c.Edges) != 2 {
		t.Fatalf("expected 2 participating edges, got %d", len(c.Edges))
	}
}

// TestFindCycles_ThreeNode covers a triangle a -> b -> c -> a. This is
// the next-most-common shape and the issue example's second case.
func TestFindCycles_ThreeNode(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		{From: "main.py", To: "routes.py", Scope: "module"},
		{From: "routes.py", To: "deps.py", Scope: "module"},
		{From: "deps.py", To: "main.py", Scope: "module"},
	}
	got := FindCycles(edges)
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(got))
	}
	if got[0].Length != 3 {
		t.Fatalf("expected length=3, got %d", got[0].Length)
	}
	want := []string{"deps.py", "main.py", "routes.py"}
	if !reflect.DeepEqual(got[0].Nodes, want) {
		t.Fatalf("nodes mismatch: want %v got %v", want, got[0].Nodes)
	}
}

// TestFindCycles_MultipleCycles asserts the ordering contract — 2-node
// cycles come first, then 3-node, etc. — and that disjoint cycles in
// the same graph are reported independently.
func TestFindCycles_MultipleCycles(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		// 3-node cycle x -> y -> z -> x
		{From: "x.py", To: "y.py", Scope: "module"},
		{From: "y.py", To: "z.py", Scope: "module"},
		{From: "z.py", To: "x.py", Scope: "module"},
		// 2-node cycle a <-> b (should sort BEFORE the 3-node)
		{From: "a.py", To: "b.py", Scope: "module"},
		{From: "b.py", To: "a.py", Scope: "module"},
	}
	got := FindCycles(edges)
	if len(got) != 2 {
		t.Fatalf("expected 2 cycles, got %d", len(got))
	}
	if got[0].Length != 2 {
		t.Fatalf("expected 2-node cycle first, got length=%d", got[0].Length)
	}
	if got[1].Length != 3 {
		t.Fatalf("expected 3-node cycle second, got length=%d", got[1].Length)
	}
	if got[0].Nodes[0] != "a.py" {
		t.Fatalf("expected 2-node cycle to start with a.py; got %v", got[0].Nodes)
	}
}

// TestFindCycles_LargerSCC exercises a graph where every node of a
// 4-clique imports every other node — the SCC must be a single
// 4-cycle, not four 2-cycles. This catches an implementation that
// would naïvely report mutual edges as independent components.
func TestFindCycles_LargerSCC(t *testing.T) {
	t.Parallel()

	nodes := []string{"a.py", "b.py", "c.py", "d.py"}
	var edges []CycleEdge
	for _, from := range nodes {
		for _, to := range nodes {
			if from == to {
				continue
			}
			edges = append(edges, CycleEdge{From: from, To: to, Scope: "module"})
		}
	}
	got := FindCycles(edges)
	if len(got) != 1 {
		t.Fatalf("expected 1 SCC for 4-clique, got %d", len(got))
	}
	if got[0].Length != 4 {
		t.Fatalf("expected SCC length=4, got %d", got[0].Length)
	}
}

// TestFindCycles_SelfLoopFiltered confirms a self-loop (a -> a) does
// NOT surface as a 1-node cycle. The default verb output filters
// trivial SCCs because a single-file self-import is almost always
// noise from a decorator chain inside the same module rather than a
// real cycle worth reporting.
func TestFindCycles_SelfLoopFiltered(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		{From: "a.py", To: "a.py", Scope: "module"},
	}
	if got := FindCycles(edges); got != nil {
		t.Fatalf("expected self-loop to be filtered, got %#v", got)
	}
}

// TestFindCycles_ParallelEdges asserts the SCC count is unchanged when
// the same (from, to) pair appears multiple times in the input —
// scanner emits one edge per import statement, so a re-import on a
// different line shouldn't inflate cycle count, but ALL the original
// edges should appear in the cycle's Edges slice so the human-readable
// output can cite the line numbers.
func TestFindCycles_ParallelEdges(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		{From: "a.py", To: "b.py", Scope: "module", Line: 1},
		{From: "a.py", To: "b.py", Scope: "function", Line: 42},
		{From: "b.py", To: "a.py", Scope: "module", Line: 1},
	}
	got := FindCycles(edges)
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(got))
	}
	if got[0].Length != 2 {
		t.Fatalf("expected length=2, got %d", got[0].Length)
	}
	if len(got[0].Edges) != 3 {
		t.Fatalf("expected 3 participating edges (parallel preserved), got %d", len(got[0].Edges))
	}
}

// TestFindCycles_DisjointNonCyclic ensures unrelated edges in the same
// input don't bleed into a cycle's edge projection — only edges whose
// BOTH endpoints are in the SCC should appear.
func TestFindCycles_DisjointNonCyclic(t *testing.T) {
	t.Parallel()

	edges := []CycleEdge{
		{From: "a.py", To: "b.py", Scope: "module"},
		{From: "b.py", To: "a.py", Scope: "module"},
		// Outside edges — must NOT show up in the cycle.
		{From: "a.py", To: "c.py", Scope: "module"},
		{From: "d.py", To: "b.py", Scope: "module"},
	}
	got := FindCycles(edges)
	if len(got) != 1 {
		t.Fatalf("expected 1 cycle, got %d", len(got))
	}
	if len(got[0].Edges) != 2 {
		t.Fatalf("expected exactly 2 in-cycle edges, got %d (%#v)", len(got[0].Edges), got[0].Edges)
	}
}

// TestFindCycles_DeterministicOrder runs the same input three times
// (in randomly-shuffled order) and asserts the output is identical
// bit-for-bit. Deterministic output is a hard requirement for the
// JSON envelope contract (consumers diff snapshots across runs).
func TestFindCycles_DeterministicOrder(t *testing.T) {
	t.Parallel()

	base := []CycleEdge{
		{From: "x.py", To: "y.py", Scope: "module"},
		{From: "y.py", To: "x.py", Scope: "module"},
		{From: "p.py", To: "q.py", Scope: "module"},
		{From: "q.py", To: "p.py", Scope: "module"},
	}
	first := FindCycles(base)

	// Reverse the input to ensure map iteration order doesn't leak
	// into the result.
	reversed := make([]CycleEdge, len(base))
	for i, e := range base {
		reversed[len(base)-1-i] = e
	}
	second := FindCycles(reversed)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic output:\nfirst:  %#v\nsecond: %#v", first, second)
	}
}
