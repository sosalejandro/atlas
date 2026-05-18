// Package playwright parses Playwright's JSON reporter output into the
// shared coverage.Run/Result shape.
//
// Use Playwright's built-in `--reporter=json` to produce input. The
// reporter writes a single JSON object with a nested `suites` tree;
// every spec inside every suite (including nested describes) is unfolded
// into one coverage.Result.
//
// Status mapping (Playwright → coverage):
//
//	"passed"            → pass
//	"failed"/"timedOut" → fail
//	"skipped"           → skip
//	other               → fail (defensive)
//
// Retries: when a spec has multiple `results` entries (retried tests),
// the final entry decides the status and duration; earlier failures are
// dropped — flakiness is a Phase 6 reporting concern, not an ingest one.
//
// FeatureID resolution: pulled from `@atlas:feature <id>` / `@testreg
// <id>` tokens appearing in the spec title, then falls back to the test
// file path's leaf directory + base name (e.g. `e2e/auth/login.spec.ts`
// → `auth.login`).
package playwright

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
func Framework() coverage.Framework { return coverage.FrameworkPlaywright }

type report struct {
	Stats  *stats  `json:"stats,omitempty"`
	Suites []suite `json:"suites"`
}

type stats struct {
	StartTime string `json:"startTime"`
	Duration  float64 `json:"duration"` // ms
	Expected  int    `json:"expected"`
	Unexpected int   `json:"unexpected"`
	Flaky     int    `json:"flaky"`
	Skipped   int    `json:"skipped"`
}

type suite struct {
	Title  string  `json:"title"`
	File   string  `json:"file"`
	Suites []suite `json:"suites"`
	Specs  []spec  `json:"specs"`
}

type spec struct {
	Title string     `json:"title"`
	File  string     `json:"file"`
	Tests []testCase `json:"tests"`
}

type testCase struct {
	Status   string         `json:"status"` // "expected"|"unexpected"|"skipped"|"flaky"
	Duration float64        `json:"duration"`
	Results  []testRunBlock `json:"results"`
}

type testRunBlock struct {
	Status   string         `json:"status"` // "passed"|"failed"|"timedOut"|"skipped"
	Duration float64        `json:"duration"` // ms
	Error    *errorBlock    `json:"error,omitempty"`
}

type errorBlock struct {
	Message string `json:"message"`
}

var (
	atlasFeatureRe = regexp.MustCompile(`@atlas:feature\s+([A-Za-z0-9_.-]+)`)
	testregRe      = regexp.MustCompile(`@testreg\s+([A-Za-z0-9_.,-]+)`)
)

// Parse reads a Playwright JSON report from r.
func Parse(r io.Reader) (coverage.Run, []coverage.Result, error) {
	dec := json.NewDecoder(r)
	var rep report
	if err := dec.Decode(&rep); err != nil {
		return coverage.Run{}, nil, fmt.Errorf("playwright: decode: %w", err)
	}

	var out []coverage.Result
	var pass, fail, skip int
	for _, s := range rep.Suites {
		walk(s, s.File, &out, &pass, &fail, &skip)
	}

	run := coverage.Run{
		Framework: coverage.FrameworkPlaywright,
	}
	if rep.Stats != nil {
		// startTime is ISO-8601; parse best-effort.
		if t, err := time.Parse(time.RFC3339, rep.Stats.StartTime); err == nil {
			run.StartedAt = t.UTC()
			run.FinishedAt = t.UTC().Add(time.Duration(rep.Stats.Duration * float64(time.Millisecond)))
		}
	}
	summary, _ := json.Marshal(map[string]int{"pass": pass, "fail": fail, "skip": skip})
	run.SummaryJSON = string(summary)
	return run, out, nil
}

func walk(s suite, inheritedFile string, out *[]coverage.Result, pass, fail, skip *int) {
	file := s.File
	if file == "" {
		file = inheritedFile
	}
	for _, sp := range s.Specs {
		spFile := sp.File
		if spFile == "" {
			spFile = file
		}
		for _, tc := range sp.Tests {
			status := coverage.StatusFail
			duration := time.Duration(tc.Duration) * time.Millisecond
			var message string
			if len(tc.Results) > 0 {
				last := tc.Results[len(tc.Results)-1]
				duration = time.Duration(last.Duration) * time.Millisecond
				if last.Error != nil {
					message = last.Error.Message
				}
				switch last.Status {
				case "passed":
					status = coverage.StatusPass
				case "failed", "timedOut":
					status = coverage.StatusFail
				case "skipped":
					status = coverage.StatusSkip
				}
			} else {
				switch tc.Status {
				case "expected":
					status = coverage.StatusPass
				case "skipped":
					status = coverage.StatusSkip
				case "unexpected":
					status = coverage.StatusFail
				}
			}
			switch status {
			case coverage.StatusPass:
				*pass++
			case coverage.StatusFail:
				*fail++
			case coverage.StatusSkip:
				*skip++
			}
			*out = append(*out, coverage.Result{
				TestName:  sp.Title,
				FilePath:  filepath.ToSlash(spFile),
				FeatureID: deriveFeature(sp.Title, spFile),
				Status:    status,
				Duration:  duration,
				Message:   message,
			})
		}
	}
	for _, child := range s.Suites {
		walk(child, file, out, pass, fail, skip)
	}
}

// deriveFeature pulls the FeatureID from an `@atlas:feature` or `@testreg`
// token in the test title, falling back to a path-based heuristic.
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

// inferFromPath turns "e2e/auth/login.spec.ts" → "auth.login" and
// "tests/checkout.spec.ts" → "checkout".
func inferFromPath(path string) string {
	if path == "" {
		return ""
	}
	p := filepath.ToSlash(path)
	base := filepath.Base(p)
	for _, suf := range []string{".spec.ts", ".spec.tsx", ".spec.js", ".test.ts", ".test.tsx", ".test.js"} {
		base = strings.TrimSuffix(base, suf)
	}
	base = strings.ToLower(base)
	dir := filepath.Dir(p)
	parts := strings.Split(dir, "/")
	noise := map[string]bool{"e2e": true, "tests": true, "test": true, "specs": true, "spec": true, ".": true, "": true}
	for i := len(parts) - 1; i >= 0; i-- {
		seg := strings.ToLower(parts[i])
		if !noise[seg] {
			return seg + "." + base
		}
	}
	return base
}
