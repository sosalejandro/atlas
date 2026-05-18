// Package vitest parses Vitest's `--reporter=json` output into the shared
// coverage.Run/Result shape.
//
// Vitest's reporter mirrors Jest's: a top-level `testResults` array, one
// entry per test file, each carrying an `assertionResults` array of
// per-test outcomes. Status values are Jest-compatible:
//
//	"passed" / "failed" / "skipped" / "pending" / "todo"
//
// FeatureID resolution follows the same `@atlas:feature` / `@testreg`
// token rule as the other parsers — pulled from the assertion's
// `fullName` field, then from the test file's parent directory.
package vitest

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Framework returns the constant identifying this parser.
func Framework() coverage.Framework { return coverage.FrameworkVitest }

type report struct {
	StartTime   int64        `json:"startTime"`   // epoch ms (Vitest emits ms)
	NumPassed   int          `json:"numPassedTests"`
	NumFailed   int          `json:"numFailedTests"`
	NumSkipped  int          `json:"numPendingTests"`
	Success     *bool        `json:"success,omitempty"`
	TestResults []testResult `json:"testResults"`
}

type testResult struct {
	Name             string            `json:"name"` // file path
	StartTime        int64             `json:"startTime"`
	EndTime          int64             `json:"endTime"`
	AssertionResults []assertionResult `json:"assertionResults"`
}

type assertionResult struct {
	Title           string   `json:"title"`
	FullName        string   `json:"fullName"`
	AncestorTitles  []string `json:"ancestorTitles"`
	Status          string   `json:"status"`
	Duration        float64  `json:"duration"` // ms
	FailureMessages []string `json:"failureMessages"`
}

var (
	atlasFeatureRe = regexp.MustCompile(`@atlas:feature\s+([A-Za-z0-9_.-]+)`)
	testregRe      = regexp.MustCompile(`@testreg\s+([A-Za-z0-9_.,-]+)`)
)

// Parse reads a Vitest JSON report.
func Parse(r io.Reader) (coverage.Run, []coverage.Result, error) {
	dec := json.NewDecoder(r)
	var rep report
	if err := dec.Decode(&rep); err != nil {
		return coverage.Run{}, nil, fmt.Errorf("vitest: decode: %w", err)
	}
	results, pass, fail, skip := flatten(rep)

	run := coverage.Run{Framework: coverage.FrameworkVitest}
	if rep.StartTime > 0 {
		run.StartedAt = time.UnixMilli(rep.StartTime).UTC()
	}
	// Best-effort FinishedAt = max endTime across files.
	var maxEnd int64
	for _, tr := range rep.TestResults {
		if tr.EndTime > maxEnd {
			maxEnd = tr.EndTime
		}
	}
	if maxEnd > 0 {
		run.FinishedAt = time.UnixMilli(maxEnd).UTC()
	}
	summary, _ := json.Marshal(map[string]int{"pass": pass, "fail": fail, "skip": skip})
	run.SummaryJSON = string(summary)
	return run, results, nil
}

// flatten is shared between vitest and jest because the two report shapes
// agree on `testResults[].assertionResults[]`. The Jest parser delegates
// here after re-typing its own report struct.
func flatten(rep report) ([]coverage.Result, int, int, int) {
	out := make([]coverage.Result, 0, 64)
	var pass, fail, skip int
	for _, tr := range rep.TestResults {
		filePath := filepath.ToSlash(tr.Name)
		for _, ar := range tr.AssertionResults {
			status := mapStatus(ar.Status)
			switch status {
			case coverage.StatusPass:
				pass++
			case coverage.StatusFail:
				fail++
			case coverage.StatusSkip:
				skip++
			}
			testName := ar.FullName
			if testName == "" {
				if len(ar.AncestorTitles) > 0 {
					testName = strings.Join(append(ar.AncestorTitles, ar.Title), " > ")
				} else {
					testName = ar.Title
				}
			}
			var msg string
			if len(ar.FailureMessages) > 0 {
				msg = strings.Join(ar.FailureMessages, "\n")
			}
			out = append(out, coverage.Result{
				TestName:  testName,
				FilePath:  filePath,
				FeatureID: deriveFeature(testName, filePath),
				Status:    status,
				Duration:  time.Duration(ar.Duration) * time.Millisecond,
				Message:   msg,
			})
		}
	}
	return out, pass, fail, skip
}

func mapStatus(s string) coverage.Status {
	switch s {
	case "passed":
		return coverage.StatusPass
	case "failed":
		return coverage.StatusFail
	case "skipped", "pending", "todo":
		return coverage.StatusSkip
	default:
		return coverage.StatusFail
	}
}

// FlattenReport is a re-export so coverage/jest can reuse the shared
// shape without duplicating the report struct. It accepts the same JSON
// bytes Vitest produces; Jest's reporter is API-compatible.
func FlattenReport(jsonBytes []byte) ([]coverage.Result, int, int, int, error) {
	var rep report
	if err := json.Unmarshal(jsonBytes, &rep); err != nil {
		return nil, 0, 0, 0, fmt.Errorf("vitest: flatten unmarshal: %w", err)
	}
	r, p, f, s := flatten(rep)
	return r, p, f, s, nil
}

func deriveFeature(title, file string) *shared.FeatureID {
	if m := atlasFeatureRe.FindStringSubmatch(title); len(m) == 2 {
		f := shared.FeatureID(strings.ToLower(m[1]))
		return &f
	}
	if m := testregRe.FindStringSubmatch(title); len(m) == 2 {
		first := strings.TrimSpace(strings.Split(m[1], ",")[0])
		if first != "" {
			f := shared.FeatureID(strings.ToLower(first))
			return &f
		}
	}
	if id := inferFromPath(file); id != "" {
		f := shared.FeatureID(id)
		return &f
	}
	return nil
}

func inferFromPath(path string) string {
	if path == "" {
		return ""
	}
	p := filepath.ToSlash(path)
	base := filepath.Base(p)
	for _, suf := range []string{".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".spec.ts", ".spec.tsx", ".spec.js"} {
		base = strings.TrimSuffix(base, suf)
	}
	base = strings.ToLower(base)
	dir := filepath.Dir(p)
	parts := strings.Split(dir, "/")
	noise := map[string]bool{"src": true, "lib": true, "test": true, "tests": true, "__tests__": true, ".": true, "": true}
	for i := len(parts) - 1; i >= 0; i-- {
		seg := strings.ToLower(parts[i])
		if !noise[seg] {
			return seg + "." + base
		}
	}
	return base
}
