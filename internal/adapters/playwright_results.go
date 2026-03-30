package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sosalejandro/testreg/internal/ports"
)

// PlaywrightResultParser parses Playwright JSON reporter output.
type PlaywrightResultParser struct{}

// NewPlaywrightResultParser creates a new PlaywrightResultParser.
func NewPlaywrightResultParser() *PlaywrightResultParser {
	return &PlaywrightResultParser{}
}

// Name returns the parser's display name.
func (p *PlaywrightResultParser) Name() string {
	return "Playwright JSON Parser"
}

// playwrightReport is the top-level structure of Playwright's JSON reporter output.
type playwrightReport struct {
	Suites []playwrightSuite `json:"suites"`
}

type playwrightSuite struct {
	Title  string            `json:"title"`
	File   string            `json:"file"`
	Suites []playwrightSuite `json:"suites"`
	Specs  []playwrightSpec  `json:"specs"`
}

type playwrightSpec struct {
	Title string           `json:"title"`
	Tests []playwrightTest `json:"tests"`
}

type playwrightTest struct {
	Status   string             `json:"status"` // "expected", "unexpected", "skipped"
	Duration float64            `json:"duration"`
	Results  []playwrightResult `json:"results"`
}

type playwrightResult struct {
	Status   string           `json:"status"` // "passed", "failed", "timedOut", "skipped"
	Duration float64          `json:"duration"`
	Error    *playwrightError `json:"error,omitempty"`
}

type playwrightError struct {
	Message string `json:"message"`
}

// Parse reads a Playwright JSON result file or directory and returns test results.
// If resultPath is a directory, it looks for results.json or report.json inside.
func (p *PlaywrightResultParser) Parse(resultPath string) ([]ports.TestResult, error) {
	info, err := os.Stat(resultPath)
	if err != nil {
		return nil, fmt.Errorf("accessing %s: %w", resultPath, err)
	}

	var jsonPath string
	if info.IsDir() {
		// Look for common Playwright output filenames
		for _, name := range []string{"results.json", "report.json", "test-results.json"} {
			candidate := filepath.Join(resultPath, name)
			if _, statErr := os.Stat(candidate); statErr == nil {
				jsonPath = candidate
				break
			}
		}
		if jsonPath == "" {
			return nil, fmt.Errorf("no JSON result file found in %s (expected results.json, report.json, or test-results.json)", resultPath)
		}
	} else {
		jsonPath = resultPath
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", jsonPath, err)
	}

	var report playwrightReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parsing JSON in %s: %w", jsonPath, err)
	}

	var results []ports.TestResult
	for _, suite := range report.Suites {
		results = append(results, extractPlaywrightResults(suite)...)
	}

	return results, nil
}

func extractPlaywrightResults(suite playwrightSuite) []ports.TestResult {
	var results []ports.TestResult

	filePath := suite.File
	if filePath == "" {
		filePath = suite.Title
	}

	for _, spec := range suite.Specs {
		for _, test := range spec.Tests {
			passed := test.Status == "expected"
			var errMsg string
			var dur time.Duration

			if len(test.Results) > 0 {
				last := test.Results[len(test.Results)-1]
				dur = time.Duration(last.Duration) * time.Millisecond
				if last.Error != nil {
					errMsg = last.Error.Message
				}
				passed = last.Status == "passed"
			} else {
				dur = time.Duration(test.Duration) * time.Millisecond
			}

			results = append(results, ports.TestResult{
				FeatureID: inferFeatureFromPath(filePath),
				FilePath:  filePath,
				Passed:    passed,
				Duration:  dur,
				Error:     errMsg,
			})
		}
	}

	// Recurse into nested suites
	for _, child := range suite.Suites {
		if child.File == "" {
			child.File = filePath
		}
		results = append(results, extractPlaywrightResults(child)...)
	}

	return results
}

// inferFeatureFromPath attempts to extract a feature ID from the test file path.
// For example, "e2e/auth.spec.ts" -> "auth", "e2e/meals/log.spec.ts" -> "meals.log"
func inferFeatureFromPath(filePath string) string {
	// Normalize to forward slashes
	normalized := filepath.ToSlash(filePath)

	// Get the base name without extension
	base := filepath.Base(normalized)
	base = strings.TrimSuffix(base, ".spec.ts")
	base = strings.TrimSuffix(base, ".spec.js")

	// Try to include parent directory for domain context
	dir := filepath.Dir(normalized)
	parts := strings.Split(dir, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		lower := strings.ToLower(parts[i])
		if lower != "e2e" && lower != "tests" && lower != "test" && lower != "specs" {
			return lower + "." + strings.ToLower(base)
		}
	}

	return strings.ToLower(base)
}
