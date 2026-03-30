package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/spf13/cobra"
)

var checkFormat string

var checkCmd = &cobra.Command{
	Use:   "check <feature-id>",
	Short: "Show detailed coverage for a specific feature",
	Long: `Displays comprehensive coverage information for a single feature,
including all test entries, gaps, and actionable suggestions for
improving coverage.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		featureID := args[0]

		store := adapters.NewYAMLStore()
		useCase := app.NewCheckFeatureUseCase(store)

		result, err := useCase.Execute(resolvedRegistryDir(), featureID)
		if err != nil {
			return fmt.Errorf("checking feature %q: %w", featureID, err)
		}

		if checkFormat == "json" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}

		reporter := adapters.NewTerminalReporter()

		// Convert app.EntryDetail to adapters.EntryDetail
		entries := make(map[string]adapters.EntryDetail)
		for k, v := range result.Entries {
			entries[k] = adapters.EntryDetail{
				Status:   v.Status,
				Files:    v.Files,
				Mocked:   v.Mocked,
				PassRate: v.PassRate,
				LastRun:  v.LastRun,
			}
		}

		return reporter.RenderFeatureDetail(
			result.Feature,
			result.DomainName,
			entries,
			result.Gaps,
			result.Suggestions,
			result.FullyCovered,
		)
	},
}

func init() {
	checkCmd.Flags().StringVar(&checkFormat, "format", "table", "Output format: table or json")
	rootCmd.AddCommand(checkCmd)
}
