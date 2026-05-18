package maestro

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
)

// goldenJUnit is what `maestro test --format=JUNIT` writes.
const goldenJUnit = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="apps/mobile/e2e/flows" tests="3" failures="1" skipped="1" time="42.5" timestamp="2026-05-18T10:00:00Z">
    <testcase name="auth-login-valid @atlas:feature auth.login" classname="login" time="12.3" file="apps/mobile/e2e/flows/auth-login-valid.yaml"/>
    <testcase name="auth-login-invalid" classname="login" time="8.4" file="apps/mobile/e2e/flows/auth-login-invalid.yaml">
      <failure message="assertVisible failed">Expected text 'Welcome' not found</failure>
    </testcase>
    <testcase name="@testreg checkout.success place-order" classname="checkout" time="0" file="apps/mobile/e2e/flows/place-order.yaml">
      <skipped message="device unavailable"/>
    </testcase>
  </testsuite>
</testsuites>`

func TestParse_JUnit(t *testing.T) {
	run, results, err := Parse(strings.NewReader(goldenJUnit))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if run.Framework != coverage.FrameworkMaestro {
		t.Errorf("Framework = %q", run.Framework)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}

	byName := map[string]coverage.Result{}
	for _, r := range results {
		byName[r.TestName] = r
	}

	valid := byName["auth-login-valid @atlas:feature auth.login"]
	if valid.Status != coverage.StatusPass {
		t.Errorf("valid status = %q", valid.Status)
	}
	if valid.FeatureID == nil || *valid.FeatureID != "auth.login" {
		t.Errorf("valid feature = %v", valid.FeatureID)
	}

	invalid := byName["auth-login-invalid"]
	if invalid.Status != coverage.StatusFail {
		t.Errorf("invalid status = %q", invalid.Status)
	}
	if invalid.Message == "" {
		t.Error("invalid message empty")
	}
	if invalid.FeatureID == nil || *invalid.FeatureID != "auth.login" {
		t.Errorf("invalid feature = %v, want auth.login from flow filename", invalid.FeatureID)
	}

	skip := byName["@testreg checkout.success place-order"]
	if skip.Status != coverage.StatusSkip {
		t.Errorf("skip status = %q", skip.Status)
	}
	if skip.FeatureID == nil || *skip.FeatureID != "checkout.success" {
		t.Errorf("skip feature = %v, want checkout.success", skip.FeatureID)
	}
}

// goldenJSON is the alternative summary shape some wrappers emit.
const goldenJSON = `{
  "started_at": "2026-05-18T10:00:00Z",
  "flows": [
    {"name": "auth-login", "file": "apps/mobile/e2e/flows/auth-login.yaml", "status": "passed", "duration_ms": 1200},
    {"name": "checkout-failure", "file": "apps/mobile/e2e/flows/checkout.yaml", "status": "failed", "duration_ms": 800, "message": "tap target not visible"},
    {"name": "onboarding", "file": "apps/mobile/e2e/flows/onboarding.yaml", "status": "skipped", "duration_ms": 0}
  ]
}`

func TestParse_JSONSummary(t *testing.T) {
	_, results, err := Parse(strings.NewReader(goldenJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}
	var p, f, s int
	for _, r := range results {
		switch r.Status {
		case coverage.StatusPass:
			p++
		case coverage.StatusFail:
			f++
		case coverage.StatusSkip:
			s++
		}
	}
	if p != 1 || f != 1 || s != 1 {
		t.Errorf("p/f/s = %d/%d/%d", p, f, s)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	_, results, err := Parse(strings.NewReader(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParse_UnrecognisedFormat(t *testing.T) {
	_, _, err := Parse(strings.NewReader("garbage non-xml non-json"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParse_SoloTestSuite(t *testing.T) {
	// Some runners emit a single <testsuite> with no wrapper.
	xml := `<testsuite name="solo" tests="1" failures="0" timestamp="2026-05-18T10:00:00Z"><testcase name="t" time="1"/></testsuite>`
	_, results, err := Parse(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 1 || results[0].Status != coverage.StatusPass {
		t.Errorf("solo: got %+v", results)
	}
}

func TestInferFromFlow(t *testing.T) {
	cases := map[string]string{
		"auth-login":      "auth.login",
		"checkout":        "checkout",
		"meal_log_food":   "meal.log",
		"feature flag x":  "feature.flag",
		"":                "",
	}
	for in, want := range cases {
		if got := inferFromFlow(in, ""); got != want {
			t.Errorf("inferFromFlow(%q) = %q, want %q", in, got, want)
		}
	}
}
