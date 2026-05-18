package adapters

import (
	"fmt"
	"os"
	"strings"

	"github.com/sosalejandro/atlas/internal/domain"
)

// MarkdownReporter generates a COVERAGE.md file with GitHub-flavored markdown tables.
type MarkdownReporter struct {
	OutputPath string // file path to write the report
}

// NewMarkdownReporter creates a new MarkdownReporter.
func NewMarkdownReporter(outputPath string) *MarkdownReporter {
	return &MarkdownReporter{OutputPath: outputPath}
}

// Render writes the report as a markdown file.
func (r *MarkdownReporter) Render(report *domain.Report) error {
	var sb strings.Builder

	sb.WriteString("# Test Coverage Report\n\n")
	sb.WriteString(fmt.Sprintf("**Generated:** %s  \n", report.GeneratedAt))
	sb.WriteString(fmt.Sprintf("**Project:** %s\n\n", report.ProjectRoot))

	// Summary metrics
	m := report.Metrics
	sb.WriteString("## Summary\n\n")
	sb.WriteString(fmt.Sprintf("| Metric | Count | Percentage |\n"))
	sb.WriteString(fmt.Sprintf("| --- | --- | --- |\n"))
	sb.WriteString(fmt.Sprintf("| Total Features | %d | — |\n", m.TotalFeatures))
	sb.WriteString(fmt.Sprintf("| Unit Covered | %d | %s |\n", m.CoveredUnit, percent(m.CoveredUnit, m.TotalFeatures)))
	sb.WriteString(fmt.Sprintf("| Integration Covered | %d | %s |\n", m.CoveredIntegration, percent(m.CoveredIntegration, m.TotalFeatures)))
	sb.WriteString(fmt.Sprintf("| E2E Covered | %d | %s |\n", m.CoveredE2E, percent(m.CoveredE2E, m.TotalFeatures)))
	sb.WriteString(fmt.Sprintf("| Missing Unit | %d | %s |\n", m.MissingUnit, percent(m.MissingUnit, m.TotalFeatures)))
	sb.WriteString(fmt.Sprintf("| Missing E2E | %d | %s |\n", m.MissingE2E, percent(m.MissingE2E, m.TotalFeatures)))
	sb.WriteString(fmt.Sprintf("| Failing E2E | %d | %s |\n", m.FailingE2E, percent(m.FailingE2E, m.TotalFeatures)))
	sb.WriteString("\n")

	// Priority breakdown
	sb.WriteString("## Coverage by Priority\n\n")
	sb.WriteString("| Priority | Total | Unit | Integration | E2E | Missing E2E |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, p := range []domain.Priority{domain.PriorityCritical, domain.PriorityHigh, domain.PriorityMedium, domain.PriorityLow} {
		pm, ok := m.ByPriority[p]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("| **%s** | %d | %d | %d | %d | %d |\n",
			strings.ToUpper(string(p)), pm.Total, pm.CoveredUnit, pm.CoveredIntegration, pm.CoveredE2E, pm.MissingE2E))
	}
	sb.WriteString("\n")

	// Domain-level table
	sb.WriteString("## Coverage by Domain\n\n")
	sb.WriteString("| Domain | Features | Unit | Integration | E2E |\n")
	sb.WriteString("| --- | --- | --- | --- | --- |\n")
	for domainName, dm := range m.ByDomain {
		sb.WriteString(fmt.Sprintf("| %s | %d | %s | %s | %s |\n",
			domainName, dm.TotalFeatures,
			ratio(dm.CoveredUnit, dm.TotalFeatures),
			ratio(dm.CoveredIntegration, dm.TotalFeatures),
			ratio(dm.CoveredE2E, dm.TotalFeatures),
		))
	}
	sb.WriteString("\n")

	// Detailed per-domain sections
	for _, d := range report.Domains {
		sb.WriteString(fmt.Sprintf("## %s\n\n", capitalizeFirst(d.Name)))
		sb.WriteString(fmt.Sprintf("_%s_\n\n", d.Description))

		if len(d.Features) == 0 {
			sb.WriteString("No features registered.\n\n")
			continue
		}

		sb.WriteString("| Feature | Priority | Unit Backend | Unit Web | Unit Mobile | Integ Backend | E2E Web | E2E Mobile |\n")
		sb.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")

		for _, f := range d.Features {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s | %s |\n",
				f.Name,
				string(f.Priority),
				statusEmoji(f.Status["unit.backend"]),
				statusEmoji(f.Status["unit.web"]),
				statusEmoji(f.Status["unit.mobile"]),
				statusEmoji(f.Status["integration.backend"]),
				statusEmoji(f.Status["e2e.web"]),
				statusEmoji(f.Status["e2e.mobile"]),
			))
		}
		sb.WriteString("\n")

		// List gaps for this domain
		hasGaps := false
		for _, f := range d.Features {
			if len(f.Gaps) > 0 {
				if !hasGaps {
					sb.WriteString("### Gaps\n\n")
					hasGaps = true
				}
				sb.WriteString(fmt.Sprintf("**%s** (%s)\n", f.Name, f.ID))
				for _, gap := range f.Gaps {
					sb.WriteString(fmt.Sprintf("- %s\n", gap))
				}
				sb.WriteString("\n")
			}
		}
	}

	// Write to file
	if err := os.WriteFile(r.OutputPath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("writing report to %s: %w", r.OutputPath, err)
	}

	return nil
}

func statusEmoji(s domain.Status) string {
	switch s {
	case domain.StatusCovered:
		return "✅"
	case domain.StatusPartial:
		return "🟡"
	case domain.StatusMissing:
		return "❌"
	case domain.StatusFailing:
		return "🔴"
	case domain.StatusNotApplicable:
		return "➖"
	default:
		return "—"
	}
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func percent(count, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(count)/float64(total)*100)
}

func ratio(count, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", count, total, float64(count)/float64(total)*100)
}
