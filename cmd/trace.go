package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/spf13/cobra"
)

var (
	traceDepth     int
	traceVerbose   bool
	traceFormat    string
	traceListNodes bool
	traceValidate  bool
	traceKind      string
)

var traceCmd = &cobra.Command{
	Use:   "trace <feature-id>",
	Short: "Trace a feature's dependency graph",
	Long: `Traces the call graph for a feature starting from its API entry points.
Shows the chain of handlers, services, repositories, and queries that
implement the feature. Useful for understanding what code is involved
when a feature breaks.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		featureID := args[0]

		// Load graph configuration.
		graphSection, err := adapters.LoadGraphConfig(resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("loading graph config: %w", err)
		}

		config := graphSection.ToPortsConfig()
		config.ProjectRoot = resolvedProjectRoot()

		// Override max depth if the flag was explicitly set.
		if cmd.Flags().Changed("depth") {
			config.MaxDepth = traceDepth
		}

		// Create dependencies.
		store := adapters.NewYAMLStore()
		builder := adapters.NewGraphBuilder(config)
		useCase := app.NewTraceFeatureUseCase(store, builder)

		result, err := useCase.Execute(resolvedRegistryDir(), featureID, config)
		if err != nil {
			return fmt.Errorf("tracing feature %q: %w", featureID, err)
		}

		// Handle --list-nodes: flat deduplicated list of node IDs.
		if traceListNodes {
			return outputTraceNodeList(result, traceKind)
		}

		// Handle --validate: check trace integrity.
		if traceValidate {
			return outputTraceValidation(result)
		}

		// Render output.
		if traceFormat == "json" {
			return outputTraceJSON(result)
		}

		return outputTraceTree(result)
	},
}

func init() {
	traceCmd.Flags().IntVar(&traceDepth, "depth", 0, "Max traversal depth (default from config, fallback 10)")
	traceCmd.Flags().BoolVar(&traceVerbose, "verbose", false, "Include utility functions in trace")
	traceCmd.Flags().StringVar(&traceFormat, "format", "tree", "Output format: tree, json")
	traceCmd.Flags().BoolVar(&traceListNodes, "list-nodes", false, "Output flat list of all node IDs")
	traceCmd.Flags().BoolVar(&traceValidate, "validate", false, "Validate trace integrity (duplicates, cycles, missing refs)")
	traceCmd.Flags().StringVar(&traceKind, "kind", "", "Filter nodes by kind (handler, service, repository, query, component, hook)")
	rootCmd.AddCommand(traceCmd)
}

func outputTraceJSON(result *app.TraceOutput) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// collectTraceNodes walks all trace trees and collects unique nodes,
// optionally filtering by NodeKind.
func collectTraceNodes(result *app.TraceOutput, kindFilter string) []*domain.Node {
	seen := make(map[string]bool)
	var nodes []*domain.Node

	var walk func(tn *domain.TraceNode)
	walk = func(tn *domain.TraceNode) {
		if tn == nil || tn.Node == nil {
			return
		}
		if seen[tn.Node.ID] {
			for _, child := range tn.Children {
				walk(child)
			}
			return
		}
		seen[tn.Node.ID] = true

		if kindFilter == "" || string(tn.Node.Kind) == kindFilter {
			nodes = append(nodes, tn.Node)
		}

		for _, child := range tn.Children {
			walk(child)
		}
	}

	for _, trace := range result.Traces {
		if trace.Root != nil {
			walk(trace.Root)
		}
	}

	// Sort for deterministic output.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})

	return nodes
}

// outputTraceNodeList prints a flat, deduplicated list of node IDs.
func outputTraceNodeList(result *app.TraceOutput, kindFilter string) error {
	nodes := collectTraceNodes(result, kindFilter)
	if len(nodes) == 0 {
		if kindFilter != "" {
			fmt.Fprintf(os.Stderr, "No nodes found with kind %q\n", kindFilter)
		} else {
			fmt.Fprintln(os.Stderr, "No nodes found in trace")
		}
		return nil
	}
	for _, n := range nodes {
		fmt.Println(n.ID)
	}
	return nil
}

// validateTrace checks a trace output for integrity issues: duplicate node IDs
// across traces, cycles, and warnings from the trace engine.
func validateTrace(result *app.TraceOutput) (warnings []string) {
	// Check for duplicate node IDs across all traces.
	idCount := make(map[string]int)
	var walkCount func(tn *domain.TraceNode)
	walkCount = func(tn *domain.TraceNode) {
		if tn == nil || tn.Node == nil {
			return
		}
		idCount[tn.Node.ID]++
		for _, child := range tn.Children {
			walkCount(child)
		}
	}
	for _, trace := range result.Traces {
		if trace.Root != nil {
			walkCount(trace.Root)
		}
	}

	// Report duplicates (nodes appearing more than once across traces,
	// excluding cycle back-references which are expected).
	var duplicates []string
	for id, count := range idCount {
		if count > 1 {
			duplicates = append(duplicates, fmt.Sprintf("duplicate node %q appears %d times", id, count))
		}
	}
	sort.Strings(duplicates)
	warnings = append(warnings, duplicates...)

	// Report cycles from each TraceResult.
	for i, trace := range result.Traces {
		for _, cycle := range trace.Cycles {
			warnings = append(warnings, fmt.Sprintf("trace[%d]: cycle detected on edge %s -> %s", i, cycle.From, cycle.To))
		}
		// Surface warnings from the trace engine (e.g., missing references).
		for _, w := range trace.Warnings {
			warnings = append(warnings, fmt.Sprintf("trace[%d]: %s", i, w))
		}
	}

	return warnings
}

// outputTraceValidation runs validation checks and prints results.
func outputTraceValidation(result *app.TraceOutput) error {
	warnings := validateTrace(result)
	if len(warnings) == 0 {
		fmt.Println("Trace validation passed: no issues found.")
		return nil
	}

	fmt.Printf("Trace validation found %d issue(s):\n", len(warnings))
	for _, w := range warnings {
		fmt.Printf("  WARNING: %s\n", w)
	}
	return nil
}

func outputTraceTree(result *app.TraceOutput) error {
	renderer := adapters.NewGraphRenderer()

	fmt.Printf("\n  Feature: %s (%s)\n", result.FeatureName, result.FeatureID)
	fmt.Printf("  Priority: %s\n", result.Priority)

	if len(result.APISurfaces) > 0 {
		fmt.Println("  API Surfaces:")
		for _, api := range result.APISurfaces {
			fmt.Printf("    %s %s\n", api.Method, api.Path)
		}
	}

	if len(result.Traces) == 0 {
		fmt.Println("\n  No entry points found — feature has no API surfaces to trace.")
		return nil
	}

	for i, trace := range result.Traces {
		if i > 0 {
			fmt.Println("  ---")
		}
		renderer.RenderTrace(trace, os.Stdout)
	}

	if len(result.TestFiles) > 0 {
		fmt.Println("  Known test files:")
		for _, f := range result.TestFiles {
			fmt.Printf("    %s\n", f)
		}
		fmt.Println()
	}

	return nil
}
