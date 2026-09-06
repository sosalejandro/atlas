package graph

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	atlastest "github.com/sosalejandro/atlas/packages/testing"
)

// The graph layer's property tests.
//
// Two things are being defended here, and they fail differently.
//
// The cycle report is what `atlas codebase cycles` prints and what a reviewer
// acts on, so it has to be a function of the graph's CONNECTIVITY and nothing
// else. Tarjan indexes nodes internally; a report that depended on which
// integer a node happened to be assigned would be stable on the corpus and
// unstable on a real repo where a rename shifts every index. Renaming every
// node and re-running is the cheapest way to tell the two apart.
//
// Referential closure is the other. Issue #97 persisted edges pointing at
// symbols that were not their endpoints — the graph was the right size and
// the wrong shape, so `symbols: N  edges: M` never moved. MergeNode is the
// in-memory operation with the same hazard: it retargets edges when a
// placeholder resolves to a different id, and an edge it forgot would point
// at a node that no longer exists.

// buildDigraph converts a generated digraph into cycle-finder input.
func cycleEdges(g atlastest.Digraph) []CycleEdge {
	out := make([]CycleEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		out = append(out, CycleEdge{From: e.From, To: e.To, Line: e.Line})
	}
	return out
}

// TestProperty_FindCycles_IsStableUnderRenaming asserts the cycle report
// depends on connectivity alone.
//
// The report is renamed forward — every node identifier mapped through the
// same bijection — and compared to the report produced from the renamed
// graph. Equality means Tarjan's internal numbering, the map iteration in
// buildAdjacency, and the sort keys all cancel out. Inequality means a
// reviewer's cycle list can change because somebody renamed a file, which is
// the kind of churn that teaches people to ignore the report.
func TestProperty_FindCycles_IsStableUnderRenaming(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		g := atlastest.GenDigraph(r)
		renamed, mapping := g.Relabel(r)

		original := FindCycles(cycleEdges(g))
		underNewNames := FindCycles(cycleEdges(renamed))

		want := renderCycles(applyMapping(original, mapping))
		got := renderCycles(underNewNames)
		if want != got {
			t.Fatalf("seed %d: cycle report changed under a pure rename\nexpected (original, renamed forward):\n%s\ngot (from the renamed graph):\n%s",
				r.Seed(), want, got)
		}
	})
}

// TestProperty_FindCycles_ReportsOnlyRealComponents checks the report against
// the definition rather than against a previous run.
//
// For every reported cycle: each node must reach every other node in it (that
// is what strongly connected MEANS), every reported edge must have both
// endpoints inside it, and no node may appear in two components (SCCs
// partition the graph). A golden test would pin whatever Tarjan produced on
// the day it was written; this pins what a strongly-connected component is,
// which is the thing a user is being told about.
func TestProperty_FindCycles_ReportsOnlyRealComponents(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		g := atlastest.GenDigraph(r)
		edges := cycleEdges(g)
		cycles := FindCycles(edges)

		seen := map[string]int{}
		for ci, c := range cycles {
			if c.Length != len(c.Nodes) {
				t.Fatalf("seed %d: cycle %d reports length %d over %d nodes", r.Seed(), ci, c.Length, len(c.Nodes))
			}
			members := map[string]bool{}
			for _, n := range c.Nodes {
				if prev, dup := seen[n]; dup {
					t.Fatalf("seed %d: node %s appears in cycles %d and %d; components must partition",
						r.Seed(), n, prev, ci)
				}
				seen[n] = ci
				members[n] = true
			}
			for _, e := range c.Edges {
				if !members[e.From] || !members[e.To] {
					t.Fatalf("seed %d: cycle %d lists edge %s -> %s with an endpoint outside the component",
						r.Seed(), ci, e.From, e.To)
				}
			}
			for _, from := range c.Nodes {
				for _, to := range c.Nodes {
					if from != to && !reaches(edges, from, to) {
						t.Fatalf("seed %d: cycle %d claims a component containing %s and %s, but %s cannot reach %s",
							r.Seed(), ci, from, to, from, to)
					}
				}
			}
		}
	})
}

// TestProperty_MergeNode_KeepsTheGraphReferentiallyClosed is the in-memory
// half of the issue #97 guard.
//
// A graph is built with edges only between nodes that exist, then a random
// series of placeholder resolutions is applied — some to brand new ids, some
// onto ids that are already present, which is the merge-into-existing case
// that has to fold rather than duplicate. After every step, every edge
// endpoint must still name a node the graph holds.
//
// The failure this catches is silent by construction: a forgotten retarget
// leaves an edge pointing at an id that was just deleted, the edge count does
// not move, and the first symptom is `atlas chain` walking off the end of a
// call chain that used to work.
func TestProperty_MergeNode_KeepsTheGraphReferentiallyClosed(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		d := atlastest.GenDigraph(r)
		g := New()
		for _, n := range d.Nodes {
			g.AddNode(&Node{Symbol: shared.Symbol{ID: shared.SymbolID(n), Kind: shared.KindFunc}})
		}
		for _, e := range d.Edges {
			g.AddEdge(shared.SymbolID(e.From), shared.SymbolID(e.To))
		}
		assertClosed(t, r, g, "after construction")

		live := append([]string(nil), d.Nodes...)
		for step := range r.IntRange(1, 6) {
			if len(live) == 0 {
				break
			}
			i := r.IntN(len(live))
			old := live[i]

			// Half the merges resolve onto a fresh id (the placeholder case);
			// half resolve onto an id already in the graph (the fold case,
			// where two placeholders turn out to be the same symbol).
			resolved := fmt.Sprintf("resolved-%d-%d", r.Seed(), step)
			if len(live) > 1 && r.Chance(1, 2) {
				resolved = live[(i+1)%len(live)]
			}
			g.MergeNode(shared.SymbolID(old), &Node{
				Symbol: shared.Symbol{ID: shared.SymbolID(resolved), Kind: shared.KindFunc},
			})
			live[i] = resolved
			assertClosed(t, r, g, fmt.Sprintf("after merge %s -> %s", old, resolved))
		}
	})
}

// TestProperty_Adjacency_AgreesWithTheEdgeSlice pins the cache against its
// source of truth.
//
// Out and In answer from lazily-built adjacency maps that every mutation
// invalidates. A missed invalidation makes them answer from a graph that no
// longer exists — and because the stale answer is a PREVIOUS correct answer,
// nothing about it looks wrong. Recomputing from g.Edges directly and
// comparing is the only way to see it.
func TestProperty_Adjacency_AgreesWithTheEdgeSlice(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		d := atlastest.GenDigraph(r)
		g := New()
		for _, n := range d.Nodes {
			g.AddNode(&Node{Symbol: shared.Symbol{ID: shared.SymbolID(n), Kind: shared.KindFunc}})
		}
		for i, e := range d.Edges {
			g.AddEdge(shared.SymbolID(e.From), shared.SymbolID(e.To))
			// Interleave reads with writes: a cache that is only ever read
			// after the last write is never asked the stale question.
			if i%3 == 0 {
				g.Out(shared.SymbolID(Pick(r, d.Nodes)))
			}
		}
		for _, n := range d.Nodes {
			id := shared.SymbolID(n)
			wantOut, wantIn := 0, 0
			for _, e := range g.Edges {
				if e.From == id {
					wantOut++
				}
				if e.To == id {
					wantIn++
				}
			}
			if got := len(g.Out(id)); got != wantOut {
				t.Fatalf("seed %d: Out(%s) returned %d edges, the edge slice holds %d", r.Seed(), n, got, wantOut)
			}
			if got := len(g.In(id)); got != wantIn {
				t.Fatalf("seed %d: In(%s) returned %d edges, the edge slice holds %d", r.Seed(), n, got, wantIn)
			}
		}
	})
}

// Pick is a local alias so the table-driven reads above stay readable.
func Pick(r *atlastest.Rand, xs []string) string { return atlastest.Pick(r, xs) }

// assertClosed fails when any edge endpoint names a node the graph no longer
// holds.
func assertClosed(t *testing.T, r *atlastest.Rand, g *Graph, when string) {
	t.Helper()
	nodes := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodes = append(nodes, string(id))
	}
	edges := make([]atlastest.DiEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, atlastest.DiEdge{From: string(e.From), To: string(e.To), Line: e.Line})
	}
	if err := atlastest.CheckEdgesResolve(nodes, edges); err != nil {
		t.Fatalf("seed %d: %s: %v", r.Seed(), when, err)
	}
}

// applyMapping renames a cycle report forward through a node mapping and
// re-sorts it the way FindCycles promises to. Re-sorting is required: the
// promise is an ORDER over identifiers, so renaming the identifiers changes
// the order, and comparing without re-sorting would test the rename rather
// than the report.
func applyMapping(cycles []Cycle, mapping map[string]string) []Cycle {
	out := make([]Cycle, 0, len(cycles))
	for _, c := range cycles {
		mapped := Cycle{Length: c.Length}
		for _, n := range c.Nodes {
			mapped.Nodes = append(mapped.Nodes, mapping[n])
		}
		sort.Strings(mapped.Nodes)
		for _, e := range c.Edges {
			mapped.Edges = append(mapped.Edges, CycleEdge{From: mapping[e.From], To: mapping[e.To], Scope: e.Scope, Line: e.Line})
		}
		sort.Slice(mapped.Edges, func(i, j int) bool {
			if mapped.Edges[i].From != mapped.Edges[j].From {
				return mapped.Edges[i].From < mapped.Edges[j].From
			}
			if mapped.Edges[i].To != mapped.Edges[j].To {
				return mapped.Edges[i].To < mapped.Edges[j].To
			}
			return mapped.Edges[i].Line < mapped.Edges[j].Line
		})
		out = append(out, mapped)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Length != out[j].Length {
			return out[i].Length < out[j].Length
		}
		return out[i].Nodes[0] < out[j].Nodes[0]
	})
	return out
}

// renderCycles is the comparable form of a report: one line per cycle listing
// its nodes and edges. Text rather than reflect.DeepEqual so a failure shows
// which component moved.
func renderCycles(cycles []Cycle) string {
	var b strings.Builder
	for _, c := range cycles {
		fmt.Fprintf(&b, "len=%d nodes=%s edges=", c.Length, strings.Join(c.Nodes, ","))
		for _, e := range c.Edges {
			fmt.Fprintf(&b, "%s->%s@%d ", e.From, e.To, e.Line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// reaches answers "is there a directed path from src to dst" by breadth-first
// search over the raw edge list — deliberately not by reusing anything the
// code under test builds.
func reaches(edges []CycleEdge, src, dst string) bool {
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	seen := map[string]bool{src: true}
	queue := []string{src}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if next == dst {
				return true
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}
