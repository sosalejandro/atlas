// @testreg metrics.playwright-parser
package adapters

import (
	"testing"
	"time"
)

func TestParsePlaywrightMetrics_BasicPassFail(t *testing.T) {
	input := `{
  "suites": [
    {
      "title": "Auth Tests",
      "file": "e2e/auth.spec.ts",
      "suites": [],
      "specs": [
        {
          "title": "should login",
          "tests": [
            {
              "status": "expected",
              "duration": 1500,
              "results": [
                {"status": "passed", "duration": 1500, "retry": 0}
              ]
            }
          ]
        },
        {
          "title": "should fail on bad password",
          "tests": [
            {
              "status": "unexpected",
              "duration": 3000,
              "results": [
                {"status": "failed", "duration": 3000, "retry": 0, "error": {"message": "timeout"}}
              ]
            }
          ]
        }
      ]
    }
  ]
}`

	run, err := ParsePlaywrightMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParsePlaywrightMetrics() error = %v", err)
	}

	if run.Framework != "playwright" {
		t.Errorf("Framework = %q, want 'playwright'", run.Framework)
	}

	if run.TotalTests != 2 {
		t.Errorf("TotalTests = %d, want 2", run.TotalTests)
	}

	if run.Passed != 1 {
		t.Errorf("Passed = %d, want 1", run.Passed)
	}

	if run.Failed != 1 {
		t.Errorf("Failed = %d, want 1", run.Failed)
	}

	// Verify per-test metrics.
	if len(run.TestMetrics) != 2 {
		t.Fatalf("TestMetrics length = %d, want 2", len(run.TestMetrics))
	}

	login := run.TestMetrics[0]
	if login.Status != "pass" {
		t.Errorf("TestMetrics[0].Status = %q, want 'pass'", login.Status)
	}
	if login.Duration != 1500*time.Millisecond {
		t.Errorf("TestMetrics[0].Duration = %v, want 1500ms", login.Duration)
	}
}

func TestParsePlaywrightMetrics_FlakyWithRetries(t *testing.T) {
	input := `{
  "suites": [
    {
      "title": "Flaky Suite",
      "file": "e2e/flaky.spec.ts",
      "suites": [],
      "specs": [
        {
          "title": "eventually passes",
          "tests": [
            {
              "status": "expected",
              "duration": 5000,
              "results": [
                {"status": "failed", "duration": 2000, "retry": 0},
                {"status": "failed", "duration": 2000, "retry": 1},
                {"status": "passed", "duration": 1000, "retry": 2}
              ]
            }
          ]
        }
      ]
    }
  ]
}`

	run, err := ParsePlaywrightMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParsePlaywrightMetrics() error = %v", err)
	}

	if run.Flaky != 1 {
		t.Errorf("Flaky = %d, want 1", run.Flaky)
	}

	if run.Passed != 1 {
		t.Errorf("Passed = %d, want 1 (flaky tests that pass still count)", run.Passed)
	}

	if len(run.TestMetrics) != 1 {
		t.Fatalf("TestMetrics length = %d, want 1", len(run.TestMetrics))
	}

	tm := run.TestMetrics[0]
	if tm.Status != "flaky" {
		t.Errorf("Status = %q, want 'flaky'", tm.Status)
	}
	if tm.Retries != 2 {
		t.Errorf("Retries = %d, want 2", tm.Retries)
	}
}

func TestParsePlaywrightMetrics_NestedSuites(t *testing.T) {
	input := `{
  "suites": [
    {
      "title": "Parent",
      "file": "e2e/parent.spec.ts",
      "specs": [],
      "suites": [
        {
          "title": "Child",
          "file": "",
          "suites": [],
          "specs": [
            {
              "title": "nested test",
              "tests": [
                {
                  "status": "expected",
                  "duration": 500,
                  "results": [{"status": "passed", "duration": 500}]
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}`

	run, err := ParsePlaywrightMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParsePlaywrightMetrics() error = %v", err)
	}

	if run.TotalTests != 1 {
		t.Errorf("TotalTests = %d, want 1", run.TotalTests)
	}

	// Child suite should inherit parent's file path.
	if run.TestMetrics[0].File != "e2e/parent.spec.ts" {
		t.Errorf("File = %q, want 'e2e/parent.spec.ts' (inherited from parent)", run.TestMetrics[0].File)
	}
}

func TestParsePlaywrightMetrics_EmptySuites(t *testing.T) {
	input := `{"suites": []}`

	run, err := ParsePlaywrightMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParsePlaywrightMetrics() error = %v", err)
	}

	if run.TotalTests != 0 {
		t.Errorf("TotalTests = %d, want 0", run.TotalTests)
	}
}

func TestParsePlaywrightMetrics_InvalidJSON(t *testing.T) {
	_, err := ParsePlaywrightMetrics([]byte("not json"))
	if err == nil {
		t.Fatal("Expected error for invalid JSON")
	}
}
