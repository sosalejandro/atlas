// @testreg metrics.renderer
package adapters

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/testreg/internal/domain"
)

// --- fixtures ---

func newTestQualitySignals() *domain.QualitySignals {
	return &domain.QualitySignals{
		SlowestTests: []domain.TestMetric{
			{Name: "TestSlowQuery", File: "repo/user_test.go", Duration: 3 * time.Second},
			{Name: "TestHeavyProcess", File: "svc/process_test.go", Duration: 1500 * time.Millisecond},
		},
		FlakyTests: []domain.TestMetric{
			{Name: "TestRaceCondition", File: "svc/concurrent_test.go", Retries: 2},
		},
		MemoryHogs: []domain.TestMetric{
			{Name: "BenchmarkAlloc", File: "svc/alloc_test.go", BytesPerOp: 2 * 1024 * 1024, AllocsPerOp: 500},
		},
		RaceConditions: []domain.TestMetric{
			{Name: "TestConcurrentMap", File: "svc/map_test.go", RaceDetected: true},
		},
		FailingTrends: []string{"auth.login", "billing.checkout"},
	}
}

func newTestQualitySignalsClean() *domain.QualitySignals {
	return &domain.QualitySignals{}
}

// --- RenderQualitySignals ---

func TestMetricsRendererRenderQualitySignals(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	signals := newTestQualitySignals()
	r.RenderQualitySignals(signals)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"header", "Quality Signals"},
		{"slowest heading", "Slowest Tests"},
		{"slow test name", "TestSlowQuery"},
		{"slow test file", "repo/user_test.go"},
		{"slow test duration", "3.00s"},
		{"flaky heading", "Flaky Tests"},
		{"flaky test name", "TestRaceCondition"},
		{"flaky retries", "2 retries"},
		{"memory heading", "Memory Intensive"},
		{"memory test name", "BenchmarkAlloc"},
		{"memory bytes", "MB/op"},
		{"race heading", "Race Conditions"},
		{"race test name", "TestConcurrentMap"},
		{"trend heading", "Health Trends"},
		{"trend feature", "auth.login"},
		{"trend feature 2", "billing.checkout"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderQualitySignals missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestMetricsRendererRenderQualitySignalsClean(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	signals := newTestQualitySignalsClean()
	r.RenderQualitySignals(signals)

	got := buf.String()
	if !strings.Contains(got, "No quality signals detected") {
		t.Error("clean signals should show 'No quality signals detected'")
	}
}

// --- RenderFeatureHealth ---

func TestMetricsRendererRenderFeatureHealth(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	trend := &domain.FeatureHealthTrend{
		FeatureID: "auth.login",
		DataPoints: []domain.HealthDataPoint{
			{
				Timestamp:   time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC),
				HealthScore: 0.85,
				PassRate:    0.95,
				AvgDuration: 150 * time.Millisecond,
			},
			{
				Timestamp:   time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC),
				HealthScore: 0.74,
				PassRate:    0.90,
				AvgDuration: 200 * time.Millisecond,
			},
		},
	}

	r.RenderFeatureHealth(trend)

	got := buf.String()

	checks := []struct {
		label    string
		contains string
	}{
		{"feature ID", "auth.login"},
		{"heading", "Health Trend"},
		{"column header Timestamp", "Timestamp"},
		{"column header Health", "Health"},
		{"column header Pass Rate", "Pass Rate"},
		{"health score 85%", "85%"},
		{"health score 74%", "74%"},
		{"pass rate 95%", "95%"},
		{"date", "2026-03-30"},
	}

	for _, c := range checks {
		if !strings.Contains(got, c.contains) {
			t.Errorf("RenderFeatureHealth missing %s: expected to contain %q", c.label, c.contains)
		}
	}
}

func TestMetricsRendererRenderFeatureHealthEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	trend := &domain.FeatureHealthTrend{
		FeatureID: "empty.feature",
	}

	r.RenderFeatureHealth(trend)

	got := buf.String()
	if !strings.Contains(got, "No data points available") {
		t.Error("empty trend should show 'No data points available'")
	}
}

// --- RenderSlowestTests ---

func TestMetricsRendererRenderSlowestTests(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	tests := []domain.TestMetric{
		{Name: "TestFast", File: "fast_test.go", Duration: 10 * time.Millisecond},
		{Name: "TestSlow", File: "slow_test.go", Duration: 5 * time.Second},
		{Name: "TestMedium", File: "medium_test.go", Duration: 500 * time.Millisecond},
	}

	r.RenderSlowestTests(tests, 1*time.Second)

	got := buf.String()

	if !strings.Contains(got, "Slow Tests") {
		t.Error("missing slow tests heading")
	}
	if !strings.Contains(got, "TestSlow") {
		t.Error("should include TestSlow (5s > 1s threshold)")
	}
	if strings.Contains(got, "TestFast") {
		t.Error("should NOT include TestFast (10ms < 1s threshold)")
	}
	if strings.Contains(got, "TestMedium") {
		t.Error("should NOT include TestMedium (500ms < 1s threshold)")
	}
}

func TestMetricsRendererRenderSlowestTestsNoneAboveThreshold(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	tests := []domain.TestMetric{
		{Name: "TestQuick", File: "quick_test.go", Duration: 10 * time.Millisecond},
	}

	r.RenderSlowestTests(tests, 1*time.Second)

	got := buf.String()
	if !strings.Contains(got, "No tests exceed the threshold") {
		t.Error("expected 'No tests exceed the threshold' message")
	}
}

// --- RenderJSON ---

func TestMetricsRendererRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	signals := newTestQualitySignals()
	if err := r.RenderJSON(signals); err != nil {
		t.Fatalf("RenderJSON returned error: %v", err)
	}

	raw := buf.Bytes()
	if !json.Valid(raw) {
		t.Error("output is not valid JSON")
	}

	got := buf.String()
	if !strings.Contains(got, "TestSlowQuery") {
		t.Error("JSON output missing slowest test name")
	}
	if !strings.Contains(got, "TestRaceCondition") {
		t.Error("JSON output missing flaky test name")
	}
	if !strings.Contains(got, "auth.login") {
		t.Error("JSON output missing failing trend feature")
	}
}

// --- Helper: formatDurationHuman ---

func TestFormatDurationHuman(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  string
	}{
		{500 * time.Nanosecond, "500ns"},
		{50 * time.Millisecond, "50ms"},
		{2500 * time.Millisecond, "2.50s"},
		{90 * time.Second, "1.5m"},
	}

	for _, tt := range tests {
		got := formatDurationHuman(tt.input)
		if got != tt.want {
			t.Errorf("formatDurationHuman(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Helper: formatBytes ---

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{512, "512 B/op"},
		{2048, "2.0 KB/op"},
		{2 * 1024 * 1024, "2.0 MB/op"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.input)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Helper: truncate ---

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is..."},
		{"exact len!", 10, "exact len!"},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

// --- Helper: formatInt ---

func TestFormatInt(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
	}

	for _, tt := range tests {
		got := formatInt(tt.input)
		if got != tt.want {
			t.Errorf("formatInt(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- NewMetricsRendererTo ---

func TestNewMetricsRendererTo(t *testing.T) {
	var buf bytes.Buffer
	r := NewMetricsRendererTo(&buf, false)

	if r == nil {
		t.Fatal("expected non-nil renderer")
	}
	if r.color {
		t.Error("expected color=false")
	}
}

// --- Icon helpers (no color) ---

func TestMetricsRendererIconsNoColor(t *testing.T) {
	r := &MetricsRenderer{color: false}

	if got := r.warningIcon(); got != "[!]" {
		t.Errorf("warningIcon() = %q, want [!]", got)
	}
	if got := r.failIcon(); got != "[X]" {
		t.Errorf("failIcon() = %q, want [X]", got)
	}
	if got := r.trendDownIcon(); got != "[v]" {
		t.Errorf("trendDownIcon() = %q, want [v]", got)
	}
}
