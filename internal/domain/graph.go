package domain

import (
	"sort"
	"strings"
)

// NodeKind classifies a function node in the call graph.
type NodeKind string

const (
	NodeHandler    NodeKind = "handler"
	NodeService    NodeKind = "service"
	NodeRepository NodeKind = "repository"
	NodeQuery      NodeKind = "query"
	NodeComponent  NodeKind = "component"
	NodeHook       NodeKind = "hook"
	NodeEndpoint   NodeKind = "endpoint"
	NodeExternal   NodeKind = "external"
)

// Node represents a single function/method in the call graph.
// Defined once, referenced many times (pointer semantics).
type Node struct {
	ID        string   `json:"id" yaml:"id"`
	Kind      NodeKind `json:"kind" yaml:"kind"`
	File      string   `json:"file" yaml:"file"`
	Line      int      `json:"line" yaml:"line"`
	Doc       string   `json:"doc,omitempty" yaml:"doc,omitempty"`
	Signature string   `json:"signature,omitempty" yaml:"signature,omitempty"`
	Package   string   `json:"package,omitempty" yaml:"package,omitempty"`
}

// Edge represents a directed "calls" relationship between two nodes.
type Edge struct {
	From      string `json:"from" yaml:"from"`
	To        string `json:"to" yaml:"to"`
	Cycle     bool   `json:"cycle,omitempty" yaml:"cycle,omitempty"`
	Ambiguous bool   `json:"ambiguous,omitempty" yaml:"ambiguous,omitempty"`
}

// Graph is the complete call graph DAG.
type Graph struct {
	Nodes map[string]*Node `json:"nodes" yaml:"nodes"`
	Edges []Edge           `json:"edges" yaml:"edges"`

	// Lazily built adjacency lists. Invalidated when edges change.
	outgoing map[string][]string // nodeID -> list of callee IDs
	incoming map[string][]string // nodeID -> list of caller IDs
}

// NewGraph creates an empty graph.
func NewGraph() *Graph {
	return &Graph{
		Nodes: make(map[string]*Node),
	}
}

// AddNode adds a node, deduplicating by ID. If a node with the same ID
// already exists, it is not replaced.
func (g *Graph) AddNode(n *Node) {
	if n == nil {
		return
	}
	if _, exists := g.Nodes[n.ID]; exists {
		return
	}
	g.Nodes[n.ID] = n
}

// MergeNode replaces a placeholder node (empty File) with a fully-resolved
// node while preserving all existing edges. If the IDs differ, edges pointing
// to the old ID are retargeted to the new ID and the old node is removed.
func (g *Graph) MergeNode(oldID string, resolved *Node) {
	if resolved == nil {
		return
	}

	g.invalidateAdjacency()

	// Remove old placeholder node.
	delete(g.Nodes, oldID)

	// Add the resolved node (may already exist from Phase 2).
	if _, exists := g.Nodes[resolved.ID]; !exists {
		g.Nodes[resolved.ID] = resolved
	}

	// If the IDs are the same, nothing more to do on edges.
	if oldID == resolved.ID {
		return
	}

	// Retarget all edges that reference oldID.
	for i := range g.Edges {
		if g.Edges[i].From == oldID {
			g.Edges[i].From = resolved.ID
		}
		if g.Edges[i].To == oldID {
			g.Edges[i].To = resolved.ID
		}
	}
}

// AddEdge adds a directed edge from -> to. It marks the edge as a cycle
// if adding it would create a path from "to" back to "from".
func (g *Graph) AddEdge(from, to string) {
	g.invalidateAdjacency()

	cycle := g.hasPath(to, from)
	g.Edges = append(g.Edges, Edge{
		From:  from,
		To:    to,
		Cycle: cycle,
	})
}

// AddAmbiguousEdge adds an edge marked as heuristically resolved.
func (g *Graph) AddAmbiguousEdge(from, to string) {
	g.invalidateAdjacency()

	cycle := g.hasPath(to, from)
	g.Edges = append(g.Edges, Edge{
		From:      from,
		To:        to,
		Cycle:     cycle,
		Ambiguous: true,
	})
}

// Callees returns all direct callees of a node.
func (g *Graph) Callees(nodeID string) []*Node {
	g.buildAdjacency()

	ids := g.outgoing[nodeID]
	result := make([]*Node, 0, len(ids))
	for _, id := range ids {
		if n, ok := g.Nodes[id]; ok {
			result = append(result, n)
		}
	}
	return result
}

// Callers returns all direct callers of a node.
func (g *Graph) Callers(nodeID string) []*Node {
	g.buildAdjacency()

	ids := g.incoming[nodeID]
	result := make([]*Node, 0, len(ids))
	for _, id := range ids {
		if n, ok := g.Nodes[id]; ok {
			result = append(result, n)
		}
	}
	return result
}

// TraceFrom performs a depth-first traversal from a root node,
// returning an ordered tree of nodes with cycle detection.
// maxDepth of 0 means unlimited depth.
func (g *Graph) TraceFrom(rootID string, maxDepth int) *TraceResult {
	g.buildAdjacency()

	result := &TraceResult{
		Confidence: 1.0,
	}

	rootNode, exists := g.Nodes[rootID]
	if !exists {
		result.Warnings = append(result.Warnings, "root node not found: "+rootID)
		result.Confidence = 0.0
		return result
	}

	visited := make(map[string]bool)
	root := g.traceNode(rootNode, 0, maxDepth, visited, result)
	result.Root = root
	result.TotalNodes = len(visited)

	// Compute max depth reached.
	result.MaxDepth = g.computeMaxDepth(root)

	return result
}

// FindPathTo finds a linear path from a caller to the target node by tracing
// incoming edges (reverse BFS). If routeHint is non-empty, it prefers paths
// whose root node ID contains the hint. Returns the path as a slice of node IDs
// from root to target (inclusive), or nil if no path found.
func (g *Graph) FindPathTo(targetID string, routeHint string, maxDepth int) []string {
	g.buildAdjacency()

	if _, ok := g.Nodes[targetID]; !ok {
		return nil
	}

	// BFS backward from target to find all reachable roots.
	type pathEntry struct {
		nodeID string
		path   []string
	}

	queue := []pathEntry{{nodeID: targetID, path: []string{targetID}}}
	visited := map[string]bool{targetID: true}
	var allPaths [][]string

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if len(current.path) > maxDepth {
			continue
		}

		callerIDs := g.incoming[current.nodeID]
		if len(callerIDs) == 0 {
			// This is a root — no one calls it. Record the path.
			// Reverse so it goes root → ... → target.
			reversed := make([]string, len(current.path))
			for i, id := range current.path {
				reversed[len(current.path)-1-i] = id
			}
			allPaths = append(allPaths, reversed)
			continue
		}

		for _, callerID := range callerIDs {
			if visited[callerID] {
				continue
			}
			visited[callerID] = true
			newPath := make([]string, len(current.path)+1)
			copy(newPath, current.path)
			newPath[len(current.path)] = callerID
			queue = append(queue, pathEntry{nodeID: callerID, path: newPath})
		}
	}

	if len(allPaths) == 0 {
		return nil
	}

	// Pick the best path.
	// Priority 1: path whose root contains the route hint.
	if routeHint != "" {
		for _, p := range allPaths {
			if strings.Contains(p[0], routeHint) {
				return p
			}
		}
	}

	// Priority 2: path whose root is a route: node.
	for _, p := range allPaths {
		if strings.HasPrefix(p[0], "route:") {
			return p
		}
	}

	// Priority 3: longest path (most context).
	best := allPaths[0]
	for _, p := range allPaths[1:] {
		if len(p) > len(best) {
			best = p
		}
	}
	return best
}

// TraceCallersFrom traces upward from a node, building a tree of callers.
// This is the reverse of TraceFrom — it follows incoming edges.
// Returns a tree rooted at the deepest caller, with the target node as a leaf.
func (g *Graph) TraceCallersFrom(nodeID string, maxDepth int) []*TraceNode {
	g.buildAdjacency()

	_, exists := g.Nodes[nodeID]
	if !exists {
		return nil
	}

	// BFS upward to find all caller chains
	visited := make(map[string]bool)
	visited[nodeID] = true
	return g.traceCallersRecursive(nodeID, 0, maxDepth, visited)
}

func (g *Graph) traceCallersRecursive(nodeID string, depth, maxDepth int, visited map[string]bool) []*TraceNode {
	if maxDepth > 0 && depth >= maxDepth {
		return nil
	}

	callerIDs := g.incoming[nodeID]
	sorted := make([]string, len(callerIDs))
	copy(sorted, callerIDs)
	sort.Strings(sorted)

	var results []*TraceNode
	for _, callerID := range sorted {
		if visited[callerID] {
			continue
		}
		callerNode, ok := g.Nodes[callerID]
		if !ok {
			continue
		}
		visited[callerID] = true

		tn := &TraceNode{
			Node:  callerNode,
			Depth: depth,
		}

		// Recurse upward
		parentNodes := g.traceCallersRecursive(callerID, depth+1, maxDepth, visited)
		if len(parentNodes) > 0 {
			// Wrap this node as a child of each parent
			for _, parent := range parentNodes {
				parentCopy := *parent
				parentCopy.Children = append(parentCopy.Children, tn)
				results = append(results, &parentCopy)
			}
		} else {
			// This is a root caller — no one calls it
			results = append(results, tn)
		}
	}

	return results
}

// traceNode recursively builds the trace tree via DFS.
func (g *Graph) traceNode(node *Node, depth, maxDepth int, visited map[string]bool, result *TraceResult) *TraceNode {
	tn := &TraceNode{
		Node:  node,
		Depth: depth,
	}

	if visited[node.ID] {
		tn.IsCycle = true
		result.Cycles = append(result.Cycles, Edge{
			From:  node.ID,
			To:    node.ID,
			Cycle: true,
		})
		return tn
	}

	visited[node.ID] = true

	if maxDepth > 0 && depth >= maxDepth {
		return tn
	}

	calleeIDs := g.outgoing[node.ID]
	// Sort for deterministic output.
	sorted := make([]string, len(calleeIDs))
	copy(sorted, calleeIDs)
	sort.Strings(sorted)

	for _, calleeID := range sorted {
		calleeNode, ok := g.Nodes[calleeID]
		if !ok {
			result.Warnings = append(result.Warnings, "edge references unknown node: "+calleeID)
			result.Confidence *= 0.9
			continue
		}
		child := g.traceNode(calleeNode, depth+1, maxDepth, visited, result)
		tn.Children = append(tn.Children, child)
	}

	return tn
}

// computeMaxDepth returns the maximum depth in a trace tree.
func (g *Graph) computeMaxDepth(tn *TraceNode) int {
	if tn == nil {
		return 0
	}
	max := tn.Depth
	for _, child := range tn.Children {
		d := g.computeMaxDepth(child)
		if d > max {
			max = d
		}
	}
	return max
}

// hasPath checks if there is a directed path from src to dst using existing edges.
// Used before adding a new edge to detect if adding it would create a cycle.
func (g *Graph) hasPath(src, dst string) bool {
	if src == dst {
		return true
	}

	// Build a temporary outgoing map from current edges for the search.
	out := make(map[string][]string)
	for _, e := range g.Edges {
		out[e.From] = append(out[e.From], e.To)
	}

	visited := make(map[string]bool)
	return g.dfs(src, dst, out, visited)
}

// dfs performs depth-first search from current toward target in the given adjacency map.
func (g *Graph) dfs(current, target string, adj map[string][]string, visited map[string]bool) bool {
	if current == target {
		return true
	}
	if visited[current] {
		return false
	}
	visited[current] = true

	for _, next := range adj[current] {
		if g.dfs(next, target, adj, visited) {
			return true
		}
	}
	return false
}

// InvalidateAdjacency clears the cached adjacency lists so they are rebuilt
// on next access. Exported for use by adapters that manipulate edges directly.
func (g *Graph) InvalidateAdjacency() {
	g.outgoing = nil
	g.incoming = nil
}

// invalidateAdjacency is the unexported alias for internal use.
func (g *Graph) invalidateAdjacency() {
	g.InvalidateAdjacency()
}

// buildAdjacency populates the outgoing and incoming adjacency lists from edges.
// No-op if already built.
func (g *Graph) buildAdjacency() {
	if g.outgoing != nil {
		return
	}

	g.outgoing = make(map[string][]string)
	g.incoming = make(map[string][]string)

	for _, e := range g.Edges {
		g.outgoing[e.From] = append(g.outgoing[e.From], e.To)
		g.incoming[e.To] = append(g.incoming[e.To], e.From)
	}
}

// TraceResult is the output of a trace operation.
type TraceResult struct {
	Root       *TraceNode
	TotalNodes int
	MaxDepth   int
	Cycles     []Edge
	Confidence float64
	Warnings   []string
}

// TraceNode is a node in the trace tree (with children).
type TraceNode struct {
	Node     *Node
	Children []*TraceNode
	Depth    int
	IsCycle  bool
}

// FeatureGraph links a feature to its graph entry points.
type FeatureGraph struct {
	EntryPoints []string `yaml:"entry_points"`
	APIBoundary string   `yaml:"api_boundary,omitempty"`
}

