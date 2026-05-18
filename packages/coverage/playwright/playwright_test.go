package playwright

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
)

// goldenReport mirrors the structure of Playwright --reporter=json.
const goldenReport = `{
  "stats": {
    "startTime": "2026-05-18T10:00:00.000Z",
    "duration": 4321,
    "expected": 2,
    "unexpected": 1,
    "flaky": 0,
    "skipped": 1
  },
  "suites": [
    {
      "title": "e2e/auth/login.spec.ts",
      "file": "e2e/auth/login.spec.ts",
      "specs": [
        {
          "title": "logs in @atlas:feature auth.login",
          "tests": [
            {
              "status": "expected",
              "results": [{"status": "passed", "duration": 1200}]
            }
          ]
        },
        {
          "title": "rejects bad password",
          "tests": [
            {
              "status": "unexpected",
              "results": [
                {"status": "failed", "duration": 800, "error": {"message": "expected toast 'wrong password'"}}
              ]
            }
          ]
        }
      ],
      "suites": [
        {
          "title": "nested",
          "specs": [
            {
              "title": "shows reset link",
              "tests": [{"status": "skipped", "results": [{"status": "skipped", "duration": 0}]}]
            }
          ]
        }
      ]
    },
    {
      "title": "e2e/checkout.spec.ts",
      "file": "e2e/checkout.spec.ts",
      "specs": [
        {
          "title": "@testreg checkout.success places order",
          "tests": [
            {"status": "expected", "results": [{"status": "passed", "duration": 2100}]}
          ]
        }
      ]
    }
  ]
}`

func TestParse_Golden(t *testing.T) {
	run, results, err := Parse(strings.NewReader(goldenReport))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if run.Framework != coverage.FrameworkPlaywright {
		t.Errorf("Framework = %q", run.Framework)
	}
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4", len(results))
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
		t.Errorf("pass/fail/skip = %d/%d/%d, want 2/1/1", pass, fail, skip)
	}

	byTitle := map[string]coverage.Result{}
	for _, r := range results {
		byTitle[r.TestName] = r
	}

	login, ok := byTitle["logs in @atlas:feature auth.login"]
	if !ok {
		t.Fatal("missing login test")
	}
	if login.FeatureID == nil || *login.FeatureID != "auth.login" {
		t.Errorf("login feature = %v, want auth.login", login.FeatureID)
	}
	if login.FilePath != "e2e/auth/login.spec.ts" {
		t.Errorf("login file = %q", login.FilePath)
	}

	bad, ok := byTitle["rejects bad password"]
	if !ok {
		t.Fatal("missing bad-password test")
	}
	if bad.Message == "" {
		t.Error("bad-password message empty")
	}
	if bad.FeatureID == nil || *bad.FeatureID != "auth.login" {
		// path-derived (auth/login.spec.ts → auth.login)
		t.Errorf("bad-password feature = %v, want auth.login from path", bad.FeatureID)
	}

	checkout, ok := byTitle["@testreg checkout.success places order"]
	if !ok {
		t.Fatal("missing checkout test")
	}
	if checkout.FeatureID == nil || *checkout.FeatureID != "checkout.success" {
		t.Errorf("checkout feature = %v, want checkout.success", checkout.FeatureID)
	}
}

func TestParse_EmptyDocument(t *testing.T) {
	_, results, err := Parse(strings.NewReader(`{"suites":[]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParse_Malformed(t *testing.T) {
	_, _, err := Parse(strings.NewReader(`{not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParse_LargeReport(t *testing.T) {
	// Build a synthetic report with 1500 specs to stress slice growth.
	type spec struct {
		Title string `json:"title"`
		Tests []struct {
			Status  string `json:"status"`
			Results []struct {
				Status   string  `json:"status"`
				Duration float64 `json:"duration"`
			} `json:"results"`
		} `json:"tests"`
	}
	type suite struct {
		Title string `json:"title"`
		File  string `json:"file"`
		Specs []spec `json:"specs"`
	}
	type report struct {
		Suites []suite `json:"suites"`
	}
	rep := report{Suites: []suite{{Title: "bulk", File: "e2e/bulk.spec.ts"}}}
	for i := 0; i < 1500; i++ {
		var s spec
		s.Title = "case "
		s.Tests = []struct {
			Status  string `json:"status"`
			Results []struct {
				Status   string  `json:"status"`
				Duration float64 `json:"duration"`
			} `json:"results"`
		}{{Status: "expected", Results: []struct {
			Status   string  `json:"status"`
			Duration float64 `json:"duration"`
		}{{Status: "passed", Duration: 1}}}}
		rep.Suites[0].Specs = append(rep.Suites[0].Specs, s)
	}
	buf, _ := json.Marshal(rep)
	_, results, err := Parse(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 1500 {
		t.Errorf("len = %d, want 1500", len(results))
	}
}

func TestInferFromPath(t *testing.T) {
	cases := map[string]string{
		"e2e/auth/login.spec.ts":         "auth.login",
		"e2e/checkout.spec.ts":           "checkout",
		"tests/web/billing/upgrade.spec.ts": "billing.upgrade",
		"":                               "",
	}
	for in, want := range cases {
		if got := inferFromPath(in); got != want {
			t.Errorf("inferFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
