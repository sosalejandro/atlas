package adapters

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sosalejandro/testreg/internal/domain"
	"gopkg.in/yaml.v3"
)

const metricsHistoryFile = "history.yaml"
const metricsSubdir = "docs/testing/metrics"

// MetricsStore persists and queries historical test metrics.
type MetricsStore struct{}

// NewMetricsStore creates a new MetricsStore.
func NewMetricsStore() *MetricsStore {
	return &MetricsStore{}
}

// metricsDir returns the metrics storage directory under the given project root.
func metricsDir(projectRoot string) string {
	return filepath.Join(projectRoot, metricsSubdir)
}

// historyPath returns the full path to the history YAML file.
func historyPath(projectRoot string) string {
	return filepath.Join(metricsDir(projectRoot), metricsHistoryFile)
}

// Append adds a new test run to the history file.
// Keeps at most MaxHistoryRuns entries (FIFO eviction).
func (s *MetricsStore) Append(projectRoot string, run *domain.TestRunMetrics) error {
	history, err := s.LoadHistory(projectRoot)
	if err != nil {
		return fmt.Errorf("loading history before append: %w", err)
	}

	history.Runs = append(history.Runs, *run)

	// FIFO eviction: keep only the latest MaxHistoryRuns entries.
	if len(history.Runs) > domain.MaxHistoryRuns {
		history.Runs = history.Runs[len(history.Runs)-domain.MaxHistoryRuns:]
	}

	return s.saveHistory(projectRoot, history)
}

// LoadHistory reads the full metrics history from disk.
// Returns an empty history (not an error) if the file does not exist.
func (s *MetricsStore) LoadHistory(projectRoot string) (*domain.MetricsHistory, error) {
	p := historyPath(projectRoot)

	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &domain.MetricsHistory{}, nil
		}
		return nil, fmt.Errorf("reading metrics history %s: %w", p, err)
	}

	var history domain.MetricsHistory
	if err := yaml.Unmarshal(data, &history); err != nil {
		return nil, fmt.Errorf("parsing metrics history %s: %w", p, err)
	}

	return &history, nil
}

// GetQualitySignals analyzes the history and returns aggregated signals.
func (s *MetricsStore) GetQualitySignals(history *domain.MetricsHistory) *domain.QualitySignals {
	if history == nil || len(history.Runs) == 0 {
		return &domain.QualitySignals{}
	}

	signals := &domain.QualitySignals{}

	// Collect all test metrics from history (latest first).
	var allMetrics []domain.TestMetric
	// Track per-feature pass rates across recent runs for trend analysis.
	type featureStats struct {
		passRates []float64 // ordered chronologically
	}
	featureMap := make(map[string]*featureStats)

	for i := len(history.Runs) - 1; i >= 0; i-- {
		run := history.Runs[i]
		for _, tm := range run.TestMetrics {
			allMetrics = append(allMetrics, tm)

			if tm.FeatureID != "" {
				fs, ok := featureMap[tm.FeatureID]
				if !ok {
					fs = &featureStats{}
					featureMap[tm.FeatureID] = fs
				}
				rate := 0.0
				if tm.Status == "pass" || tm.Status == "flaky" {
					rate = 1.0
				}
				fs.passRates = append(fs.passRates, rate)
			}
		}
	}

	// Slowest tests: top 10 by duration across all runs.
	signals.SlowestTests = topNByDuration(allMetrics, 10)

	// Flaky tests: any test that had retries > 0 in any run.
	seen := make(map[string]bool)
	for _, tm := range allMetrics {
		if tm.Retries > 0 || tm.Status == "flaky" {
			key := tm.File + "::" + tm.Name
			if !seen[key] {
				seen[key] = true
				signals.FlakyTests = append(signals.FlakyTests, tm)
			}
		}
	}

	// Memory hogs: top 10 by bytes_per_op (Go-specific).
	signals.MemoryHogs = topNByMemory(allMetrics, 10)

	// Race conditions: any test with race_detected=true.
	raceSeen := make(map[string]bool)
	for _, tm := range allMetrics {
		if tm.RaceDetected {
			key := tm.File + "::" + tm.Name
			if !raceSeen[key] {
				raceSeen[key] = true
				signals.RaceConditions = append(signals.RaceConditions, tm)
			}
		}
	}

	// Failing trends: features whose average pass rate is declining over the
	// last 3+ data points.
	for fid, fs := range featureMap {
		if len(fs.passRates) < 3 {
			continue
		}
		// Compare first half average to second half average.
		mid := len(fs.passRates) / 2
		firstHalf := avgFloat(fs.passRates[:mid])
		secondHalf := avgFloat(fs.passRates[mid:])
		if secondHalf < firstHalf-0.05 { // at least 5% decline
			signals.FailingTrends = append(signals.FailingTrends, fid)
		}
	}

	return signals
}

// GetFeatureHealthTrend builds a health trend for a specific feature across all runs.
func (s *MetricsStore) GetFeatureHealthTrend(history *domain.MetricsHistory, featureID string) *domain.FeatureHealthTrend {
	trend := &domain.FeatureHealthTrend{FeatureID: featureID}

	for _, run := range history.Runs {
		var passed, total int
		var totalDur time.Duration
		for _, tm := range run.TestMetrics {
			if tm.FeatureID != featureID {
				continue
			}
			total++
			totalDur += tm.Duration
			if tm.Status == "pass" || tm.Status == "flaky" {
				passed++
			}
		}

		if total == 0 {
			continue
		}

		passRate := float64(passed) / float64(total)
		avgDur := totalDur / time.Duration(total)

		// Health score: weighted combination of pass rate and speed.
		// Pass rate is the dominant factor (80%), speed inversely weighted (20%).
		healthScore := passRate * 0.8
		// Speed factor: tests under 1s get full marks, linearly decreasing to 0 at 30s.
		speedFactor := 1.0 - float64(avgDur)/float64(30*time.Second)
		if speedFactor < 0 {
			speedFactor = 0
		}
		if speedFactor > 1 {
			speedFactor = 1
		}
		healthScore += speedFactor * 0.2

		trend.DataPoints = append(trend.DataPoints, domain.HealthDataPoint{
			Timestamp:   run.Timestamp,
			HealthScore: healthScore,
			PassRate:    passRate,
			AvgDuration: avgDur,
		})
	}

	return trend
}

// saveHistory writes the history to disk, creating directories as needed.
func (s *MetricsStore) saveHistory(projectRoot string, history *domain.MetricsHistory) error {
	dir := metricsDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating metrics directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(history)
	if err != nil {
		return fmt.Errorf("marshaling metrics history: %w", err)
	}

	p := historyPath(projectRoot)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return fmt.Errorf("writing metrics history %s: %w", p, err)
	}

	return nil
}

// topNByDuration returns the N slowest test metrics, sorted descending by duration.
func topNByDuration(metrics []domain.TestMetric, n int) []domain.TestMetric {
	if len(metrics) == 0 {
		return nil
	}

	// Deduplicate by name+file, keeping the longest duration seen.
	type dedupKey struct{ name, file string }
	best := make(map[dedupKey]domain.TestMetric)
	for _, tm := range metrics {
		k := dedupKey{tm.Name, tm.File}
		if existing, ok := best[k]; !ok || tm.Duration > existing.Duration {
			best[k] = tm
		}
	}

	sorted := make([]domain.TestMetric, 0, len(best))
	for _, tm := range best {
		sorted = append(sorted, tm)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Duration > sorted[j].Duration
	})

	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// topNByMemory returns the N most memory-intensive test metrics (Go-specific).
func topNByMemory(metrics []domain.TestMetric, n int) []domain.TestMetric {
	var withMem []domain.TestMetric
	for _, tm := range metrics {
		if tm.BytesPerOp > 0 {
			withMem = append(withMem, tm)
		}
	}

	if len(withMem) == 0 {
		return nil
	}

	sort.Slice(withMem, func(i, j int) bool {
		return withMem[i].BytesPerOp > withMem[j].BytesPerOp
	})

	if len(withMem) > n {
		withMem = withMem[:n]
	}
	return withMem
}

// avgFloat computes the arithmetic mean of a float64 slice.
func avgFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}
