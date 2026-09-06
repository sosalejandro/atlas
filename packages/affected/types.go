// Package affected answers "given this diff, what do I actually need to run?".
//
// The selection is driven by the `test_coverage` table (schema 0010, issue
// #104), which records, per test, exactly which production symbols that test
// executed. Read forwards it locates a feature's implementation; read
// backwards it is a precise inverse index — the tests that reach a symbol —
// which is test impact analysis (issue #90).
//
// Static call-edge closure is deliberately NOT used as the selection
// mechanism. Call edges miss interface dispatch, DI containers, reflection and
// table-driven registries; a subset built from them looks convincing and
// quietly omits the test that would have caught the regression. Execution
// evidence has none of those blind spots, and where the evidence is missing
// the honest answer is to run everything rather than to substitute a weaker
// signal for it.
//
// # Why the fallback matters more than the selection
//
// Every rule in this package is arranged so that uncertainty produces
// OutcomeRunAll rather than a small selection. An empty selection is
// indistinguishable, to a CI runner, from "nothing needs testing" — so a bug
// anywhere in this package would surface as a green build that ran no tests.
// The failure mode of being too conservative is a slow pipeline; the failure
// mode of being too clever is a shipped regression. See Fallback.
package affected

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// LineRange is a 1-based, inclusive line span on the POST-image (the HEAD side
// of the diff) — the side the symbol index is keyed on, because atlas indexed
// the working tree, not the merge base.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Outcome is what the selection concluded. It is a closed set precisely so a
// CI script can branch on it without parsing prose.
type Outcome string

const (
	// OutcomeSelected means the diff was narrowed: run SelectedTests.
	OutcomeSelected Outcome = "selected"

	// OutcomeRunAll means the diff could NOT be narrowed safely. Run the whole
	// suite. Fallbacks says which rule fired and why.
	OutcomeRunAll Outcome = "run-all"

	// OutcomeNoChanges means the diff is empty. Running nothing is the correct
	// answer here and it is NOT a bail-out — keeping it distinct from
	// OutcomeSelected-with-zero-tests is what lets a reader tell "nothing
	// changed" apart from "something changed and nothing covers it".
	OutcomeNoChanges Outcome = "no-changes"
)

// Fallback reasons. These strings are part of the --json contract; a CI job
// that alerts on "we stopped getting a reduction" keys on them.
const (
	// ReasonUnindexedFile: a changed file atlas holds no symbols for. It may
	// be a language the scanner does not cover, a generated directory, or a
	// file added since the last scan. Either way there is nothing to map the
	// change onto, so no subset can be justified.
	ReasonUnindexedFile = "unindexed-file"

	// ReasonBuildConfig: a dependency or build/CI input changed (go.mod,
	// go.sum, Makefile, a workflow file). A dependency bump can alter the
	// behaviour of any package in the repo, and none of that is visible as a
	// symbol-level edit.
	ReasonBuildConfig = "build-config"

	// ReasonTestInfra: shared test scaffolding changed (testdata fixtures, a
	// testutil package, conftest.py). Coverage records which PRODUCTION
	// symbols a test executed, never which helpers it called, so there is no
	// evidence linking a fixture to its consumers.
	ReasonTestInfra = "test-infra"

	// ReasonNoEvidence: the coverage frontier is empty, or carries no
	// per-test rows at all. This is the trap the whole package exists to
	// avoid: with no evidence every TestsExecuting lookup returns nothing,
	// and the naive result is an empty selection that reads as "run nothing".
	ReasonNoEvidence = "no-test-evidence"

	// ReasonNoTestsIndexed: atlas has indexed no test symbols, so there is no
	// suite to take a subset OF and no denominator to report a reduction
	// against.
	ReasonNoTestsIndexed = "no-tests-indexed"

	// ReasonUnrunnableTest: a changed symbol in a test file is not itself a
	// runnable test — a helper, a fixture, TestMain. Its callers are other
	// tests, and no coverage row records a test calling a helper, so the set
	// of affected tests is unknowable from evidence.
	ReasonUnrunnableTest = "unrunnable-test"
)

// Fallback is one reason the selection could not be narrowed. Path is the
// changed file that triggered it, empty for repo-wide reasons.
type Fallback struct {
	Reason string `json:"reason"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
}

// Widening is a place the selection had to grow beyond the changed lines. It
// is reported rather than silently applied: a run that is wider than the diff
// suggests is a legitimate result, but a reader must be able to see why the
// reduction is smaller than they expected.
type Widening struct {
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Detail string `json:"detail"`
}

// ChangedSymbol is one indexed symbol the diff landed inside.
type ChangedSymbol struct {
	SymbolID      int64           `json:"symbol_id"`
	QualifiedName shared.SymbolID `json:"qualified_name"`
	FilePath      string          `json:"file_path"`
	Line          int             `json:"line"`
	EndLine       int             `json:"end_line,omitempty"`
	IsTest        bool            `json:"is_test"`

	// Widened marks a symbol pulled in by the package-widening rule rather
	// than by a changed line landing inside it.
	Widened bool `json:"widened,omitempty"`
}

// SelectedTest is one test the runner should execute.
type SelectedTest struct {
	SymbolID      int64           `json:"symbol_id"`
	QualifiedName shared.SymbolID `json:"qualified_name"`

	// RunName is the bare `go test -run` token (e.g. "TestCheckout"). Derived
	// from the qualified name, which the scanner may have disambiguated with a
	// package path or a "#file.go" suffix.
	RunName string `json:"run_name"`

	FilePath string `json:"file_path"`
	Package  string `json:"package"`

	// NoHistory marks a test with no rows in test_coverage. That is the
	// signature of a NEW test: it has no execution history precisely because
	// it has never run. Such a test is always selected — reading its empty
	// evidence as "reaches nothing, skip it" is exactly backwards.
	NoHistory bool `json:"no_history,omitempty"`

	// Why lists the reasons this test was selected, in discovery order:
	// "changed" for a test the diff edited, or "executes <qualified name>".
	Why []string `json:"why"`
}

// Evidence describes the coverage the selection was computed FROM. It exists
// because the evidence and the diff are from different commits: the tests were
// measured at some earlier revision, and every conclusion below is only as
// current as that measurement. Callers MUST surface RunIDs and Age.
type Evidence struct {
	RunIDs           []int64       `json:"run_ids"`
	Group            *string       `json:"group,omitempty"`
	NewestFinishedAt time.Time     `json:"newest_finished_at"`
	Age              time.Duration `json:"-"`
	AgeSeconds       int64         `json:"age_seconds"`

	// TestsWithEvidence is the number of distinct tests that contributed rows,
	// summed per run. Runs in one frontier are different frameworks measuring
	// different suites, so the sum is the honest total; the per-run breakdown
	// is kept alongside it for a reader who wants to check that.
	TestsWithEvidence int           `json:"tests_with_evidence"`
	PerRunTests       map[int64]int `json:"per_run_tests,omitempty"`
	Frameworks        []string      `json:"frameworks,omitempty"`
}

// Selection is the whole answer: what to run, what could not be narrowed, and
// how much was saved.
type Selection struct {
	Outcome Outcome `json:"outcome"`
	Since   string  `json:"since"`

	ChangedFiles []string `json:"changed_files"`

	// InertFiles are changed paths ruled incapable of changing behaviour
	// (markdown, licence text). The list is reported so the reader can audit
	// exactly what atlas chose to ignore.
	InertFiles []string `json:"inert_files,omitempty"`

	ChangedSymbols []ChangedSymbol `json:"changed_symbols"`

	// UncoveredSymbols are changed symbols no test recorded executing. This is
	// evidence of absence, not missing evidence — the per-test ingest records
	// every executing test — so it is reported rather than treated as a
	// fallback. It is also the most actionable line in the output: a change
	// nothing tests.
	UncoveredSymbols []ChangedSymbol `json:"uncovered_symbols,omitempty"`

	SelectedTests []SelectedTest     `json:"selected_tests"`
	Packages      []string           `json:"packages,omitempty"`
	Features      []shared.FeatureID `json:"features,omitempty"`

	// TotalTests is the size of the indexed suite — the number of tests CI
	// would run without atlas, and the denominator of Reduction.
	TotalTests int `json:"total_tests"`

	Fallbacks []Fallback `json:"fallbacks,omitempty"`
	Widenings []Widening `json:"widenings,omitempty"`
	Evidence  Evidence   `json:"evidence"`
}

// RunAll reports whether the caller must run the entire suite.
func (s Selection) RunAll() bool { return s.Outcome == OutcomeRunAll }

// Reduction is the share of the indexed suite this selection SKIPS, in [0,1].
// Zero when there is nothing to reduce (run-all, or no indexed tests), so a
// dashboard plotting it never reads a bail-out as a saving.
func (s Selection) Reduction() float64 {
	if s.RunAll() || s.TotalTests <= 0 {
		return 0
	}
	skipped := s.TotalTests - len(s.SelectedTests)
	if skipped <= 0 {
		return 0
	}
	return float64(skipped) / float64(s.TotalTests)
}

// RunPattern renders the selection as a `go test -run` regex, anchored so
// "TestCheckout" cannot also match "TestCheckoutIdempotent". Empty when
// nothing is selected — callers MUST NOT treat an empty pattern as "match
// everything", which is what an unanchored empty regex would mean to go test.
func (s Selection) RunPattern() string {
	if len(s.SelectedTests) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.SelectedTests))
	seen := make(map[string]struct{}, len(s.SelectedTests))
	for _, t := range s.SelectedTests {
		if _, dup := seen[t.RunName]; dup {
			continue
		}
		seen[t.RunName] = struct{}{}
		names = append(names, regexp.QuoteMeta(t.RunName))
	}
	sort.Strings(names)
	return "^(" + strings.Join(names, "|") + ")$"
}
