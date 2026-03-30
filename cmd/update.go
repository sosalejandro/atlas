package cmd

import (
	"fmt"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/ports"
	"github.com/spf13/cobra"
)

var (
	updatePlaywright string
	updateGotest     string
	updateMaestro    string
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Ingest test results and update the registry",
	Long: `Parses test result output files (Playwright JSON, go test -json, or Maestro)
and updates the registry YAML files with pass/fail status, pass rates, and
last-run dates.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := adapters.NewYAMLStore()
		useCase := app.NewUpdateCoverageUseCase(store, store)

		var parser ports.ResultParser
		var resultPath, platform, testType string

		switch {
		case updatePlaywright != "":
			parser = adapters.NewPlaywrightResultParser()
			resultPath = updatePlaywright
			platform = "web"
			testType = "e2e"

		case updateGotest != "":
			parser = adapters.NewGoTestResultParser()
			resultPath = updateGotest
			platform = "backend"
			testType = "unit" // go test results may contain unit + integration

		case updateMaestro != "":
			parser = adapters.NewMaestroResultParser()
			resultPath = updateMaestro
			platform = "mobile"
			testType = "e2e"

		default:
			return fmt.Errorf("specify a result source: --playwright, --gotest, or --maestro")
		}

		// Parse the results
		results, err := parser.Parse(resultPath)
		if err != nil {
			return fmt.Errorf("parsing results from %s: %w", resultPath, err)
		}

		// Update the registry
		updateResult, err := useCase.Execute(resolvedRegistryDir(), results, platform, testType)
		if err != nil {
			return fmt.Errorf("updating coverage: %w", err)
		}

		fmt.Printf("Update complete.\n\n")
		fmt.Printf("  Results processed: %d\n", updateResult.Processed)
		fmt.Printf("  Features updated:  %d\n", updateResult.Updated)
		fmt.Printf("  Unmapped results:  %d\n", updateResult.Unmapped)
		fmt.Printf("  Failures found:    %d\n", updateResult.Failures)
		fmt.Println()

		if len(updateResult.Details) > 0 {
			fmt.Println("Changes:")
			for _, d := range updateResult.Details {
				fmt.Printf("  %s [%s]: %s → %s\n", d.FeatureID, d.EntryPath, d.OldStatus, d.NewStatus)
				if d.PassRate > 0 {
					fmt.Printf("    pass rate: %.0f%%\n", d.PassRate*100)
				}
			}
			fmt.Println()
		}

		return nil
	},
}

func init() {
	updateCmd.Flags().StringVar(&updatePlaywright, "playwright", "", "Path to Playwright JSON results directory or file")
	updateCmd.Flags().StringVar(&updateGotest, "gotest", "", "Path to go test -json output file")
	updateCmd.Flags().StringVar(&updateMaestro, "maestro", "", "Path to Maestro output directory")
	rootCmd.AddCommand(updateCmd)
}
