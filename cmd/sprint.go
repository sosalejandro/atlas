package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/spf13/cobra"
)

var (
	sprintLimit    int
	sprintPriority string
	sprintGroupBy  string
	sprintFormat   string
)

var sprintCmd = &cobra.Command{
	Use:   "sprint",
	Short: "Rank features by priority-weighted gap score for sprint planning",
	Long: `Generates a prioritised list of features for sprint planning by computing
a priority-weighted gap score for each feature:

  score = weight × max(0, target − health)

Weights:  critical=4  high=3  medium=2  low=1
Targets:  critical=1.0  high=0.8  medium=0.6  low=0.4

Features already at or above their target are excluded. The remaining
features are sorted by score (descending) and optionally grouped by
fix type or domain.`,
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

		// Create dependencies and run audit.
		store := adapters.NewYAMLStore()
		builder := adapters.NewGoASTScanner()
		traceUC := app.NewTraceFeatureUseCase(store, builder)
		auditUC := app.NewAuditFeatureUseCase(traceUC, store)

		results, err := auditUC.ExecuteAll(resolvedRegistryDir(), config)
		if err != nil {
			return fmt.Errorf("auditing all features: %w", err)
		}

		// Compute scored entries, filtering out score <= 0.
		var scored []scoredFeature
		for _, r := range results {
			w := priorityWeights[r.Priority]
			if w == 0 {
				w = 1
			}
			target := priorityTargets[r.Priority]
			if target == 0 {
				target = 0.5
			}
			delta := target - r.HealthScore
			if delta < 0 {
				delta = 0
			}
			score := w * delta
			if score <= 0 {
				continue
			}
			scored = append(scored, scoredFeature{
				Score:    score,
				Priority: r.Priority,
				Health:   r.HealthScore,
				Target:   target,
				Feature:  r,
			})
		}

		// Sort by score descending.
		sort.SliceStable(scored, func(i, j int) bool {
			return scored[i].Score > scored[j].Score
		})

		// Filter by --priority if set.
		if sprintPriority != "" {
			allowed := make(map[string]bool)
			for _, p := range strings.Split(sprintPriority, ",") {
				allowed[strings.TrimSpace(p)] = true
			}
			var filtered []scoredFeature
			for _, s := range scored {
				if allowed[s.Priority] {
					filtered = append(filtered, s)
				}
			}
			scored = filtered
		}

		// Apply limit.
		limit := sprintLimit
		if limit > 0 && limit < len(scored) {
			scored = scored[:limit]
		}

		// Render output.
		switch sprintFormat {
		case "json":
			return renderSprintJSON(scored)
		default:
			renderSprintTerminal(scored)
			return nil
		}
	},
}

func init() {
	sprintCmd.Flags().IntVarP(&sprintLimit, "limit", "n", 20, "Top N results")
	sprintCmd.Flags().StringVar(&sprintPriority, "priority", "", "Filter by priority (comma-separated, e.g. critical,high)")
	sprintCmd.Flags().StringVar(&sprintGroupBy, "group-by", "", "Group output by: type, domain")
	sprintCmd.Flags().StringVar(&sprintFormat, "format", "terminal", "Output format: terminal, json")
	rootCmd.AddCommand(sprintCmd)
}

// --- terminal rendering ---

type scoredFeature struct {
	Score    float64
	Priority string
	Health   float64
	Target   float64
	Feature  *domain.AuditOutput
}

func renderSprintTerminal(scored []scoredFeature) {
	out := os.Stdout
	useColor := isFileTTY(out)

	fmt.Fprintln(out)
	header := fmt.Sprintf("Sprint Priorities (%d features, sorted by priority score):", len(scored))
	if useColor {
		fmt.Fprintf(out, "  \033[1m%s\033[0m\n", header)
	} else {
		fmt.Fprintf(out, "  %s\n", header)
	}
	fmt.Fprintln(out)

	// Table header.
	fmt.Fprintf(out, "  %6s  %-10s %6s  %6s  %s\n", "Score", "Priority", "Health", "Target", "Feature")
	fmt.Fprintf(out, "  %s\n", strings.Repeat("─", 70))

	for _, s := range scored {
		healthPct := fmt.Sprintf("%d%%", int(s.Health*100))
		targetPct := fmt.Sprintf("%d%%", int(s.Target*100))
		fmt.Fprintf(out, "  %6.2f  %-10s %5s  %6s  %s\n",
			s.Score,
			s.Priority,
			healthPct,
			targetPct,
			s.Feature.FeatureID,
		)
	}

	// Group-by section.
	switch sprintGroupBy {
	case "type":
		renderGroupByType(scored, out, useColor)
	case "domain":
		renderGroupByDomain(scored, out, useColor)
	}

	fmt.Fprintln(out)
}

func renderGroupByType(scored []scoredFeature, out *os.File, useColor bool) {
	// Count features per fix type using actions and perf gaps.
	typeCounts := make(map[string]int)

	for _, s := range scored {
		// Deduplicate types per feature so we count features, not actions.
		seenTypes := make(map[string]bool)

		for _, action := range s.Feature.Actions {
			tt := normalizeTestType(action.TestType)
			if !seenTypes[tt] {
				seenTypes[tt] = true
				typeCounts[tt]++
			}
		}

		for _, pg := range s.Feature.PerfGaps {
			tt := normalizePerfGapType(pg.GapType)
			if tt != "" && !seenTypes[tt] {
				seenTypes[tt] = true
				typeCounts[tt]++
			}
		}
	}

	if len(typeCounts) == 0 {
		return
	}

	fmt.Fprintln(out)
	if useColor {
		fmt.Fprintf(out, "  \033[1mBy Fix Type:\033[0m\n")
	} else {
		fmt.Fprintf(out, "  By Fix Type:\n")
	}

	// Sort type names for stable output.
	typeOrder := []string{"unit tests", "integration tests", "e2e tests", "benchmarks", "race tests"}
	for _, t := range typeOrder {
		if count, ok := typeCounts[t]; ok {
			fmt.Fprintf(out, "    %-20s %d features\n", t+":", count)
			delete(typeCounts, t)
		}
	}
	// Any remaining types not in the predefined order.
	remaining := make([]string, 0, len(typeCounts))
	for t := range typeCounts {
		remaining = append(remaining, t)
	}
	sort.Strings(remaining)
	for _, t := range remaining {
		fmt.Fprintf(out, "    %-20s %d features\n", t+":", typeCounts[t])
	}
}

func renderGroupByDomain(scored []scoredFeature, out *os.File, useColor bool) {
	// Group features by domain prefix (part before the first dot).
	domainMap := make(map[string][]scoredFeature)
	var domainOrder []string
	seen := make(map[string]bool)

	for _, s := range scored {
		d := extractDomain(s.Feature.FeatureID)
		if !seen[d] {
			seen[d] = true
			domainOrder = append(domainOrder, d)
		}
		domainMap[d] = append(domainMap[d], s)
	}

	sort.Strings(domainOrder)

	fmt.Fprintln(out)
	if useColor {
		fmt.Fprintf(out, "  \033[1mBy Domain:\033[0m\n")
	} else {
		fmt.Fprintf(out, "  By Domain:\n")
	}

	for _, d := range domainOrder {
		features := domainMap[d]
		fmt.Fprintf(out, "\n    %s (%d features):\n", d, len(features))
		for _, s := range features {
			healthPct := fmt.Sprintf("%d%%", int(s.Health*100))
			fmt.Fprintf(out, "      %6.2f  %5s  %s\n", s.Score, healthPct, s.Feature.FeatureID)
		}
	}
}

// --- JSON rendering ---

func renderSprintJSON(scored []scoredFeature) error {
	type jsonEntry struct {
		Score     float64 `json:"score"`
		Priority  string  `json:"priority"`
		Health    float64 `json:"health"`
		Target    float64 `json:"target"`
		FeatureID string  `json:"feature_id"`
	}

	entries := make([]jsonEntry, len(scored))
	for i, s := range scored {
		entries[i] = jsonEntry{
			Score:     s.Score,
			Priority:  s.Priority,
			Health:    s.Health,
			Target:    s.Target,
			FeatureID: s.Feature.FeatureID,
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// --- helpers ---

// normalizeTestType maps action test types to display names.
func normalizeTestType(testType string) string {
	switch testType {
	case "unit":
		return "unit tests"
	case "integration":
		return "integration tests"
	case "e2e":
		return "e2e tests"
	default:
		return testType + " tests"
	}
}

// normalizePerfGapType maps perf gap types to display names.
func normalizePerfGapType(gapType string) string {
	switch gapType {
	case "no-benchmark":
		return "benchmarks"
	case "no-race-test":
		return "race tests"
	default:
		return ""
	}
}

// extractDomain returns the domain prefix from a feature ID.
// For "auth.login" it returns "auth", for "recipes.create" it returns "recipes".
// If there is no dot, the entire ID is returned.
func extractDomain(featureID string) string {
	if idx := strings.Index(featureID, "."); idx > 0 {
		return featureID[:idx]
	}
	return featureID
}
