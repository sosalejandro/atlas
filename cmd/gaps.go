package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/spf13/cobra"
)

var (
	gapsFeature   string
	gapsPriority  string
	gapsFormat    string
	gapsMinHealth float64
	gapsLimit     int
)

var gapsCmd = &cobra.Command{
	Use:   "gaps",
	Short: "Extract actionable test gap information",
	Long: `Extracts actionable gap information in structured formats, useful for
feeding into AI subagents for automated test gap fixing.

Output formats:
  terminal   — standard gap listing per feature (default)
  json       — JSON array of features with their gaps
  actionable — structured gap listing with file paths and suggested actions
  prompt     — optimized for AI consumption with annotation instructions`,
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		// Load graph configuration.
		graphSection, err := adapters.LoadGraphConfig(resolvedProjectRoot())
		if err != nil {
			return fmt.Errorf("loading graph config: %w", err)
		}

		config := graphSection.ToPortsConfig()
		config.ProjectRoot = resolvedProjectRoot()

		// Create dependencies.
		store := adapters.NewYAMLStore()
		builder := adapters.NewGraphBuilder(config)
		traceUC := app.NewTraceFeatureUseCase(store, builder)
		auditUC := app.NewAuditFeatureUseCase(traceUC, store)

		// Run audit: single feature or all features.
		var results []*domain.AuditOutput
		if gapsFeature != "" {
			result, err := auditUC.Execute(resolvedRegistryDir(), gapsFeature, config)
			if err != nil {
				return fmt.Errorf("auditing feature %q: %w", gapsFeature, err)
			}
			results = []*domain.AuditOutput{result}
		} else {
			all, err := auditUC.ExecuteAll(resolvedRegistryDir(), config)
			if err != nil {
				return fmt.Errorf("auditing all features: %w", err)
			}
			results = all
		}

		// Filter by priority.
		if gapsPriority != "" {
			allowed := make(map[string]bool)
			for _, p := range strings.Split(gapsPriority, ",") {
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

		// Filter by min-health.
		if gapsMinHealth > 0 {
			var filtered []*domain.AuditOutput
			for _, r := range results {
				if r.HealthScore < gapsMinHealth {
					filtered = append(filtered, r)
				}
			}
			results = filtered
		}

		// Only keep features that have gaps.
		var withGaps []*domain.AuditOutput
		for _, r := range results {
			if len(r.Gaps) > 0 {
				withGaps = append(withGaps, r)
			}
		}
		results = withGaps

		// Apply limit.
		if gapsLimit > 0 && gapsLimit < len(results) {
			results = results[:gapsLimit]
		}

		// Render in the chosen format.
		switch gapsFormat {
		case "json":
			return renderGapsJSON(results)
		case "actionable":
			renderGapsActionable(results)
		case "prompt":
			renderGapsPrompt(results)
		default:
			renderGapsTerminal(results)
		}

		return nil
	},
}

func init() {
	gapsCmd.Flags().StringVar(&gapsFeature, "feature", "", "Show gaps for a specific feature")
	gapsCmd.Flags().StringVar(&gapsPriority, "priority", "", "Filter features by priority (comma-separated)")
	gapsCmd.Flags().StringVar(&gapsFormat, "format", "terminal", "Output format: terminal, json, actionable, prompt")
	gapsCmd.Flags().Float64Var(&gapsMinHealth, "min-health", 0, "Only features below this health score (0.0-1.0)")
	gapsCmd.Flags().IntVarP(&gapsLimit, "limit", "n", 0, "Limit number of features shown")
	rootCmd.AddCommand(gapsCmd)
}

// gapFeatureJSON is the JSON representation of a feature's gaps.
type gapFeatureJSON struct {
	FeatureID   string         `json:"feature_id"`
	Priority    string         `json:"priority"`
	HealthScore float64        `json:"health_score"`
	Gaps        []gapEntryJSON `json:"gaps"`
}

// gapEntryJSON is the JSON representation of a single gap.
type gapEntryJSON struct {
	NodeID   string `json:"node_id"`
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Action   string `json:"action,omitempty"`
	TestFile string `json:"test_file,omitempty"`
	TestType string `json:"test_type,omitempty"`
}

func renderGapsJSON(results []*domain.AuditOutput) error {
	var out []gapFeatureJSON
	for _, r := range results {
		f := gapFeatureJSON{
			FeatureID:   r.FeatureID,
			Priority:    r.Priority,
			HealthScore: r.HealthScore,
		}
		for i, g := range r.Gaps {
			entry := gapEntryJSON{
				NodeID:   g.NodeID,
				Kind:     g.Kind,
				File:     g.File,
				Line:     g.Line,
				Severity: g.Severity,
				Reason:   g.Reason,
			}
			if i < len(r.Actions) {
				entry.Action = r.Actions[i].Description
				entry.TestFile = r.Actions[i].File
				entry.TestType = r.Actions[i].TestType
			}
			f.Gaps = append(f.Gaps, entry)
		}
		out = append(out, f)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

func renderGapsTerminal(results []*domain.AuditOutput) {
	useColor := isFileTTY(os.Stdout)

	for i, r := range results {
		if i > 0 {
			fmt.Println()
		}

		healthPct := r.HealthScore * 100
		if useColor {
			fmt.Printf("\033[1m%s\033[0m (%s, health: %.0f%%) — %d gaps\n",
				r.FeatureID, r.Priority, healthPct, len(r.Gaps))
		} else {
			fmt.Printf("%s (%s, health: %.0f%%) — %d gaps\n",
				r.FeatureID, r.Priority, healthPct, len(r.Gaps))
		}

		for _, g := range r.Gaps {
			sev := strings.ToUpper(g.Severity)
			loc := ""
			if g.File != "" {
				loc = fmt.Sprintf(" (%s:%d)", g.File, g.Line)
			}
			if useColor {
				color := severityColor(g.Severity)
				fmt.Printf("  %s[%s]\033[0m %s — %s%s\n", color, sev, g.NodeID, g.Reason, loc)
			} else {
				fmt.Printf("  [%s] %s — %s%s\n", sev, g.NodeID, g.Reason, loc)
			}
		}
	}
}

func renderGapsActionable(results []*domain.AuditOutput) {
	for i, r := range results {
		if i > 0 {
			fmt.Println()
		}

		healthPct := r.HealthScore * 100
		fmt.Printf("Feature: %s (%s, health: %.0f%%)\n", r.FeatureID, r.Priority, healthPct)

		for j, g := range r.Gaps {
			sev := strings.ToUpper(g.Severity)
			fmt.Printf("  [%s] %s -- %s\n", sev, g.NodeID, g.Reason)

			if g.File != "" {
				fmt.Printf("    File: %s:%d\n", g.File, g.Line)
			}

			if j < len(r.Actions) {
				action := r.Actions[j]
				fmt.Printf("    Action: %s\n", action.Description)
				if action.File != "" {
					fmt.Printf("    Pattern: %s\n", testPattern(action.TestType))
				}
			}
			fmt.Println()
		}
	}
}

func renderGapsPrompt(results []*domain.AuditOutput) {
	for i, r := range results {
		if i > 0 {
			fmt.Println()
		}

		healthPct := r.HealthScore * 100
		fmt.Printf("## Feature: %s\n", r.FeatureID)
		fmt.Printf("Priority: %s | Health: %.0f%% | Target: 100%%\n", r.Priority, healthPct)
		fmt.Println()
		fmt.Printf("### Gaps (%d):\n", len(r.Gaps))

		for j, g := range r.Gaps {
			sev := strings.ToUpper(g.Severity)
			fmt.Printf("%d. %s: %s has %s\n", j+1, sev, g.NodeID, g.Reason)

			if g.File != "" {
				fmt.Printf("   - Source: %s:%d\n", g.File, g.Line)
			}

			if j < len(r.Actions) {
				action := r.Actions[j]
				fmt.Printf("   - Write: %s\n", promptActionDescription(action))
				fmt.Printf("   - Annotation: // @testreg %s #real\n", r.FeatureID)
			}
		}
	}
}

// severityColor returns the ANSI color escape for a gap severity.
func severityColor(severity string) string {
	switch severity {
	case "critical":
		return "\033[31m" // red
	case "high":
		return "\033[33m" // yellow
	case "medium":
		return "\033[36m" // cyan
	case "low":
		return "\033[37m" // white
	default:
		return ""
	}
}

// testPattern returns a human-readable test pattern suggestion for a test type.
func testPattern(testType string) string {
	switch testType {
	case "unit":
		return "table-driven test with mock repository"
	case "integration":
		return "TestMain setup with test database"
	case "e2e":
		return "end-to-end test with real browser/device"
	default:
		return "standard test pattern"
	}
}

// promptActionDescription formats an audit action for the prompt format.
func promptActionDescription(action domain.AuditAction) string {
	desc := action.Description
	if action.File != "" {
		desc += " in " + action.File
	}
	return desc
}
