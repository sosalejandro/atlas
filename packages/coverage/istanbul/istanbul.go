// Package istanbul parses an Istanbul / V8 `coverage-final.json` report
// (the JSON reporter emitted by `vitest run --coverage` with the v8 provider,
// by `nyc`, and by jest's istanbul provider) into per-file executed statement
// spans — the raw material for attributing REAL production-code execution to
// FE symbols (and thence features). It is the front-end analogue of the
// `gocover` package: where gocover turns a Go coverprofile into per-block
// NumStmts, istanbul turns each file's statementMap + execution-count map into
// a list of statements with a covered/total verdict.
//
// coverage-final.json shape (one object keyed by absolute file path):
//
//	{ "/abs/path/File.tsx": {
//	    "path": "/abs/path/File.tsx",
//	    "statementMap": { "0": {"start":{"line":12,"column":4},"end":{"line":14,"column":6}}, ... },
//	    "s": { "0": 3, "1": 0 },   // execution count per statement id
//	    "fnMap": {...}, "f": {...}, "branchMap": {...}, "b": {...}
//	} }
//
// Each statement is weighted as ONE statement (count > 0 ⇒ covered) — the
// istanbul model is already one entry per statement, and the report is already
// merged per file, so no MergeBlocks-style dedup is needed (unlike a Go
// `-coverpkg=./...` profile, which repeats every span once per tested package).
package istanbul

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// Statement is one istanbul statement entry: its source line span and whether
// it executed at least once.
type Statement struct {
	StartLine int
	EndLine   int
	Count     int // execution count; >0 means executed
}

// Executed reports whether the statement ran at least once.
func (s Statement) Executed() bool { return s.Count > 0 }

// position mirrors the {"line":N,"column":N} shape inside statementMap.
type position struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// statementRange mirrors one statementMap entry.
type statementRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

// fileCoverage mirrors the per-file object in coverage-final.json. Only the
// statement-level fields are decoded; fnMap/f/branchMap/b are ignored (Tier B
// is statement coverage, matching go-cover's "(statements)" denominator).
type fileCoverage struct {
	Path         string                    `json:"path"`
	StatementMap map[string]statementRange `json:"statementMap"`
	S            map[string]int            `json:"s"`
}

// Parse reads a coverage-final.json report and returns, per file path, the
// list of statements (with covered/total verdict) in stable statement-id
// order. The returned map key is the file path AS REPORTED by istanbul
// (absolute, or app-relative depending on the reporter config); the ingest
// layer reconciles it against repo-relative atlas symbol paths.
//
// A malformed report fails loudly rather than silently under-reporting:
// truncated/garbage JSON returns an error.
func Parse(r io.Reader) (map[string][]Statement, error) {
	var raw map[string]fileCoverage
	dec := json.NewDecoder(r)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("istanbul: decode coverage-final.json: %w", err)
	}
	out := make(map[string][]Statement, len(raw))
	for key, fc := range raw {
		file := fc.Path
		if file == "" {
			file = key // some reporters omit the inner "path"; fall back to the map key
		}
		stmts, err := statementsOf(fc)
		if err != nil {
			return nil, fmt.Errorf("istanbul: file %q: %w", file, err)
		}
		out[file] = stmts
	}
	return out, nil
}

// statementsOf turns one file's statementMap + execution-count map into a
// stable-ordered slice of Statement. Statement ids are numeric strings ("0",
// "1", …); they are sorted numerically so output is deterministic. A statement
// id present in statementMap but absent from `s` is treated as count 0 (not
// executed) rather than dropped — its statement still counts toward the total.
func statementsOf(fc fileCoverage) ([]Statement, error) {
	ids := make([]int, 0, len(fc.StatementMap))
	rangeByID := make(map[int]statementRange, len(fc.StatementMap))
	for sid, sr := range fc.StatementMap {
		n, err := strconv.Atoi(sid)
		if err != nil {
			return nil, fmt.Errorf("non-numeric statement id %q: %w", sid, err)
		}
		ids = append(ids, n)
		rangeByID[n] = sr
	}
	sort.Ints(ids)
	stmts := make([]Statement, 0, len(ids))
	for _, n := range ids {
		sr := rangeByID[n]
		count := fc.S[strconv.Itoa(n)] // missing → 0
		start := sr.Start.Line
		end := sr.End.Line
		if end < start {
			end = start
		}
		stmts = append(stmts, Statement{StartLine: start, EndLine: end, Count: count})
	}
	return stmts, nil
}
