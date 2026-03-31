// @testreg workflow.diff
package cmd

import (
	"math"
	"testing"
)

func TestComputeDiff_ImprovedAndUnchanged(t *testing.T) {
	baseline := &Snapshot{Features: map[string]float64{"a": 0.5, "b": 0.8}}
	current := &Snapshot{Features: map[string]float64{"a": 0.8, "b": 0.8}}
	result := computeDiff(baseline, current, "test")

	if len(result.Improved) != 1 {
		t.Fatalf("expected 1 improved, got %d", len(result.Improved))
	}
	if result.Improved[0].FeatureID != "a" {
		t.Errorf("improved feature = %q, want %q", result.Improved[0].FeatureID, "a")
	}
	if math.Abs(result.Improved[0].Delta-0.3) > 0.0001 {
		t.Errorf("improved delta = %v, want 0.3", result.Improved[0].Delta)
	}
	if result.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1", result.Unchanged)
	}
}

func TestComputeDiff_RemovedFeature(t *testing.T) {
	baseline := &Snapshot{Features: map[string]float64{"a": 0.5, "b": 0.8}}
	current := &Snapshot{Features: map[string]float64{"a": 0.5}}
	result := computeDiff(baseline, current, "test")

	if len(result.Removed) != 1 {
		t.Fatalf("expected 1 removed, got %d", len(result.Removed))
	}
	if result.Removed[0].FeatureID != "b" {
		t.Errorf("removed feature = %q, want %q", result.Removed[0].FeatureID, "b")
	}
}

func TestComputeDiff_NewFeature(t *testing.T) {
	baseline := &Snapshot{Features: map[string]float64{"a": 0.5}}
	current := &Snapshot{Features: map[string]float64{"a": 0.5, "c": 0.3}}
	result := computeDiff(baseline, current, "test")

	if len(result.NewFeatures) != 1 {
		t.Fatalf("expected 1 new feature, got %d", len(result.NewFeatures))
	}
	if result.NewFeatures[0].FeatureID != "c" {
		t.Errorf("new feature = %q, want %q", result.NewFeatures[0].FeatureID, "c")
	}
	if result.NewFeatures[0].To != 0.3 {
		t.Errorf("new feature To = %v, want 0.3", result.NewFeatures[0].To)
	}
}

func TestComputeDiff_Regressed(t *testing.T) {
	baseline := &Snapshot{Features: map[string]float64{"a": 0.8}}
	current := &Snapshot{Features: map[string]float64{"a": 0.5}}
	result := computeDiff(baseline, current, "test")

	if len(result.Regressed) != 1 {
		t.Fatalf("expected 1 regressed, got %d", len(result.Regressed))
	}
	if result.Regressed[0].FeatureID != "a" {
		t.Errorf("regressed feature = %q, want %q", result.Regressed[0].FeatureID, "a")
	}
	if math.Abs(result.Regressed[0].Delta-(-0.3)) > 0.0001 {
		t.Errorf("regressed delta = %v, want -0.3", result.Regressed[0].Delta)
	}
}

func TestComputeDiff_MixedScenario(t *testing.T) {
	baseline := &Snapshot{Features: map[string]float64{"a": 0.5, "b": 0.8}}
	current := &Snapshot{Features: map[string]float64{"a": 0.8, "b": 0.8, "c": 0.3}}
	result := computeDiff(baseline, current, "test")

	if len(result.Improved) != 1 {
		t.Errorf("improved count = %d, want 1", len(result.Improved))
	}
	if result.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1", result.Unchanged)
	}
	if len(result.NewFeatures) != 1 {
		t.Errorf("new features count = %d, want 1", len(result.NewFeatures))
	}
	if len(result.Regressed) != 0 {
		t.Errorf("regressed count = %d, want 0", len(result.Regressed))
	}
}

func TestRoundTo4(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{0.123456789, 0.1235},
		{1.0, 1.0},
		{0.0, 0.0},
		{0.99999, 1.0},
	}

	for _, tc := range tests {
		got := roundTo4(tc.input)
		if math.Abs(got-tc.want) > 1e-10 {
			t.Errorf("roundTo4(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestFormatDeltaPct(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0.5, " 50%"},
		{1.0, "100%"},
		{0.0, "  0%"},
	}

	for _, tc := range tests {
		got := formatDeltaPct(tc.input)
		if got != tc.want {
			t.Errorf("formatDeltaPct(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestFormatScorePct(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0.74, " 74%"},
		{1.0, "100%"},
		{0.0, "  0%"},
	}

	for _, tc := range tests {
		got := formatScorePct(tc.input)
		if got != tc.want {
			t.Errorf("formatScorePct(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
