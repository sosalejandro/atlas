// @testreg metrics.vitest-parser
package adapters

import (
	"testing"
	"time"
)

func TestParseVitestMetrics_BasicPassFail(t *testing.T) {
	input := `{
  "testResults": [
    {
      "name": "src/auth/__tests__/login.test.ts",
      "assertionResults": [
        {"title": "should authenticate user", "status": "passed", "duration": 45},
        {"title": "should reject bad creds", "status": "failed", "duration": 12}
      ]
    },
    {
      "name": "src/meals/__tests__/log.test.ts",
      "assertionResults": [
        {"title": "should log a meal", "status": "passed", "duration": 30}
      ]
    }
  ]
}`

	run, err := ParseVitestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseVitestMetrics() error = %v", err)
	}

	if run.Framework != "vitest" {
		t.Errorf("Framework = %q, want 'vitest'", run.Framework)
	}

	if run.TotalTests != 3 {
		t.Errorf("TotalTests = %d, want 3", run.TotalTests)
	}

	if run.Passed != 2 {
		t.Errorf("Passed = %d, want 2", run.Passed)
	}

	if run.Failed != 1 {
		t.Errorf("Failed = %d, want 1", run.Failed)
	}

	if len(run.TestMetrics) != 3 {
		t.Fatalf("TestMetrics length = %d, want 3", len(run.TestMetrics))
	}

	// Check first test metric.
	tm := run.TestMetrics[0]
	if tm.Name != "should authenticate user" {
		t.Errorf("TestMetrics[0].Name = %q, want 'should authenticate user'", tm.Name)
	}
	if tm.Status != "pass" {
		t.Errorf("TestMetrics[0].Status = %q, want 'pass'", tm.Status)
	}
	if tm.Duration != 45*time.Millisecond {
		t.Errorf("TestMetrics[0].Duration = %v, want 45ms", tm.Duration)
	}
	if tm.File != "src/auth/__tests__/login.test.ts" {
		t.Errorf("TestMetrics[0].File = %q, want 'src/auth/__tests__/login.test.ts'", tm.File)
	}
}

func TestParseVitestMetrics_SkippedTests(t *testing.T) {
	input := `{
  "testResults": [
    {
      "name": "skipped.test.ts",
      "assertionResults": [
        {"title": "skipped test", "status": "skipped", "duration": 0},
        {"title": "pending test", "status": "pending", "duration": 0}
      ]
    }
  ]
}`

	run, err := ParseVitestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseVitestMetrics() error = %v", err)
	}

	if run.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", run.Skipped)
	}

	if run.TotalTests != 2 {
		t.Errorf("TotalTests = %d, want 2", run.TotalTests)
	}
}

func TestParseVitestMetrics_EmptyResults(t *testing.T) {
	input := `{"testResults": []}`

	run, err := ParseVitestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseVitestMetrics() error = %v", err)
	}

	if run.TotalTests != 0 {
		t.Errorf("TotalTests = %d, want 0", run.TotalTests)
	}
}

func TestParseVitestMetrics_InvalidJSON(t *testing.T) {
	_, err := ParseVitestMetrics([]byte("{broken"))
	if err == nil {
		t.Fatal("Expected error for invalid JSON")
	}
}
