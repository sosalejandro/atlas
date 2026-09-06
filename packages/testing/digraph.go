package atlastest

import "fmt"

// DiEdge is one directed edge between opaque node identifiers.
type DiEdge struct {
	From string
	To   string
	Line int
}

// Digraph is a directed multigraph plus the node set, which is NOT simply the
// endpoints of Edges: a graph consumer that only ever sees endpoints cannot
// tell an isolated node from a node that does not exist, and "every edge
// endpoint resolves to a node that exists" is only a meaningful property when
// the two sets are tracked apart.
type Digraph struct {
	Nodes []string
	Edges []DiEdge
}

// GenDigraph builds a random directed multigraph that is guaranteed to
// contain cycles.
//
// Guaranteed, not incidental: a uniformly random sparse digraph is mostly
// acyclic, so a cycle-finding property fed one would spend most of its seeds
// asserting that an empty result equals an empty result. Seeding explicit
// rings — including overlapping ones that Tarjan must fuse into a single
// strongly-connected component — is what puts the algorithm under load.
func GenDigraph(r *Rand) Digraph {
	n := r.IntRange(4, 14)
	g := Digraph{}
	for i := range n {
		g.Nodes = append(g.Nodes, fmt.Sprintf("n%02d", i))
	}

	// Rings. Each walks a random subset of nodes and closes back on itself.
	for range r.IntRange(1, 3) {
		size := r.IntRange(2, minInt(n, 5))
		ring := make([]string, 0, size)
		for range size {
			ring = append(ring, Pick(r, g.Nodes))
		}
		for i := range ring {
			from, to := ring[i], ring[(i+1)%len(ring)]
			if from == to {
				continue // self-loops are deliberately not reported as cycles
			}
			g.Edges = append(g.Edges, DiEdge{From: from, To: to, Line: r.IntRange(1, 200)})
		}
	}

	// Chaff: acyclic-ish extra edges so the components have to be separated
	// from the rest of the graph rather than being the whole graph.
	for range r.IntRange(0, 2*n) {
		g.Edges = append(g.Edges, DiEdge{
			From: Pick(r, g.Nodes),
			To:   Pick(r, g.Nodes),
			Line: r.IntRange(1, 200),
		})
	}
	return g
}

// Relabel returns an isomorphic copy of g under a bijective renaming, plus
// the mapping used.
//
// The new labels are drawn from a shuffled pool whose lexical order is
// unrelated to the old one. That is the point: a cycle report that is stable
// only because both runs happened to sort the same way is not stable, it is
// lucky, and the bug it hides — a component's identity depending on node
// ORDER rather than node CONNECTIVITY — is exactly what renumbering exposes.
func (g Digraph) Relabel(r *Rand) (Digraph, map[string]string) {
	labels := make([]string, len(g.Nodes))
	for i := range labels {
		labels[i] = fmt.Sprintf("%s%03d", string(rune('z'-i%26)), (i*37)%997)
	}
	r.Shuffle(len(labels), func(i, j int) { labels[i], labels[j] = labels[j], labels[i] })

	mapping := make(map[string]string, len(g.Nodes))
	out := Digraph{Nodes: make([]string, 0, len(g.Nodes))}
	for i, n := range g.Nodes {
		mapping[n] = labels[i]
		out.Nodes = append(out.Nodes, labels[i])
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, DiEdge{From: mapping[e.From], To: mapping[e.To], Line: e.Line})
	}
	return out, mapping
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
