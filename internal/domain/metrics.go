package domain

import "time"

// TestRunMetrics captures performance data from a single test run.
type TestRunMetrics struct {
	Timestamp   time.Time    `yaml:"timestamp" json:"timestamp"`
	Framework   string       `yaml:"framework" json:"framework"`          // go, playwright, vitest, jest, maestro
	TotalTests  int          `yaml:"total_tests" json:"total_tests"`
	Passed      int          `yaml:"passed" json:"passed"`
	Failed      int          `yaml:"failed" json:"failed"`
	Skipped     int          `yaml:"skipped" json:"skipped"`
	Flaky       int          `yaml:"flaky" json:"flaky"`                  // tests that passed on retry
	WallTime    time.Duration `yaml:"wall_time" json:"wall_time"`         // total execution time
	TestMetrics []TestMetric `yaml:"tests,omitempty" json:"tests,omitempty"`
}

// TestMetric captures metrics for a single test function.
type TestMetric struct {
	Name      string        `yaml:"name" json:"name"`
	File      string        `yaml:"file" json:"file"`
	FeatureID string        `yaml:"feature_id,omitempty" json:"feature_id,omitempty"`
	Status    string        `yaml:"status" json:"status"` // pass, fail, skip, flaky
	Duration  time.Duration `yaml:"duration" json:"duration"`
	Retries   int           `yaml:"retries,omitempty" json:"retries,omitempty"`

	// Go-specific metrics (from -benchmem, -race)
	AllocsPerOp  int64 `yaml:"allocs_per_op,omitempty" json:"allocs_per_op,omitempty"`
	BytesPerOp   int64 `yaml:"bytes_per_op,omitempty" json:"bytes_per_op,omitempty"`
	RaceDetected bool  `yaml:"race_detected,omitempty" json:"race_detected,omitempty"`

	// Coverage (all frameworks)
	CoveragePercent float64 `yaml:"coverage_pct,omitempty" json:"coverage_pct,omitempty"`
}

// MetricsHistory stores historical test run data for trend analysis.
type MetricsHistory struct {
	Runs []TestRunMetrics `yaml:"runs" json:"runs"`
}

// FeatureHealthTrend tracks how a feature's health changes over time.
type FeatureHealthTrend struct {
	FeatureID  string            `yaml:"feature_id" json:"feature_id"`
	DataPoints []HealthDataPoint `yaml:"data_points" json:"data_points"`
}

// HealthDataPoint is a single snapshot of a feature's health at a point in time.
type HealthDataPoint struct {
	Timestamp   time.Time     `yaml:"timestamp" json:"timestamp"`
	HealthScore float64       `yaml:"health_score" json:"health_score"`
	GapCount    int           `yaml:"gap_count" json:"gap_count"`
	PassRate    float64       `yaml:"pass_rate" json:"pass_rate"`
	AvgDuration time.Duration `yaml:"avg_duration" json:"avg_duration"`
}

// QualitySignals aggregated from metrics history.
type QualitySignals struct {
	SlowestTests   []TestMetric `json:"slowest_tests"`   // top 10 by duration
	FlakyTests     []TestMetric `json:"flaky_tests"`     // tests with retries > 0
	MemoryHogs     []TestMetric `json:"memory_hogs"`     // top 10 by bytes_per_op (Go only)
	RaceConditions []TestMetric `json:"race_conditions"` // tests with race_detected=true
	FailingTrends  []string     `json:"failing_trends"`  // features whose health is declining
}

// MaxHistoryRuns is the maximum number of runs kept in the history file (FIFO eviction).
const MaxHistoryRuns = 100
