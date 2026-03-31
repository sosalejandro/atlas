package cmd

import (
	"fmt"
	"os"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/ports"
	"github.com/spf13/cobra"
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Discover test files and map them to registry features",
	Long: `Walks the project tree using all registered test scanners (Go, Vitest,
Playwright, Maestro, Jest) and maps discovered test files to features in
the registry. Unmapped tests are saved to _unmapped.yaml for manual review.
The registry YAML files are updated with new file references and status changes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		store := adapters.NewYAMLStore()
		scanners := []ports.TestScanner{
			adapters.NewGoScanner(),
			adapters.NewVitestScanner(),
			adapters.NewPlaywrightScanner(),
			adapters.NewMaestroScanner(),
			adapters.NewJestScanner(),
			adapters.NewPythonScanner(),
		}

		useCase := app.NewScanTestsUseCase(store, store, scanners)
		result, err := useCase.Execute(resolvedProjectRoot(), resolvedRegistryDir())
		if err != nil {
			return fmt.Errorf("scanning tests: %w", err)
		}

		fmt.Printf("Scan complete.\n\n")
		fmt.Printf("  Total test files: %d\n", result.TotalTests)
		fmt.Printf("  Mapped:           %d\n", result.MappedTests)
		fmt.Printf("  Unmapped:         %d\n", result.UnmappedTests)
		fmt.Println()

		if result.MappedTests > 0 {
			fmt.Println("Mapped tests:")
			for _, m := range result.Mapped {
				fmt.Printf("  %s → %s\n", m.Test.FilePath, m.FeatureID)
			}
			fmt.Println()
		}

		if result.UnmappedTests > 0 {
			fmt.Println("Unmapped tests (saved to _unmapped.yaml for review):")
			for _, u := range result.Unmapped {
				fmt.Printf("  %s [%s/%s/%s]\n", u.FilePath, u.Framework, u.Platform, u.TestType)
			}
			fmt.Println()
		}

		fmt.Println("Registry updated.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(scanCmd)
}
