// Package maestro parses Maestro's test result output.
//
// Maestro ships two stable machine-readable formats:
//
//  1. JUnit XML — emitted via `maestro test --format=JUNIT --output=report.xml`.
//     This is the canonical format and what CI workflows consume.
//
//  2. A single-line summary JSON — emitted by some wrappers and CI
//     postprocessors.
//
// This parser sniffs the leading byte of the input to decide which
// dispatcher to use. JUnit XML starts with `<` (after BOM/whitespace);
// JSON starts with `{` or `[`.
//
// Each `<testcase>` row maps to one coverage.Result. A child `<failure>`
// or `<error>` element flips status to fail and its inner text becomes
// the Message. A child `<skipped>` flips status to skip. Otherwise pass.
//
// FeatureID resolution prefers `@atlas:feature` / `@testreg` tokens in
// the testcase name; falls back to the flow filename (suffix stripped).
package maestro

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
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
func Framework() coverage.Framework { return coverage.FrameworkMaestro }

type junitTestSuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	TestSuites []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	XMLName   xml.Name        `xml:"testsuite"`
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Errors    int             `xml:"errors,attr"`
	Skipped   int             `xml:"skipped,attr"`
	Time      float64         `xml:"time,attr"`
	Timestamp string          `xml:"timestamp,attr"`
	Cases     []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	XMLName   xml.Name      `xml:"testcase"`
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      float64       `xml:"time,attr"`
	File      string        `xml:"file,attr"`
	Failure   *junitFailure `xml:"failure"`
	Error     *junitFailure `xml:"error"`
	Skipped   *junitSkipped `xml:"skipped"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr"`
}

// summaryJSON is the alternative format some CI wrappers emit. It is
// intentionally minimal — just enough to record per-flow outcomes.
type summaryJSON struct {
	StartedAt string          `json:"started_at"`
	Flows     []summaryJSONFlow `json:"flows"`
}

type summaryJSONFlow struct {
	Name     string  `json:"name"`
	File     string  `json:"file"`
	Status   string  `json:"status"` // "passed" | "failed" | "skipped"
	Duration float64 `json:"duration_ms"`
	Message  string  `json:"message,omitempty"`
}

var (
	atlasFeatureRe = regexp.MustCompile(`@atlas:feature\s+([A-Za-z0-9_.-]+)`)
	testregRe      = regexp.MustCompile(`@testreg\s+([A-Za-z0-9_.,-]+)`)
)

// Parse reads either Maestro JUnit XML or summary JSON.
func Parse(r io.Reader) (coverage.Run, []coverage.Result, error) {
	br := bufio.NewReader(r)
	// Skip leading whitespace to peek a non-blank byte.
	var first byte
	for {
		b, err := br.Peek(1)
		if err == io.EOF {
			return coverage.Run{Framework: coverage.FrameworkMaestro, SummaryJSON: `{"pass":0,"fail":0,"skip":0}`}, nil, nil
		}
		if err != nil {
			return coverage.Run{}, nil, fmt.Errorf("maestro: peek: %w", err)
		}
		if b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r' {
			_, _ = br.ReadByte()
			continue
		}
		first = b[0]
		break
	}

	switch first {
	case '<':
		return parseJUnit(br)
	case '{', '[':
		return parseJSON(br)
	default:
		return coverage.Run{}, nil, fmt.Errorf("maestro: unrecognised input (leading byte %q); expected JUnit XML or summary JSON", first)
	}
}

func parseJUnit(r io.Reader) (coverage.Run, []coverage.Result, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return coverage.Run{}, nil, fmt.Errorf("maestro: read xml: %w", err)
	}
	suites, err := unwrapTestsuites(raw)
	if err != nil {
		return coverage.Run{}, nil, err
	}

	var out []coverage.Result
	var pass, fail, skip int
	var earliest, latest time.Time
	for _, s := range suites {
		if ts, ok := suiteWindow(s); ok {
			if earliest.IsZero() || ts.start.Before(earliest) {
				earliest = ts.start
			}
			if ts.end.After(latest) {
				latest = ts.end
			}
		}
		for _, c := range s.Cases {
			res := caseToResult(c)
			switch res.Status {
			case coverage.StatusPass:
				pass++
			case coverage.StatusFail:
				fail++
			case coverage.StatusSkip:
				skip++
			}
			out = append(out, res)
		}
	}

	run := coverage.Run{
		Framework:  coverage.FrameworkMaestro,
		StartedAt:  earliest.UTC(),
		FinishedAt: latest.UTC(),
	}
	summary, _ := json.Marshal(map[string]int{"pass": pass, "fail": fail, "skip": skip})
	run.SummaryJSON = string(summary)
	return run, out, nil
}

// unwrapTestsuites accepts both the `<testsuites>` and the bare
// `<testsuite>` shapes Maestro may emit and returns a flat slice.
func unwrapTestsuites(raw []byte) ([]junitTestSuite, error) {
	var suites junitTestSuites
	if err := xml.Unmarshal(raw, &suites); err == nil && len(suites.TestSuites) > 0 {
		return suites.TestSuites, nil
	}
	var solo junitTestSuite
	if err := xml.Unmarshal(raw, &solo); err != nil {
		return nil, fmt.Errorf("maestro: unmarshal xml: %w", err)
	}
	return []junitTestSuite{solo}, nil
}

type suiteTimeWindow struct{ start, end time.Time }

func suiteWindow(s junitTestSuite) (suiteTimeWindow, bool) {
	ts, err := time.Parse(time.RFC3339, s.Timestamp)
	if err != nil || ts.IsZero() {
		return suiteTimeWindow{}, false
	}
	return suiteTimeWindow{start: ts, end: ts.Add(time.Duration(s.Time * float64(time.Second)))}, true
}

func caseToResult(c junitTestCase) coverage.Result {
	res := coverage.Result{
		TestName: c.Name,
		Duration: time.Duration(c.Time * float64(time.Second)),
		FilePath: filepath.ToSlash(c.File),
	}
	switch {
	case c.Failure != nil:
		res.Status = coverage.StatusFail
		res.Message = firstNonEmpty(c.Failure.Message, c.Failure.Body)
	case c.Error != nil:
		res.Status = coverage.StatusFail
		res.Message = firstNonEmpty(c.Error.Message, c.Error.Body)
	case c.Skipped != nil:
		res.Status = coverage.StatusSkip
		res.Message = c.Skipped.Message
	default:
		res.Status = coverage.StatusPass
	}
	res.FeatureID = deriveFeature(c.Name, res.FilePath)
	return res
}

func parseJSON(r io.Reader) (coverage.Run, []coverage.Result, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return coverage.Run{}, nil, fmt.Errorf("maestro: read json: %w", err)
	}
	// Strip whitespace; allow either object form (`{flows: ...}`) or a raw
	// array of flows.
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var flows []summaryJSONFlow
		if err := json.Unmarshal(raw, &flows); err != nil {
			return coverage.Run{}, nil, fmt.Errorf("maestro: decode json array: %w", err)
		}
		return buildFromFlows(summaryJSON{Flows: flows}), nil, nil //nolint:nakedret // unused error swap kept for symmetry
	}
	var rep summaryJSON
	if err := json.Unmarshal(raw, &rep); err != nil {
		return coverage.Run{}, nil, fmt.Errorf("maestro: decode json: %w", err)
	}
	run, out := buildFromFlowsR(rep)
	return run, out, nil
}

func buildFromFlowsR(rep summaryJSON) (coverage.Run, []coverage.Result) {
	var pass, fail, skip int
	out := make([]coverage.Result, 0, len(rep.Flows))
	for _, f := range rep.Flows {
		var status coverage.Status
		switch strings.ToLower(f.Status) {
		case "passed", "pass", "success":
			status = coverage.StatusPass
			pass++
		case "skipped", "skip":
			status = coverage.StatusSkip
			skip++
		default:
			status = coverage.StatusFail
			fail++
		}
		out = append(out, coverage.Result{
			TestName:  f.Name,
			FilePath:  filepath.ToSlash(f.File),
			FeatureID: deriveFeature(f.Name, f.File),
			Status:    status,
			Duration:  time.Duration(f.Duration) * time.Millisecond,
			Message:   f.Message,
		})
	}
	run := coverage.Run{Framework: coverage.FrameworkMaestro}
	if t, err := time.Parse(time.RFC3339, rep.StartedAt); err == nil {
		run.StartedAt = t.UTC()
	}
	summary, _ := json.Marshal(map[string]int{"pass": pass, "fail": fail, "skip": skip})
	run.SummaryJSON = string(summary)
	return run, out
}

// buildFromFlows is the array-input variant. It packs the flows into the
// summaryJSON shape so the two code paths converge.
func buildFromFlows(rep summaryJSON) coverage.Run {
	r, _ := buildFromFlowsR(rep)
	return r
}

func deriveFeature(name, file string) *shared.FeatureID {
	if m := atlasFeatureRe.FindStringSubmatch(name); len(m) == 2 {
		f := shared.FeatureID(strings.ToLower(m[1]))
		return &f
	}
	if m := testregRe.FindStringSubmatch(name); len(m) == 2 {
		first := strings.TrimSpace(strings.Split(m[1], ",")[0])
		if first != "" {
			f := shared.FeatureID(strings.ToLower(first))
			return &f
		}
	}
	if id := inferFromFlow(name, file); id != "" {
		f := shared.FeatureID(id)
		return &f
	}
	return nil
}

func inferFromFlow(name, file string) string {
	candidate := name
	if file != "" {
		candidate = filepath.Base(file)
		for _, suf := range []string{".yaml", ".yml"} {
			candidate = strings.TrimSuffix(candidate, suf)
		}
	}
	if candidate == "" {
		return ""
	}
	c := strings.ToLower(candidate)
	c = strings.ReplaceAll(c, "-", ".")
	c = strings.ReplaceAll(c, "_", ".")
	c = strings.ReplaceAll(c, " ", ".")
	parts := strings.Split(c, ".")
	clean := parts[:0]
	for _, p := range parts {
		if p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) >= 2 {
		return clean[0] + "." + clean[1]
	}
	if len(clean) == 1 {
		return clean[0]
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}
