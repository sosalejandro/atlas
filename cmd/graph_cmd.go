package cmd

import (
	"fmt"
	"os"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/spf13/cobra"
)

var (
	graphFormat string
	graphOutput string
)

var graphCmd = &cobra.Command{
	Use:   "graph <feature-id>",
	Short: "Export a feature's dependency graph",
	Long: `Exports the dependency graph for a feature in a format suitable for
visualization. Supports Graphviz DOT, Mermaid flowchart, and JSON
output. The graph shows handlers, services, repositories, and queries
with their call relationships.`,
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

		// Create dependencies.
		store := adapters.NewYAMLStore()
		builder := adapters.NewGoASTScanner()
		traceUC := app.NewTraceFeatureUseCase(store, builder)

		result, err := traceUC.Execute(resolvedRegistryDir(), featureID, config)
		if err != nil {
			return fmt.Errorf("tracing feature %q: %w", featureID, err)
		}

		if len(result.Traces) == 0 {
			fmt.Fprintf(os.Stderr, "No entry points found for feature %q — nothing to graph.\n", featureID)
			return nil
		}

		// Determine output destination.
		w := os.Stdout
		if graphOutput != "" {
			f, err := os.Create(graphOutput)
			if err != nil {
				return fmt.Errorf("creating output file %q: %w", graphOutput, err)
			}
			defer f.Close()
			w = f
		}

		renderer := adapters.NewGraphRenderer()

		// Render all traces. For graph export we use the first trace as the
		// primary visualization. Multiple entry points are rendered sequentially.
		for _, trace := range result.Traces {
			switch graphFormat {
			case "dot":
				renderer.RenderDOT(trace, w)
			case "mermaid":
				renderer.RenderMermaid(trace, w)
			case "json":
				renderer.RenderJSON(trace, w)
			default:
				return fmt.Errorf("unsupported format %q: use dot, mermaid, or json", graphFormat)
			}
		}

		if graphOutput != "" {
			fmt.Fprintf(os.Stderr, "Graph written to %s\n", graphOutput)
		}

		return nil
	},
}

func init() {
	graphCmd.Flags().StringVar(&graphFormat, "format", "dot", "Output format: dot, mermaid, json")
	graphCmd.Flags().StringVar(&graphOutput, "output", "", "Write to file instead of stdout")
	rootCmd.AddCommand(graphCmd)
}
