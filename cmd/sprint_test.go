// @testreg workflow.sprint
package cmd

import "testing"

func TestNormalizeTestType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"unit", "unit tests"},
		{"integration", "integration tests"},
		{"e2e", "e2e tests"},
		{"other", "other tests"},
	}

	for _, tc := range tests {
		got := normalizeTestType(tc.input)
		if got != tc.want {
			t.Errorf("normalizeTestType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizePerfGapType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"no-benchmark", "benchmarks"},
		{"no-race-test", "race tests"},
		{"unknown", ""},
	}

	for _, tc := range tests {
		got := normalizePerfGapType(tc.input)
		if got != tc.want {
			t.Errorf("normalizePerfGapType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"auth.login", "auth"},
		{"meals.log.create", "meals"},
		{"nodot", "nodot"},
	}

	for _, tc := range tests {
		got := extractDomain(tc.input)
		if got != tc.want {
			t.Errorf("extractDomain(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
