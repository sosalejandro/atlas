package cmd

import (
	"fmt"
	"os"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/spf13/cobra"
)

var (
	contractFormat string
	contractLayer  int
)

var contractCmd = &cobra.Command{
	Use:   "contract <feature-id>",
	Short: "Show the full API contract and call chain for a feature",
	Long: `Traces the dependency chain and extracts type information at each layer.
Shows input/output contracts, data transformations, and business rules
from the GraphQL schema down to the SQL query.

Without graphql.schema_dirs in .testreg.yaml, the contract starts from the
handler layer. Without type_checking, it shows the call chain and function
signatures but not struct field tables (those require go/types).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		featureID := args[0]

		graphSection, err := adapters.LoadGraphConfig(resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("loading graph config: %w", err)
		}

		config := graphSection.ToPortsConfig()
		config.ProjectRoot = resolvedProjectRoot()

		store := adapters.NewYAMLStore()
		builder := adapters.NewGraphBuilder(config)
		traceUC := app.NewTraceFeatureUseCase(store, builder)
		contractUC := app.NewContractFeatureUseCase(traceUC, store)

		// If type_checking is on, set up the type extractor AFTER the trace
		// runs (the trace populates the TypedScanner cache).
		result, err := contractUC.Execute(resolvedRegistryDir(), featureID, config)
		if err != nil {
			return fmt.Errorf("building contract for feature %q: %w", featureID, err)
		}

		// Post-trace: enrich with struct fields if TypedScanner cached packages.
		if typed, ok := builder.(*adapters.TypedScanner); ok {
			if pkgs := typed.LoadedPackages(); pkgs != nil {
				extractor := adapters.NewTypeExtractor(pkgs)
				result.EnrichWithTypes(extractor)
			}
		}

		switch contractFormat {
		case "json":
			renderer := adapters.NewContractRendererToWriter(os.Stdout, false)
			return renderer.RenderJSON(result)
		case "markdown":
			renderer := adapters.NewContractRendererToWriter(os.Stdout, false)
			renderer.RenderMarkdown(result, contractLayer)
			return nil
		default:
			useColor := isFileTTY(os.Stdout)
			renderer := adapters.NewContractRendererToWriter(os.Stdout, useColor)
			renderer.RenderTerminal(result, contractLayer)
			return nil
		}
	},
}

func init() {
	contractCmd.Flags().StringVar(&contractFormat, "format", "terminal", "Output format: terminal, json, markdown")
	contractCmd.Flags().IntVar(&contractLayer, "layer", 0, "Show only up to this layer depth (0 = all)")
	rootCmd.AddCommand(contractCmd)
}
