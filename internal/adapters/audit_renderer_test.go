// @testreg audit.renderer
package adapters

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/internal/domain"
)

// --- fixtures ---

func newTestAuditOutput() *domain.AuditOutput {
	return &domain.AuditOutput{
		FeatureID:   "auth.login",
		FeatureName: "User Login",
		Priority:    "critical",
		HealthScore: 0.74,
		LayerCoverage: []domain.LayerCoverage{
			{Layer: "handler", Tested: 1, Total: 1, Percentage: 100},
			{Layer: "service", Tested: 0, Total: 2, Percentage: 0},
		},
		Gaps: []domain.AuditGap{
			{NodeID: "svc.Login", Severity: "critical", Reason: "no unit test"},
		},
		Actions: []domain.AuditAction{
			{Priority: 1, Description: "Add unit test for svc.Login", File: "internal/svc/login.go"},
		},
	}
}

func newTestAuditOutputList() []*domain.AuditOutput {
	return []*domain.AuditOutput{
		newTestAuditOutput(),
		{
			FeatureID:   "billing.checkout",
			FeatureName: "Checkout",
			Priority:    "high",
			HealthScore: 0.92,
			LayerCoverage: []domain.LayerCoverage{
				{Layer: "handler", Tested: 2, Total: 2, Percentage: 100},
				{Layer: "service", Tested: 1, Total: 1, Percentage: 100},
			},
		},
	}
}

// --- NewAuditRendererToWriter ---

func TestAuditRendererNewToWriter(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	if r == nil {
		t.Fatal("expected non-nil renderer")
	}
	if r.color {
		t.Error("expected color=false")
	}
}

// --- RenderJSON ---

func TestAuditRendererRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	output := newTestAuditOutput()
	if err := r.RenderJSON(output); err != nil {
		t.Fatalf("RenderJSON returned error: %v", err)
	}

	raw := buf.Bytes()
	if !json.Valid(raw) {
		t.Error("output is not valid JSON")
	}

	got := buf.String()
	if !strings.Contains(got, "auth.login") {
		t.Error("JSON output missing feature ID auth.login")
	}
	if !strings.Contains(got, "User Login") {
		t.Error("JSON output missing feature name")
	}
}

// --- RenderSummary ---

func TestAuditRendererRenderSummary(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	outputs := newTestAuditOutputList()
	r.RenderSummary(outputs)

	got := buf.String()

	if !strings.Contains(got, "auth.login") {
		t.Error("summary missing feature ID auth.login")
	}
	if !strings.Contains(got, "billing.checkout") {
		t.Error("summary missing feature ID billing.checkout")
	}
	if !strings.Contains(got, "74%") {
		t.Error("summary missing health percentage 74%")
	}
	if !strings.Contains(got, "92%") {
		t.Error("summary missing health percentage 92%")
	}
	if !strings.Contains(got, "Feature Health Report") {
		t.Error("summary missing header text")
	}
}

func TestAuditRendererRenderSummaryEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	r.RenderSummary(nil)
	got := buf.String()
	if !strings.Contains(got, "No features found") {
		t.Error("empty summary should indicate no features found")
	}
}

// --- RenderMarkdownSingle ---

func TestAuditRendererRenderMarkdownSingle(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	output := newTestAuditOutput()
	r.RenderMarkdownSingle(output)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"feature header", "# Feature Health: auth.login"},
		{"priority", "**Priority:** critical"},
		{"health score", "74%"},
		{"layer table header", "| Layer | Tested | Total | Coverage |"},
		{"handler row", "| Handler | 1 | 1 | 100% |"},
		{"gap heading", "## Gaps (1)"},
		{"gap content", "svc.Login"},
		{"gap severity", "CRITICAL"},
		{"actions heading", "## Recommended Actions"},
		{"action description", "Add unit test for svc.Login"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("markdown single missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

// --- RenderMarkdownSummary ---

func TestAuditRendererRenderMarkdownSummary(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	outputs := newTestAuditOutputList()
	r.RenderMarkdownSummary(outputs)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"report header", "# Feature Health Report"},
		{"table header", "| Feature | Priority | Health | Perf | Gaps | E2E | Unit |"},
		{"auth row", "auth.login"},
		{"billing row", "billing.checkout"},
		{"health pct", "74%"},
		{"high priority", "high"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("markdown summary missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

// --- Helper: capitalize ---

func TestCapitalize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"handler", "Handler"},
		{"service", "Service"},
		{"", ""},
		{"A", "A"},
		{"abc", "Abc"},
	}

	for _, tt := range tests {
		got := capitalize(tt.input)
		if got != tt.want {
			t.Errorf("capitalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Helper: shortFileName ---

func TestShortFileName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"path/to/file.go", "file.go"},
		{"file.go", "file.go"},
		{"a/b/c/d/test_login.go", "test_login.go"},
		{"", ""},
	}

	for _, tt := range tests {
		got := shortFileName(tt.input)
		if got != tt.want {
			t.Errorf("shortFileName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- RenderSingle (terminal) ---

func TestAuditRendererRenderSingle(t *testing.T) {
	var buf bytes.Buffer
	r := NewAuditRendererToWriter(&buf, false)

	output := newTestAuditOutput()
	r.RenderSingle(output)

	got := buf.String()

	if !strings.Contains(got, "auth.login") {
		t.Error("single render missing feature ID")
	}
	if !strings.Contains(got, "74%") {
		t.Error("single render missing health percentage")
	}
	if !strings.Contains(got, "Coverage by Layer") {
		t.Error("single render missing layer coverage section")
	}
	if !strings.Contains(got, "svc.Login") {
		t.Error("single render missing gap node ID")
	}
	if !strings.Contains(got, "no unit test") {
		t.Error("single render missing gap reason")
	}
	if !strings.Contains(got, "Recommended Actions") {
		t.Error("single render missing actions section")
	}
}
