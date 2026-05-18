package vitest

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
)

const golden = `{
  "startTime": 1747555200000,
  "numPassedTests": 2,
  "numFailedTests": 1,
  "numPendingTests": 1,
  "success": false,
  "testResults": [
    {
      "name": "src/features/auth/login.test.ts",
      "startTime": 1747555200000,
      "endTime":   1747555201234,
      "assertionResults": [
        {
          "title": "rejects empty password",
          "fullName": "auth login rejects empty password",
          "ancestorTitles": ["auth", "login"],
          "status": "passed",
          "duration": 12
        },
        {
          "title": "@atlas:feature auth.login succeeds",
          "fullName": "@atlas:feature auth.login succeeds",
          "ancestorTitles": [],
          "status": "passed",
          "duration": 8
        }
      ]
    },
    {
      "name": "src/features/billing/upgrade.test.ts",
      "startTime": 1747555200500,
      "endTime":   1747555201000,
      "assertionResults": [
        {
          "title": "applies discount",
          "fullName": "billing applies discount",
          "ancestorTitles": ["billing"],
          "status": "failed",
          "duration": 35,
          "failureMessages": ["expected $9 got $10"]
        },
        {
          "title": "todo: refund flow",
          "fullName": "billing todo: refund flow",
          "ancestorTitles": ["billing"],
          "status": "todo",
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
	if run.Framework != coverage.FrameworkVitest {
		t.Errorf("Framework = %q", run.Framework)
	}
	if len(results) != 4 {
		t.Fatalf("len = %d, want 4", len(results))
	}
	var pass, fail, skip int
	for _, r := range results {
		switch r.Status {
		case coverage.StatusPass:
			pass++
		case coverage.StatusFail:
			fail++
		case coverage.StatusSkip:
			skip++
		}
	}
	if pass != 2 || fail != 1 || skip != 1 {
		t.Errorf("counts %d/%d/%d, want 2/1/1", pass, fail, skip)
	}

	// Feature derivation
	byName := map[string]coverage.Result{}
	for _, r := range results {
		byName[r.TestName] = r
	}
	login := byName["@atlas:feature auth.login succeeds"]
	if login.FeatureID == nil || *login.FeatureID != "auth.login" {
		t.Errorf("@atlas login feature = %v", login.FeatureID)
	}
	billDiscount := byName["billing applies discount"]
	if billDiscount.FeatureID == nil || *billDiscount.FeatureID != "billing.upgrade" {
		t.Errorf("discount feature = %v, want billing.upgrade from path", billDiscount.FeatureID)
	}
	if billDiscount.Message == "" {
		t.Error("discount message empty")
	}
}

func TestParse_Malformed(t *testing.T) {
	_, _, err := Parse(strings.NewReader(`{garbage`))
	if err == nil {
		t.Fatal("want error")
	}
}

func TestMapStatus(t *testing.T) {
	cases := map[string]coverage.Status{
		"passed":  coverage.StatusPass,
		"failed":  coverage.StatusFail,
		"skipped": coverage.StatusSkip,
		"pending": coverage.StatusSkip,
		"todo":    coverage.StatusSkip,
		"":        coverage.StatusFail, // defensive
	}
	for in, want := range cases {
		if got := mapStatus(in); got != want {
			t.Errorf("mapStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParse_EmptyTestResults(t *testing.T) {
	_, results, err := Parse(strings.NewReader(`{"testResults":[]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}
