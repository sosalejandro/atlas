package ports

import "time"

// TestResult represents the outcome of a single test execution.
type TestResult struct {
	FeatureID string        // mapped feature ID, empty if unmapped
	FilePath  string        // path to the test file
	Passed    bool          // whether the test passed
	Duration  time.Duration // how long the test took
	Error     string        // error message if failed
}

// ResultParser reads test result output and produces structured test results.
type ResultParser interface {
	// Name returns the human-readable name of this parser (e.g., "Playwright JSON Parser").
	Name() string

	// Parse reads the result file at resultPath and returns structured test results.
	Parse(resultPath string) ([]TestResult, error)
}
