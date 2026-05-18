package jest

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
)

const golden = `{
  "startTime": 1747555200000,
  "success": false,
  "numPassedTests": 1,
  "numFailedTests": 1,
  "numPendingTests": 1,
  "testResults": [
    {
      "name": "apps/mobile/src/screens/__tests__/home.test.tsx",
      "startTime": 1747555200000,
      "endTime":   1747555200200,
      "assertionResults": [
        {
          "title": "renders greeting @atlas:feature home.greeting",
          "fullName": "Home renders greeting @atlas:feature home.greeting",
          "ancestorTitles": ["Home"],
          "status": "passed",
          "duration": 12
        },
        {
          "title": "shows error banner",
          "fullName": "Home shows error banner",
          "ancestorTitles": ["Home"],
          "status": "failed",
          "duration": 18,
          "failureMessages": ["banner not found"]
        },
        {
          "title": "logs analytics",
          "fullName": "Home logs analytics",
          "ancestorTitles": ["Home"],
          "status": "skipped",
          "duration": 0
        }
      ]
    }
  ]
}`

func TestParse_Golden(t *testing.T) {
	run, results, err := Parse(strings.NewReader(golden))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if run.Framework != coverage.FrameworkJest {
		t.Errorf("Framework = %q", run.Framework)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}

	byName := map[string]coverage.Result{}
	for _, r := range results {
		byName[r.TestName] = r
	}

	greeting := byName["Home renders greeting @atlas:feature home.greeting"]
	if greeting.FeatureID == nil || *greeting.FeatureID != "home.greeting" {
		t.Errorf("greeting feature = %v", greeting.FeatureID)
	}
	if greeting.Status != coverage.StatusPass {
		t.Errorf("greeting status = %q", greeting.Status)
	}

	err1 := byName["Home shows error banner"]
	if err1.Status != coverage.StatusFail {
		t.Errorf("error banner status = %q", err1.Status)
	}
	if err1.Message != "banner not found" {
		t.Errorf("error banner message = %q", err1.Message)
	}

	skip := byName["Home logs analytics"]
	if skip.Status != coverage.StatusSkip {
		t.Errorf("logs status = %q", skip.Status)
	}
}

func TestParse_Malformed(t *testing.T) {
	_, _, err := Parse(strings.NewReader(`{garbage`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_EmptyDoc(t *testing.T) {
	_, results, err := Parse(strings.NewReader(`{"testResults":[]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}
