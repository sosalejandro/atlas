package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
	"github.com/spf13/cobra"
)

var (
	updatePlaywright  string
	updateGotest      string
	updateMaestro     string
	updateWithMetrics bool
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Ingest test results and update the registry",
	Long: `Parses test result output files (Playwright JSON, go test -json, or Maestro)
and updates the registry YAML files with pass/fail status, pass rates, and
last-run dates.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

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

		// Optionally capture metrics from the test results.
		if updateWithMetrics {
			metricsRun, metricsErr := parseMetricsFromResults(resultPath, platform)
			if metricsErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not parse metrics: %s\n", metricsErr)
			} else if metricsRun != nil {
				metricsStore := adapters.NewMetricsStore()
				if appendErr := metricsStore.Append(resolvedProjectRoot(), metricsRun); appendErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not save metrics: %s\n", appendErr)
				} else {
					fmt.Printf("  Metrics captured: %d tests, %d passed, %d failed, %d skipped\n",
						metricsRun.TotalTests, metricsRun.Passed, metricsRun.Failed, metricsRun.Skipped)
					fmt.Println()
				}
			}
		}

		return nil
	},
}

// parseMetricsFromResults dispatches to the correct metrics parser based on
// the platform/framework that produced the results.
func parseMetricsFromResults(resultPath, platform string) (*domain.TestRunMetrics, error) {
	switch platform {
	case "backend":
		data, err := os.ReadFile(resultPath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", resultPath, err)
		}
		return adapters.ParseGoTestMetrics(data)

	case "web":
		data, err := readResultFile(resultPath)
		if err != nil {
			return nil, err
		}
		return adapters.ParsePlaywrightMetrics(data)

	default:
		return nil, fmt.Errorf("metrics parsing not supported for platform %q", platform)
	}
}

// readResultFile reads a JSON result file, handling both direct file paths and
// directories (looking for results.json, report.json, or test-results.json).
func readResultFile(resultPath string) ([]byte, error) {
	info, err := os.Stat(resultPath)
	if err != nil {
		return nil, fmt.Errorf("accessing %s: %w", resultPath, err)
	}

	if !info.IsDir() {
		data, err := os.ReadFile(resultPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", resultPath, err)
		}
		return data, nil
	}

	for _, name := range []string{"results.json", "report.json", "test-results.json"} {
		candidate := filepath.Join(resultPath, name)
		if data, readErr := os.ReadFile(candidate); readErr == nil {
			return data, nil
		}
	}

	return nil, fmt.Errorf("no JSON result file found in %s", resultPath)
}

func init() {
	updateCmd.Flags().StringVar(&updatePlaywright, "playwright", "", "Path to Playwright JSON results directory or file")
	updateCmd.Flags().StringVar(&updateGotest, "gotest", "", "Path to go test -json output file")
	updateCmd.Flags().StringVar(&updateMaestro, "maestro", "", "Path to Maestro output directory")
	updateCmd.Flags().BoolVar(&updateWithMetrics, "with-metrics", false, "Also capture test metrics into history")
	rootCmd.AddCommand(updateCmd)
}
