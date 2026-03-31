package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/spf13/cobra"
)

// Snapshot represents a point-in-time capture of feature health scores.
type Snapshot struct {
	Timestamp time.Time          `json:"timestamp"`
	Features  map[string]float64 `json:"features"`
}

// DiffEntry represents the health change for a single feature.
type DiffEntry struct {
	FeatureID string  `json:"feature_id"`
	From      float64 `json:"from"`
	To        float64 `json:"to"`
	Delta     float64 `json:"delta"`
}

// DiffResult holds the full comparison output.
type DiffResult struct {
	BaselineLabel string      `json:"baseline_label"`
	Improved      []DiffEntry `json:"improved,omitempty"`
	Regressed     []DiffEntry `json:"regressed,omitempty"`
	Unchanged     int         `json:"unchanged"`
	NewFeatures   []DiffEntry `json:"new_features,omitempty"`
	Removed       []DiffEntry `json:"removed_features,omitempty"`
	AvgDelta      float64     `json:"avg_delta"`
}

var (
	diffBaseline     string
	diffSaveSnapshot string
	diffFrom         string
	diffTo           string
	diffFormat       string
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Compare health snapshots across sprints",
	Long: `Compare feature health scores between snapshots to track progress
across sprints. Snapshots are stored in .testreg-cache/snapshots/.

Modes:
  testreg diff --save-snapshot sprint-1    Save current audit as a named snapshot
  testreg diff                             Compare current state vs latest snapshot
  testreg diff --baseline path/to/file     Compare current state vs a specific file
  testreg diff --from sprint-1 --to sprint-2  Compare two named snapshots`,
	RunE: func(cmd *cobra.Command, args []string) error {
		metrics := adapters.NewMetrics(metricsEnabled)
		defer metrics.Print(os.Stderr)

		snapshotDir := filepath.Join(resolvedProjectRoot(), ".testreg-cache", "snapshots")
		if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
			return fmt.Errorf("creating snapshot directory: %w", err)
		}

		// Mode 4: Compare two named snapshots (no audit needed).
		if diffFrom != "" {
			return runSnapshotDiff(snapshotDir)
		}

		// All other modes need a current audit snapshot.
		current, err := buildCurrentSnapshot()
		if err != nil {
			return err
		}

		// Mode 1: Save snapshot.
		if diffSaveSnapshot != "" {
			return saveSnapshot(current, snapshotDir, diffSaveSnapshot)
		}

		// Mode 2: Compare against latest.
		if diffBaseline == "" {
			diffBaseline = filepath.Join(snapshotDir, "latest.json")
		}

		// Mode 3: Compare against specified baseline.
		baseline, label, err := loadBaseline(diffBaseline, snapshotDir)
		if err != nil {
			return err
		}

		result := computeDiff(baseline, current, label)
		return renderDiff(result)
	},
}

func init() {
	diffCmd.Flags().StringVar(&diffBaseline, "baseline", "", "Path to baseline JSON file to compare against")
	diffCmd.Flags().StringVar(&diffSaveSnapshot, "save-snapshot", "", "Save current audit as a named snapshot (e.g. \"sprint-1\")")
	diffCmd.Flags().StringVar(&diffFrom, "from", "", "Named snapshot to compare from")
	diffCmd.Flags().StringVar(&diffTo, "to", "", "Named snapshot to compare to (default: current)")
	diffCmd.Flags().StringVar(&diffFormat, "format", "terminal", "Output format: terminal, json")
	rootCmd.AddCommand(diffCmd)
}

// buildCurrentSnapshot runs the audit across all features and returns a Snapshot.
func buildCurrentSnapshot() (*Snapshot, error) {
	graphSection, err := adapters.LoadGraphConfig(resolvedProjectRoot())
	if err != nil {
		return nil, fmt.Errorf("loading graph config: %w", err)
	}

	config := graphSection.ToPortsConfig()
	config.ProjectRoot = resolvedProjectRoot()

	store := adapters.NewYAMLStore()
	builder := adapters.NewGoASTScanner()
	traceUC := app.NewTraceFeatureUseCase(store, builder)
	auditUC := app.NewAuditFeatureUseCase(traceUC, store)

	results, err := auditUC.ExecuteAll(resolvedRegistryDir(), config)
	if err != nil {
		return nil, fmt.Errorf("running audit: %w", err)
	}

	features := make(map[string]float64, len(results))
	for _, r := range results {
		features[r.FeatureID] = roundTo4(r.HealthScore)
	}

	return &Snapshot{
		Timestamp: time.Now().UTC(),
		Features:  features,
	}, nil
}

// saveSnapshot writes the snapshot to the named file and to latest.json.
func saveSnapshot(snap *Snapshot, snapshotDir, name string) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling snapshot: %w", err)
	}

	namedPath := filepath.Join(snapshotDir, name+".json")
	if err := os.WriteFile(namedPath, data, 0o644); err != nil {
		return fmt.Errorf("writing snapshot %q: %w", namedPath, err)
	}

	latestPath := filepath.Join(snapshotDir, "latest.json")
	if err := os.WriteFile(latestPath, data, 0o644); err != nil {
		return fmt.Errorf("writing latest snapshot: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Snapshot saved: %s\n", namedPath)
	fmt.Fprintf(os.Stdout, "Also saved as: %s\n", latestPath)
	fmt.Fprintf(os.Stdout, "Features captured: %d\n", len(snap.Features))
	return nil
}

// loadBaseline loads a snapshot from a file path. If the path is not absolute
// and doesn't exist as-is, it tries to resolve it as a named snapshot.
func loadBaseline(path, snapshotDir string) (*Snapshot, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading baseline %q: %w", path, err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, "", fmt.Errorf("parsing baseline %q: %w", path, err)
	}

	label := filepath.Base(path)
	label = strings.TrimSuffix(label, ".json")
	return &snap, label, nil
}

// loadNamedSnapshot loads a snapshot by name from the snapshot directory.
func loadNamedSnapshot(snapshotDir, name string) (*Snapshot, error) {
	path := filepath.Join(snapshotDir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading snapshot %q: %w", path, err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parsing snapshot %q: %w", path, err)
	}
	return &snap, nil
}

// runSnapshotDiff compares two named snapshots.
func runSnapshotDiff(snapshotDir string) error {
	fromSnap, err := loadNamedSnapshot(snapshotDir, diffFrom)
	if err != nil {
		return err
	}

	var toSnap *Snapshot
	if diffTo != "" {
		toSnap, err = loadNamedSnapshot(snapshotDir, diffTo)
		if err != nil {
			return err
		}
	} else {
		// Default: compare against current audit.
		toSnap, err = buildCurrentSnapshot()
		if err != nil {
			return err
		}
	}

	result := computeDiff(fromSnap, toSnap, diffFrom)
	return renderDiff(result)
}

// computeDiff compares two snapshots and produces a DiffResult.
func computeDiff(baseline, current *Snapshot, label string) *DiffResult {
	result := &DiffResult{
		BaselineLabel: label,
	}

	// Track all feature IDs from both snapshots.
	allFeatures := make(map[string]bool)
	for id := range baseline.Features {
		allFeatures[id] = true
	}
	for id := range current.Features {
		allFeatures[id] = true
	}

	var totalDelta float64
	var deltaCount int

	for id := range allFeatures {
		fromScore, inBaseline := baseline.Features[id]
		toScore, inCurrent := current.Features[id]

		if !inBaseline && inCurrent {
			// New feature not in baseline.
			result.NewFeatures = append(result.NewFeatures, DiffEntry{
				FeatureID: id,
				From:      0,
				To:        toScore,
				Delta:     toScore,
			})
			continue
		}

		if inBaseline && !inCurrent {
			// Feature removed.
			result.Removed = append(result.Removed, DiffEntry{
				FeatureID: id,
				From:      fromScore,
				To:        0,
				Delta:     -fromScore,
			})
			continue
		}

		delta := toScore - fromScore
		totalDelta += delta
		deltaCount++

		entry := DiffEntry{
			FeatureID: id,
			From:      fromScore,
			To:        toScore,
			Delta:     delta,
		}

		if math.Abs(delta) < 0.0001 {
			result.Unchanged++
		} else if delta > 0 {
			result.Improved = append(result.Improved, entry)
		} else {
			result.Regressed = append(result.Regressed, entry)
		}
	}

	// Sort improved by delta descending (biggest improvement first).
	sort.Slice(result.Improved, func(i, j int) bool {
		return result.Improved[i].Delta > result.Improved[j].Delta
	})

	// Sort regressed by delta ascending (biggest regression first).
	sort.Slice(result.Regressed, func(i, j int) bool {
		return result.Regressed[i].Delta < result.Regressed[j].Delta
	})

	// Sort new features by score descending.
	sort.Slice(result.NewFeatures, func(i, j int) bool {
		return result.NewFeatures[i].To > result.NewFeatures[j].To
	})

	if deltaCount > 0 {
		result.AvgDelta = totalDelta / float64(deltaCount)
	}

	return result
}

// renderDiff outputs the diff result in the selected format.
func renderDiff(result *DiffResult) error {
	switch diffFormat {
	case "json":
		return renderDiffJSON(result)
	default:
		renderDiffTerminal(result)
		return nil
	}
}

// renderDiffJSON outputs the diff as JSON.
func renderDiffJSON(result *DiffResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling diff result: %w", err)
	}
	fmt.Fprintln(os.Stdout, string(data))
	return nil
}

// renderDiffTerminal outputs a human-readable diff to stdout.
func renderDiffTerminal(result *DiffResult) {
	out := os.Stdout
	color := isFileTTY(out)

	fmt.Fprintf(out, "\nHealth Changes (since %s):\n\n", result.BaselineLabel)

	// Improved features.
	if len(result.Improved) > 0 {
		header := fmt.Sprintf("  Improved (%d features):", len(result.Improved))
		if color {
			fmt.Fprintf(out, "  \033[32mImproved (%d features):\033[0m\n", len(result.Improved))
		} else {
			fmt.Fprintln(out, header)
		}
		for _, e := range result.Improved {
			pctStr := formatDeltaPct(e.Delta)
			fromStr := formatScorePct(e.From)
			toStr := formatScorePct(e.To)
			line := fmt.Sprintf("    +%s  %-40s %s -> %s", pctStr, e.FeatureID, fromStr, toStr)
			if color {
				fmt.Fprintf(out, "    \033[32m+%s\033[0m  %-40s %s -> %s\n", pctStr, e.FeatureID, fromStr, toStr)
			} else {
				fmt.Fprintln(out, line)
			}
		}
		fmt.Fprintln(out)
	}

	// Regressed features.
	if len(result.Regressed) > 0 {
		if color {
			fmt.Fprintf(out, "  \033[31mRegressed (%d features):\033[0m\n", len(result.Regressed))
		} else {
			fmt.Fprintf(out, "  Regressed (%d features):\n", len(result.Regressed))
		}
		for _, e := range result.Regressed {
			pctStr := formatDeltaPct(math.Abs(e.Delta))
			fromStr := formatScorePct(e.From)
			toStr := formatScorePct(e.To)
			if color {
				fmt.Fprintf(out, "    \033[31m-%s\033[0m  %-40s %s -> %s\n", pctStr, e.FeatureID, fromStr, toStr)
			} else {
				fmt.Fprintf(out, "    -%s  %-40s %s -> %s\n", pctStr, e.FeatureID, fromStr, toStr)
			}
		}
		fmt.Fprintln(out)
	}

	// New features.
	if len(result.NewFeatures) > 0 {
		if color {
			fmt.Fprintf(out, "  \033[36mNew (%d features):\033[0m\n", len(result.NewFeatures))
		} else {
			fmt.Fprintf(out, "  New (%d features):\n", len(result.NewFeatures))
		}
		for _, e := range result.NewFeatures {
			toStr := formatScorePct(e.To)
			if color {
				fmt.Fprintf(out, "    \033[36m new \033[0m  %-40s %s\n", e.FeatureID, toStr)
			} else {
				fmt.Fprintf(out, "     new   %-40s %s\n", e.FeatureID, toStr)
			}
		}
		fmt.Fprintln(out)
	}

	// Removed features.
	if len(result.Removed) > 0 {
		if color {
			fmt.Fprintf(out, "  \033[33mRemoved (%d features):\033[0m\n", len(result.Removed))
		} else {
			fmt.Fprintf(out, "  Removed (%d features):\n", len(result.Removed))
		}
		for _, e := range result.Removed {
			fromStr := formatScorePct(e.From)
			if color {
				fmt.Fprintf(out, "    \033[33m rem \033[0m  %-40s was %s\n", e.FeatureID, fromStr)
			} else {
				fmt.Fprintf(out, "     rem   %-40s was %s\n", e.FeatureID, fromStr)
			}
		}
		fmt.Fprintln(out)
	}

	// Unchanged count.
	fmt.Fprintf(out, "  Unchanged: %d features\n\n", result.Unchanged)

	// Summary line.
	sign := "+"
	if result.AvgDelta < 0 {
		sign = ""
	}
	avgPct := result.AvgDelta * 100
	if color {
		clr := "\033[32m"
		if result.AvgDelta < 0 {
			clr = "\033[31m"
		}
		if math.Abs(result.AvgDelta) < 0.0001 {
			clr = "\033[0m"
		}
		fmt.Fprintf(out, "  Summary: %s%s%.1f%%\033[0m average health change\n\n", clr, sign, avgPct)
	} else {
		fmt.Fprintf(out, "  Summary: %s%.1f%% average health change\n\n", sign, avgPct)
	}
}

// formatDeltaPct formats a delta (0.0-1.0) as a percentage string like "100%" with right-aligned width.
func formatDeltaPct(delta float64) string {
	pct := delta * 100
	return fmt.Sprintf("%3.0f%%", pct)
}

// formatScorePct formats a score (0.0-1.0) as a percentage string like " 74%".
func formatScorePct(score float64) string {
	pct := score * 100
	return fmt.Sprintf("%3.0f%%", pct)
}

// roundTo4 rounds a float to 4 decimal places.
func roundTo4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
