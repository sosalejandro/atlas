// Package gotest parses `go test -json` line-delimited output into the
// shared coverage.Run/Result shape.
//
// Input: any io.Reader producing one JSON object per line, as emitted by
// `go test -json ./...`. Non-JSON lines (build output, `compile: ...`
// banners) are tolerated and silently skipped.
//
// Test name handling:
//
//   - Subtests ("TestX/case=foo") are kept as distinct results — each
//     subtest produces its own pass/fail/skip event.
//   - The parser fills QualifiedSymbol with "<short-pkg>.<TestName>" so
//     the orchestrator can resolve it via store.Symbols.FindByQualifiedName.
//     Subtests get the parent symbol (everything before the first `/`).
//   - FeatureID is derived from an `@atlas:feature <id>` or `@testreg
//     <id>` token embedded in the test's output stream (the canonical way
//     a Go test self-annotates without source parsing). Absent → nil.
package gotest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Framework returns the constant identifying this parser. Stable.
func Framework() coverage.Framework { return coverage.FrameworkGoTest }

// event is one row of `go test -json` output.
type event struct {
	Time    time.Time `json:"Time"`
	Action  string    `json:"Action"` // run, output, pass, fail, skip, bench, pause, cont
	Package string    `json:"Package"`
	Test    string    `json:"Test"`
	Elapsed float64   `json:"Elapsed"` // seconds
	Output  string    `json:"Output"`
}

// inline-annotation grammar — matches the same `@atlas:feature <id>` and
// `@testreg <id>` forms the codeindex/annotations parser accepts, with
// the difference that here we read from runtime stdout, not source.
var (
	atlasFeatureRe = regexp.MustCompile(`@atlas:feature\s+([A-Za-z0-9_.-]+)`)
	testregRe      = regexp.MustCompile(`@testreg\s+([A-Za-z0-9_.,-]+)`)
)

// Parse reads `go test -json` output from r and returns a Run + Results
// pair ready for store.Coverage().InsertRunWithResults.
//
// Time bounds (StartedAt, FinishedAt) reflect the earliest "run" and
// latest terminal action seen in the stream. If the stream is empty the
// times stay zero and the orchestrator falls back to wall-clock.
//
// Errors are returned only for I/O failures on r. Malformed JSON lines
// are skipped, matching `go test -json`'s own tolerant behaviour.
func Parse(r io.Reader) (coverage.Run, []coverage.Result, error) {
	st := newParseState()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		st.touchTime(ev.Time)
		if ev.Test == "" {
			continue
		}
		st.applyEvent(ev)
	}
	if err := sc.Err(); err != nil {
		return coverage.Run{}, nil, fmt.Errorf("gotest: scan: %w", err)
	}
	return st.finalize(), st.materialize(), nil
}

// parseState is the in-progress accumulator threaded through Parse.
type parseState struct {
	results  map[evKey]*accum
	earliest time.Time
	latest   time.Time
	pass     int
	fail     int
	skip     int
}

type evKey struct{ pkg, name string }

type accum struct {
	pkg       string
	test      string
	status    coverage.Status
	duration  time.Duration
	message   string
	feature   *shared.FeatureID
	hadResult bool
}

func newParseState() *parseState { return &parseState{results: map[evKey]*accum{}} }

func (s *parseState) touchTime(t time.Time) {
	if t.IsZero() {
		return
	}
	if s.earliest.IsZero() || t.Before(s.earliest) {
		s.earliest = t
	}
	if s.latest.IsZero() || t.After(s.latest) {
		s.latest = t
	}
}

func (s *parseState) applyEvent(ev event) {
	k := evKey{pkg: ev.Package, name: ev.Test}
	a, ok := s.results[k]
	if !ok {
		a = &accum{pkg: ev.Package, test: ev.Test}
		s.results[k] = a
	}
	switch ev.Action {
	case "output":
		applyOutput(a, ev.Output)
	case "pass":
		a.status = coverage.StatusPass
		a.duration = secondsToDuration(ev.Elapsed)
		a.hadResult = true
		s.pass++
	case "fail":
		a.status = coverage.StatusFail
		a.duration = secondsToDuration(ev.Elapsed)
		a.hadResult = true
		s.fail++
	case "skip":
		a.status = coverage.StatusSkip
		a.duration = secondsToDuration(ev.Elapsed)
		a.hadResult = true
		s.skip++
	}
}

// applyOutput extracts annotation hints + first error excerpt from an
// `output` event into the accumulator.
func applyOutput(a *accum, out string) {
	if a.feature == nil {
		if m := atlasFeatureRe.FindStringSubmatch(out); len(m) == 2 {
			f := shared.FeatureID(strings.ToLower(m[1]))
			a.feature = &f
		} else if m := testregRe.FindStringSubmatch(out); len(m) == 2 {
			// Legacy form may be comma-separated — first ID wins.
			first := strings.TrimSpace(strings.Split(m[1], ",")[0])
			if first != "" {
				f := shared.FeatureID(strings.ToLower(first))
				a.feature = &f
			}
		}
	}
	if (strings.Contains(out, "--- FAIL") || strings.Contains(out, "FAIL:")) && a.message == "" {
		a.message = strings.TrimSpace(out)
	}
}

func (s *parseState) materialize() []coverage.Result {
	out := make([]coverage.Result, 0, len(s.results))
	for _, a := range s.results {
		if !a.hadResult {
			a.status = coverage.StatusFail
			if a.message == "" {
				a.message = "no terminal event in -json stream (truncated?)"
			}
		}
		out = append(out, coverage.Result{
			TestName:        a.pkg + "." + a.test,
			QualifiedSymbol: qualifiedName(a.pkg, a.test),
			Status:          a.status,
			Duration:        a.duration,
			Message:         a.message,
			FeatureID:       a.feature,
		})
	}
	return out
}

func (s *parseState) finalize() coverage.Run {
	summary, _ := json.Marshal(struct {
		Pass int `json:"pass"`
		Fail int `json:"fail"`
		Skip int `json:"skip"`
	}{s.pass, s.fail, s.skip})
	return coverage.Run{
		Framework:   coverage.FrameworkGoTest,
		StartedAt:   s.earliest,
		FinishedAt:  s.latest,
		SummaryJSON: string(summary),
	}
}

func secondsToDuration(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// qualifiedName turns a Go test event into Atlas's "pkg.Func" symbol id.
//
// We use the last segment of the package path as the short name (mirrors
// what codeindex/go emits when scanning the source), and we strip subtest
// suffixes ("TestX/case=foo" → "TestX") so the symbol_id lookup hits the
// parent function row.
func qualifiedName(pkgPath, testName string) shared.SymbolID {
	if pkgPath == "" || testName == "" {
		return ""
	}
	// Strip subtest path.
	if idx := strings.IndexByte(testName, '/'); idx >= 0 {
		testName = testName[:idx]
	}
	short := pkgPath
	if i := strings.LastIndexByte(pkgPath, '/'); i >= 0 {
		short = pkgPath[i+1:]
	}
	return shared.SymbolID(short + "." + testName)
}
