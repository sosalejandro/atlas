package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/spf13/cobra"
)

var (
	reportFormat string
	reportOutput string
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate a coverage report",
	Long: `Generates a comprehensive coverage report from the current registry state.
Outputs markdown (default) or JSON format. Markdown reports are written to
a file (default: docs/testing/COVERAGE.md).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := adapters.NewYAMLStore()
		useCase := app.NewGenerateReportUseCase(store)

		report, err := useCase.Execute(resolvedRegistryDir(), resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("generating report: %w", err)
		}

		switch reportFormat {
		case "json":
			if reportOutput != "" {
				f, createErr := os.Create(reportOutput)
				if createErr != nil {
					return fmt.Errorf("creating output file %s: %w", reportOutput, createErr)
				}
				defer f.Close()
				encoder := json.NewEncoder(f)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return fmt.Errorf("encoding JSON: %w", err)
				}
				fmt.Printf("JSON report written to %s\n", reportOutput)
			} else {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}

		case "md", "markdown":
			outputPath := reportOutput
			if outputPath == "" {
				outputPath = filepath.Join(resolvedProjectRoot(), "docs", "testing", "COVERAGE.md")
			}

			// Ensure the output directory exists
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				return fmt.Errorf("creating output directory: %w", err)
			}

			mdReporter := adapters.NewMarkdownReporter(outputPath)
			if err := mdReporter.Render(report); err != nil {
				return err
			}
			fmt.Printf("Markdown report written to %s\n", outputPath)

		default:
			// Terminal output
			termReporter := adapters.NewTerminalReporter()
			return termReporter.Render(report)
		}

		return nil
	},
}

func init() {
	reportCmd.Flags().StringVar(&reportFormat, "format", "md", "Output format: md, json, or terminal")
	reportCmd.Flags().StringVar(&reportOutput, "output", "", "Output file path (default: docs/testing/COVERAGE.md for md)")
	rootCmd.AddCommand(reportCmd)
}
