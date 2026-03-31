package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/sosalejandro/testreg/internal/domain"
)

// AuditRenderer renders feature health audit reports to terminal or file.
type AuditRenderer struct {
	out   io.Writer
	color bool
}

// NewAuditRenderer creates a new AuditRenderer with TTY auto-detection.
func NewAuditRenderer() *AuditRenderer {
	return &AuditRenderer{
		out:   os.Stdout,
		color: isTTY(),
	}
}

// NewAuditRendererToWriter creates a renderer that writes to a specific writer.
func NewAuditRendererToWriter(w io.Writer, color bool) *AuditRenderer {
	return &AuditRenderer{
		out:   w,
		color: color,
	}
}

// RenderSingle renders a detailed health report for a single feature.
func (r *AuditRenderer) RenderSingle(output *domain.AuditOutput) {
	w := r.out

	// Header
	fmt.Fprintln(w)
	healthPct := int(math.Round(output.HealthScore * 100))
	healthStr := r.colorizeHealth(fmt.Sprintf("%d%%", healthPct), output.HealthScore)
	priorityStr := r.colorizePriority(output.Priority)

	fmt.Fprintf(w, "  %s  Health: %s\n",
		r.c(colorBold, fmt.Sprintf("Feature: %s (%s)", output.FeatureID, priorityStr)),
		healthStr,
	)
	r.writeDoubleLine(w, 55)

	// Dependency chain with test annotations
	if len(output.TraceResults) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorBold, "Dependency Chain:"))
		for _, tr := range output.TraceResults {
			if tr != nil && tr.Root != nil {
				r.writeAnnotatedTrace(w, tr.Root, output.AnnotatedNodes, "    ", true, true)
			}
		}
	}

	// Coverage by layer
	if len(output.LayerCoverage) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorBold, "Coverage by Layer:"))
		for _, lc := range output.LayerCoverage {
			pct := lc.Percentage
			bar := r.progressBar(pct, 20)
			label := fmt.Sprintf("%-12s %d/%-2d (%3.0f%%)", capitalize(lc.Layer)+":", lc.Tested, lc.Total, pct)
			fmt.Fprintf(w, "    %s %s\n", label, bar)
		}
	}

	// E2E coverage
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", r.c(colorBold, "E2E Coverage:"))
	r.writeE2EStatus(w, "Web", output.E2EWeb)
	r.writeE2EStatus(w, "Mobile", output.E2EMobile)

	// Gaps
	if len(output.Gaps) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorBold, fmt.Sprintf("Gaps (%d):", len(output.Gaps))))
		for _, gap := range output.Gaps {
			sevLabel := r.colorizeSeverity(gap.Severity)
			fmt.Fprintf(w, "    %s [%s] %s -- %s\n",
				r.c(colorRed, "\u2718"), sevLabel, gap.NodeID, gap.Reason)
			if gap.File != "" {
				loc := gap.File
				if gap.Line > 0 {
					loc = fmt.Sprintf("%s:%d", gap.File, gap.Line)
				}
				fmt.Fprintf(w, "       %s %s\n", r.c(colorDim, "\u2192"), r.c(colorDim, loc))
			}
		}
	} else {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorGreen, "No coverage gaps detected!"))
	}

	// Performance gaps
	if len(output.PerfGaps) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorBold, fmt.Sprintf("Performance Gaps (%d):", len(output.PerfGaps))))
		for _, pg := range output.PerfGaps {
			sevLabel := r.colorizeSeverity(pg.Severity)
			fmt.Fprintf(w, "    %s [%s] %s \u2014 %s\n",
				r.c(colorRed, "\u2718"), sevLabel, pg.NodeID, pg.Reason)
			fmt.Fprintf(w, "       %s %s\n", r.c(colorDim, "\u2192"), pg.Suggestion)
			if pg.Command != "" {
				fmt.Fprintf(w, "       %s %s\n", r.c(colorDim, "$"), r.c(colorDim, pg.Command))
			}
		}
	}

	// Performance score
	if output.PerfScore != nil {
		fmt.Fprintln(w)
		overallPct := int(math.Round(output.PerfScore.Overall * 100))
		fmt.Fprintf(w, "  %s %s\n", r.c(colorBold, "Performance Score:"),
			r.colorizeHealth(fmt.Sprintf("%d%%", overallPct), output.PerfScore.Overall))

		// Benchmark coverage bar
		benchPct := output.PerfScore.BenchmarkCoverage * 100
		benchBar := r.progressBar(benchPct, 20)
		fmt.Fprintf(w, "    %-22s %d/%-2d (%3.0f%%) %s\n",
			"Benchmark coverage:",
			output.PerfScore.BenchmarkedNodes, output.PerfScore.BenchmarkableNodes,
			benchPct, benchBar)

		// Race test coverage bar
		racePct := output.PerfScore.RaceTestCoverage * 100
		raceBar := r.progressBar(racePct, 20)
		fmt.Fprintf(w, "    %-22s %d/%-2d (%3.0f%%) %s\n",
			"Race test coverage:",
			output.PerfScore.RaceTestedNodes, output.PerfScore.ConcurrentNodes,
			racePct, raceBar)
	}

	// Recommended actions
	if len(output.Actions) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", r.c(colorBold, "Recommended Actions:"))
		for _, action := range output.Actions {
			fmt.Fprintf(w, "    %d. %s\n", action.Priority, action.Description)
			if action.File != "" {
				fmt.Fprintf(w, "       %s %s\n", r.c(colorDim, "File:"), r.c(colorDim, action.File))
			}
		}
	}

	fmt.Fprintln(w)
}

// RenderSummary renders a summary table for all features.
func (r *AuditRenderer) RenderSummary(outputs []*domain.AuditOutput) {
	w := r.out

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s\n", r.c(colorBold, "Feature Health Report \u2014 All Features"))
	r.writeDoubleLine(w, 55)
	fmt.Fprintln(w)

	if len(outputs) == 0 {
		fmt.Fprintf(w, "  %s\n", r.c(colorDim, "No features found in registry."))
		fmt.Fprintln(w)
		return
	}

	// Calculate column widths dynamically.
	featureW := 26
	for _, o := range outputs {
		if len(o.FeatureID)+2 > featureW {
			featureW = len(o.FeatureID) + 2
		}
	}

	priorityW := 10
	healthW := 8
	perfW := 6
	gapsW := 6
	e2eW := 6
	unitW := 6

	// Header
	r.drawTop(w, featureW, priorityW, healthW, perfW, gapsW, e2eW, unitW)
	r.drawHeaderRowWithPerf(w, featureW, priorityW, healthW, perfW, gapsW, e2eW, unitW)
	r.drawMid(w, featureW, priorityW, healthW, perfW, gapsW, e2eW, unitW)

	// Data rows
	for _, o := range outputs {
		healthPct := int(math.Round(o.HealthScore * 100))
		healthStr := r.colorizeHealth(fmt.Sprintf("%3d%%", healthPct), o.HealthScore)
		gapCount := fmt.Sprintf("%d", len(o.Gaps))

		// Performance score column.
		perfStr := r.c(colorDim, " --")
		if o.PerfScore != nil {
			perfPct := int(math.Round(o.PerfScore.Overall * 100))
			perfStr = r.colorizeHealth(fmt.Sprintf("%3d%%", perfPct), o.PerfScore.Overall)
		}

		e2eIcon := r.c(colorRed, "\u2718")
		if (o.E2EWeb != nil && o.E2EWeb.Covered) || (o.E2EMobile != nil && o.E2EMobile.Covered) {
			e2eIcon = r.c(colorGreen, "\u2713")
		}

		unitIcon := r.c(colorRed, "\u2718")
		for _, lc := range o.LayerCoverage {
			if (lc.Layer == "handler" || lc.Layer == "service" || lc.Layer == "component") && lc.Tested > 0 {
				unitIcon = r.c(colorGreen, "\u2713")
				break
			}
		}

		priorityStr := r.colorizePriority(o.Priority)

		// Write the row with ANSI overhead compensation for alignment.
		fmt.Fprintf(w, "  \u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502 %-*s\u2502\n",
			featureW-2, o.FeatureID,
			priorityW-2+r.ansiLen(priorityStr), priorityStr,
			healthW-2+r.ansiLen(healthStr), healthStr,
			perfW-2+r.ansiLen(perfStr), perfStr,
			gapsW-2, gapCount,
			e2eW-2+r.ansiLen(e2eIcon), e2eIcon,
			unitW-2+r.ansiLen(unitIcon), unitIcon,
		)
	}

	r.drawBot(w, featureW, priorityW, healthW, perfW, gapsW, e2eW, unitW)
	fmt.Fprintln(w)

	// Summary statistics.
	total := len(outputs)
	healthy := 0
	critical := 0
	for _, o := range outputs {
		if o.HealthScore >= 0.8 {
			healthy++
		}
		if o.HealthScore < 0.5 {
			critical++
		}
	}

	fmt.Fprintf(w, "  Total: %d  |  %s: %d  |  %s: %d\n",
		total,
		r.c(colorGreen, "Healthy (\u226580%)"), healthy,
		r.c(colorRed, "Critical (<50%)"), critical,
	)
	fmt.Fprintln(w)
}

// RenderJSON writes the audit output as JSON.
func (r *AuditRenderer) RenderJSON(output interface{}) error {
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// RenderMarkdownSingle renders the audit as Markdown suitable for documentation.
func (r *AuditRenderer) RenderMarkdownSingle(output *domain.AuditOutput) {
	w := r.out

	healthPct := int(math.Round(output.HealthScore * 100))
	fmt.Fprintf(w, "# Feature Health: %s\n\n", output.FeatureID)
	fmt.Fprintf(w, "**Priority:** %s | **Health Score:** %d%%\n\n", output.Priority, healthPct)

	// Layer coverage table
	if len(output.LayerCoverage) > 0 {
		fmt.Fprint(w, "## Coverage by Layer\n\n")
		fmt.Fprintln(w, "| Layer | Tested | Total | Coverage |")
		fmt.Fprintln(w, "|-------|--------|-------|----------|")
		for _, lc := range output.LayerCoverage {
			fmt.Fprintf(w, "| %s | %d | %d | %.0f%% |\n", capitalize(lc.Layer), lc.Tested, lc.Total, lc.Percentage)
		}
		fmt.Fprintln(w)
	}

	// E2E coverage
	fmt.Fprint(w, "## E2E Coverage\n\n")
	writeE2EMarkdown(w, "Web", output.E2EWeb)
	writeE2EMarkdown(w, "Mobile", output.E2EMobile)
	fmt.Fprintln(w)

	// Gaps
	if len(output.Gaps) > 0 {
		fmt.Fprintf(w, "## Gaps (%d)\n\n", len(output.Gaps))
		for _, gap := range output.Gaps {
			fmt.Fprintf(w, "- **[%s]** `%s` -- %s", strings.ToUpper(gap.Severity), gap.NodeID, gap.Reason)
			if gap.File != "" {
				loc := gap.File
				if gap.Line > 0 {
					loc = fmt.Sprintf("%s:%d", gap.File, gap.Line)
				}
				fmt.Fprintf(w, " (`%s`)", loc)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}

	// Performance gaps
	if len(output.PerfGaps) > 0 {
		fmt.Fprintf(w, "## Performance Gaps (%d)\n\n", len(output.PerfGaps))
		for _, pg := range output.PerfGaps {
			fmt.Fprintf(w, "- **[%s]** `%s` -- %s\n", strings.ToUpper(pg.Severity), pg.NodeID, pg.Reason)
			fmt.Fprintf(w, "  - %s\n", pg.Suggestion)
			if pg.Command != "" {
				fmt.Fprintf(w, "  - `%s`\n", pg.Command)
			}
		}
		fmt.Fprintln(w)
	}

	// Performance score
	if output.PerfScore != nil {
		fmt.Fprint(w, "## Performance Score\n\n")
		overallPct := int(math.Round(output.PerfScore.Overall * 100))
		fmt.Fprintf(w, "**Overall:** %d%%\n\n", overallPct)
		benchPct := int(math.Round(output.PerfScore.BenchmarkCoverage * 100))
		racePct := int(math.Round(output.PerfScore.RaceTestCoverage * 100))
		fmt.Fprintf(w, "| Metric | Covered | Total | Percentage |\n")
		fmt.Fprintf(w, "|--------|---------|-------|------------|\n")
		fmt.Fprintf(w, "| Benchmark | %d | %d | %d%% |\n",
			output.PerfScore.BenchmarkedNodes, output.PerfScore.BenchmarkableNodes, benchPct)
		fmt.Fprintf(w, "| Race test | %d | %d | %d%% |\n",
			output.PerfScore.RaceTestedNodes, output.PerfScore.ConcurrentNodes, racePct)
		fmt.Fprintln(w)
	}

	// Actions
	if len(output.Actions) > 0 {
		fmt.Fprint(w, "## Recommended Actions\n\n")
		for _, action := range output.Actions {
			fmt.Fprintf(w, "%d. %s", action.Priority, action.Description)
			if action.File != "" {
				fmt.Fprintf(w, " (`%s`)", action.File)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}
}

// RenderMarkdownSummary renders the all-features summary as Markdown.
func (r *AuditRenderer) RenderMarkdownSummary(outputs []*domain.AuditOutput) {
	w := r.out

	fmt.Fprint(w, "# Feature Health Report\n\n")
	fmt.Fprintln(w, "| Feature | Priority | Health | Perf | Gaps | E2E | Unit |")
	fmt.Fprintln(w, "|---------|----------|--------|------|------|-----|------|")

	for _, o := range outputs {
		healthPct := int(math.Round(o.HealthScore * 100))
		perfStr := "--"
		if o.PerfScore != nil {
			perfStr = fmt.Sprintf("%d%%", int(math.Round(o.PerfScore.Overall*100)))
		}
		e2e := "No"
		if (o.E2EWeb != nil && o.E2EWeb.Covered) || (o.E2EMobile != nil && o.E2EMobile.Covered) {
			e2e = "Yes"
		}
		unit := "No"
		for _, lc := range o.LayerCoverage {
			if (lc.Layer == "handler" || lc.Layer == "service" || lc.Layer == "component") && lc.Tested > 0 {
				unit = "Yes"
				break
			}
		}
		fmt.Fprintf(w, "| %s | %s | %d%% | %s | %d | %s | %s |\n",
			o.FeatureID, o.Priority, healthPct, perfStr, len(o.Gaps), e2e, unit)
	}
	fmt.Fprintln(w)
}

// ---------------------------------------------------------------------------
// Internal rendering helpers
// ---------------------------------------------------------------------------

// writeAnnotatedTrace recursively renders a trace node with test status annotations.
func (r *AuditRenderer) writeAnnotatedTrace(w io.Writer, tn *domain.TraceNode, annotations []domain.AnnotatedNode, prefix string, isLast, isRoot bool) {
	if tn == nil || tn.Node == nil {
		return
	}

	// Build connector.
	var connector string
	if isRoot {
		connector = prefix
	} else if isLast {
		connector = prefix + "\u2514\u2500 "
	} else {
		connector = prefix + "\u251c\u2500 "
	}

	// Color the node by kind.
	nodeLabel := r.colorizeNodeKind(tn.Node.Kind, tn.Node.ID)

	// Find annotation for this node.
	status, testFiles := findAnnotation(tn.Node.ID, annotations)
	statusStr := r.testStatusLabel(status, testFiles)

	// File reference.
	fileRef := ""
	if tn.Node.File != "" {
		if tn.Node.Line > 0 {
			fileRef = fmt.Sprintf("%s:%d", tn.Node.File, tn.Node.Line)
		} else {
			fileRef = tn.Node.File
		}
	}

	// Compose the line.
	leftPart := connector + nodeLabel
	if statusStr != "" {
		leftPart += "  " + statusStr
	}

	if fileRef != "" {
		visLen := visibleLength(leftPart)
		const refCol = 70
		padding := refCol - visLen
		if padding < 2 {
			padding = 2
		}
		fmt.Fprintf(w, "%s%s%s\n", leftPart, strings.Repeat(" ", padding), r.c(colorDim, fileRef))
	} else {
		fmt.Fprintln(w, leftPart)
	}

	if tn.IsCycle {
		return
	}

	childPrefix := prefix
	if !isRoot {
		if isLast {
			childPrefix += "   "
		} else {
			childPrefix += "\u2502  "
		}
	}

	for i, child := range tn.Children {
		last := i == len(tn.Children)-1
		r.writeAnnotatedTrace(w, child, annotations, childPrefix, last, false)
	}
}

// findAnnotation looks up the test status for a node ID in the annotations list.
func findAnnotation(nodeID string, annotations []domain.AnnotatedNode) (string, []string) {
	for _, a := range annotations {
		if a.NodeID == nodeID {
			return a.TestStatus, a.TestFiles
		}
	}
	return "untested", nil
}

// testStatusLabel returns a colored test status indicator.
func (r *AuditRenderer) testStatusLabel(status string, testFiles []string) string {
	switch status {
	case "tested":
		label := r.c(colorGreen, "\u2713 tested")
		if len(testFiles) > 0 {
			label += " " + r.c(colorDim, "("+shortFileName(testFiles[0])+")")
		}
		return label
	case "partial":
		label := r.c(colorYellow, "\u25d0 partial")
		if len(testFiles) > 0 {
			label += " " + r.c(colorDim, "("+shortFileName(testFiles[0])+")")
		}
		return label
	case "untested":
		return r.c(colorRed, "\u2718 NO TEST")
	default:
		return ""
	}
}

// writeE2EStatus renders a single E2E coverage line.
func (r *AuditRenderer) writeE2EStatus(w io.Writer, platform string, status *domain.E2ECoverageStatus) {
	if status == nil || !status.Covered {
		fmt.Fprintf(w, "    %-8s %s\n", platform+":", r.c(colorRed, "\u2718 missing"))
		return
	}

	icon := r.c(colorGreen, "\u2713")
	files := ""
	if len(status.TestFiles) > 0 {
		files = shortFileName(status.TestFiles[0])
	}
	testLabel := ""
	if status.TestCount > 0 {
		testLabel = fmt.Sprintf(" (%d tests)", status.TestCount)
	}
	fmt.Fprintf(w, "    %-8s %s %s%s\n", platform+":", icon, files, testLabel)
}

func writeE2EMarkdown(w io.Writer, platform string, status *domain.E2ECoverageStatus) {
	if status == nil || !status.Covered {
		fmt.Fprintf(w, "- **%s:** Missing\n", platform)
		return
	}
	files := ""
	if len(status.TestFiles) > 0 {
		files = status.TestFiles[0]
	}
	fmt.Fprintf(w, "- **%s:** Covered (%s, %d tests)\n", platform, files, status.TestCount)
}

// writeDoubleLine writes a double-line separator.
func (r *AuditRenderer) writeDoubleLine(w io.Writer, width int) {
	fmt.Fprintf(w, "  %s\n", strings.Repeat("\u2550", width))
}

// progressBar creates a terminal progress bar using block characters.
func (r *AuditRenderer) progressBar(pct float64, width int) string {
	filled := int(math.Round(pct / 100.0 * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	empty := width - filled

	bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", empty)

	if pct >= 80 {
		return r.c(colorGreen, bar)
	} else if pct >= 50 {
		return r.c(colorYellow, bar)
	}
	return r.c(colorRed, bar)
}

// colorizeSeverity returns a severity string with appropriate color.
func (r *AuditRenderer) colorizeSeverity(severity string) string {
	upper := strings.ToUpper(severity)
	switch severity {
	case "critical":
		return r.c(colorRed, upper)
	case "high":
		return r.c(colorYellow, upper)
	case "medium":
		return r.c(colorCyan, upper)
	case "low":
		return r.c(colorDim, upper)
	default:
		return upper
	}
}

// colorizePriority returns a priority string with appropriate color.
func (r *AuditRenderer) colorizePriority(priority string) string {
	switch priority {
	case "critical":
		return r.c(colorRed, priority)
	case "high":
		return r.c(colorYellow, priority)
	case "medium":
		return r.c(colorCyan, priority)
	case "low":
		return r.c(colorDim, priority)
	default:
		return priority
	}
}

// colorizeHealth returns a health percentage string colored by threshold.
func (r *AuditRenderer) colorizeHealth(label string, score float64) string {
	if score >= 0.80 {
		return r.c(colorGreen, label)
	} else if score >= 0.50 {
		return r.c(colorYellow, label)
	}
	return r.c(colorRed, label)
}

// colorizeNodeKind returns a node ID colored by its kind.
func (r *AuditRenderer) colorizeNodeKind(kind domain.NodeKind, id string) string {
	if !r.color {
		return id
	}
	switch kind {
	case domain.NodeHandler, domain.NodeEndpoint:
		return colorCyan + id + colorReset
	case domain.NodeService:
		return colorGreen + id + colorReset
	case domain.NodeRepository:
		return colorYellow + id + colorReset
	case domain.NodeQuery:
		return colorMagenta + id + colorReset
	case domain.NodeExternal:
		return colorRed + id + colorReset
	case domain.NodeComponent:
		return colorCyan + id + colorReset
	case domain.NodeHook:
		return colorGreen + id + colorReset
	default:
		return id
	}
}

// c wraps text with ANSI color if color output is enabled.
func (r *AuditRenderer) c(ansiColor, text string) string {
	if !r.color {
		return text
	}
	return ansiColor + text + colorReset
}

// ansiLen returns the number of invisible ANSI bytes in a string.
func (r *AuditRenderer) ansiLen(s string) int {
	if !r.color {
		return 0
	}
	visible := visibleLength(s)
	return len(s) - visible
}

// ---------------------------------------------------------------------------
// Table drawing helpers (reusing terminal_reporter.go patterns)
// ---------------------------------------------------------------------------

func (r *AuditRenderer) drawTop(w io.Writer, widths ...int) {
	fmt.Fprint(w, "  \u250c")
	for i, width := range widths {
		fmt.Fprint(w, strings.Repeat("\u2500", width))
		if i < len(widths)-1 {
			fmt.Fprint(w, "\u252c")
		}
	}
	fmt.Fprintln(w, "\u2510")
}

func (r *AuditRenderer) drawMid(w io.Writer, widths ...int) {
	fmt.Fprint(w, "  \u251c")
	for i, width := range widths {
		fmt.Fprint(w, strings.Repeat("\u2500", width))
		if i < len(widths)-1 {
			fmt.Fprint(w, "\u253c")
		}
	}
	fmt.Fprintln(w, "\u2524")
}

func (r *AuditRenderer) drawBot(w io.Writer, widths ...int) {
	fmt.Fprint(w, "  \u2514")
	for i, width := range widths {
		fmt.Fprint(w, strings.Repeat("\u2500", width))
		if i < len(widths)-1 {
			fmt.Fprint(w, "\u2534")
		}
	}
	fmt.Fprintln(w, "\u2518")
}

func (r *AuditRenderer) drawHeaderRow(w io.Writer, featureW, priorityW, healthW, gapsW, e2eW, unitW int) {
	fmt.Fprintf(w, "  \u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502\n",
		featureW, " Feature",
		priorityW, " Priority",
		healthW, " Health",
		gapsW, " Gaps",
		e2eW, " E2E",
		unitW, " Unit",
	)
}

func (r *AuditRenderer) drawHeaderRowWithPerf(w io.Writer, featureW, priorityW, healthW, perfW, gapsW, e2eW, unitW int) {
	fmt.Fprintf(w, "  \u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502%-*s\u2502\n",
		featureW, " Feature",
		priorityW, " Priority",
		healthW, " Health",
		perfW, " Perf",
		gapsW, " Gaps",
		e2eW, " E2E",
		unitW, " Unit",
	)
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

// shortFileName returns just the filename from a path.
func shortFileName(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return path
}

// capitalize returns a string with the first letter uppercased.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
