// @testreg report.markdown-renderer
package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/testreg/internal/domain"
)

// --- fixtures ---

func newTestMarkdownReport() *domain.Report {
	return &domain.Report{
		GeneratedAt: "2026-03-31T12:00:00Z",
		ProjectRoot: "/home/user/project",
		Metrics: domain.Metrics{
			TotalFeatures:      3,
			CoveredUnit:        2,
			CoveredIntegration: 1,
			CoveredE2E:         1,
			MissingUnit:        1,
			MissingE2E:         2,
			FailingE2E:         0,
			ByPriority: map[domain.Priority]domain.PriorityMetrics{
				domain.PriorityCritical: {Total: 1, CoveredUnit: 1, CoveredE2E: 1},
				domain.PriorityHigh:     {Total: 2, CoveredUnit: 1, MissingE2E: 2},
			},
			ByDomain: map[string]domain.DomainMetrics{
				"auth":    {TotalFeatures: 1, CoveredUnit: 1, CoveredE2E: 1},
				"billing": {TotalFeatures: 2, CoveredUnit: 1, CoveredIntegration: 1},
			},
		},
		Domains: []domain.DomainReport{
			{
				Name:        "auth",
				Description: "Authentication features",
				Features: []domain.FeatureReport{
					{
						ID:       "auth.login",
						Name:     "User Login",
						Priority: domain.PriorityCritical,
						Status: map[string]domain.Status{
							"unit.backend": domain.StatusCovered,
							"e2e.web":      domain.StatusCovered,
						},
					},
				},
			},
			{
				Name:        "billing",
				Description: "Payment and billing",
				Features: []domain.FeatureReport{
					{
						ID:       "billing.checkout",
						Name:     "Checkout",
						Priority: domain.PriorityHigh,
						Status: map[string]domain.Status{
							"unit.backend": domain.StatusCovered,
							"e2e.web":      domain.StatusMissing,
						},
						Gaps: []string{"Missing E2E web tests"},
					},
					{
						ID:       "billing.refund",
						Name:     "Refund",
						Priority: domain.PriorityHigh,
						Status: map[string]domain.Status{
							"unit.backend": domain.StatusMissing,
							"e2e.web":      domain.StatusMissing,
						},
						Gaps: []string{"Missing unit backend tests", "Missing E2E web tests"},
					},
				},
			},
		},
	}
}

// --- Render ---

func TestMarkdownReporterRender(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "COVERAGE.md")

	r := NewMarkdownReporter(outPath)
	report := newTestMarkdownReport()

	if err := r.Render(report); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	got := string(data)

	checks := []struct {
		label    string
		contains string
	}{
		{"title", "# Test Coverage Report"},
		{"generated date", "2026-03-31"},
		{"project root", "/home/user/project"},
		{"summary heading", "## Summary"},
		{"total features", "Total Features | 3"},
		{"unit covered count", "Unit Covered | 2"},
		{"priority heading", "## Coverage by Priority"},
		{"critical priority", "**CRITICAL**"},
		{"domain heading", "## Coverage by Domain"},
		{"auth domain section", "## Auth"},
		{"billing domain section", "## Billing"},
		{"feature name login", "User Login"},
		{"feature name checkout", "Checkout"},
		{"status covered emoji", "\u2705"},  // ✅
		{"status missing emoji", "\u274c"},  // ❌
		{"gaps heading", "### Gaps"},
		{"gap text", "Missing E2E web tests"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("Markdown output missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestMarkdownReporterRenderEmptyDomains(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "COVERAGE.md")

	r := NewMarkdownReporter(outPath)
	report := &domain.Report{
		GeneratedAt: "2026-03-31T12:00:00Z",
		ProjectRoot: "/empty",
		Metrics: domain.Metrics{
			ByPriority: make(map[domain.Priority]domain.PriorityMetrics),
			ByDomain:   make(map[string]domain.DomainMetrics),
		},
		Domains: []domain.DomainReport{
			{
				Name:        "empty",
				Description: "An empty domain",
			},
		},
	}

	if err := r.Render(report); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	got := string(data)
	if !strings.Contains(got, "No features registered") {
		t.Error("empty domain should show 'No features registered'")
	}
}

// --- Helper: statusEmoji ---

func TestStatusEmoji(t *testing.T) {
	tests := []struct {
		status domain.Status
		want   string
	}{
		{domain.StatusCovered, "\u2705"},       // ✅
		{domain.StatusPartial, "\U0001f7e1"},   // 🟡
		{domain.StatusMissing, "\u274c"},        // ❌
		{domain.StatusFailing, "\U0001f534"},    // 🔴
		{domain.StatusNotApplicable, "\u2796"},  // ➖
		{domain.Status("unknown"), "\u2014"},    // —
	}

	for _, tt := range tests {
		got := statusEmoji(tt.status)
		if got != tt.want {
			t.Errorf("statusEmoji(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// --- Helper: capitalizeFirst ---

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"auth", "Auth"},
		{"billing", "Billing"},
		{"", ""},
		{"A", "A"},
	}

	for _, tt := range tests {
		got := capitalizeFirst(tt.input)
		if got != tt.want {
			t.Errorf("capitalizeFirst(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Helper: percent ---

func TestPercent(t *testing.T) {
	got := percent(3, 4)
	if !strings.Contains(got, "75.0%") {
		t.Errorf("percent(3, 4) = %q, expected to contain 75.0%%", got)
	}

	got = percent(0, 0)
	if got != "\u2014" { // —
		t.Errorf("percent(0, 0) = %q, expected em-dash", got)
	}
}

// --- Helper: ratio ---

func TestRatio(t *testing.T) {
	got := ratio(2, 3)
	if !strings.Contains(got, "2/3") {
		t.Errorf("ratio(2, 3) = %q, expected to contain 2/3", got)
	}
	if !strings.Contains(got, "67%") {
		t.Errorf("ratio(2, 3) = %q, expected to contain 67%%", got)
	}

	got = ratio(0, 0)
	if got != "\u2014" { // —
		t.Errorf("ratio(0, 0) = %q, expected em-dash", got)
	}
}
