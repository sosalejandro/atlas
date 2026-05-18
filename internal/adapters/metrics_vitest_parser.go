package adapters

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/internal/domain"
)

// vitestReport mirrors the top-level structure of Vitest's --reporter=json output.
type vitestReport struct {
	TestResults []vitestTestResult `json:"testResults"`
}

type vitestTestResult struct {
	Name             string                  `json:"name"`
	AssertionResults []vitestAssertionResult `json:"assertionResults"`
}

type vitestAssertionResult struct {
	Title    string  `json:"title"`
	FullName string  `json:"fullName"`
	Status   string  `json:"status"`   // "passed", "failed", "skipped", "pending"
	Duration float64 `json:"duration"` // milliseconds
}

// ParseVitestMetrics reads Vitest --reporter=json output and extracts per-test
// metrics including timing and pass/fail status.
func ParseVitestMetrics(jsonOutput []byte) (*domain.TestRunMetrics, error) {
	var report vitestReport
	if err := json.Unmarshal(jsonOutput, &report); err != nil {
		return nil, fmt.Errorf("parsing vitest JSON: %w", err)
	}

	run := &domain.TestRunMetrics{
		Timestamp: time.Now(),
		Framework: "vitest",
	}

	for _, tr := range report.TestResults {
		filePath := tr.Name
		for _, ar := range tr.AssertionResults {
			tm := domain.TestMetric{
				Name:      ar.Title,
				File:      filePath,
				FeatureID: inferFeatureFromPath(filePath),
				Duration:  time.Duration(ar.Duration) * time.Millisecond,
			}

			switch ar.Status {
			case "passed":
				tm.Status = "pass"
				run.Passed++
			case "failed":
				tm.Status = "fail"
				run.Failed++
			case "skipped", "pending":
				tm.Status = "skip"
				run.Skipped++
			default:
				tm.Status = "fail"
				run.Failed++
			}

			run.TotalTests++
			run.WallTime += tm.Duration
			run.TestMetrics = append(run.TestMetrics, tm)
		}
	}

	return run, nil
}
