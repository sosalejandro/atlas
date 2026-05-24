package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/store"
)

// cyclesFlags holds the cobra-bound state for `atlas codebase cycles`.
// Kept as a struct (rather than file-scope vars) so multiple
// invocations in tests don't bleed flag values between runs.
type cyclesFlags struct {
	Scope       string
	ScopeFilter string
}

// scopeFilterAll is the sentinel value users pass to --scope-filter
// to request every import edge regardless of edge_meta. Anything else
// is interpreted as one of the canonical store.EdgeMetaImportScope*
// values and validated up-front.
const scopeFilterAll = "all"

// validScopeFilters lists every value --scope-filter accepts. The
// default "module" matches the issue spec — module-level imports are
// the only ones that cause a real load-time circular-import error,
// so they're the highest-signal default. The other scope tags
// (function/conditional/type_checking/try_guard) are deferred or
// intentional and stay hidden unless the user opts in.
var validScopeFilters = []string{
	store.EdgeMetaImportScopeModule,
	store.EdgeMetaImportScopeFunction,
	store.EdgeMetaImportScopeConditional,
	store.EdgeMetaImportScopeTypeChecking,
	store.EdgeMetaImportScopeTryGuard,
	scopeFilterAll,
}

func newCodebaseCyclesCmd() *cobra.Command {
	cf := &cyclesFlags{ScopeFilter: store.EdgeMetaImportScopeModule}
	cmd := &cobra.Command{
		Use:   "cycles",
		Short: "Detect circular imports via SCC over the import graph",
		Long: `cycles walks the kind='import' edge subgraph (joined to symbols
on both endpoints for the file path) and detects strongly-connected
components via Tarjan's algorithm. Single-node SCCs are filtered —
only real circular imports (>= 2 distinct files) are reported.

Output groups cycles by length (2-node first), names every file in
the cycle, and surfaces the import-scope tag (module / function /
conditional / type_checking / try_guard) on every participating edge
so deferred-import workarounds can be told apart from real
load-time cycles.

Default --scope-filter is "module" (only real cycles); pass
"--scope-filter all" to include every cycle regardless of import
scope, or one of "function", "conditional", "type_checking",
"try_guard" to target a single scope. Closes issue atlas-internal #14.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCodebaseCycles(cmd, cf)
		},
	}
	cmd.Flags().StringVar(&cf.Scope, "scope", "",
		"narrow analysis to symbols whose qualified name starts with this prefix")
	cmd.Flags().StringVar(&cf.ScopeFilter, "scope-filter", store.EdgeMetaImportScopeModule,
		"import-scope tag to include (module|function|conditional|type_checking|try_guard|all)")
	return cmd
}

// codebaseCyclesResult is the JSON envelope's Result body. Cycles is
// the deterministic list returned by graph.FindCycles; TotalEdges is
// the size of the projection the SCC algorithm walked over (useful
// for "did my filter actually filter anything" sanity checks).
type codebaseCyclesResult struct {
	Cycles     []graph.Cycle `json:"cycles"`
	TotalEdges int           `json:"total_edges"`
	Scope      string        `json:"scope,omitempty"`
	Filter     string        `json:"scope_filter"`
}

func runCodebaseCycles(cmd *cobra.Command, cf *cyclesFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Validate --scope-filter against the closed vocabulary before
	// touching the DB. An unknown value here is a user error (typo
	// in the flag), not a "no cycles match" case — failing fast
	// keeps the surprise low.
	if !isValidScopeFilter(cf.ScopeFilter) {
		return fmt.Errorf("codebase cycles: --scope-filter must be one of %s",
			strings.Join(validScopeFilters, "|"))
	}

	s, err := openStoreForRead(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	filter := store.ImportEdgeFilter{SymbolPrefix: cf.Scope}
	if cf.ScopeFilter != scopeFilterAll {
		filter.Scopes = []string{cf.ScopeFilter}
	}

	rows, err := s.Edges().ListImportEdges(ctx, filter)
	if err != nil {
		return fmt.Errorf("codebase cycles: %w", err)
	}

	// Project store rows into the graph-layer edge shape. The two
	// types are deliberately distinct (store doesn't depend on
	// graph and vice versa) so this conversion is the seam between
	// the persistence layer and the algorithm.
	edges := make([]graph.CycleEdge, 0, len(rows))
	for _, r := range rows {
		edges = append(edges, graph.CycleEdge{
			From:  r.FromFile,
			To:    r.ToFile,
			Scope: r.Scope,
			Line:  r.Line,
		})
	}
	cycles := graph.FindCycles(edges)

	res := codebaseCyclesResult{
		Cycles:     cycles,
		TotalEdges: len(rows),
		Scope:      cf.Scope,
		Filter:     cf.ScopeFilter,
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "codebase.cycles",
			map[string]any{"scope": cf.Scope, "scope_filter": cf.ScopeFilter},
			res, nil)
	}
	renderCyclesText(cmd.OutOrStdout(), res)
	return nil
}

// isValidScopeFilter is a closed-set membership check on the
// --scope-filter flag. We do this in Go rather than relying on cobra
// because cobra's enum validator is not yet a stable surface and we
// want a single source of truth for the vocabulary.
func isValidScopeFilter(v string) bool {
	for _, s := range validScopeFilters {
		if v == s {
			return true
		}
	}
	return false
}

// renderCyclesText writes the human-friendly grouped output the issue
// example specifies. Cycles are bucketed by length (2-node, 3-node,
// ...), each bucket's count is printed, then each cycle's nodes are
// listed with the `<->` (2-node) or `->` (longer) connector. Scope
// hints — when a cycle's edges include any non-module scope — are
// annotated inline so the user can tell deferred-import workarounds
// from real cycles at a glance.
func renderCyclesText(w io.Writer, res codebaseCyclesResult) {
	if len(res.Cycles) == 0 {
		fmt.Fprintf(w, "atlas codebase cycles\n  no cycles found (scanned %d import edges, filter=%s)\n",
			res.TotalEdges, res.Filter)
		return
	}

	// Bucket by length so we can print "2-node cycles: N" headers
	// matching the issue spec.
	buckets := make(map[int][]graph.Cycle)
	var lengths []int
	for _, c := range res.Cycles {
		if _, ok := buckets[c.Length]; !ok {
			lengths = append(lengths, c.Length)
		}
		buckets[c.Length] = append(buckets[c.Length], c)
	}
	sort.Ints(lengths)

	fmt.Fprintln(w, "atlas codebase cycles")
	for i, n := range lengths {
		if i > 0 {
			fmt.Fprintln(w)
		}
		group := buckets[n]
		fmt.Fprintf(w, "  %d-node cycles: %d\n", n, len(group))
		for _, c := range group {
			renderOneCycle(w, c)
		}
	}
}

// renderOneCycle prints a single cycle in the issue-spec format. For
// 2-node cycles we use the bidirectional arrow; for longer cycles we
// show the directed walk plus an explicit close-the-loop arrow back
// to the starting node so the cycle is unambiguous. Edges that
// carry a non-module scope get a parenthetical annotation flagging
// them as deferred-import workarounds.
func renderOneCycle(w io.Writer, c graph.Cycle) {
	if c.Length == 2 {
		fmt.Fprintf(w, "    %s\n", c.Nodes[0])
		fmt.Fprintf(w, "    <-> %s\n", c.Nodes[1])
	} else {
		// Reconstruct the directed walk by following edges. The
		// SCC gives us the node set; we need the cycle order
		// for the user. Walk forward greedily: start at the
		// alphabetically-first node, follow any outgoing edge
		// to another node in the SCC, repeat until we close.
		walk := reconstructCycleWalk(c)
		for i, node := range walk {
			if i == 0 {
				fmt.Fprintf(w, "    %s\n", node)
			} else {
				fmt.Fprintf(w, "    -> %s\n", node)
			}
		}
		// Explicit close so the cycle isn't ambiguous to a
		// human scanner.
		fmt.Fprintf(w, "    -> %s\n", walk[0])
	}
	// Annotate deferred-import edges so the user can tell which
	// cycle is intentional vs unintentional.
	for _, e := range c.Edges {
		if e.Scope != "" && e.Scope != store.EdgeMetaImportScopeModule {
			fmt.Fprintf(w, "    (%s-import edge at %s:%d — flagged as %s scope)\n",
				e.Scope, e.From, e.Line, e.Scope)
		}
	}
}

// reconstructCycleWalk converts an unordered SCC node set + edge list
// into the directed walk a human reader expects (a -> b -> c -> a).
// We do a simple greedy DFS starting at the alphabetically-first
// node, always picking the alphabetically-smallest unvisited next
// hop. This is O(N^2) on N-node cycles but N is bounded by a few
// hundred in the worst case so the constant factor doesn't matter.
func reconstructCycleWalk(c graph.Cycle) []string {
	if len(c.Nodes) == 0 {
		return nil
	}
	adjacency := make(map[string][]string, len(c.Nodes))
	for _, e := range c.Edges {
		adjacency[e.From] = append(adjacency[e.From], e.To)
	}
	for k := range adjacency {
		sort.Strings(adjacency[k])
	}

	start := c.Nodes[0]
	walk := []string{start}
	visited := map[string]bool{start: true}
	current := start
	for len(walk) < len(c.Nodes) {
		var next string
		for _, n := range adjacency[current] {
			if !visited[n] {
				next = n
				break
			}
		}
		if next == "" {
			// Shouldn't happen on a real SCC (every node
			// has an outgoing edge to another SCC member)
			// but guard against pathological inputs.
			break
		}
		walk = append(walk, next)
		visited[next] = true
		current = next
	}
	return walk
}
