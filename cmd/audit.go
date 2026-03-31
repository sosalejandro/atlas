package cmd

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
	"github.com/spf13/cobra"
)

var (
	auditAll          bool
	auditFormat       string
	auditOutput       string
	auditMinHealth    float64
	auditPriority     string
	auditSort         string
	auditSummary      bool
	auditUnconfigured bool
	auditRescan       bool
	auditLimit        int
)

var auditCmd = &cobra.Command{
	Use:   "audit [feature-id]",
	Short: "Generate a unified feature health report",
	Long: `Generates a comprehensive health report for a feature by combining
dependency graph traces, test coverage data, and gap analysis.

If a feature-id is provided, a detailed report is generated for that
feature. If no feature-id is given (or --all is used), a summary table
is generated for all features sorted by health score (worst first).

The health score is a weighted average of layer coverage:
  Handler: 30%, Service: 30%, Repository: 25%, Query: 15%

Each node in the dependency graph is annotated with its test status:
  tested     — a test file directly covers this node's file
  partial    — a test exists in the same package but may not cover this function
  untested   — no test file maps to this node

Gaps are prioritized by severity:
  CRITICAL — handlers/services with no unit test
  HIGH     — repositories with no integration test
  MEDIUM   — SQL queries with no test at all
  LOW      — other untested nodes`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		// Run scan first if --rescan is set.
		if auditRescan {
			store := adapters.NewYAMLStore()
			scanners := []ports.TestScanner{
				adapters.NewGoScanner(),
				adapters.NewVitestScanner(),
				adapters.NewPlaywrightScanner(),
				adapters.NewMaestroScanner(),
				adapters.NewJestScanner(),
			}
			scanUC := app.NewScanTestsUseCase(store, store, scanners)
			if _, err := scanUC.Execute(resolvedProjectRoot(), resolvedRegistryDir()); err != nil {
				return fmt.Errorf("rescan failed: %w", err)
			}
			fmt.Fprintln(os.Stderr, "Rescan complete.")
		}

		// Determine mode: single feature or all features.
		singleFeature := len(args) == 1 && !auditAll && !auditSummary && !auditUnconfigured

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
		auditUC := app.NewAuditFeatureUseCase(traceUC, store)

		// Determine output writer.
		out := os.Stdout
		if auditOutput != "" {
			f, err := os.Create(auditOutput)
			if err != nil {
				return fmt.Errorf("creating output file %q: %w", auditOutput, err)
			}
			defer f.Close()
			out = f
		}

		if singleFeature {
			return runSingleAudit(auditUC, args[0], config, out)
		}
		return runAllAudit(auditUC, config, out)
	},
}

func init() {
	auditCmd.Flags().BoolVar(&auditAll, "all", false, "Show all features in summary mode")
	auditCmd.Flags().StringVar(&auditFormat, "format", "terminal", "Output format: terminal, json, markdown")
	auditCmd.Flags().StringVar(&auditOutput, "output", "", "Write to file instead of stdout")
	auditCmd.Flags().Float64Var(&auditMinHealth, "min-health", 0, "Only show features below this health score (0.0-1.0)")
	auditCmd.Flags().StringVar(&auditPriority, "priority", "", "Filter by priority: critical,high,medium,low (comma-separated)")
	auditCmd.Flags().StringVar(&auditSort, "sort", "", "Sort order: health, priority-score, name (default: health ascending)")
	auditCmd.Flags().BoolVar(&auditSummary, "summary", false, "Show aggregate counts per priority tier")
	auditCmd.Flags().BoolVar(&auditUnconfigured, "unconfigured", false, "Show features with no API surfaces (0% health, 0 gaps)")
	auditCmd.Flags().BoolVar(&auditRescan, "rescan", false, "Run scan before auditing to ensure fresh data")
	auditCmd.Flags().IntVarP(&auditLimit, "limit", "n", 0, "Limit output to top N results")
	rootCmd.AddCommand(auditCmd)
}

func runSingleAudit(uc *app.AuditFeatureUseCase, featureID string, config ports.GraphConfig, out *os.File) error {
	result, err := uc.Execute(resolvedRegistryDir(), featureID, config)
	if err != nil {
		return fmt.Errorf("auditing feature %q: %w", featureID, err)
	}

	useColor := out == os.Stdout && isFileTTY(out)

	switch auditFormat {
	case "json":
		renderer := adapters.NewAuditRendererToWriter(out, false)
		return renderer.RenderJSON(result)
	case "markdown":
		renderer := adapters.NewAuditRendererToWriter(out, false)
		renderer.RenderMarkdownSingle(result)
		return nil
	default:
		renderer := adapters.NewAuditRendererToWriter(out, useColor)
		renderer.RenderSingle(result)
		return nil
	}
}

func runAllAudit(uc *app.AuditFeatureUseCase, config ports.GraphConfig, out *os.File) error {
	results, err := uc.ExecuteAll(resolvedRegistryDir(), config)
	if err != nil {
		return fmt.Errorf("auditing all features: %w", err)
	}

	// Filter by priority if specified.
	if auditPriority != "" {
		allowed := make(map[string]bool)
		for _, p := range strings.Split(auditPriority, ",") {
			allowed[strings.TrimSpace(p)] = true
		}
		var filtered []*domain.AuditOutput
		for _, r := range results {
			if allowed[r.Priority] {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	// Filter by min-health if specified.
	if auditMinHealth > 0 {
		var filtered []*domain.AuditOutput
		for _, r := range results {
			if r.HealthScore < auditMinHealth {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	// Filter unconfigured features (0% health AND 0 gaps).
	if auditUnconfigured {
		var filtered []*domain.AuditOutput
		for _, r := range results {
			if r.HealthScore == 0 && len(r.Gaps) == 0 {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	// Sort results.
	switch auditSort {
	case "priority-score":
		sort.SliceStable(results, func(i, j int) bool {
			return priorityScore(results[i]) > priorityScore(results[j])
		})
	case "name":
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].FeatureID < results[j].FeatureID
		})
	case "health":
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].HealthScore < results[j].HealthScore
		})
	// default: already sorted by health ascending from ExecuteAll
	}

	// Limit results.
	if auditLimit > 0 && auditLimit < len(results) {
		results = results[:auditLimit]
	}

	useColor := out == os.Stdout && isFileTTY(out)

	// Summary mode: aggregate counts per priority tier.
	if auditSummary {
		renderer := adapters.NewAuditRendererToWriter(out, useColor)
		switch auditFormat {
		case "json":
			return renderer.RenderJSON(buildAuditSummary(results))
		default:
			renderAuditSummary(renderer, results, out, useColor)
			return nil
		}
	}

	switch auditFormat {
	case "json":
		renderer := adapters.NewAuditRendererToWriter(out, false)
		return renderer.RenderJSON(results)
	case "markdown":
		renderer := adapters.NewAuditRendererToWriter(out, false)
		renderer.RenderMarkdownSummary(results)
		return nil
	default:
		renderer := adapters.NewAuditRendererToWriter(out, useColor)
		renderer.RenderSummary(results)
		return nil
	}
}

// Priority weights and targets for scoring.
var (
	priorityWeights = map[string]float64{"critical": 4, "high": 3, "medium": 2, "low": 1}
	priorityTargets = map[string]float64{"critical": 1.0, "high": 0.8, "medium": 0.6, "low": 0.4}
)

// priorityScore computes the weighted gap score: weight * max(0, target - health).
func priorityScore(o *domain.AuditOutput) float64 {
	w := priorityWeights[o.Priority]
	if w == 0 {
		w = 1
	}
	target := priorityTargets[o.Priority]
	if target == 0 {
		target = 0.5
	}
	delta := target - o.HealthScore
	if delta < 0 {
		delta = 0
	}
	return w * delta
}

// auditSummaryTier holds aggregate counts for a priority tier.
type auditSummaryTier struct {
	Priority string  `json:"priority"`
	AtTarget int     `json:"at_target"`
	Total    int     `json:"total"`
	GapCount int     `json:"gap_count"`
	Pct      float64 `json:"percentage"`
}

// buildAuditSummary aggregates audit results by priority tier.
func buildAuditSummary(results []*domain.AuditOutput) map[string]interface{} {
	tierMap := map[string]*auditSummaryTier{}
	for _, p := range []string{"critical", "high", "medium", "low"} {
		tierMap[p] = &auditSummaryTier{Priority: p}
	}

	totalAtTarget := 0
	for _, r := range results {
		t, ok := tierMap[r.Priority]
		if !ok {
			t = &auditSummaryTier{Priority: r.Priority}
			tierMap[r.Priority] = t
		}
		t.Total++
		t.GapCount += len(r.Gaps)
		target := priorityTargets[r.Priority]
		if target == 0 {
			target = 0.5
		}
		if r.HealthScore >= target {
			t.AtTarget++
			totalAtTarget++
		}
	}

	tiers := make([]auditSummaryTier, 0, 4)
	for _, p := range []string{"critical", "high", "medium", "low"} {
		t := tierMap[p]
		if t.Total > 0 {
			t.Pct = float64(t.AtTarget) / float64(t.Total) * 100
		}
		tiers = append(tiers, *t)
	}

	return map[string]interface{}{
		"tiers":       tiers,
		"total":       len(results),
		"at_target":   totalAtTarget,
		"overall_pct": func() float64 { if len(results) == 0 { return 0 }; return float64(totalAtTarget) / float64(len(results)) * 100 }(),
	}
}

// renderAuditSummary prints the priority summary table with progress bars.
func renderAuditSummary(_ *adapters.AuditRenderer, results []*domain.AuditOutput, out *os.File, color bool) {
	summary := buildAuditSummary(results)
	tiers := summary["tiers"].([]auditSummaryTier)
	total := summary["total"].(int)
	atTarget := summary["at_target"].(int)
	overallPct := summary["overall_pct"].(float64)

	fmt.Fprintln(out)
	if color {
		fmt.Fprintf(out, "  \033[1mPriority Summary:\033[0m\n")
	} else {
		fmt.Fprintf(out, "  Priority Summary:\n")
	}

	for _, t := range tiers {
		if t.Total == 0 {
			continue
		}
		bar := makeBar(t.Pct, 10)
		fmt.Fprintf(out, "    %-10s %d/%d at target  (%d gaps)  %s %3.0f%%\n",
			strings.ToUpper(t.Priority), t.AtTarget, t.Total, t.GapCount, bar, t.Pct)
	}

	fmt.Fprintf(out, "\n    Overall: %d/%d features at target (%.0f%%)\n\n", atTarget, total, overallPct)
}

// makeBar creates a simple progress bar string.
func makeBar(pct float64, width int) string {
	filled := int(math.Round(pct / 100.0 * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", width-filled)
}

// isFileTTY checks if a file descriptor is a terminal.
func isFileTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
