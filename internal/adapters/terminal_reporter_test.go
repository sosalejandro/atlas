// @testreg report.terminal-renderer
package adapters

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/internal/domain"
)

// --- fixtures ---

func newTestReport() *domain.Report {
	return &domain.Report{
		GeneratedAt: "2026-03-31T12:00:00Z",
		ProjectRoot: "/home/user/project",
		Metrics: domain.Metrics{
			TotalFeatures:      4,
			CoveredUnit:        3,
			CoveredIntegration: 2,
			CoveredE2E:         1,
			MissingUnit:        1,
			MissingE2E:         3,
			FailingE2E:         1,
			ByPriority: map[domain.Priority]domain.PriorityMetrics{
				domain.PriorityCritical: {Total: 2, CoveredUnit: 2, CoveredE2E: 1, MissingE2E: 1},
				domain.PriorityHigh:     {Total: 2, CoveredUnit: 1, MissingE2E: 2},
			},
			ByDomain: map[string]domain.DomainMetrics{
				"auth": {TotalFeatures: 2, CoveredUnit: 2, CoveredIntegration: 1, CoveredE2E: 1, FailingE2E: 1},
				"billing": {TotalFeatures: 2, CoveredUnit: 1, CoveredIntegration: 1},
			},
		},
		Domains: []domain.DomainReport{
			{
				Name:        "auth",
				Description: "Authentication domain",
				Features: []domain.FeatureReport{
					{
						ID:       "auth.login",
						Name:     "User Login",
						Priority: domain.PriorityCritical,
						Status:   map[string]domain.Status{"unit.backend": domain.StatusCovered, "e2e.web": domain.StatusCovered},
					},
					{
						ID:       "auth.register",
						Name:     "User Registration",
						Priority: domain.PriorityCritical,
						Status:   map[string]domain.Status{"unit.backend": domain.StatusCovered, "e2e.web": domain.StatusFailing},
					},
				},
			},
			{
				Name:        "billing",
				Description: "Billing domain",
				Features: []domain.FeatureReport{
					{
						ID:       "billing.checkout",
						Name:     "Checkout",
						Priority: domain.PriorityHigh,
						Status:   map[string]domain.Status{"unit.backend": domain.StatusCovered},
					},
				},
			},
		},
	}
}

func newTerminalReporterForTest(buf *bytes.Buffer) *TerminalReporter {
	return &TerminalReporter{
		out:   buf,
		color: false,
	}
}

// --- Render ---

func TestTerminalReporterRender(t *testing.T) {
	var buf bytes.Buffer
	r := newTerminalReporterForTest(&buf)

	report := newTestReport()
	if err := r.Render(report); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"title", "Test Coverage Registry"},
		{"generated date", "2026-03-31"},
		{"project root", "/home/user/project"},
		{"domain auth", "auth"},
		{"domain billing", "billing"},
		{"column header Domain", "Domain"},
		{"column header Unit", "Unit"},
		{"column header E2E", "E2E"},
		{"table border top", "\u250c"},     // ┌
		{"table border bottom", "\u2518"},  // ┘
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("Render missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestTerminalReporterRenderShowsCriticalGaps(t *testing.T) {
	var buf bytes.Buffer
	r := newTerminalReporterForTest(&buf)

	report := newTestReport()
	if err := r.Render(report); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	got := buf.String()

	if !strings.Contains(got, "Critical gaps") {
		t.Error("expected critical gaps warning in output")
	}
	if !strings.Contains(got, "Failing E2E") {
		t.Error("expected failing E2E warning in output")
	}
}

func TestTerminalReporterRenderEmptyDomains(t *testing.T) {
	var buf bytes.Buffer
	r := newTerminalReporterForTest(&buf)

	report := &domain.Report{
		GeneratedAt: "2026-03-31T12:00:00Z",
		ProjectRoot: "/empty",
		Metrics: domain.Metrics{
			ByPriority: make(map[domain.Priority]domain.PriorityMetrics),
			ByDomain:   make(map[string]domain.DomainMetrics),
		},
	}

	if err := r.Render(report); err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "Test Coverage Registry") {
		t.Error("empty report should still show title")
	}
}

// --- RenderFeatureDetail ---

func TestTerminalReporterRenderFeatureDetail(t *testing.T) {
	var buf bytes.Buffer
	r := newTerminalReporterForTest(&buf)

	feature := &domain.Feature{
		ID:          "auth.login",
		Name:        "User Login",
		Description: "Handles user authentication",
		Priority:    domain.PriorityCritical,
		Surfaces: domain.Surfaces{
			Web: &domain.WebSurface{Route: "/login", Component: "LoginPage"},
			API: []domain.APISurface{{Method: "POST", Path: "/api/auth/login"}},
		},
	}

	entries := map[string]EntryDetail{
		"unit.backend": {Status: domain.StatusCovered, Files: []string{"login_test.go"}, Mocked: false, PassRate: 1.0, LastRun: "2026-03-30"},
		"e2e.web":      {Status: domain.StatusMissing},
	}

	gaps := []string{"Missing E2E web tests"}
	suggestions := []string{"Add Playwright test for login flow"}

	if err := r.RenderFeatureDetail(feature, "auth", entries, gaps, suggestions, false); err != nil {
		t.Fatalf("RenderFeatureDetail returned error: %v", err)
	}

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"feature ID", "auth.login"},
		{"priority", "CRITICAL"},
		{"description", "Handles user authentication"},
		{"web surface", "/login"},
		{"component", "LoginPage"},
		{"API method", "POST"},
		{"API path", "/api/auth/login"},
		{"coverage section", "Coverage:"},
		{"test file", "login_test.go"},
		{"status icon OK", "[OK]"},
		{"status icon missing", "[--]"},
		{"gaps detected", "GAPS DETECTED"},
		{"gap text", "Missing E2E web tests"},
		{"suggestion", "Add Playwright test for login flow"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderFeatureDetail missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestTerminalReporterRenderFeatureDetailFullyCovered(t *testing.T) {
	var buf bytes.Buffer
	r := newTerminalReporterForTest(&buf)

	feature := &domain.Feature{
		ID:       "auth.login",
		Name:     "User Login",
		Priority: domain.PriorityCritical,
	}

	entries := map[string]EntryDetail{
		"unit.backend": {Status: domain.StatusCovered},
	}

	if err := r.RenderFeatureDetail(feature, "auth", entries, nil, nil, true); err != nil {
		t.Fatalf("RenderFeatureDetail returned error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "FULLY COVERED") {
		t.Error("fully covered feature should show FULLY COVERED status")
	}
}

// --- statusIcon (no color) ---

func TestTerminalReporterStatusIconNoColor(t *testing.T) {
	r := &TerminalReporter{color: false}

	tests := []struct {
		status domain.Status
		want   string
	}{
		{domain.StatusCovered, "[OK]"},
		{domain.StatusPartial, "[~~]"},
		{domain.StatusMissing, "[--]"},
		{domain.StatusFailing, "[!!]"},
		{domain.StatusNotApplicable, "[NA]"},
		{domain.Status("unknown"), "[??]"},
	}

	for _, tt := range tests {
		got := r.statusIcon(tt.status)
		if got != tt.want {
			t.Errorf("statusIcon(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// --- formatPercent ---

func TestTerminalReporterFormatPercent(t *testing.T) {
	r := &TerminalReporter{color: false}

	got := r.formatPercent(3, 4)
	if !strings.Contains(got, "75%") {
		t.Errorf("formatPercent(3, 4) = %q, expected to contain 75%%", got)
	}

	got = r.formatPercent(0, 0)
	if got != "\u2014" { // —
		t.Errorf("formatPercent(0, 0) = %q, expected em-dash", got)
	}
}
