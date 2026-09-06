package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	"github.com/sosalejandro/atlas/packages/graph"
)

// newResolveCmd implements `atlas resolve` — the instrument for issue #87.
//
// Every other verb reports WHAT the call graph says. This one reports how
// atlas arrived at it: which Go packages type-checked, which fell back to
// name matching and why, and the resulting edges split by resolution tier
// (issue #146). Those are different questions, and conflating them is
// what let two years of name-heuristic call resolution look like an
// answer. A repo whose graph is 90% syntactic can still be browsed; the
// change-impact numbers taken over it are guesses stacked on guesses, and
// nothing else in the CLI would say so.
//
// It does not touch the store. The tier histogram in `atlas doctor` reads
// what the last scan PERSISTED; this reads what a scan of the working
// tree would produce right now, which is the number you want while you
// are fixing the build that made half the repo degrade.
func newResolveCmd() *cobra.Command {
	var (
		root string
		ast  bool
	)

	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Report how the Go call graph was resolved (typed / name / syntactic)",
		Long: `resolve scans the Go tree and reports the MECHANISM behind every
call edge rather than the edges themselves.

Atlas resolves Go calls with go/packages and go/types, falling back per
package to AST name matching when a package does not type-check. That
fallback is silent by design -- a scan of a half-refactored repo has to
succeed -- so this verb is how you find out it happened:

  packages       how many type-checked, how many degraded, and the first
                 type error for each degradation
  edges by tier  typed        a type checker resolved it
                 name_resolved  a name was bound to an indexed declaration
                 syntactic      the shape of the source suggested it
  timings        what the type-checked load cost, so a slow scan has an
                 attributable number rather than a suspicion

--ast re-runs the same scan with typed resolution disabled and prints
both histograms side by side. That is the diff worth reading before
trusting a change to the resolver, and the one number that must never
move the wrong way: the count of edges atlas is guessing about.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runResolve(cmd, root, ast)
		},
	}

	cmd.Flags().StringVar(&root, "root", ".", "project root to scan")
	cmd.Flags().BoolVar(&ast, "ast", false,
		"also scan with typed resolution disabled and compare the two histograms")

	return cmd
}

// resolveResult is the JSON shape. Counts are of EDGES for the histogram
// and of PACKAGES for the load, which the field names have to keep
// straight because they are routinely off by an order of magnitude from
// each other and nothing else would catch a mix-up.
type resolveResult struct {
	Root string `json:"root"`

	Packages    int `json:"packages"`
	TypeChecked int `json:"type_checked"`
	Degraded    int `json:"degraded"`

	IndexedFiles      int `json:"indexed_files"`
	TypedIndexedFiles int `json:"typed_indexed_files"`

	CallGraph   bool `json:"call_graph"`
	InvokeSites int  `json:"invoke_sites"`

	Symbols int `json:"symbols"`
	Edges   int `json:"edges"`

	// Tiers is the histogram, keyed by the tier vocabulary in
	// packages/graph. Every tier appears even at zero: a histogram whose
	// columns come and go cannot be diffed across runs, and diffing it is
	// the point.
	Tiers map[string]int `json:"tiers"`

	// ASTTiers is the same histogram with typed resolution disabled,
	// present only with --ast.
	ASTTiers map[string]int `json:"ast_tiers,omitempty"`

	DegradedPackages []goscan.DegradedPackage `json:"degraded_packages,omitempty"`

	LoadMS      int64 `json:"load_ms"`
	CallGraphMS int64 `json:"call_graph_ms"`
	ScanMS      int64 `json:"scan_ms"`

	// ASTScanMS is the same scan with typed resolution off, present only
	// with --ast. The pair is the answer to "what does type checking
	// cost me", which is a question about the WHOLE scan and not about
	// the load: the typed path also skips a second parse of every file
	// it adopts, so the load duration alone overstates the price.
	ASTScanMS int64 `json:"ast_scan_ms,omitempty"`

	// Unavailable is set when nothing could be type-checked at all -- no
	// module, no toolchain -- as opposed to a tree that loaded and does
	// not compile.
	Unavailable string `json:"unavailable,omitempty"`
}

func runResolve(cmd *cobra.Command, root string, withAST bool) error {
	ctx := cmd.Context()

	start := time.Now()
	res, err := goscan.Scan(ctx, root, goscan.Options{})
	elapsed := time.Since(start)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}

	out := resolveResult{
		Root:    root,
		Symbols: len(res.Symbols),
		Edges:   len(res.Graph.Edges),
		Tiers:   tierHistogram(res),
		ScanMS:  elapsed.Milliseconds(),
	}
	if r := res.Resolution; r != nil {
		out.Packages = r.Packages
		out.TypeChecked = r.TypeChecked
		out.Degraded = r.Degraded
		out.IndexedFiles = r.IndexedFiles
		out.TypedIndexedFiles = r.TypedIndexedFiles
		out.CallGraph = r.CallGraph
		out.InvokeSites = r.InvokeSites
		out.DegradedPackages = r.DegradedPackages
		out.LoadMS = r.LoadDuration.Milliseconds()
		out.CallGraphMS = r.CallGraphDuration.Milliseconds()
		out.Unavailable = r.Unavailable
	}

	if withAST {
		astStart := time.Now()
		astRes, err := goscan.Scan(ctx, root, goscan.Options{SkipTypedResolution: true})
		out.ASTScanMS = time.Since(astStart).Milliseconds()
		if err != nil {
			return fmt.Errorf("scan %s without types: %w", root, err)
		}
		out.ASTTiers = tierHistogram(astRes)
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "resolve",
			map[string]any{"root": root, "ast": withAST}, out, res.Warnings)
	}
	printResolveText(cmd, out, res.Warnings)
	return nil
}

// tierHistogram counts a scan's edges per tier, with every tier in the
// closed vocabulary present.
func tierHistogram(res *goscan.Result) map[string]int {
	h := make(map[string]int, len(graph.AllTiers()))
	for _, t := range graph.AllTiers() {
		h[string(t)] = 0
	}
	for _, e := range res.Graph.Edges {
		h[string(e.Tier)]++
	}
	return h
}

func printResolveText(cmd *cobra.Command, r resolveResult, warnings []string) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Go call resolution for %s\n", r.Root)

	if r.Unavailable != "" {
		fmt.Fprintf(w, "  type checking unavailable: %s\n", r.Unavailable)
		fmt.Fprintf(w, "  every edge below was produced by name matching.\n")
	} else {
		fmt.Fprintf(w, "  packages: %d type-checked, %d degraded (of %d)\n",
			r.TypeChecked, r.Degraded, r.Packages)
		fmt.Fprintf(w, "  files:    %d of %d indexed files resolved with types\n",
			r.TypedIndexedFiles, r.IndexedFiles)
		fmt.Fprintf(w, "  dispatch: class-hierarchy analysis %s, %d interface call sites resolved\n",
			enabledWord(r.CallGraph), r.InvokeSites)
		fmt.Fprintf(w, "  cost:     load %dms, call graph %dms, whole scan %dms\n",
			r.LoadMS, r.CallGraphMS, r.ScanMS)
	}

	fmt.Fprintf(w, "\n  %d symbols, %d edges\n", r.Symbols, r.Edges)
	printTierTable(cmd, r)
	if r.ASTScanMS > 0 {
		// Rendered through %8s rather than %8dms so the two duration
		// cells land under the two count columns above them. A table
		// whose last row is offset by the width of "ms" reads as a
		// different table.
		fmt.Fprintf(w, "    %-14s %8s %8s\n", "scan",
			millis(r.ASTScanMS), millis(r.ScanMS))
	}

	if len(r.DegradedPackages) > 0 {
		fmt.Fprintf(w, "\n  degraded packages (first type error each):\n")
		for _, d := range r.DegradedPackages {
			fmt.Fprintf(w, "    %s\n      %s\n", d.Path, d.Error)
		}
	}
	for _, warn := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", warn)
	}
}

// printTierTable renders the histogram in the fixed strength order
// packages/graph declares, with the --ast column beside it when asked.
func printTierTable(cmd *cobra.Command, r resolveResult) {
	w := cmd.OutOrStdout()
	if r.ASTTiers == nil {
		for _, t := range graph.AllTiers() {
			fmt.Fprintf(w, "    %-14s %6d\n", t, r.Tiers[string(t)])
		}
		return
	}
	fmt.Fprintf(w, "    %-14s %8s %8s %8s\n", "tier", "--ast", "typed", "delta")
	for _, t := range graph.AllTiers() {
		before, after := r.ASTTiers[string(t)], r.Tiers[string(t)]
		fmt.Fprintf(w, "    %-14s %8d %8d %+8d\n", t, before, after, after-before)
	}
}

// millis renders a duration cell ("5672ms").
func millis(ms int64) string { return strconv.FormatInt(ms, 10) + "ms" }

func enabledWord(on bool) string {
	if on {
		return "ran"
	}
	return "did not run"
}
