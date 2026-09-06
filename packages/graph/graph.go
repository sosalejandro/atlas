package graph

import (
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Node is one entry in the call graph. Each node corresponds to exactly one
// shared.Symbol — the embedded Symbol carries position/kind/signature,
// the graph layer adds caller/callee adjacency on top.
//
// Pointer semantics: a Node is created once, referenced many times. AddNode
// deduplicates by ID; MergeNode supports replacing a placeholder with a
// fully-resolved node while preserving every edge that pointed at the
// placeholder.
type Node struct {
	shared.Symbol
}

// Edge is a directed "calls" / "depends-on" relationship between two
// symbols.
//
// Cycle is set automatically when AddEdge would close a cycle (the existing
// graph already has a directed path To→From). Ambiguous is set when the
// scanner resolved the call heuristically (e.g. interface → concrete via DI
// without an explicit binding). Both flags are advisory — consumers may
// still walk through cycle/ambiguous edges; the markers exist so renderers
// can warn.
//
// Kind labels the relationship semantics. Sub-scanners emit kinds in their
// native vocabulary (the Python scanner emits "import", "inheritance",
// "decorator", "call"; the Go scanner emits "call"). The store-side
// ingestor maps these to the closed EdgeKind enum before persistence —
// unknown kinds default to "call". Zero value ("") is back-compat with
// pre-v0.4 callers and is treated as "call" by downstream consumers.
type Edge struct {
	From shared.SymbolID `json:"from"`
	To   shared.SymbolID `json:"to"`
	Kind string          `json:"kind,omitempty"`
	// Line is the 1-based source line where this edge originated
	// (the import statement, the call site, the decorator line,
	// etc.). Zero means "no per-edge anchor available" — the
	// store-side ingestor falls back to the from-symbol's
	// declaration line in that case. Sub-scanners that know the
	// precise origin (currently scanner.py; scanner.ts + the Go
	// scanner do not yet populate this) should set it so callers
	// can drill from an edge to its true source location. Closes
	// issue #17 (PR #68).
	Line      int  `json:"line,omitempty"`
	Cycle     bool `json:"cycle,omitempty"`
	Ambiguous bool `json:"ambiguous,omitempty"`
	// Meta is a kind-specific qualifier carried opaquely through
	// the graph layer. Today only `import` edges from the Python
	// scanner populate this: the value is one of "module",
	// "function", "conditional", "type_checking", "try_guard"
	// (scanner.py's SCOPE_* constants) and lets downstream queries
	// distinguish "definitely-live module-level import" from
	// "deferred / type-checking-only import". Closes issue #16.
	//
	// Empty string means "no qualifier" and is back-compat with
	// every pre-#16 caller; existing tests + non-Python scanners
	// are unaffected.
	Meta string `json:"meta,omitempty"`

	// Tier records which mechanism resolved this edge — see tier.go.
	// It is set by the scanner that produced the edge and carried
	// through unchanged; nothing between here and the store may
	// infer, upgrade or default it.
	//
	// The zero value (TierUnset) means "this producer did not say",
	// and packages/store refuses to persist it. That is deliberate:
	// the graph layer is also used for in-memory work that never
	// reaches a database (packages/diff, `atlas trace` fixtures),
	// so a constructor without a tier still has to exist — but an
	// edge that came from one of those must never be filed away as
	// though a scanner had vouched for it. Closes issue #146.
	Tier ResolutionTier `json:"tier,omitempty"`
}

// Graph is the in-memory call-graph DAG.
//
// Adjacency lists are lazily built on first walk and invalidated on any
// edge mutation. Callers do not need to manage that — the public API
// reseeds the caches transparently. Direct field mutation (Graph.Edges =
// kept) requires InvalidateAdjacency afterward; the codeindex/go scanner
// is the only known caller that does this for prune passes.
type Graph struct {
	Nodes map[shared.SymbolID]*Node `json:"nodes"`
	Edges []Edge                    `json:"edges"`

	// Lazily built adjacency lists; nil-when-stale, repopulated by
	// buildAdjacency on demand.
	outgoing map[shared.SymbolID][]shared.SymbolID
	incoming map[shared.SymbolID][]shared.SymbolID
}

// New creates an empty graph.
func New() *Graph {
	return &Graph{Nodes: make(map[shared.SymbolID]*Node)}
}

// AddNode adds a node, deduplicating by ID. If a node with the same ID
// already exists, it is left untouched (callers can MergeNode to replace).
func (g *Graph) AddNode(n *Node) {
	if n == nil || n.ID == "" {
		return
	}
	if _, exists := g.Nodes[n.ID]; exists {
		return
	}
	g.Nodes[n.ID] = n
}

// MergeNode replaces a placeholder node (typically with an empty
// FilePosition) with a fully-resolved node, preserving every edge that
// referenced the placeholder.
//
// If oldID == resolved.ID the operation is a no-op apart from upserting the
// resolved record. If the IDs differ, all edges pointing at oldID are
// retargeted to resolved.ID and the old node is removed.
func (g *Graph) MergeNode(oldID shared.SymbolID, resolved *Node) {
	if resolved == nil {
		return
	}
	g.invalidateAdjacency()

	delete(g.Nodes, oldID)
	if _, exists := g.Nodes[resolved.ID]; !exists {
		g.Nodes[resolved.ID] = resolved
	}

	if oldID == resolved.ID {
		return
	}

	for i := range g.Edges {
		if g.Edges[i].From == oldID {
			g.Edges[i].From = resolved.ID
		}
		if g.Edges[i].To == oldID {
			g.Edges[i].To = resolved.ID
		}
	}
}

// AddEdge appends a directed edge from→to and marks it as a Cycle if
// adding it would create a path from to→from in the existing graph.
//
// Cycle detection runs a DFS over the current edges; for very large graphs
// (>100k edges) this is the only O(E) cost — keep it in mind for the
// future SQLite-backed path which can use a recursive CTE instead.
//
// Kind defaults to the empty string (treated as "call" by downstream
// consumers). Callers that want to record a specific relationship kind
// (e.g. "inheritance", "import") should use AddEdgeKind.
//
// This overload and its Kind/Line/Meta siblings record NO resolution
// tier, so the edges they build cannot be persisted (issue #146 —
// packages/store refuses an edge whose producer never said which
// mechanism resolved it). They remain because the graph layer is also
// used for work that never reaches a database: packages/diff, the
// `atlas trace` fixtures, and the cycle tests here. A scanner uses
// AddEdgeTier / AddEdgeKindLineTier / AddEdgeKindLineMetaTier /
// AddAmbiguousEdgeTier instead.
func (g *Graph) AddEdge(from, to shared.SymbolID) {
	g.AddEdgeKind(from, to, "")
}

// AddEdgeKind is AddEdge with an explicit relationship kind. The Python
// sub-scanner uses this to preserve the "import" / "inheritance" /
// "decorator" / "call" distinction emitted by scanner.py. Callers should
// pass the same vocabulary the store ingestor recognises (see
// packages/store.EdgeKind*) — unknown values are tolerated by the graph
// layer but will fall back to EdgeKindCall at persistence time.
func (g *Graph) AddEdgeKind(from, to shared.SymbolID, kind string) {
	g.AddEdgeKindLineMeta(from, to, kind, 0, "")
}

// AddEdgeKindLine is AddEdgeKind with the 1-based source line where
// this edge originated (the import statement line, the call-site
// line, the decorator line, etc.). Pass 0 when no per-edge line is
// available — the store-side ingestor will fall back to the
// from-symbol's declaration line in that case, preserving the
// pre-fix behaviour for sub-scanners that don't yet supply per-edge
// anchors.
//
// This is the entry point sub-scanners use when they know the
// precise origin line and want it persisted on the edge row (issue
// atlas-internal #17: Python import edges all reported line=1 before
// the wire-through).
func (g *Graph) AddEdgeKindLine(from, to shared.SymbolID, kind string, line int) {
	g.AddEdgeKindLineMeta(from, to, kind, line, "")
}

// AddEdgeKindMeta is AddEdgeKind with an opaque per-kind qualifier
// stored on Edge.Meta. Today the only producer is the Python
// scanner, which tags `import` edges with their lexical scope
// ("module" / "function" / "conditional" / "type_checking" /
// "try_guard") so downstream dead-code analysis can distinguish
// definitely-live module-level imports from deferred ones. Closes
// issue #16.
//
// The Meta vocabulary is kind-namespaced: a "module" string on an
// import edge means something totally different from the same
// string on a future scanner-emitted kind. The store's persistence
// layer rejects values outside the kind's allow-list via
// store.IsValidEdgeMeta.
func (g *Graph) AddEdgeKindMeta(from, to shared.SymbolID, kind, meta string) {
	g.AddEdgeKindLineMeta(from, to, kind, 0, meta)
}

// AddEdgeKindLineMeta is the canonical full-fidelity edge constructor
// — every other AddEdge* helper delegates here. It accepts both the
// per-edge source line (issue #17) and the kind-specific Meta
// qualifier (issue #16) so a caller that has both signals doesn't
// have to choose. Pass 0/"" for the slots you don't have.
func (g *Graph) AddEdgeKindLineMeta(from, to shared.SymbolID, kind string, line int, meta string) {
	g.AddEdgeKindLineMetaTier(from, to, kind, line, meta, TierUnset)
}

// AddEdgeKindLineMetaTier is the one place an Edge is constructed. Every
// other AddEdge* helper funnels here so the cycle check, the adjacency
// invalidation and the field set cannot drift apart between overloads.
//
// tier states which mechanism resolved this edge (issue #146). Scanners
// MUST pass a real tier; TierUnset is accepted here only because the
// provenance-free overloads above still exist for in-memory consumers,
// and packages/store refuses to persist what they build.
func (g *Graph) AddEdgeKindLineMetaTier(from, to shared.SymbolID, kind string, line int, meta string, tier ResolutionTier) {
	g.invalidateAdjacency()
	cycle := g.hasPath(to, from)
	g.Edges = append(g.Edges, Edge{
		From:  from,
		To:    to,
		Kind:  kind,
		Line:  line,
		Meta:  meta,
		Cycle: cycle,
		Tier:  tier,
	})
}

// AddEdgeTier is AddEdge with the resolution tier stated. This is the
// entry point a scanner uses for a plain call edge; the bare AddEdge is
// for graphs that never get persisted.
func (g *Graph) AddEdgeTier(from, to shared.SymbolID, tier ResolutionTier) {
	g.AddEdgeKindLineMetaTier(from, to, "", 0, "", tier)
}

// AddEdgeKindLineTier is AddEdgeKindLine with the resolution tier
// stated — the shape the Python and TypeScript sub-scanners need, where
// the kind and the per-edge line are both known.
func (g *Graph) AddEdgeKindLineTier(from, to shared.SymbolID, kind string, line int, tier ResolutionTier) {
	g.AddEdgeKindLineMetaTier(from, to, kind, line, "", tier)
}

// AddAmbiguousEdge is AddEdge with Ambiguous=true. Used by the codeindex
// scanners when an interface→concrete resolution had multiple candidates
// or fell back to fuzzy matching. It records no tier; see
// AddAmbiguousEdgeTier.
func (g *Graph) AddAmbiguousEdge(from, to shared.SymbolID) {
	g.AddAmbiguousEdgeTier(from, to, TierUnset)
}

// AddAmbiguousEdgeTier is AddEdgeTier with Ambiguous=true.
//
// Ambiguity and tier answer different questions and neither implies the
// other. The tier says which mechanism ran; Ambiguous says that
// mechanism saw more than one candidate and picked. A name_resolved
// edge can be ambiguous (two packages declare the short name) and a
// syntactic one can be unambiguous (one substring match, still a
// guess). Both are persisted, because collapsing them would throw away
// the finer of the two signals — which is precisely what atlas did with
// Ambiguous before #146: computed on every edge, dropped at the storage
// boundary.
func (g *Graph) AddAmbiguousEdgeTier(from, to shared.SymbolID, tier ResolutionTier) {
	// Routed through the canonical constructor and then marked, rather
	// than appending its own Edge, so there stays exactly one place
	// that decides Cycle and invalidates the adjacency caches. The
	// append below it adds exactly one element, so the index is safe.
	g.AddEdgeKindLineMetaTier(from, to, "", 0, "", tier)
	g.Edges[len(g.Edges)-1].Ambiguous = true
}

// Out returns the outgoing edges of nodeID — every edge whose From equals
// nodeID. Edges are returned in insertion order (not sorted).
func (g *Graph) Out(nodeID shared.SymbolID) []Edge {
	g.buildAdjacency()
	calleeIDs := g.outgoing[nodeID]
	if len(calleeIDs) == 0 {
		return nil
	}
	// Walk g.Edges once to reconstruct full Edge values (we need the
	// Cycle/Ambiguous flags, not just the To IDs).
	out := make([]Edge, 0, len(calleeIDs))
	for _, e := range g.Edges {
		if e.From == nodeID {
			out = append(out, e)
		}
	}
	return out
}

// In returns the incoming edges of nodeID — every edge whose To equals
// nodeID. Edges are returned in insertion order.
func (g *Graph) In(nodeID shared.SymbolID) []Edge {
	g.buildAdjacency()
	callerIDs := g.incoming[nodeID]
	if len(callerIDs) == 0 {
		return nil
	}
	in := make([]Edge, 0, len(callerIDs))
	for _, e := range g.Edges {
		if e.To == nodeID {
			in = append(in, e)
		}
	}
	return in
}

// Callees returns all direct callee Nodes of nodeID. Convenience wrapper
// around Out that resolves to *Node and skips missing nodes.
func (g *Graph) Callees(nodeID shared.SymbolID) []*Node {
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

// Callers returns all direct caller Nodes of nodeID.
func (g *Graph) Callers(nodeID shared.SymbolID) []*Node {
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

// TraceFrom performs a depth-first traversal from rootID and returns an
// ordered tree of nodes with cycle detection.
//
// maxDepth of 0 means unlimited depth. Cycles short-circuit (the duplicate
// visit becomes a TraceNode with IsCycle=true and no children).
func (g *Graph) TraceFrom(rootID shared.SymbolID, maxDepth int) *TraceResult {
	g.buildAdjacency()
	result := &TraceResult{Confidence: 1.0}

	root, exists := g.Nodes[rootID]
	if !exists {
		result.Warnings = append(result.Warnings, "root node not found: "+string(rootID))
		result.Confidence = 0
		return result
	}

	visited := make(map[shared.SymbolID]bool)
	result.Root = g.traceNode(root, 0, maxDepth, visited, result)
	result.TotalNodes = len(visited)
	result.MaxDepth = computeMaxDepth(result.Root)
	return result
}

// FindPathTo finds a linear path from a caller to targetID by tracing
// incoming edges (reverse BFS). When routeHint is non-empty the picker
// prefers paths whose root SymbolID contains the hint. Returns nil if no
// path exists.
func (g *Graph) FindPathTo(targetID shared.SymbolID, routeHint string, maxDepth int) []shared.SymbolID {
	g.buildAdjacency()
	if _, ok := g.Nodes[targetID]; !ok {
		return nil
	}

	type pathEntry struct {
		nodeID shared.SymbolID
		path   []shared.SymbolID
	}
	queue := []pathEntry{{nodeID: targetID, path: []shared.SymbolID{targetID}}}
	visited := map[shared.SymbolID]bool{targetID: true}
	var allPaths [][]shared.SymbolID

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if len(current.path) > maxDepth {
			continue
		}

		callerIDs := g.incoming[current.nodeID]
		if len(callerIDs) == 0 {
			// Reverse so the path runs root → target.
			reversed := make([]shared.SymbolID, len(current.path))
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
			newPath := make([]shared.SymbolID, len(current.path)+1)
			copy(newPath, current.path)
			newPath[len(current.path)] = callerID
			queue = append(queue, pathEntry{nodeID: callerID, path: newPath})
		}
	}

	if len(allPaths) == 0 {
		return nil
	}

	// Priority 1: path whose root contains the route hint.
	if routeHint != "" {
		for _, p := range allPaths {
			if strings.Contains(string(p[0]), routeHint) {
				return p
			}
		}
	}
	// Priority 2: path whose root is a route: node.
	for _, p := range allPaths {
		if strings.HasPrefix(string(p[0]), "route:") {
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

// TraceCallersFrom traces upward from nodeID, building a tree of callers.
// Reverse of TraceFrom. Returns the slice of root caller chains; each
// element is the deepest caller chain ending at nodeID as a leaf.
func (g *Graph) TraceCallersFrom(nodeID shared.SymbolID, maxDepth int) []*TraceNode {
	g.buildAdjacency()
	if _, exists := g.Nodes[nodeID]; !exists {
		return nil
	}
	visited := map[shared.SymbolID]bool{nodeID: true}
	return g.traceCallersRecursive(nodeID, 0, maxDepth, visited)
}

func (g *Graph) traceCallersRecursive(nodeID shared.SymbolID, depth, maxDepth int, visited map[shared.SymbolID]bool) []*TraceNode {
	if maxDepth > 0 && depth >= maxDepth {
		return nil
	}

	callerIDs := g.incoming[nodeID]
	sorted := make([]shared.SymbolID, len(callerIDs))
	copy(sorted, callerIDs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

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

		tn := &TraceNode{Node: callerNode, Depth: depth}
		parents := g.traceCallersRecursive(callerID, depth+1, maxDepth, visited)
		if len(parents) > 0 {
			for _, parent := range parents {
				parentCopy := *parent
				parentCopy.Children = append(parentCopy.Children, tn)
				results = append(results, &parentCopy)
			}
		} else {
			results = append(results, tn)
		}
	}
	return results
}

func (g *Graph) traceNode(node *Node, depth, maxDepth int, visited map[shared.SymbolID]bool, result *TraceResult) *TraceNode {
	tn := &TraceNode{Node: node, Depth: depth}

	if visited[node.ID] {
		tn.IsCycle = true
		result.Cycles = append(result.Cycles, Edge{From: node.ID, To: node.ID, Cycle: true})
		return tn
	}
	visited[node.ID] = true

	if maxDepth > 0 && depth >= maxDepth {
		return tn
	}

	calleeIDs := g.outgoing[node.ID]
	sorted := make([]shared.SymbolID, len(calleeIDs))
	copy(sorted, calleeIDs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	for _, calleeID := range sorted {
		callee, ok := g.Nodes[calleeID]
		if !ok {
			result.Warnings = append(result.Warnings, "edge references unknown node: "+string(calleeID))
			result.Confidence *= 0.9
			continue
		}
		child := g.traceNode(callee, depth+1, maxDepth, visited, result)
		tn.Children = append(tn.Children, child)
	}
	return tn
}

func computeMaxDepth(tn *TraceNode) int {
	if tn == nil {
		return 0
	}
	max := tn.Depth
	for _, child := range tn.Children {
		d := computeMaxDepth(child)
		if d > max {
			max = d
		}
	}
	return max
}

// hasPath checks if there is a directed path from src to dst.
func (g *Graph) hasPath(src, dst shared.SymbolID) bool {
	if src == dst {
		return true
	}
	adj := make(map[shared.SymbolID][]shared.SymbolID)
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	visited := make(map[shared.SymbolID]bool)
	return dfs(src, dst, adj, visited)
}

func dfs(current, target shared.SymbolID, adj map[shared.SymbolID][]shared.SymbolID, visited map[shared.SymbolID]bool) bool {
	if current == target {
		return true
	}
	if visited[current] {
		return false
	}
	visited[current] = true
	for _, next := range adj[current] {
		if dfs(next, target, adj, visited) {
			return true
		}
	}
	return false
}

// InvalidateAdjacency clears the cached adjacency lists. Callers that
// mutate g.Edges directly (e.g. prune passes) must call this before the
// next walk.
func (g *Graph) InvalidateAdjacency() {
	g.outgoing = nil
	g.incoming = nil
}

func (g *Graph) invalidateAdjacency() {
	g.InvalidateAdjacency()
}

func (g *Graph) buildAdjacency() {
	if g.outgoing != nil {
		return
	}
	g.outgoing = make(map[shared.SymbolID][]shared.SymbolID)
	g.incoming = make(map[shared.SymbolID][]shared.SymbolID)
	for _, e := range g.Edges {
		g.outgoing[e.From] = append(g.outgoing[e.From], e.To)
		g.incoming[e.To] = append(g.incoming[e.To], e.From)
	}
}

// TraceResult is the output of TraceFrom — a tree plus per-walk stats.
type TraceResult struct {
	Root       *TraceNode `json:"root,omitempty"`
	TotalNodes int        `json:"total_nodes"`
	MaxDepth   int        `json:"max_depth"`
	Cycles     []Edge     `json:"cycles,omitempty"`
	Confidence float64    `json:"confidence"`
	Warnings   []string   `json:"warnings,omitempty"`
}

// TraceNode is a single node in the trace tree. IsCycle indicates the walk
// short-circuited because this node had been visited; in that case
// Children will be empty.
type TraceNode struct {
	Node     *Node        `json:"node"`
	Children []*TraceNode `json:"children,omitempty"`
	Depth    int          `json:"depth"`
	IsCycle  bool         `json:"is_cycle,omitempty"`
}
