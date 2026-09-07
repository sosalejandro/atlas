package cli

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/scip"
	"github.com/spf13/cobra"
)

func newSCIPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scip",
		Short: "Read an index produced by somebody else's indexer (SCIP)",
		Long: `scip reads an index.scip -- the interchange format produced by scip-java,
scip-python, scip-ruby, scip-dotnet, rust-analyzer and others -- and reports
what atlas would take from it.

This is how atlas covers a language it has no scanner for. What it is NOT is
a claim that atlas verified any of it: every edge from this path is recorded
at the "imported" tier, because "scip-java said so" and "we type-checked it"
are different claims even when they usually agree. The tier histogram then
tells you exactly how much of your graph rests on someone else's work.

The counters are the point as much as the symbols are. An index whose
references all lead outside it produces no edges, and without the counts that
is indistinguishable from a clean run over code with no calls.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newSCIPInspectCmd())
	return cmd
}

func newSCIPInspectCmd() *cobra.Command {
	var input string
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Report what atlas would import from an index.scip",
		Long: `inspect parses an index.scip and reports the symbols, edges and -- most
usefully -- what it could NOT attribute, without writing anything.

Run it before wiring an indexer into CI: it answers "is this index worth
ingesting" in one command, which is a cheaper question than discovering the
answer after it is in the store.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSCIPInspect(cmd, input)
		},
	}
	cmd.Flags().StringVar(&input, "input", "",
		"path to index.scip, or - for stdin (required)")
	return cmd
}

func runSCIPInspect(cmd *cobra.Command, input string) error {
	if input == "" {
		return usagef("scip inspect: --input is required (path to index.scip, or - for stdin)")
	}

	var r io.Reader
	if input == "-" {
		r = cmd.InOrStdin()
	} else {
		f, err := os.Open(input)
		if err != nil {
			// Cannot look, rather than looked and found nothing. See
			// exitcode.go: a CI step that treats these alike retries the
			// wrong one.
			return undeterminedf("scip inspect: open %s: %w", input, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	idx, err := scip.Read(r)
	if err != nil {
		return undeterminedf("scip inspect: %w", err)
	}
	res := scip.Convert(idx)

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "scip.inspect",
			map[string]any{"input": input},
			map[string]any{
				"tool":         res.Tool,
				"project_root": res.ProjectRoot,
				"languages":    res.Languages,
				"symbols":      len(res.Symbols),
				"edges":        len(res.Edges),
				"tier":         string(graph.TierImported),
				"stats":        res.Stats,
			}, nil)
	}
	printSCIPInspect(stdoutOrJSON(cmd), res)
	return nil
}

func printSCIPInspect(w io.Writer, res scip.Result) {
	tool := res.Tool
	if tool == "" {
		tool = "(the index names no tool)"
	}
	fmt.Fprintf(w, "atlas scip inspect — %s\n\n", tool)
	if res.ProjectRoot != "" {
		fmt.Fprintf(w, "  project root  %s\n", res.ProjectRoot)
	}
	fmt.Fprintf(w, "  documents     %d\n", res.Stats.Documents)
	fmt.Fprintf(w, "  symbols       %d", len(res.Symbols))
	if res.Stats.DefinitionsLocal > 0 {
		fmt.Fprintf(w, "  (+%d function-scoped, not addressable across files)",
			res.Stats.DefinitionsLocal)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  edges         %d, all at the %q tier\n\n",
		len(res.Edges), graph.TierImported)

	if len(res.Languages) > 0 {
		langs := make([]string, 0, len(res.Languages))
		for l := range res.Languages {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		fmt.Fprintln(w, "  by language")
		for _, l := range langs {
			fmt.Fprintf(w, "    %-14s %d definitions\n", l, res.Languages[l])
		}
		fmt.Fprintln(w)
	}

	// The half that is usually omitted, printed first among equals: what the
	// index could not tell us. A reader deciding whether to trust this needs
	// the denominator, not the headline.
	st := res.Stats
	fmt.Fprintf(w, "  of %d reference(s):\n", st.References)
	fmt.Fprintf(w, "    %6d became edges\n", st.Edges)
	fmt.Fprintf(w, "    %6d named a symbol this index does not define "+
		"(a call out of the indexed set)\n", st.ReferencesUnresolved)
	fmt.Fprintf(w, "    %6d were function-scoped\n", st.ReferencesLocal)
	fmt.Fprintf(w, "    %6d sat inside no definition, so there is no caller "+
		"to attribute them to\n", st.ReferencesOutsideDefinition)

	if !st.Reconciles() {
		// Arithmetic that does not add up is a bug in atlas, not a property
		// of the input, and saying so is cheaper than a user wondering.
		fmt.Fprintf(w, "\n  WARNING: these do not sum to %d. That is an atlas bug; "+
			"please report it with the index that produced it.\n", st.References)
	}
	if len(res.Edges) == 0 && st.References > 0 {
		fmt.Fprintln(w, "\n  No edges. This index defines symbols but atlas could attribute")
		fmt.Fprintln(w, "  no call between them — most often because the indexer emits no")
		fmt.Fprintln(w, "  enclosing ranges. The symbols are still worth importing; the")
		fmt.Fprintln(w, "  call graph is not there to import.")
	}
}
