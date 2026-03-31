// @testreg metrics.go-parser
package adapters

import (
	"testing"
	"time"
)

func TestParseGoTestMetrics_BasicPassFail(t *testing.T) {
	input := `{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"github.com/user/proj/src/auth","Test":"TestLogin"}
{"Time":"2026-03-30T10:00:00.5Z","Action":"pass","Package":"github.com/user/proj/src/auth","Test":"TestLogin","Elapsed":0.5}
{"Time":"2026-03-30T10:00:01Z","Action":"run","Package":"github.com/user/proj/src/auth","Test":"TestRegister"}
{"Time":"2026-03-30T10:00:02Z","Action":"output","Package":"github.com/user/proj/src/auth","Test":"TestRegister","Output":"FAIL: Expected 200\n"}
{"Time":"2026-03-30T10:00:02Z","Action":"fail","Package":"github.com/user/proj/src/auth","Test":"TestRegister","Elapsed":1.0}
`

	run, err := ParseGoTestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if run.Framework != "go" {
		t.Errorf("Framework = %q, want %q", run.Framework, "go")
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

	if len(run.TestMetrics) != 2 {
		t.Fatalf("TestMetrics length = %d, want 2", len(run.TestMetrics))
	}

	// Find the passing test.
	var passFound, failFound bool
	for _, tm := range run.TestMetrics {
		if tm.Name == "TestLogin" {
			passFound = true
			if tm.Status != "pass" {
				t.Errorf("TestLogin status = %q, want 'pass'", tm.Status)
			}
			if tm.Duration != 500*time.Millisecond {
				t.Errorf("TestLogin duration = %v, want 500ms", tm.Duration)
			}
		}
		if tm.Name == "TestRegister" {
			failFound = true
			if tm.Status != "fail" {
				t.Errorf("TestRegister status = %q, want 'fail'", tm.Status)
			}
		}
	}

	if !passFound {
		t.Error("TestLogin not found in metrics")
	}
	if !failFound {
		t.Error("TestRegister not found in metrics")
	}
}

func TestParseGoTestMetrics_SkippedTests(t *testing.T) {
	input := `{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"github.com/user/proj/src/auth","Test":"TestSkipped"}
{"Time":"2026-03-30T10:00:00Z","Action":"skip","Package":"github.com/user/proj/src/auth","Test":"TestSkipped"}
`

	run, err := ParseGoTestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if run.TotalTests != 0 {
		t.Errorf("TotalTests = %d, want 0 (skipped tests should not count)", run.TotalTests)
	}

	if run.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", run.Skipped)
	}
}

func TestParseGoTestMetrics_BenchmarkStats(t *testing.T) {
	input := `{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"github.com/user/proj/src/perf","Test":"BenchmarkSort"}
{"Time":"2026-03-30T10:00:01Z","Action":"output","Package":"github.com/user/proj/src/perf","Test":"BenchmarkSort","Output":"BenchmarkSort-8  1000  1234 ns/op  256 B/op  4 allocs/op\n"}
{"Time":"2026-03-30T10:00:01Z","Action":"pass","Package":"github.com/user/proj/src/perf","Test":"BenchmarkSort","Elapsed":1.0}
`

	run, err := ParseGoTestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if len(run.TestMetrics) != 1 {
		t.Fatalf("TestMetrics length = %d, want 1", len(run.TestMetrics))
	}

	tm := run.TestMetrics[0]
	if tm.BytesPerOp != 256 {
		t.Errorf("BytesPerOp = %d, want 256", tm.BytesPerOp)
	}

	if tm.AllocsPerOp != 4 {
		t.Errorf("AllocsPerOp = %d, want 4", tm.AllocsPerOp)
	}
}

func TestParseGoTestMetrics_RaceDetection(t *testing.T) {
	input := `{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"github.com/user/proj/src/cache","Test":"TestConcurrent"}
{"Time":"2026-03-30T10:00:00.5Z","Action":"output","Package":"github.com/user/proj/src/cache","Output":"WARNING: DATA RACE\n"}
{"Time":"2026-03-30T10:00:01Z","Action":"fail","Package":"github.com/user/proj/src/cache","Test":"TestConcurrent","Elapsed":1.0}
`

	run, err := ParseGoTestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if len(run.TestMetrics) != 1 {
		t.Fatalf("TestMetrics length = %d, want 1", len(run.TestMetrics))
	}

	if !run.TestMetrics[0].RaceDetected {
		t.Error("Expected RaceDetected=true for TestConcurrent")
	}
}

func TestParseGoTestMetrics_WallTime(t *testing.T) {
	input := `{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"pkg","Test":"TestA"}
{"Time":"2026-03-30T10:00:05Z","Action":"pass","Package":"pkg","Test":"TestA","Elapsed":5.0}
`

	run, err := ParseGoTestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if run.WallTime != 5*time.Second {
		t.Errorf("WallTime = %v, want 5s", run.WallTime)
	}
}

func TestParseGoTestMetrics_EmptyInput(t *testing.T) {
	run, err := ParseGoTestMetrics([]byte(""))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if run.TotalTests != 0 {
		t.Errorf("TotalTests = %d, want 0", run.TotalTests)
	}

	if run.Framework != "go" {
		t.Errorf("Framework = %q, want 'go'", run.Framework)
	}
}

func TestParseGoTestMetrics_NonJSONLines(t *testing.T) {
	input := `this is not json
{"Time":"2026-03-30T10:00:00Z","Action":"run","Package":"pkg","Test":"TestA"}
also not json
{"Time":"2026-03-30T10:00:01Z","Action":"pass","Package":"pkg","Test":"TestA","Elapsed":0.1}
`

	run, err := ParseGoTestMetrics([]byte(input))
	if err != nil {
		t.Fatalf("ParseGoTestMetrics() error = %v", err)
	}

	if run.TotalTests != 1 {
		t.Errorf("TotalTests = %d, want 1 (should skip non-JSON lines)", run.TotalTests)
	}
}
