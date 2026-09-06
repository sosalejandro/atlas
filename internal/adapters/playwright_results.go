package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/internal/ports"
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

// inferFeatureFromPath derives a feature ID from a test file path:
// "e2e/auth.spec.ts" -> "auth", "e2e/meals/log.spec.ts" -> "meals.log".
//
// The path arrives inside a Playwright JSON report, so it is treated as a
// slash-separated string (normalised by slashPath) rather than handed to
// the filepath package. filepath is host-relative and that is precisely
// wrong here: on Linux filepath.Dir leaves a Windows-produced
// `e2e\meals\log.spec.ts` as one opaque segment, and on Windows
// filepath.Dir rewrites a POSIX report's separators to `\` so the split on
// "/" finds nothing. Either way the feature ID would depend on which
// runner wrote the report rather than on the path itself — issue #143.
func inferFeatureFromPath(filePath string) string {
	normalized := slashPath(filePath)

	// Base name without the spec extension.
	base := path.Base(normalized)
	base = strings.TrimSuffix(base, ".spec.ts")
	base = strings.TrimSuffix(base, ".spec.js")

	// Walk the directory segments innermost-outwards and take the first
	// that names a domain rather than a test-layout convention:
	// "e2e/meals/log.spec.ts" is the meals feature, while "e2e/auth.spec.ts"
	// has no domain directory and falls through to the base name alone.
	parts := strings.Split(path.Dir(normalized), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		lower := strings.ToLower(parts[i])
		switch lower {
		// "." and "" are what path.Dir yields for a bare filename and for a
		// leading separator; neither is a directory the caller named.
		case "", ".", "e2e", "tests", "test", "specs":
			continue
		}
		return lower + "." + strings.ToLower(base)
	}

	return strings.ToLower(base)
}
