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
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// traceEnvelope is the stable JSON contract for `atlas trace`.
//
// Per docs/architecture.md §6:
//   - schema_version pinned to "v1"; additive changes within v1 do NOT bump
//   - command identifies the verb so a multi-output consumer can dispatch
//   - generated_at is UTC RFC3339
//   - data is the payload (trace tree + chain summary)
//
// JSON tag names use lowerCamel per the v1 convention.
type traceEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Command       string          `json:"command"`
	GeneratedAt   string          `json:"generated_at"`
	Data          traceData       `json:"data"`
}

type traceData struct {
	FeatureID   string             `json:"feature_id"`
	Root        shared.SymbolID    `json:"root"`
	Confidence  float64            `json:"confidence"`
	MaxDepth    int                `json:"max_depth"`
	TotalNodes  int                `json:"total_nodes"`
	Cycles      []graph.Edge       `json:"cycles,omitempty"`
	Chain       []chainEntry       `json:"chain"`
	Warnings    []string           `json:"warnings,omitempty"`
}

type chainEntry struct {
	ID    shared.SymbolID    `json:"id"`
	Kind  shared.SymbolKind  `json:"kind"`
	File  string             `json:"file,omitempty"`
	Line  int                `json:"line,omitempty"`
	Depth int                `json:"depth"`
}

// newTraceCmd builds `atlas trace --root <dir> --feature <id>`.
//
// Phase 1 contract: --feature is treated as a SymbolID (exact match) or as a
// fuzzy suffix match against codeindex symbols. The first hit is the trace
// root. Future phases will look up the feature via the resolver and emit a
// trace per implementation symbol.
func newTraceCmd() *cobra.Command {
	var (
		root     string
		feature  string
		maxDepth int
	)
	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Trace the call graph for a feature or symbol",
		Long:  "Indexes <root>, finds the symbol matching --feature, and emits the call chain as JSON.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if root == "" || feature == "" {
				return fmt.Errorf("--root and --feature are required")
			}
			return runTrace(cmd.Context(), cmd.OutOrStdout(), root, feature, maxDepth)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "project root directory (required)")
	cmd.Flags().StringVar(&feature, "feature", "", "feature id or symbol id to trace (required)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", 10, "maximum trace depth")
	return cmd
}

func runTrace(ctx context.Context, w io.Writer, root, feature string, maxDepth int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	idx, err := codeindex.IndexProject(ctx, root, codeindex.Options{
		GoOptions: goscan.Options{},
	})
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
			Chain:      flatten(trace.Root),
			Warnings:   trace.Warnings,
		},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
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

// flatten linearises a trace tree into a depth-first list of chain
// entries for compact JSON.
func flatten(n *graph.TraceNode) []chainEntry {
	if n == nil {
		return nil
	}
	var out []chainEntry
	walk(n, &out)
	return out
}

func walk(n *graph.TraceNode, out *[]chainEntry) {
	if n == nil {
		return
	}
	*out = append(*out, chainEntry{
		ID:    n.Node.ID,
		Kind:  n.Node.Kind,
		File:  n.Node.Position.Path,
		Line:  n.Node.Position.Line,
		Depth: n.Depth,
	})
	for _, c := range n.Children {
		walk(c, out)
	}
}
