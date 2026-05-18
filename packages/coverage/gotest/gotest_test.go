package gotest

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
)

// goldenMixed exercises pass/fail/skip + subtests + @atlas:feature
// annotation in the output stream + a non-JSON build banner that must be
// tolerated.
const goldenMixed = `# github.com/atlas/example
{"Time":"2026-05-18T10:00:00Z","Action":"run","Package":"github.com/atlas/example/auth","Test":"TestLogin"}
{"Time":"2026-05-18T10:00:00.001Z","Action":"output","Package":"github.com/atlas/example/auth","Test":"TestLogin","Output":"    auth_test.go:42: @atlas:feature auth.login\n"}
{"Time":"2026-05-18T10:00:00.500Z","Action":"pass","Package":"github.com/atlas/example/auth","Test":"TestLogin","Elapsed":0.5}
{"Time":"2026-05-18T10:00:00.600Z","Action":"run","Package":"github.com/atlas/example/auth","Test":"TestLogin/case=invalid"}
{"Time":"2026-05-18T10:00:00.700Z","Action":"fail","Package":"github.com/atlas/example/auth","Test":"TestLogin/case=invalid","Elapsed":0.1}
{"Time":"2026-05-18T10:00:00.800Z","Action":"run","Package":"github.com/atlas/example/auth","Test":"TestLogout"}
{"Time":"2026-05-18T10:00:00.801Z","Action":"output","Package":"github.com/atlas/example/auth","Test":"TestLogout","Output":"    --- SKIP: TestLogout (0.00s)\n"}
{"Time":"2026-05-18T10:00:00.802Z","Action":"skip","Package":"github.com/atlas/example/auth","Test":"TestLogout","Elapsed":0}
{"Time":"2026-05-18T10:00:00.900Z","Action":"run","Package":"github.com/atlas/example/auth","Test":"TestRegister"}
{"Time":"2026-05-18T10:00:00.910Z","Action":"output","Package":"github.com/atlas/example/auth","Test":"TestRegister","Output":"--- FAIL: TestRegister (0.01s)\n        expected 200 got 401\n"}
{"Time":"2026-05-18T10:00:01.000Z","Action":"output","Package":"github.com/atlas/example/auth","Test":"TestRegister","Output":"    @testreg auth.register\n"}
{"Time":"2026-05-18T10:00:01.000Z","Action":"fail","Package":"github.com/atlas/example/auth","Test":"TestRegister","Elapsed":0.1}
`

// parseGolden parses goldenMixed once and indexes results by TestName.
// Shared by the per-row assertion tests below to keep each function small
// (funlen budget = 40 statements).
func parseGolden(t *testing.T) (coverage.Run, map[string]coverage.Result) {
	t.Helper()
	run, results, err := Parse(strings.NewReader(goldenMixed))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if run.Framework != coverage.FrameworkGoTest {
		t.Errorf("Framework = %q, want %q", run.Framework, coverage.FrameworkGoTest)
	}
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4 (TestLogin, subtest, TestLogout, TestRegister)", len(results))
	}
	byName := map[string]coverage.Result{}
	for _, r := range results {
		byName[r.TestName] = r
	}
	return run, byName
}

func TestParse_GoldenPassRow(t *testing.T) {
	_, byName := parseGolden(t)
	login, ok := byName["github.com/atlas/example/auth.TestLogin"]
	if !ok {
		t.Fatal("missing TestLogin result")
	}
	if login.Status != coverage.StatusPass {
		t.Errorf("TestLogin status = %q, want pass", login.Status)
	}
	if login.FeatureID == nil || *login.FeatureID != "auth.login" {
		t.Errorf("TestLogin feature = %v, want auth.login", login.FeatureID)
	}
	if login.QualifiedSymbol != "auth.TestLogin" {
		t.Errorf("TestLogin qsymbol = %q, want auth.TestLogin", login.QualifiedSymbol)
	}
}

func TestParse_GoldenSubtestFail(t *testing.T) {
	_, byName := parseGolden(t)
	sub, ok := byName["github.com/atlas/example/auth.TestLogin/case=invalid"]
	if !ok {
		t.Fatal("missing subtest result")
	}
	if sub.Status != coverage.StatusFail {
		t.Errorf("subtest status = %q, want fail", sub.Status)
	}
	if sub.QualifiedSymbol != "auth.TestLogin" {
		t.Errorf("subtest qsymbol = %q, want parent auth.TestLogin", sub.QualifiedSymbol)
	}
}

func TestParse_GoldenSkipRow(t *testing.T) {
	_, byName := parseGolden(t)
	logout, ok := byName["github.com/atlas/example/auth.TestLogout"]
	if !ok {
		t.Fatal("missing TestLogout result")
	}
	if logout.Status != coverage.StatusSkip {
		t.Errorf("TestLogout status = %q, want skip", logout.Status)
	}
}

func TestParse_GoldenFailRowAndLegacyAnnotation(t *testing.T) {
	run, byName := parseGolden(t)
	reg, ok := byName["github.com/atlas/example/auth.TestRegister"]
	if !ok {
		t.Fatal("missing TestRegister result")
	}
	if reg.Status != coverage.StatusFail {
		t.Errorf("TestRegister status = %q, want fail", reg.Status)
	}
	if reg.FeatureID == nil || *reg.FeatureID != "auth.register" {
		t.Errorf("TestRegister feature = %v, want auth.register (via @testreg)", reg.FeatureID)
	}
	if reg.Message == "" {
		t.Error("TestRegister message: want captured error excerpt")
	}
	if run.SummaryJSON == "" {
		t.Error("SummaryJSON empty")
	}
}

func TestParse_EmptyInput(t *testing.T) {
	run, results, err := Parse(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
	if run.Framework != coverage.FrameworkGoTest {
		t.Errorf("Framework = %q, want go-test", run.Framework)
	}
}

func TestParse_MalformedJSONTolerated(t *testing.T) {
	input := `not json at all
{"Action":"run","Package":"github.com/atlas/example","Test":"TestX"}
{this is broken
{"Action":"pass","Package":"github.com/atlas/example","Test":"TestX","Elapsed":0.01}
`
	_, results, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (only TestX should survive)", len(results))
	}
	if results[0].Status != coverage.StatusPass {
		t.Errorf("TestX status = %q, want pass", results[0].Status)
	}
}

func TestParse_TruncatedStream(t *testing.T) {
	// "run" but no terminal event — must surface as fail with a hint.
	input := `{"Action":"run","Package":"github.com/atlas/example","Test":"TestDangling"}
`
	_, results, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Status != coverage.StatusFail {
		t.Errorf("dangling test status = %q, want fail", results[0].Status)
	}
	if !strings.Contains(results[0].Message, "truncated") {
		t.Errorf("dangling test message = %q, want a 'truncated' hint", results[0].Message)
	}
}

func TestParse_LargeRun(t *testing.T) {
	const N = 1200
	var buf bytes.Buffer
	for i := 0; i < N; i++ {
		fmt.Fprintf(&buf, `{"Action":"run","Package":"github.com/atlas/example/bulk","Test":"TestBulk%d"}`+"\n", i)
		fmt.Fprintf(&buf, `{"Action":"pass","Package":"github.com/atlas/example/bulk","Test":"TestBulk%d","Elapsed":0.001}`+"\n", i)
	}
	_, results, err := Parse(&buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != N {
		t.Errorf("len(results) = %d, want %d", len(results), N)
	}
}
