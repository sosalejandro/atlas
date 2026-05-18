// @testreg workflow.gaps
package cmd

import (
	"testing"

	"github.com/sosalejandro/atlas/internal/domain"
)

func TestSeverityColor(t *testing.T) {
	tests := []struct {
		severity string
		want     string
	}{
		{"critical", "\033[31m"},
		{"high", "\033[33m"},
		{"medium", "\033[36m"},
		{"low", "\033[37m"},
		{"unknown", ""},
	}

	for _, tc := range tests {
		got := severityColor(tc.severity)
		if got != tc.want {
			t.Errorf("severityColor(%q) = %q, want %q", tc.severity, got, tc.want)
		}
	}
}

func TestTestPattern(t *testing.T) {
	tests := []struct {
		testType string
		want     string
	}{
		{"unit", "table-driven test with mock repository"},
		{"integration", "TestMain setup with test database"},
		{"e2e", "end-to-end test with real browser/device"},
		{"other", "standard test pattern"},
	}

	for _, tc := range tests {
		got := testPattern(tc.testType)
		if got != tc.want {
			t.Errorf("testPattern(%q) = %q, want %q", tc.testType, got, tc.want)
		}
	}
}

func TestPromptActionDescription_WithFile(t *testing.T) {
	action := domain.AuditAction{
		Description: "write unit test",
		File:        "internal/auth/handler_test.go",
	}
	got := promptActionDescription(action)
	want := "write unit test in internal/auth/handler_test.go"
	if got != want {
		t.Errorf("promptActionDescription(with file) = %q, want %q", got, want)
	}
}

func TestPromptActionDescription_WithoutFile(t *testing.T) {
	action := domain.AuditAction{
		Description: "write unit test",
		File:        "",
	}
	got := promptActionDescription(action)
	want := "write unit test"
	if got != want {
		t.Errorf("promptActionDescription(without file) = %q, want %q", got, want)
	}
}
