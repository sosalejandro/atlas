// @testreg metrics.store
package adapters

import (
	"testing"
	"time"

	"github.com/sosalejandro/atlas/internal/domain"
)

func TestMetricsStoreAppendAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewMetricsStore()

	run := &domain.TestRunMetrics{
		Timestamp:  time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC),
		Framework:  "go",
		TotalTests: 5,
		Passed:     4,
		Failed:     1,
		WallTime:   3 * time.Second,
		TestMetrics: []domain.TestMetric{
			{Name: "TestLogin", File: "auth_test.go", Status: "pass", Duration: 500 * time.Millisecond},
			{Name: "TestRegister", File: "auth_test.go", Status: "fail", Duration: 1 * time.Second},
		},
	}

	if err := store.Append(tmpDir, run); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	history, err := store.LoadHistory(tmpDir)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	if len(history.Runs) != 1 {
		t.Fatalf("Runs count = %d, want 1", len(history.Runs))
	}

	loaded := history.Runs[0]
	if loaded.Framework != "go" {
		t.Errorf("Framework = %q, want 'go'", loaded.Framework)
	}

	if loaded.TotalTests != 5 {
		t.Errorf("TotalTests = %d, want 5", loaded.TotalTests)
	}

	if loaded.Passed != 4 {
		t.Errorf("Passed = %d, want 4", loaded.Passed)
	}
}

func TestMetricsStoreLoadHistoryEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewMetricsStore()

	history, err := store.LoadHistory(tmpDir)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	if len(history.Runs) != 0 {
		t.Errorf("Runs count = %d, want 0 for empty history", len(history.Runs))
	}
}

func TestMetricsStoreFIFOEviction(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewMetricsStore()

	// Append more than MaxHistoryRuns entries.
	for i := 0; i < domain.MaxHistoryRuns+10; i++ {
		run := &domain.TestRunMetrics{
			Timestamp:  time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Framework:  "go",
			TotalTests: 1,
			Passed:     1,
		}
		if err := store.Append(tmpDir, run); err != nil {
			t.Fatalf("Append(%d) error = %v", i, err)
		}
	}

	history, err := store.LoadHistory(tmpDir)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}

	if len(history.Runs) != domain.MaxHistoryRuns {
		t.Errorf("Runs count = %d, want %d (FIFO eviction)", len(history.Runs), domain.MaxHistoryRuns)
	}

	// The oldest entries should have been evicted.
	// The first remaining entry should be index 10 (seconds offset).
	first := history.Runs[0]
	if first.Timestamp.Second() != 10 {
		t.Errorf("First run timestamp second = %d, want 10 (oldest entries evicted)", first.Timestamp.Second())
	}
}

func TestMetricsStoreGetQualitySignals_Slowest(t *testing.T) {
	store := NewMetricsStore()

	history := &domain.MetricsHistory{
		Runs: []domain.TestRunMetrics{
			{
				Framework:  "go",
				TotalTests: 3,
				Passed:     3,
				TestMetrics: []domain.TestMetric{
					{Name: "TestFast", File: "a_test.go", Status: "pass", Duration: 10 * time.Millisecond},
					{Name: "TestSlow", File: "b_test.go", Status: "pass", Duration: 5 * time.Second},
					{Name: "TestMid", File: "c_test.go", Status: "pass", Duration: 1 * time.Second},
				},
			},
		},
	}

	signals := store.GetQualitySignals(history)

	if len(signals.SlowestTests) != 3 {
		t.Fatalf("SlowestTests count = %d, want 3", len(signals.SlowestTests))
	}

	// First should be the slowest.
	if signals.SlowestTests[0].Name != "TestSlow" {
		t.Errorf("SlowestTests[0].Name = %q, want 'TestSlow'", signals.SlowestTests[0].Name)
	}
}

func TestMetricsStoreGetQualitySignals_Flaky(t *testing.T) {
	store := NewMetricsStore()

	history := &domain.MetricsHistory{
		Runs: []domain.TestRunMetrics{
			{
				Framework:  "playwright",
				TotalTests: 2,
				Passed:     2,
				TestMetrics: []domain.TestMetric{
					{Name: "login", File: "auth.spec.ts", Status: "flaky", Retries: 2},
					{Name: "dashboard", File: "dash.spec.ts", Status: "pass"},
				},
			},
		},
	}

	signals := store.GetQualitySignals(history)

	if len(signals.FlakyTests) != 1 {
		t.Fatalf("FlakyTests count = %d, want 1", len(signals.FlakyTests))
	}

	if signals.FlakyTests[0].Name != "login" {
		t.Errorf("FlakyTests[0].Name = %q, want 'login'", signals.FlakyTests[0].Name)
	}
}

func TestMetricsStoreGetQualitySignals_MemoryHogs(t *testing.T) {
	store := NewMetricsStore()

	history := &domain.MetricsHistory{
		Runs: []domain.TestRunMetrics{
			{
				Framework:  "go",
				TotalTests: 2,
				Passed:     2,
				TestMetrics: []domain.TestMetric{
					{Name: "TestBulk", File: "bulk_test.go", Status: "pass", BytesPerOp: 12_000_000, AllocsPerOp: 4000},
					{Name: "TestLight", File: "light_test.go", Status: "pass"},
				},
			},
		},
	}

	signals := store.GetQualitySignals(history)

	if len(signals.MemoryHogs) != 1 {
		t.Fatalf("MemoryHogs count = %d, want 1", len(signals.MemoryHogs))
	}

	if signals.MemoryHogs[0].BytesPerOp != 12_000_000 {
		t.Errorf("MemoryHogs[0].BytesPerOp = %d, want 12000000", signals.MemoryHogs[0].BytesPerOp)
	}
}

func TestMetricsStoreGetQualitySignals_RaceConditions(t *testing.T) {
	store := NewMetricsStore()

	history := &domain.MetricsHistory{
		Runs: []domain.TestRunMetrics{
			{
				Framework:  "go",
				TotalTests: 2,
				Passed:     1,
				Failed:     1,
				TestMetrics: []domain.TestMetric{
					{Name: "TestRacy", File: "race_test.go", Status: "fail", RaceDetected: true},
					{Name: "TestClean", File: "clean_test.go", Status: "pass"},
				},
			},
		},
	}

	signals := store.GetQualitySignals(history)

	if len(signals.RaceConditions) != 1 {
		t.Fatalf("RaceConditions count = %d, want 1", len(signals.RaceConditions))
	}

	if signals.RaceConditions[0].Name != "TestRacy" {
		t.Errorf("RaceConditions[0].Name = %q, want 'TestRacy'", signals.RaceConditions[0].Name)
	}
}

func TestMetricsStoreGetQualitySignals_EmptyHistory(t *testing.T) {
	store := NewMetricsStore()

	signals := store.GetQualitySignals(&domain.MetricsHistory{})

	if len(signals.SlowestTests) != 0 {
		t.Errorf("SlowestTests count = %d, want 0", len(signals.SlowestTests))
	}
	if len(signals.FlakyTests) != 0 {
		t.Errorf("FlakyTests count = %d, want 0", len(signals.FlakyTests))
	}
}

func TestMetricsStoreGetQualitySignals_NilHistory(t *testing.T) {
	store := NewMetricsStore()

	signals := store.GetQualitySignals(nil)

	if signals == nil {
		t.Fatal("Expected non-nil signals for nil history")
	}
}

func TestMetricsStoreGetFeatureHealthTrend(t *testing.T) {
	store := NewMetricsStore()

	history := &domain.MetricsHistory{
		Runs: []domain.TestRunMetrics{
			{
				Timestamp: time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC),
				TestMetrics: []domain.TestMetric{
					{Name: "TestA", FeatureID: "auth.login", Status: "pass", Duration: 100 * time.Millisecond},
					{Name: "TestB", FeatureID: "auth.login", Status: "pass", Duration: 200 * time.Millisecond},
				},
			},
			{
				Timestamp: time.Date(2026, 3, 29, 10, 0, 0, 0, time.UTC),
				TestMetrics: []domain.TestMetric{
					{Name: "TestA", FeatureID: "auth.login", Status: "pass", Duration: 150 * time.Millisecond},
					{Name: "TestB", FeatureID: "auth.login", Status: "fail", Duration: 300 * time.Millisecond},
				},
			},
		},
	}

	trend := store.GetFeatureHealthTrend(history, "auth.login")

	if trend.FeatureID != "auth.login" {
		t.Errorf("FeatureID = %q, want 'auth.login'", trend.FeatureID)
	}

	if len(trend.DataPoints) != 2 {
		t.Fatalf("DataPoints count = %d, want 2", len(trend.DataPoints))
	}

	// First run: 2/2 pass = 100% pass rate.
	if trend.DataPoints[0].PassRate != 1.0 {
		t.Errorf("DataPoints[0].PassRate = %f, want 1.0", trend.DataPoints[0].PassRate)
	}

	// Second run: 1/2 pass = 50% pass rate.
	if trend.DataPoints[1].PassRate != 0.5 {
		t.Errorf("DataPoints[1].PassRate = %f, want 0.5", trend.DataPoints[1].PassRate)
	}
}

func TestMetricsStoreGetFeatureHealthTrend_NoData(t *testing.T) {
	store := NewMetricsStore()

	history := &domain.MetricsHistory{
		Runs: []domain.TestRunMetrics{
			{
				TestMetrics: []domain.TestMetric{
					{Name: "TestA", FeatureID: "meals.log", Status: "pass"},
				},
			},
		},
	}

	trend := store.GetFeatureHealthTrend(history, "auth.login")

	if len(trend.DataPoints) != 0 {
		t.Errorf("DataPoints count = %d, want 0 for non-existent feature", len(trend.DataPoints))
	}
}
