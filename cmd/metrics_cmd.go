package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/spf13/cobra"
)

var (
	metricsFeature string
	metricsSlow    string
	metricsFlaky   bool
	metricsRaces   bool
	metricsFormat  string
)

var metricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Show quality signals from historical test metrics",
	Long: `Analyzes historical test run data and surfaces quality signals including
the slowest tests, flaky tests, memory-intensive tests, race conditions,
and declining health trends.

Examples:
  testreg metrics                        # Show all quality signals
  testreg metrics --feature auth.login   # Show health trend for a feature
  testreg metrics --slow 5s              # Show tests slower than 5s
  testreg metrics --flaky                # Show only flaky tests
  testreg metrics --races                # Show race conditions detected
  testreg metrics --format json          # JSON output`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmdMetrics := adapters.NewMetrics(metricsEnabled)
		defer cmdMetrics.Print(os.Stderr)

		store := adapters.NewMetricsStore()
		renderer := adapters.NewMetricsRenderer()

		history, err := store.LoadHistory(resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("loading metrics history: %w", err)
		}

		if len(history.Runs) == 0 {
			fmt.Println()
			fmt.Println("  No metrics history found.")
			fmt.Println("  Run tests with --with-metrics to start capturing data:")
			fmt.Println("    testreg update --gotest results.json --with-metrics")
			fmt.Println()
			return nil
		}

		// Feature-specific health trend.
		if metricsFeature != "" {
			trend := store.GetFeatureHealthTrend(history, metricsFeature)
			if metricsFormat == "json" {
				return renderer.RenderJSON(&domain.QualitySignals{})
			}
			renderer.RenderFeatureHealth(trend)
			return nil
		}

		signals := store.GetQualitySignals(history)

		// Filtered views.
		if metricsSlow != "" {
			threshold, parseErr := time.ParseDuration(metricsSlow)
			if parseErr != nil {
				return fmt.Errorf("invalid --slow duration %q: %w", metricsSlow, parseErr)
			}
			if metricsFormat == "json" {
				return renderer.RenderJSON(signals)
			}
			renderer.RenderSlowestTests(signals.SlowestTests, threshold)
			return nil
		}

		if metricsFlaky {
			if metricsFormat == "json" {
				filtered := &domain.QualitySignals{FlakyTests: signals.FlakyTests}
				return renderer.RenderJSON(filtered)
			}
			renderer.RenderQualitySignals(&domain.QualitySignals{FlakyTests: signals.FlakyTests})
			return nil
		}

		if metricsRaces {
			if metricsFormat == "json" {
				filtered := &domain.QualitySignals{RaceConditions: signals.RaceConditions}
				return renderer.RenderJSON(filtered)
			}
			renderer.RenderQualitySignals(&domain.QualitySignals{RaceConditions: signals.RaceConditions})
			return nil
		}

		// Full quality signals.
		if metricsFormat == "json" {
			return renderer.RenderJSON(signals)
		}

		renderer.RenderQualitySignals(signals)
		return nil
	},
}

func init() {
	metricsCmd.Flags().StringVar(&metricsFeature, "feature", "", "Show health trend for a specific feature ID")
	metricsCmd.Flags().StringVar(&metricsSlow, "slow", "", "Show tests slower than this duration (e.g., 5s, 500ms)")
	metricsCmd.Flags().BoolVar(&metricsFlaky, "flaky", false, "Show only flaky tests")
	metricsCmd.Flags().BoolVar(&metricsRaces, "races", false, "Show only race conditions")
	metricsCmd.Flags().StringVar(&metricsFormat, "format", "text", "Output format: text or json")
	rootCmd.AddCommand(metricsCmd)
}
