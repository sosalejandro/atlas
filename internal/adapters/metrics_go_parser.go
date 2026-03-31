package adapters

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sosalejandro/testreg/internal/domain"
)

// goMetricEvent mirrors the JSON structure emitted by `go test -json`.
type goMetricEvent struct {
	Time    string  `json:"Time"`
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

// benchmarkRegex matches Go benchmark output lines:
//
//	BenchmarkFoo-8  1000  1234 ns/op  256 B/op  4 allocs/op
var benchmarkRegex = regexp.MustCompile(
	`Benchmark\S+\s+\d+\s+(\d+)\s+ns/op(?:\s+(\d+)\s+B/op)?(?:\s+(\d+)\s+allocs/op)?`,
)

// raceWarningPrefix is the marker the Go race detector emits.
const raceWarningPrefix = "WARNING: DATA RACE"

// ParseGoTestMetrics reads `go test -json` output and extracts per-test metrics.
// It handles timing, pass/fail status, benchmark memory stats, and race detector
// output.
func ParseGoTestMetrics(jsonOutput []byte) (*domain.TestRunMetrics, error) {
	type testState struct {
		metric   domain.TestMetric
		raceHit  bool
		finished bool
	}

	byKey := make(map[string]*testState) // key = "pkg::testname"
	skippedCount := 0
	var wallStart, wallEnd time.Time
	raceByPackage := make(map[string]bool)

	scanner := bufio.NewScanner(bytes.NewReader(jsonOutput))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev goMetricEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip non-JSON lines (build output, etc.)
		}

		// Track wall clock range.
		if ev.Time != "" {
			if t, err := time.Parse(time.RFC3339Nano, ev.Time); err == nil {
				if wallStart.IsZero() || t.Before(wallStart) {
					wallStart = t
				}
				if t.After(wallEnd) {
					wallEnd = t
				}
			}
		}

		// Package-level race detection (output events with empty Test).
		if ev.Test == "" && ev.Action == "output" && strings.Contains(ev.Output, raceWarningPrefix) {
			raceByPackage[ev.Package] = true
			continue
		}

		if ev.Test == "" {
			continue
		}

		key := ev.Package + "::" + ev.Test
		st, ok := byKey[key]

		switch ev.Action {
		case "run":
			if !ok {
				st = &testState{
					metric: domain.TestMetric{
						Name:      ev.Test,
						File:      packageToFilePath(ev.Package),
						FeatureID: inferFeatureFromGoPackage(ev.Package, ev.Test),
					},
				}
				byKey[key] = st
			}

		case "pass":
			if ok && !st.finished {
				st.metric.Status = "pass"
				st.metric.Duration = time.Duration(ev.Elapsed * float64(time.Second))
				st.finished = true
			}

		case "fail":
			if ok && !st.finished {
				st.metric.Status = "fail"
				st.metric.Duration = time.Duration(ev.Elapsed * float64(time.Second))
				st.finished = true
			}

		case "skip":
			skippedCount++
			if ok {
				st.metric.Status = "skip"
				st.finished = true
			}

		case "output":
			if !ok {
				continue
			}
			// Check for race warning specific to this test.
			if strings.Contains(ev.Output, raceWarningPrefix) {
				st.raceHit = true
				raceByPackage[ev.Package] = true
			}
			// Check for benchmark stats.
			if m := benchmarkRegex.FindStringSubmatch(ev.Output); m != nil {
				if v, err := strconv.ParseInt(m[2], 10, 64); err == nil {
					st.metric.BytesPerOp = v
				}
				if v, err := strconv.ParseInt(m[3], 10, 64); err == nil {
					st.metric.AllocsPerOp = v
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning go test output: %w", err)
	}

	// Assemble results.
	run := &domain.TestRunMetrics{
		Timestamp: time.Now(),
		Framework: "go",
	}

	if !wallStart.IsZero() && !wallEnd.IsZero() {
		run.WallTime = wallEnd.Sub(wallStart)
		run.Timestamp = wallStart
	}

	for key, st := range byKey {
		// Propagate package-level race detection.
		pkg := key[:strings.Index(key, "::")]
		if raceByPackage[pkg] || st.raceHit {
			st.metric.RaceDetected = true
		}

		if st.metric.Status == "skip" {
			run.Skipped++
			continue
		}

		run.TestMetrics = append(run.TestMetrics, st.metric)
		run.TotalTests++
		switch st.metric.Status {
		case "pass":
			run.Passed++
		case "fail":
			run.Failed++
		}
	}

	// Also count skips from events that did not have a run event.
	run.Skipped += skippedCount - run.Skipped
	if run.Skipped < 0 {
		run.Skipped = 0
	}

	return run, nil
}
