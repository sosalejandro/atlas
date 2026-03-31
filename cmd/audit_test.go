// @testreg audit.priority-filter,audit.sort,audit.summary
package cmd

import (
	"math"
	"testing"

	"github.com/sosalejandro/testreg/internal/domain"
)

func TestPriorityScore_CriticalAt0Health(t *testing.T) {
	o := &domain.AuditOutput{Priority: "critical", HealthScore: 0.0}
	got := priorityScore(o)
	if got != 4.0 {
		t.Errorf("priorityScore(critical, health=0) = %v, want 4.0", got)
	}
}

func TestPriorityScore_CriticalAt50Health(t *testing.T) {
	o := &domain.AuditOutput{Priority: "critical", HealthScore: 0.5}
	got := priorityScore(o)
	if got != 2.0 {
		t.Errorf("priorityScore(critical, health=0.5) = %v, want 2.0", got)
	}
}

func TestPriorityScore_CriticalAt100Health(t *testing.T) {
	o := &domain.AuditOutput{Priority: "critical", HealthScore: 1.0}
	got := priorityScore(o)
	if got != 0.0 {
		t.Errorf("priorityScore(critical, health=1.0) = %v, want 0.0", got)
	}
}

func TestPriorityScore_HighAt0Health(t *testing.T) {
	o := &domain.AuditOutput{Priority: "high", HealthScore: 0.0}
	got := priorityScore(o)
	// weight=3, target=0.8, delta=0.8 → 3*0.8=2.4
	want := 2.4
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("priorityScore(high, health=0) = %v, want %v", got, want)
	}
}

func TestPriorityScore_LowAt40Health(t *testing.T) {
	o := &domain.AuditOutput{Priority: "low", HealthScore: 0.4}
	got := priorityScore(o)
	// weight=1, target=0.4, delta=0 → 0.0
	if got != 0.0 {
		t.Errorf("priorityScore(low, health=0.4) = %v, want 0.0", got)
	}
}

func TestPriorityScore_UnknownPriority(t *testing.T) {
	o := &domain.AuditOutput{Priority: "unknown", HealthScore: 0.0}
	got := priorityScore(o)
	// weight=1 (default), target=0.5 (default), delta=0.5 → 0.5
	if got != 0.5 {
		t.Errorf("priorityScore(unknown, health=0) = %v, want 0.5", got)
	}
}

func TestBuildAuditSummary_MixedInput(t *testing.T) {
	results := []*domain.AuditOutput{
		{Priority: "critical", HealthScore: 1.0},  // at target (1.0)
		{Priority: "critical", HealthScore: 0.5},  // below target
		{Priority: "high", HealthScore: 0.9},       // at target (0.8)
		{Priority: "low", HealthScore: 0.1, Gaps: []domain.AuditGap{{}, {}}}, // below target, 2 gaps
	}

	summary := buildAuditSummary(results)

	tiers := summary["tiers"].([]auditSummaryTier)
	total := summary["total"].(int)
	atTarget := summary["at_target"].(int)

	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
	// at target: critical@1.0 yes, critical@0.5 no, high@0.9 yes, low@0.1 no → 2
	if atTarget != 2 {
		t.Errorf("at_target = %d, want 2", atTarget)
	}

	// Check critical tier: 1 at target out of 2
	criticalTier := tiers[0]
	if criticalTier.Priority != "critical" {
		t.Errorf("first tier priority = %q, want critical", criticalTier.Priority)
	}
	if criticalTier.Total != 2 {
		t.Errorf("critical total = %d, want 2", criticalTier.Total)
	}
	if criticalTier.AtTarget != 1 {
		t.Errorf("critical at_target = %d, want 1", criticalTier.AtTarget)
	}

	// Check low tier gap count
	lowTier := tiers[3]
	if lowTier.Priority != "low" {
		t.Errorf("fourth tier priority = %q, want low", lowTier.Priority)
	}
	if lowTier.GapCount != 2 {
		t.Errorf("low gap_count = %d, want 2", lowTier.GapCount)
	}
}

func TestBuildAuditSummary_EmptyInput(t *testing.T) {
	summary := buildAuditSummary(nil)

	total := summary["total"].(int)
	atTarget := summary["at_target"].(int)
	overallPct := summary["overall_pct"].(float64)

	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if atTarget != 0 {
		t.Errorf("at_target = %d, want 0", atTarget)
	}
	if overallPct != 0 {
		t.Errorf("overall_pct = %v, want 0", overallPct)
	}
}

func TestBuildAuditSummary_AllAtTarget(t *testing.T) {
	results := []*domain.AuditOutput{
		{Priority: "critical", HealthScore: 1.0},
		{Priority: "high", HealthScore: 0.8},
		{Priority: "medium", HealthScore: 0.6},
		{Priority: "low", HealthScore: 0.4},
	}

	summary := buildAuditSummary(results)
	overallPct := summary["overall_pct"].(float64)

	if overallPct != 100.0 {
		t.Errorf("overall_pct = %v, want 100.0", overallPct)
	}
}

func TestMakeBar_100Percent(t *testing.T) {
	bar := makeBar(100, 10)
	expected := "\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u2588"
	if bar != expected {
		t.Errorf("makeBar(100, 10) = %q, want all filled blocks", bar)
	}
}

func TestMakeBar_0Percent(t *testing.T) {
	bar := makeBar(0, 10)
	expected := "\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591"
	if bar != expected {
		t.Errorf("makeBar(0, 10) = %q, want all empty blocks", bar)
	}
}

func TestMakeBar_50Percent(t *testing.T) {
	bar := makeBar(50, 10)
	expected := "\u2588\u2588\u2588\u2588\u2588\u2591\u2591\u2591\u2591\u2591"
	if bar != expected {
		t.Errorf("makeBar(50, 10) = %q, want half filled half empty", bar)
	}
}
