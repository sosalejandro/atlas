package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/spf13/cobra"
)

var (
	statusDomain   string
	statusPriority string
	statusFormat   string
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the coverage dashboard",
	Long: `Displays a terminal table showing test coverage metrics across all domains
and platforms. Supports filtering by domain and priority, and can output
as a table or JSON.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		store := adapters.NewYAMLStore()
		useCase := app.NewGetStatusUseCase(store)

		filter := app.StatusFilter{
			Domain: statusDomain,
		}
		if statusPriority != "" {
			filter.Priority = domain.Priority(statusPriority)
			if err := filter.Priority.Validate(); err != nil {
				return err
			}
		}

		result, err := useCase.Execute(resolvedRegistryDir(), filter)
		if err != nil {
			return fmt.Errorf("computing status: %w", err)
		}

		if statusFormat == "json" {
			return outputStatusJSON(result)
		}

		return outputStatusTable(result)
	},
}

func init() {
	statusCmd.Flags().StringVar(&statusDomain, "domain", "", "Filter by domain name")
	statusCmd.Flags().StringVar(&statusPriority, "priority", "", "Filter by priority (critical, high, medium, low)")
	statusCmd.Flags().StringVar(&statusFormat, "format", "table", "Output format: table or json")
	rootCmd.AddCommand(statusCmd)
}

func outputStatusTable(result *app.StatusResult) error {
	reporter := adapters.NewTerminalReporter()

	// Build a Report from the status result for the terminal reporter
	report := &domain.Report{
		GeneratedAt: "now",
		ProjectRoot: resolvedProjectRoot(),
		Metrics:     result.Metrics,
	}

	for _, row := range result.DomainData {
		report.Domains = append(report.Domains, domain.DomainReport{
			Name: row.Domain,
		})
	}

	return reporter.Render(report)
}

func outputStatusJSON(result *app.StatusResult) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
