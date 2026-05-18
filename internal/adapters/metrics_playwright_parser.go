package adapters

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/sosalejandro/atlas/internal/domain"
)

// playwrightMetricsReport mirrors the top-level Playwright JSON reporter output
// for metrics extraction purposes.
type playwrightMetricsReport struct {
	Suites []playwrightMetricsSuite `json:"suites"`
}

type playwrightMetricsSuite struct {
	Title  string                   `json:"title"`
	File   string                   `json:"file"`
	Suites []playwrightMetricsSuite `json:"suites"`
	Specs  []playwrightMetricsSpec  `json:"specs"`
}

type playwrightMetricsSpec struct {
	Title string                   `json:"title"`
	Tests []playwrightMetricsTest  `json:"tests"`
}

type playwrightMetricsTest struct {
	Status   string                     `json:"status"`
	Duration float64                    `json:"duration"`
	Results  []playwrightMetricsResult  `json:"results"`
}

type playwrightMetricsResult struct {
	Status   string                   `json:"status"`
	Duration float64                  `json:"duration"`
	Retry    int                      `json:"retry"`
	Error    *playwrightMetricsError  `json:"error,omitempty"`
}

type playwrightMetricsError struct {
	Message string `json:"message"`
}

// ParsePlaywrightMetrics reads Playwright JSON reporter output and extracts
// per-test metrics including timing, retries, and pass/fail status.
func ParsePlaywrightMetrics(jsonOutput []byte) (*domain.TestRunMetrics, error) {
	var report playwrightMetricsReport
	if err := json.Unmarshal(jsonOutput, &report); err != nil {
		return nil, fmt.Errorf("parsing playwright JSON: %w", err)
	}

	run := &domain.TestRunMetrics{
		Timestamp: time.Now(),
		Framework: "playwright",
	}

	for _, suite := range report.Suites {
		extractPlaywrightMetricsSuite(suite, run)
	}

	return run, nil
}

func extractPlaywrightMetricsSuite(suite playwrightMetricsSuite, run *domain.TestRunMetrics) {
	filePath := suite.File
	if filePath == "" {
		filePath = suite.Title
	}

	for _, spec := range suite.Specs {
		for _, test := range spec.Tests {
			tm := domain.TestMetric{
				Name:      spec.Title,
				File:      filePath,
				FeatureID: inferFeatureFromPath(filePath),
			}

			var totalRetries int
			var finalStatus string
			var finalDuration float64

			if len(test.Results) > 0 {
				last := test.Results[len(test.Results)-1]
				finalStatus = last.Status
				finalDuration = last.Duration
				// Count retries: any result beyond the first is a retry.
				totalRetries = len(test.Results) - 1
			} else {
				finalDuration = test.Duration
				if test.Status == "expected" {
					finalStatus = "passed"
				} else {
					finalStatus = "failed"
				}
			}

			tm.Duration = time.Duration(finalDuration) * time.Millisecond
			tm.Retries = totalRetries

			switch finalStatus {
			case "passed":
				if totalRetries > 0 {
					tm.Status = "flaky"
					run.Flaky++
					run.Passed++ // still counts as passed
				} else {
					tm.Status = "pass"
					run.Passed++
				}
			case "failed", "timedOut":
				tm.Status = "fail"
				run.Failed++
			case "skipped":
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

	for _, child := range suite.Suites {
		if child.File == "" {
			child.File = filePath
		}
		extractPlaywrightMetricsSuite(child, run)
	}
}
