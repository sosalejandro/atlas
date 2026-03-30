package adapters

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sosalejandro/testreg/internal/domain"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

// TerminalReporter renders coverage reports as formatted tables to stdout.
type TerminalReporter struct {
	out   io.Writer
	color bool
}

// NewTerminalReporter creates a new TerminalReporter.
// Color is auto-detected based on whether stdout is a terminal.
func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{
		out:   os.Stdout,
		color: isTTY(),
	}
}

// Render outputs the report as a formatted terminal table.
func (r *TerminalReporter) Render(report *domain.Report) error {
	w := r.out
	m := report.Metrics

	r.writeln(w, "")
	r.writeBold(w, "  Test Coverage Registry")
	r.writeDim(w, fmt.Sprintf("  Generated: %s  |  Project: %s", report.GeneratedAt, report.ProjectRoot))
	r.writeln(w, "")

	// Build table data
	type row struct {
		domain string
		total  int
		unit   string
		integ  string
		e2e    string
	}

	var rows []row
	var totalFeatures, totalUnit, totalInteg, totalE2E int

	for _, d := range report.Domains {
		dm, ok := m.ByDomain[d.Name]
		if !ok {
			continue
		}
		totalFeatures += dm.TotalFeatures
		totalUnit += dm.CoveredUnit
		totalInteg += dm.CoveredIntegration
		totalE2E += dm.CoveredE2E

		rows = append(rows, row{
			domain: d.Name,
			total:  dm.TotalFeatures,
			unit:   r.formatCoverage(dm.CoveredUnit, dm.TotalFeatures),
			integ:  r.formatCoverage(dm.CoveredIntegration, dm.TotalFeatures),
			e2e:    r.formatCoverageWithWarnings(dm.CoveredE2E, dm.TotalFeatures, dm.FailingE2E),
		})
	}

	// Calculate column widths
	domainWidth := 22
	for _, row := range rows {
		if len(row.domain)+2 > domainWidth {
			domainWidth = len(row.domain) + 2
		}
	}

	totalWidth := 7
	unitWidth := 10
	integWidth := 10
	e2eWidth := 10

	// Draw table
	r.drawTopBorder(w, domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)
	r.drawHeaderRow(w, domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)
	r.drawSeparator(w, domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)

	for _, row := range rows {
		r.drawDataRow(w, row.domain, row.total, row.unit, row.integ, row.e2e,
			domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)
	}

	r.drawSeparator(w, domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)

	// Total row
	unitPct := r.formatPercent(totalUnit, totalFeatures)
	integPct := r.formatPercent(totalInteg, totalFeatures)
	e2ePct := r.formatPercent(totalE2E, totalFeatures)

	r.drawDataRow(w, "TOTAL", totalFeatures, unitPct, integPct, e2ePct,
		domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)

	r.drawBottomBorder(w, domainWidth, totalWidth, unitWidth, integWidth, e2eWidth)
	r.writeln(w, "")

	// Summary section
	criticalMissing := 0
	if pm, ok := m.ByPriority[domain.PriorityCritical]; ok {
		criticalMissing = pm.MissingE2E
	}
	if criticalMissing > 0 {
		r.writeColored(w, colorRed, fmt.Sprintf("  Critical gaps: %d critical features missing E2E coverage", criticalMissing))
	}
	if m.FailingE2E > 0 {
		r.writeColored(w, colorYellow, fmt.Sprintf("  Failing E2E: %d features with failing end-to-end tests", m.FailingE2E))
	}

	r.writeln(w, "")

	return nil
}

// RenderFeatureDetail outputs a detailed view of a single feature's coverage.
func (r *TerminalReporter) RenderFeatureDetail(feature *domain.Feature, domainName string, entries map[string]EntryDetail, gaps, suggestions []string, fullyCovered bool) error {
	w := r.out

	r.writeln(w, "")
	priorityStr := strings.ToUpper(string(feature.Priority))
	r.writeBold(w, fmt.Sprintf("  Feature: %s (%s)", feature.ID, priorityStr))
	r.writeDim(w, fmt.Sprintf("  %s — %s", feature.Name, feature.Description))
	r.writeln(w, "")

	// Surfaces
	r.writeBold(w, "  Surfaces:")
	if feature.Surfaces.Web != nil {
		fmt.Fprintf(w, "    Web:    %s → %s\n", feature.Surfaces.Web.Route, feature.Surfaces.Web.Component)
	}
	if feature.Surfaces.Mobile != nil {
		fmt.Fprintf(w, "    Mobile: %s\n", feature.Surfaces.Mobile.Screen)
	}
	for _, api := range feature.Surfaces.API {
		fmt.Fprintf(w, "    API:    %s %s\n", api.Method, api.Path)
	}
	r.writeln(w, "")

	// Coverage details
	r.writeBold(w, "  Coverage:")

	orderedKeys := []string{
		"unit.backend", "unit.web", "unit.mobile",
		"integration.backend", "integration.mobile",
		"e2e.web", "e2e.mobile",
	}
	labelMap := map[string]string{
		"unit.backend":        "Unit    Backend",
		"unit.web":            "Unit    Web",
		"unit.mobile":         "Unit    Mobile",
		"integration.backend": "Integ   Backend",
		"integration.mobile":  "Integ   Mobile",
		"e2e.web":             "E2E     Web",
		"e2e.mobile":          "E2E     Mobile",
	}

	for _, key := range orderedKeys {
		entry, exists := entries[key]
		if !exists {
			continue
		}

		icon := r.statusIcon(entry.Status)
		label := labelMap[key]

		mockLabel := ""
		if entry.Mocked {
			mockLabel = " [mocked]"
		} else if entry.Status.IsCovered() {
			mockLabel = " [real]"
		}

		passLabel := ""
		if entry.PassRate > 0 {
			passLabel = fmt.Sprintf(" (%.0f%%", entry.PassRate*100)
			if entry.LastRun != "" {
				passLabel += " — " + entry.LastRun
			}
			passLabel += ")"
		}

		filesStr := ""
		if len(entry.Files) > 0 {
			filesStr = "  " + strings.Join(entry.Files, ", ")
		}

		fmt.Fprintf(w, "    %s %s%s%s%s\n", icon, label, mockLabel, passLabel, filesStr)
	}

	r.writeln(w, "")

	// Status summary
	if fullyCovered {
		r.writeColored(w, colorGreen, "  Status: FULLY COVERED ✓")
	} else {
		r.writeColored(w, colorYellow, "  Status: GAPS DETECTED")
		r.writeln(w, "")
		r.writeBold(w, "  Gaps:")
		for _, gap := range gaps {
			fmt.Fprintf(w, "    • %s\n", gap)
		}
	}

	if len(suggestions) > 0 {
		r.writeln(w, "")
		r.writeBold(w, "  Suggestions:")
		for _, s := range suggestions {
			fmt.Fprintf(w, "    → %s\n", s)
		}
	}

	r.writeln(w, "")
	return nil
}

// EntryDetail is imported from the check use case for rendering.
type EntryDetail = struct {
	Status   domain.Status
	Files    []string
	Mocked   bool
	PassRate float64
	LastRun  string
}

func (r *TerminalReporter) statusIcon(s domain.Status) string {
	if !r.color {
		switch s {
		case domain.StatusCovered:
			return "[OK]"
		case domain.StatusPartial:
			return "[~~]"
		case domain.StatusMissing:
			return "[--]"
		case domain.StatusFailing:
			return "[!!]"
		case domain.StatusNotApplicable:
			return "[NA]"
		default:
			return "[??]"
		}
	}
	switch s {
	case domain.StatusCovered:
		return colorGreen + "✓" + colorReset
	case domain.StatusPartial:
		return colorYellow + "◐" + colorReset
	case domain.StatusMissing:
		return colorRed + "✗" + colorReset
	case domain.StatusFailing:
		return colorRed + "!" + colorReset
	case domain.StatusNotApplicable:
		return colorDim + "—" + colorReset
	default:
		return "?"
	}
}

func (r *TerminalReporter) formatCoverage(covered, total int) string {
	if total == 0 {
		return "—"
	}
	icon := "✓"
	if r.color && covered == total {
		icon = colorGreen + "✓" + colorReset
	} else if r.color && covered == 0 {
		icon = colorRed + "✗" + colorReset
	} else if r.color {
		icon = colorYellow + "◐" + colorReset
	} else if covered == total {
		icon = "OK"
	} else if covered == 0 {
		icon = "!!"
	}
	return fmt.Sprintf("%d/%d %s", covered, total, icon)
}

func (r *TerminalReporter) formatCoverageWithWarnings(covered, total, failing int) string {
	base := r.formatCoverage(covered, total)
	if failing > 0 {
		if r.color {
			return base + colorRed + fmt.Sprintf(" (%d failing)", failing) + colorReset
		}
		return base + fmt.Sprintf(" (%d failing)", failing)
	}
	return base
}

func (r *TerminalReporter) formatPercent(count, total int) string {
	if total == 0 {
		return "—"
	}
	pct := float64(count) / float64(total) * 100
	icon := "✓"
	if r.color && pct >= 80 {
		icon = colorGreen + "✓" + colorReset
	} else if r.color && pct >= 50 {
		icon = colorYellow + "◐" + colorReset
	} else if r.color {
		icon = colorRed + "✗" + colorReset
	} else if pct < 50 {
		icon = "!!"
	} else if pct < 80 {
		icon = "~~"
	}
	return fmt.Sprintf("%.0f%% %s", pct, icon)
}

// Box-drawing table rendering

func (r *TerminalReporter) drawTopBorder(w io.Writer, widths ...int) {
	fmt.Fprint(w, "  ┌")
	for i, width := range widths {
		fmt.Fprint(w, strings.Repeat("─", width))
		if i < len(widths)-1 {
			fmt.Fprint(w, "┬")
		}
	}
	fmt.Fprintln(w, "┐")
}

func (r *TerminalReporter) drawBottomBorder(w io.Writer, widths ...int) {
	fmt.Fprint(w, "  └")
	for i, width := range widths {
		fmt.Fprint(w, strings.Repeat("─", width))
		if i < len(widths)-1 {
			fmt.Fprint(w, "┴")
		}
	}
	fmt.Fprintln(w, "┘")
}

func (r *TerminalReporter) drawSeparator(w io.Writer, widths ...int) {
	fmt.Fprint(w, "  ├")
	for i, width := range widths {
		fmt.Fprint(w, strings.Repeat("─", width))
		if i < len(widths)-1 {
			fmt.Fprint(w, "┼")
		}
	}
	fmt.Fprintln(w, "┤")
}

func (r *TerminalReporter) drawHeaderRow(w io.Writer, domainW, totalW, unitW, integW, e2eW int) {
	fmt.Fprintf(w, "  │%-*s│%-*s│%-*s│%-*s│%-*s│\n",
		domainW, " Domain",
		totalW, " Total",
		unitW, " Unit",
		integW, " Integ.",
		e2eW, " E2E",
	)
}

func (r *TerminalReporter) drawDataRow(w io.Writer, name string, total int, unit, integ, e2e string, domainW, totalW, unitW, integW, e2eW int) {
	// For ANSI-colored strings, we need to account for invisible escape codes
	fmt.Fprintf(w, "  │ %-*s│ %-*d│ %-*s│ %-*s│ %-*s│\n",
		domainW-2, name,
		totalW-2, total,
		unitW-2+r.ansiOverhead(unit), unit,
		integW-2+r.ansiOverhead(integ), integ,
		e2eW-2+r.ansiOverhead(e2e), e2e,
	)
}

func (r *TerminalReporter) ansiOverhead(s string) int {
	if !r.color {
		return 0
	}
	// Count ANSI escape sequences (each is ~5 chars for color + 4 for reset)
	overhead := 0
	for _, seq := range []string{colorReset, colorRed, colorGreen, colorYellow, colorCyan, colorBold, colorDim} {
		overhead += strings.Count(s, seq) * len(seq)
	}
	return overhead
}

func (r *TerminalReporter) writeln(w io.Writer, s string) {
	fmt.Fprintln(w, s)
}

func (r *TerminalReporter) writeBold(w io.Writer, s string) {
	if r.color {
		fmt.Fprintf(w, "%s%s%s\n", colorBold, s, colorReset)
	} else {
		fmt.Fprintln(w, s)
	}
}

func (r *TerminalReporter) writeDim(w io.Writer, s string) {
	if r.color {
		fmt.Fprintf(w, "%s%s%s\n", colorDim, s, colorReset)
	} else {
		fmt.Fprintln(w, s)
	}
}

func (r *TerminalReporter) writeColored(w io.Writer, color, s string) {
	if r.color {
		fmt.Fprintf(w, "%s%s%s\n", color, s, colorReset)
	} else {
		fmt.Fprintln(w, s)
	}
}

// isTTY returns true if stdout is a terminal (not piped).
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
