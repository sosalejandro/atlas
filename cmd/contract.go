package cmd

import (
	"fmt"
	"os"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
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

		// Load graph configuration.
		graphSection, err := adapters.LoadGraphConfig(resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("loading graph config: %w", err)
		}

		config := graphSection.ToPortsConfig()
		config.ProjectRoot = resolvedProjectRoot()

		// Create dependencies — use NewGraphBuilder to respect type_checking config.
		store := adapters.NewYAMLStore()
		builder := adapters.NewGraphBuilder(config)
		traceUC := app.NewTraceFeatureUseCase(store, builder)
		contractUC := app.NewContractFeatureUseCase(traceUC, store)

		// If go/types is available (type_checking: true), create a TypeExtractor
		// so contract layers get enriched with struct field tables.
		if typed, ok := builder.(*adapters.TypedScanner); ok {
			// Build once to populate the package cache.
			_, buildErr := typed.Build(resolvedProjectRoot(), config)
			if buildErr == nil {
				if pkgs := typed.LoadedPackages(); pkgs != nil {
					contractUC.SetTypeExtractor(adapters.NewTypeExtractor(pkgs))
				}
			}
		}

		result, err := contractUC.Execute(resolvedRegistryDir(), featureID, config)
		if err != nil {
			return fmt.Errorf("building contract for feature %q: %w", featureID, err)
		}

		// Render in the requested format.
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
