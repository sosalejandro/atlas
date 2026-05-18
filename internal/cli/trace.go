package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newTraceCmd implements `atlas trace <id>` — walk the call graph from a
// feature or symbol id. Supports `saga:<id>` form for saga walks via the
// store-side EDA query.
func newTraceCmd() *cobra.Command {
	var (
		root             string
		maxDepth         int
		nodeModulesPaths []string
	)
	cmd := &cobra.Command{
		Use:   "trace <id>",
		Short: "Walk the call graph from a feature, symbol id, or saga",
		Long: `trace walks Atlas's call graph starting from the supplied id and
emits the chain as text (default) or JSON.

The id can be one of:

  - SymbolID            ("auth.AuthHandler.Login")    — direct match
  - feature suffix      ("AuthHandler.Login")          — fuzzy suffix
  - saga:<id>           ("saga:checkout-flow")         — uses store EDA
                                                         WalkSaga to return
                                                         ordered saga steps

By default trace runs a fresh in-memory scan rooted at --root (or the
repo root). If a state DB exists at the configured path, the store's
edges.Walk is used for the saga: form so the depth-2 follow-up edges
match what's persisted (the in-memory graph is regenerated from source
anyway).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrace(cmd, args[0], root, maxDepth, nodeModulesPaths)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root for the fresh scan (default: repo root or cwd)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 10,
		"maximum trace depth")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; useful when the scanned project lacks its own deps)")
	return cmd
}

// traceResult is the JSON payload for `atlas trace`.
type traceResult struct {
	Kind        string             `json:"kind"` // "call" | "saga"
	Root        shared.SymbolID    `json:"root,omitempty"`
	SagaID      string             `json:"saga_id,omitempty"`
	Confidence  float64            `json:"confidence,omitempty"`
	MaxDepth    int                `json:"max_depth,omitempty"`
	TotalNodes  int                `json:"total_nodes,omitempty"`
	Cycles      []graph.Edge       `json:"cycles,omitempty"`
	Chain       []traceChainEntry  `json:"chain,omitempty"`
	SagaSteps   []traceSagaStep    `json:"saga_steps,omitempty"`
	IndexRoot   string             `json:"index_root,omitempty"`
}

type traceChainEntry struct {
	ID    shared.SymbolID   `json:"id"`
	Kind  shared.SymbolKind `json:"kind"`
	Lang  string            `json:"lang,omitempty"`
	File  string            `json:"file,omitempty"`
	Line  int               `json:"line,omitempty"`
	Depth int               `json:"depth"`
}

type traceSagaStep struct {
	Order int    `json:"order"`
	File  string `json:"file"`
	Line  int    `json:"line"`
	Value string `json:"value"`
}

func runTrace(cmd *cobra.Command, target, rootArg string, maxDepth int, nodeModulesPaths []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Saga branch — go through the persisted store.
	if strings.HasPrefix(target, "saga:") {
		return runTraceSaga(cmd, ctx, strings.TrimPrefix(target, "saga:"))
	}

	rootDir := rootArg
	if rootDir == "" {
		rootDir = loaded.repoRoot
	}
	idx, err := codeindex.IndexProject(ctx, rootDir, codeindex.Options{
		GoOptions: goscan.Options{},
		TSOptions: tsscan.Options{NodeModulesPaths: nodeModulesPaths},
	})
	if err != nil {
		return fmt.Errorf("trace: index %s: %w", rootDir, err)
	}

	rootID := pickEntryPoint(idx, target)
	if rootID == "" {
		return fmt.Errorf("trace: no symbol matches %q", target)
	}
	trace := idx.Graph.TraceFrom(rootID, maxDepth)

	res := traceResult{
		Kind:       "call",
		Root:       rootID,
		Confidence: trace.Confidence,
		MaxDepth:   trace.MaxDepth,
		TotalNodes: trace.TotalNodes,
		Cycles:     trace.Cycles,
		Chain:      flattenTraceChain(trace.Root, idx.SymbolLangs),
		IndexRoot:  rootDir,
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "trace",
			map[string]any{"target": target, "root": rootDir, "max_depth": maxDepth},
			res, trace.Warnings)
	}
	printTraceCallText(cmd, res, trace.Warnings)
	return nil
}

func runTraceSaga(cmd *cobra.Command, ctx context.Context, sagaID string) error {
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("trace saga: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	steps, err := s.EDA().WalkSaga(ctx, sagaID)
	if err != nil {
		return fmt.Errorf("trace saga %q: %w", sagaID, err)
	}
	out := make([]traceSagaStep, 0, len(steps))
	for _, st := range steps {
		out = append(out, traceSagaStep{
			Order: st.Order,
			File:  st.Annotation.FilePath,
			Line:  int(st.Annotation.Line),
			Value: st.Annotation.Value,
		})
	}
	res := traceResult{Kind: "saga", SagaID: sagaID, SagaSteps: out}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "trace",
			map[string]any{"saga_id": sagaID}, res, nil)
	}
	printTraceSagaText(cmd, res)
	return nil
}

func pickEntryPoint(idx *codeindex.Index, feature string) shared.SymbolID {
	want := shared.SymbolID(feature)
	if _, ok := idx.Graph.Nodes[want]; ok {
		return want
	}
	suffix := "." + feature
	for id := range idx.Graph.Nodes {
		if id == want || strings.HasSuffix(string(id), suffix) {
			return id
		}
	}
	return ""
}

func flattenTraceChain(n *graph.TraceNode, langs map[shared.SymbolID]string) []traceChainEntry {
	if n == nil {
		return nil
	}
	var out []traceChainEntry
	walkTraceChain(n, langs, &out)
	return out
}

func walkTraceChain(n *graph.TraceNode, langs map[shared.SymbolID]string, out *[]traceChainEntry) {
	if n == nil {
		return
	}
	lang := ""
	if langs != nil {
		lang = langs[n.Node.ID]
	}
	*out = append(*out, traceChainEntry{
		ID:    n.Node.ID,
		Kind:  n.Node.Kind,
		Lang:  lang,
		File:  n.Node.Position.Path,
		Line:  n.Node.Position.Line,
		Depth: n.Depth,
	})
	for _, c := range n.Children {
		walkTraceChain(c, langs, out)
	}
}

func printTraceCallText(cmd *cobra.Command, r traceResult, warnings []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "trace %s (confidence %.2f, %d nodes)\n",
		r.Root, r.Confidence, r.TotalNodes)
	for _, e := range r.Chain {
		indent := strings.Repeat("  ", e.Depth)
		fmt.Fprintf(cmd.OutOrStdout(), "%s%s  [%s] %s:%d\n",
			indent, e.ID, e.Kind, e.File, e.Line)
	}
	for _, c := range r.Cycles {
		fmt.Fprintf(cmd.ErrOrStderr(), "  cycle: %s -> %s\n", c.From, c.To)
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}

func printTraceSagaText(cmd *cobra.Command, r traceResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "saga %s (%d steps)\n", r.SagaID, len(r.SagaSteps))
	for _, st := range r.SagaSteps {
		fmt.Fprintf(cmd.OutOrStdout(), "  step %d  %s:%d  %s\n",
			st.Order, st.File, st.Line, st.Value)
	}
}
