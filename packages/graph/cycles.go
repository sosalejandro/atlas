package graph

import "sort"

// CycleEdge is one directed edge participating in a strongly-connected
// component. The endpoints carry whatever identifier the caller chose to
// pass to FindCycles — for `atlas codebase cycles` these are file paths,
// but the algorithm itself is identifier-agnostic.
//
// Scope is the import-scope tag the underlying store edge carried (one of
// store.EdgeMetaImportScope*). Empty string means the edge had no
// qualifier (older edges) or the caller chose not to project the column.
// Line is the 1-based source line where the import statement lives, or 0
// when the caller didn't supply it.
type CycleEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Scope string `json:"scope,omitempty"`
	Line  int    `json:"line,omitempty"`
}

// Cycle is one strongly-connected component with ≥ 2 distinct nodes.
//
// Nodes is the alphabetised list of node identifiers in the SCC. Edges
// is every directed edge that participates in the cycle (i.e. both
// endpoints are in Nodes), ordered by (From, To, Line) so the output
// stays deterministic across re-runs.
//
// Length is len(Nodes) — exposed as its own field so JSON consumers can
// group/filter without re-counting.
type Cycle struct {
	Length int         `json:"length"`
	Nodes  []string    `json:"nodes"`
	Edges  []CycleEdge `json:"edges"`
}

// FindCycles runs Tarjan's strongly-connected-components algorithm over
// the supplied directed multigraph and returns every non-trivial SCC
// (component size ≥ 2). Single-node SCCs are filtered because a node
// with no self-loop is technically its own SCC under Tarjan but never a
// "cycle" in the colloquial sense the cycles verb reports.
//
// Self-loops (an edge from X to X) are NOT reported on their own — the
// caller would need to widen the size-2 filter to size-1 to surface
// them, but that's almost always noise from intra-file decorator
// chains, not a real circular-import bug.
//
// Determinism: Nodes within a Cycle are sorted alphabetically; Edges
// are sorted by (From, To, Line); the returned []Cycle is sorted first
// by Length ascending (2-node cycles first — most common + highest
// fix-value) and then by the first node alphabetically within each
// length bucket. This ordering matches the human-readable spec on issue
// #14 and the JSON envelope's expected stable order.
//
// Complexity is O(V + E) on a graph with V nodes and E edges. The
// recursion depth is bounded by V; for the largest Atlas-indexed
// codebases (tens of thousands of import edges, hundreds of files in
// any one cycle) that's well under the Go default stack ceiling.
func FindCycles(edges []CycleEdge) []Cycle {
	if len(edges) == 0 {
		return nil
	}

	// Build the adjacency list keyed by stable node identifiers.
	// Multiple parallel edges between the same (from, to) pair are
	// preserved — the SCC algorithm walks each unique destination
	// only once, but we retain every edge for the post-pass that
	// projects edges into a cycle.
	nodeIndex := make(map[string]int)
	var nodes []string
	addNode := func(id string) int {
		if i, ok := nodeIndex[id]; ok {
			return i
		}
		i := len(nodes)
		nodeIndex[id] = i
		nodes = append(nodes, id)
		return i
	}

	// adjacency[i] holds the indexed destinations reachable from
	// node i; we dedupe destinations so Tarjan doesn't revisit the
	// same target multiple times (parallel edges don't change the
	// SCC structure — only the edge projection cares about them).
	for _, e := range edges {
		addNode(e.From)
		addNode(e.To)
	}
	adjacency := make([][]int, len(nodes))
	seenEdge := make(map[[2]int]struct{}, len(edges))
	for _, e := range edges {
		from := nodeIndex[e.From]
		to := nodeIndex[e.To]
		key := [2]int{from, to}
		if _, ok := seenEdge[key]; ok {
			continue
		}
		seenEdge[key] = struct{}{}
		adjacency[from] = append(adjacency[from], to)
	}

	scc := newTarjan(adjacency)
	components := scc.run()

	// Convert the raw component slices (indexed) back into the
	// caller's string identifiers and project the participating
	// edges. Drop trivial single-node components — a node with no
	// self-loop forms an SCC of size 1 in Tarjan but isn't a cycle.
	var cycles []Cycle
	for _, comp := range components {
		if len(comp) < 2 {
			continue
		}
		idSet := make(map[string]struct{}, len(comp))
		nodeIDs := make([]string, 0, len(comp))
		for _, idx := range comp {
			nodeIDs = append(nodeIDs, nodes[idx])
			idSet[nodes[idx]] = struct{}{}
		}
		sort.Strings(nodeIDs)

		// Walk every input edge and keep the ones whose endpoints
		// both live inside this component. This is O(E·C) in the
		// worst case where C is the cycle count, but in practice
		// edges are sparse — a typical Python project has < 50
		// import cycles even on the largest repos.
		var cycleEdges []CycleEdge
		for _, e := range edges {
			if _, ok := idSet[e.From]; !ok {
				continue
			}
			if _, ok := idSet[e.To]; !ok {
				continue
			}
			cycleEdges = append(cycleEdges, e)
		}
		sort.Slice(cycleEdges, func(i, j int) bool {
			a, b := cycleEdges[i], cycleEdges[j]
			if a.From != b.From {
				return a.From < b.From
			}
			if a.To != b.To {
				return a.To < b.To
			}
			return a.Line < b.Line
		})

		cycles = append(cycles, Cycle{
			Length: len(nodeIDs),
			Nodes:  nodeIDs,
			Edges:  cycleEdges,
		})
	}

	sort.Slice(cycles, func(i, j int) bool {
		if cycles[i].Length != cycles[j].Length {
			return cycles[i].Length < cycles[j].Length
		}
		return cycles[i].Nodes[0] < cycles[j].Nodes[0]
	})
	return cycles
}

// tarjan implements Tarjan's strongly-connected-components algorithm
// over an integer-indexed adjacency list. The standard CLRS formulation:
//
//   1. DFS from every unvisited node. Each visited node gets an index
//      (discovery time) and a lowlink (the smallest index reachable
//      from its DFS subtree).
//   2. Maintain a stack of nodes "currently being explored". A node
//      stays on the stack until its DFS subtree has finished AND it
//      has been confirmed as the root of an SCC.
//   3. When DFS returns to a node whose lowlink == its own index, that
//      node is the root of an SCC: pop the stack down to (and
//      including) this node — every popped node is in the same SCC.
//
// The algorithm runs in a single pass and is O(V + E). It is
// deliberately iterative-friendly here (the recursion still happens via
// strongConnect but the per-call state lives on the receiver, not on a
// closure stack) so the same struct can be reused for additional
// queries without re-allocating slices.
type tarjan struct {
	adjacency [][]int
	index     []int  // 0-based discovery time; -1 means unvisited
	lowlink   []int  // smallest index reachable from this node's subtree
	onStack   []bool // is this node currently on the DFS stack?
	stack     []int  // DFS stack (nodes currently being explored)
	next      int    // next index to hand out
	result    [][]int
}

func newTarjan(adjacency [][]int) *tarjan {
	n := len(adjacency)
	t := &tarjan{
		adjacency: adjacency,
		index:     make([]int, n),
		lowlink:   make([]int, n),
		onStack:   make([]bool, n),
	}
	for i := range t.index {
		t.index[i] = -1
	}
	return t
}

func (t *tarjan) run() [][]int {
	for v := range t.adjacency {
		if t.index[v] == -1 {
			t.strongConnect(v)
		}
	}
	return t.result
}

// strongConnect is the recursive Tarjan step. It descends into every
// unvisited neighbour, updates the lowlink on the way back, and pops a
// complete SCC off the stack when the current node turns out to be the
// root of one.
func (t *tarjan) strongConnect(v int) {
	t.index[v] = t.next
	t.lowlink[v] = t.next
	t.next++
	t.stack = append(t.stack, v)
	t.onStack[v] = true

	for _, w := range t.adjacency[v] {
		switch {
		case t.index[w] == -1:
			// Tree edge — recurse and propagate lowlink.
			t.strongConnect(w)
			if t.lowlink[w] < t.lowlink[v] {
				t.lowlink[v] = t.lowlink[w]
			}
		case t.onStack[w]:
			// Back edge to a node currently in the DFS stack —
			// pull v's lowlink down toward w's discovery
			// index. The classic CLRS detail: we use w's
			// `index`, NOT w's `lowlink`, because lowlink can
			// have been updated by later edges to point
			// outside the current SCC.
			if t.index[w] < t.lowlink[v] {
				t.lowlink[v] = t.index[w]
			}
		}
	}

	// If v is the root of an SCC, pop the stack down to v.
	if t.lowlink[v] == t.index[v] {
		var component []int
		for {
			w := t.stack[len(t.stack)-1]
			t.stack = t.stack[:len(t.stack)-1]
			t.onStack[w] = false
			component = append(component, w)
			if w == v {
				break
			}
		}
		t.result = append(t.result, component)
	}
}
