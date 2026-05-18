package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// traceEnvelope is the stable JSON contract for `atlas trace`.
//
// Per docs/architecture.md §6:
//   - schema_version pinned to "v1"; additive changes within v1 do NOT bump.
//   - command identifies the verb so a multi-output consumer can dispatch
//   - generated_at is UTC RFC3339
//   - data is the payload (trace tree + chain summary)
//
// Phase 2 added the optional `lang` field on chain entries; that is additive
// and so does NOT change the schema_version per the rule above. Consumers
// MUST ignore unknown fields.
//
// JSON tag names use lowerCamel per the v1 convention.
type traceEnvelope struct {
	SchemaVersion string    `json:"schema_version"`
	Command       string    `json:"command"`
	GeneratedAt   string    `json:"generated_at"`
	Data          traceData `json:"data"`
}

type traceData struct {
	FeatureID  string          `json:"feature_id"`
	Root       shared.SymbolID `json:"root"`
	Confidence float64         `json:"confidence"`
	MaxDepth   int             `json:"max_depth"`
	TotalNodes int             `json:"total_nodes"`
	Cycles     []graph.Edge    `json:"cycles,omitempty"`
	Chain      []chainEntry    `json:"chain"`
	Warnings   []string        `json:"warnings,omitempty"`
}

type chainEntry struct {
	ID   shared.SymbolID   `json:"id"`
	Kind shared.SymbolKind `json:"kind"`
	// Lang identifies the source language for this hop. Values: "go", "ts",
	// or "" when the orchestrator couldn't classify it (this can happen for
	// nodes that the scanner emitted as placeholders — e.g. unresolved
	// handler references).
	Lang  string `json:"lang,omitempty"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	Depth int    `json:"depth"`
}

// newTraceCmd builds `atlas trace --root <dir> --feature <id>`.
//
// Phase 1 contract: --feature is treated as a SymbolID (exact match) or as a
// fuzzy suffix match against codeindex symbols. The first hit is the trace
// root. Future phases will look up the feature via the resolver and emit a
// trace per implementation symbol.
//
// Phase 2 additions: --node-modules-path lets the operator point the TS
// sub-scanner at an external typescript install when the project being
// traced doesn't ship its own (e.g. a minimal fixture or a backend-only
// monorepo with TS surface borrowed from a sibling package).
func newTraceCmd() *cobra.Command {
	var (
		root             string
		feature          string
		maxDepth         int
		nodeModulesPaths []string
	)
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Trace the call graph for a feature or symbol",
		Long:  "Indexes <root>, finds the symbol matching --feature, and emits the call chain as JSON.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if root == "" || feature == "" {
				return fmt.Errorf("--root and --feature are required")
			}
			return runTraceWithOpts(cmd.Context(), cmd.OutOrStdout(), root, feature, maxDepth,
				codeindex.Options{
					GoOptions: goscan.Options{},
					TSOptions: tsscan.Options{NodeModulesPaths: nodeModulesPaths},
				})
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "project root directory (required)")
	cmd.Flags().StringVar(&feature, "feature", "", "feature id or symbol id to trace (required)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 10, "maximum trace depth")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; useful when the scanned project lacks its own deps)")
	return cmd
}

// runTrace is the simple wrapper preserved for backwards-compat with
// existing tests. New callers should prefer runTraceWithOpts.
func runTrace(ctx context.Context, w io.Writer, root, feature string, maxDepth int) error {
	return runTraceWithOpts(ctx, w, root, feature, maxDepth, codeindex.Options{
		GoOptions: goscan.Options{},
	})
}

func runTraceWithOpts(ctx context.Context, w io.Writer, root, feature string, maxDepth int, opts codeindex.Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	idx, err := codeindex.IndexProject(ctx, root, opts)
	if err != nil {
		return fmt.Errorf("index project: %w", err)
	}

	rootID := pickEntryPoint(idx, feature)
	if rootID == "" {
		return fmt.Errorf("no symbol matches feature %q", feature)
	}

	trace := idx.Graph.TraceFrom(rootID, maxDepth)
	env := traceEnvelope{
		SchemaVersion: "v1",
		Command:       "trace",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Data: traceData{
			FeatureID:  feature,
			Root:       rootID,
			Confidence: trace.Confidence,
			MaxDepth:   trace.MaxDepth,
			TotalNodes: trace.TotalNodes,
			Cycles:     trace.Cycles,
			Chain:      flattenWithLang(trace.Root, idx.SymbolLangs),
			Warnings:   trace.Warnings,
		},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return fmt.Errorf("encode trace envelope: %w", err)
	}
	return nil
}

// pickEntryPoint chooses the best matching SymbolID for the requested
// feature string. Exact match wins; otherwise the first suffix-match in
// stable iteration order.
func pickEntryPoint(idx *codeindex.Index, feature string) shared.SymbolID {
	want := shared.SymbolID(feature)
	if _, ok := idx.Graph.Nodes[want]; ok {
		return want
	}
	suffix := "." + feature
	for id := range idx.Graph.Nodes {
		if id == want || hasSuffix(string(id), suffix) {
			return id
		}
	}
	return ""
}

func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// flattenWithLang linearises a trace tree into a depth-first list of chain
// entries for compact JSON, annotating each hop with its source language.
// langs is the orchestrator-built map idx.SymbolLangs (nil-safe).
func flattenWithLang(n *graph.TraceNode, langs map[shared.SymbolID]string) []chainEntry {
	if n == nil {
		return nil
	}
	var out []chainEntry
	walkWithLang(n, langs, &out)
	return out
}

func walkWithLang(n *graph.TraceNode, langs map[shared.SymbolID]string, out *[]chainEntry) {
	if n == nil {
		return
	}
	lang := ""
	if langs != nil {
		lang = langs[n.Node.ID]
	}
	*out = append(*out, chainEntry{
		ID:    n.Node.ID,
		Kind:  n.Node.Kind,
		Lang:  lang,
		File:  n.Node.Position.Path,
		Line:  n.Node.Position.Line,
		Depth: n.Depth,
	})
	for _, c := range n.Children {
		walkWithLang(c, langs, out)
	}
}
