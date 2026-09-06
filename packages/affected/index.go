package affected

import (
	"context"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// symbolIndex is the whole symbol table, arranged the three ways the selector
// reads it.
//
// It is built from ONE unfiltered List rather than a per-changed-file query,
// because two of the three uses are repo-wide anyway: the suite denominator
// needs every test symbol, and the evidence lookup resolves arbitrary test
// symbol ids back to rows. Paying for one full read is cheaper than a
// per-file query plus a second full read, and it makes the whole selection a
// snapshot of one consistent view of the table.
type symbolIndex struct {
	byFile map[string][]store.SymbolRow
	byDir  map[string][]store.SymbolRow
	byID   map[int64]store.SymbolRow

	// tests is the indexed suite: symbol id -> `go test -run` token, for every
	// symbol a plain `go test` dispatches BY NAME — TestXxx, FuzzXxx and
	// ExampleXxx. Its size is the denominator of the reduction — the number of
	// tests CI would run without atlas.
	//
	// Membership is also the gate on being selectable at all, which is why
	// BenchmarkXxx is deliberately absent: see goTestRunPrefixes.
	tests map[int64]string
}

func buildSymbolIndex(ctx context.Context, src SymbolSource) (*symbolIndex, error) {
	rows, err := src.List(ctx, store.SymbolFilter{})
	if err != nil {
		return nil, fmt.Errorf("affected: list symbols: %w", err)
	}
	idx := &symbolIndex{
		byFile: make(map[string][]store.SymbolRow),
		byDir:  make(map[string][]store.SymbolRow),
		byID:   make(map[int64]store.SymbolRow, len(rows)),
		tests:  make(map[int64]string),
	}
	for _, r := range rows {
		idx.byFile[r.FilePath] = append(idx.byFile[r.FilePath], r)
		idx.byDir[path.Dir(r.FilePath)] = append(idx.byDir[path.Dir(r.FilePath)], r)
		idx.byID[r.ID] = r
		if name, ok := runnableTestName(r); ok {
			idx.tests[r.ID] = name
		}
	}
	return idx, nil
}

// spansAt returns the symbols in a file whose declaration span contains any of
// the changed lines, and whether every range found at least one owner.
//
// A range that owns nothing is the caller's signal to widen: the edit landed
// on a package-level declaration, an import block or a comment, none of which
// the scanner records as a symbol, and all of which can change the behaviour
// of the package around them.
func (idx *symbolIndex) spansAt(filePath string, ranges []LineRange) (hits []store.SymbolRow, allMatched bool) {
	rows := idx.byFile[filePath]
	seen := make(map[int64]bool, len(rows))
	allMatched = true
	for _, r := range ranges {
		matched := false
		for _, row := range rows {
			if !spanContains(row, r) {
				continue
			}
			matched = true
			if !seen[row.ID] {
				seen[row.ID] = true
				hits = append(hits, row)
			}
		}
		if !matched {
			allMatched = false
		}
	}
	return hits, allMatched
}

// spanContains reports whether a symbol's declaration overlaps a changed
// range. A row with no end_line (a pre-0.11 scan, or a scanner that does not
// record one) degenerates to its declaration line alone — narrower than the
// truth, which is why an unmatched range widens rather than selects nothing.
func spanContains(row store.SymbolRow, r LineRange) bool {
	start := row.Line
	end := start
	if row.EndLine != nil && *row.EndLine >= start {
		end = *row.EndLine
	}
	return r.Start <= end && r.End >= start
}

// goTestRunPrefixes are the function-name prefixes a `-run` pattern can
// dispatch: TestXxx, and the two families a plain `go test` also executes —
// FuzzXxx (its seed corpus) and ExampleXxx (when it has an output comment).
// All three are selectable when the diff touches them, and all three count
// toward the suite denominator, because all three are part of what CI runs
// today without atlas.
//
// BenchmarkXxx is deliberately NOT here. `go test -run '^(BenchmarkFoo)$'`
// matches no test and runs NOTHING without -bench, so putting a benchmark in
// the pattern would produce a green run that executed nothing — the exact
// failure this package exists to prevent. A changed benchmark therefore falls
// out of the index and forces run-all through ReasonUnrunnableTest instead.
var goTestRunPrefixes = []string{"Test", "Fuzz", "Example"}

// runnableTestName derives the bare `go test -run` token from a symbol, and
// reports whether the symbol is a runnable test at all.
//
// Two things make this less trivial than "split on the dot". The scanner
// disambiguates colliding short names by promoting them to a package-qualified
// id and, failing that, by appending "#<file>.go" — so the token is the last
// dot-separated segment of the id with any file suffix stripped. And `go test`
// only treats TestXxx as a test when Xxx does not begin with a lowercase
// letter, so `Testing` is an ordinary function and must not be run.
//
// TestMain is excluded deliberately: it is the package's harness, not a test.
// Selecting it would emit a -run pattern that matches nothing, and treating it
// as an ordinary test would understate the blast radius of editing it.
func runnableTestName(row store.SymbolRow) (string, bool) {
	if !store.IsTestPath(row.FilePath) {
		return "", false
	}
	name := shortSymbolName(row.QualifiedName)
	if name == "" || name == "TestMain" {
		return "", false
	}
	for _, prefix := range goTestRunPrefixes {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return name, true
		}
		if unicode.IsLower([]rune(rest)[0]) {
			return "", false
		}
		return name, true
	}
	return "", false
}

// shortSymbolName reduces a scanner-assigned qualified name to its final
// identifier: "billing.TestCheckout" and
// "packages/billing.TestCheckout#checkout_test.go" both yield "TestCheckout".
func shortSymbolName(qn shared.SymbolID) string {
	s := string(qn)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
