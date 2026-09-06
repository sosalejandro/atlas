package affected

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// ---------------------------------------------------------------------------
// Fakes. The selector reads three narrow ports; wiring a real SQLite store for
// every rule would make these tests slow AND make the interesting cases (an
// empty test_coverage table, a symbol with no evidence) awkward to construct.
// The end-to-end path over a real store is exercised in internal/cli.
// ---------------------------------------------------------------------------

type fakeGit struct {
	files []string
	lines map[string][]LineRange
	err   error
}

func (f *fakeGit) ChangedFiles(context.Context, string) ([]string, error) {
	return f.files, f.err
}

func (f *fakeGit) ChangedLines(context.Context, string) (map[string][]LineRange, error) {
	return f.lines, f.err
}

type fakeSymbols struct{ rows []store.SymbolRow }

func (f *fakeSymbols) List(_ context.Context, filter store.SymbolFilter) ([]store.SymbolRow, error) {
	if filter.FilePath == "" {
		return f.rows, nil
	}
	var out []store.SymbolRow
	for _, r := range f.rows {
		if r.FilePath == filter.FilePath {
			out = append(out, r)
		}
	}
	return out, nil
}

// fakeEvidence models test_coverage as symbol id -> the test symbol ids that
// executed it, plus the per-run distinct-test count the frontier reports.
type fakeEvidence struct {
	tests map[int64][]int64
	count int
}

func (f *fakeEvidence) TestsExecuting(_ context.Context, runID, symbolID int64) ([]store.TestExecution, error) {
	var out []store.TestExecution
	for _, tid := range f.tests[symbolID] {
		out = append(out, store.TestExecution{TestSymbolID: tid, SymbolID: symbolID, CoveredStmts: 1, TotalStmts: 1})
	}
	_ = runID
	return out, nil
}

func (f *fakeEvidence) SymbolsExecutedBy(_ context.Context, _, testSymbolID int64) ([]store.TestExecution, error) {
	var out []store.TestExecution
	for symbolID, testIDs := range f.tests {
		for _, tid := range testIDs {
			if tid == testSymbolID {
				out = append(out, store.TestExecution{TestSymbolID: tid, SymbolID: symbolID})
			}
		}
	}
	return out, nil
}

func (f *fakeEvidence) CountTests(context.Context, int64) (int, error) { return f.count, nil }

func intPtr(v int) *int { return &v }

// baseSymbols is the fixture repo: one production package with two functions,
// and a test file with two tests. Ids are stable so the evidence map reads.
func baseSymbols() []store.SymbolRow {
	return []store.SymbolRow{
		{ID: 1, QualifiedName: "billing.Checkout", Kind: shared.KindFunc, FilePath: "billing/checkout.go", Line: 10, EndLine: intPtr(30)},
		{ID: 2, QualifiedName: "billing.Refund", Kind: shared.KindFunc, FilePath: "billing/refund.go", Line: 5, EndLine: intPtr(20)},
		{ID: 10, QualifiedName: "billing.TestCheckout", Kind: shared.KindFunc, FilePath: "billing/checkout_test.go", Line: 8, EndLine: intPtr(25)},
		{ID: 11, QualifiedName: "billing.TestRefund", Kind: shared.KindFunc, FilePath: "billing/refund_test.go", Line: 8, EndLine: intPtr(25)},
	}
}

func baseFrontier() store.CoverageFrontier {
	finished := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	return store.CoverageFrontier{
		Newest: 7,
		Runs: []store.CoverageRun{
			{ID: 7, Framework: store.FrameworkGoTest, FinishedAt: finished},
		},
	}
}

func fixedNow() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) }

func newInputs(git *fakeGit, syms *fakeSymbols, ev *fakeEvidence) Inputs {
	return Inputs{
		Git:      git,
		Symbols:  syms,
		Evidence: ev,
		Frontier: baseFrontier(),
		Since:    "origin/main",
		Now:      fixedNow,
	}
}

func mustSelect(t *testing.T, in Inputs) Selection {
	t.Helper()
	sel, err := Select(context.Background(), in)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	return sel
}

func testNames(sel Selection) []string {
	out := make([]string, 0, len(sel.SelectedTests))
	for _, tst := range sel.SelectedTests {
		out = append(out, tst.RunName)
	}
	sort.Strings(out)
	return out
}

func fallbackReasons(sel Selection) []string {
	out := make([]string, 0, len(sel.Fallbacks))
	for _, f := range sel.Fallbacks {
		out = append(out, f.Reason)
	}
	return out
}

// ---------------------------------------------------------------------------
// The happy path: a diff inside one function selects only the tests that
// recorded executing that function.
// ---------------------------------------------------------------------------

func TestSelect_NarrowsToTestsThatExecutedTheChangedSymbol(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
	ev := &fakeEvidence{
		tests: map[int64][]int64{1: {10}, 2: {11}},
		count: 2,
	}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if sel.Outcome != OutcomeSelected {
		t.Fatalf("outcome = %q, want %q (fallbacks: %v)", sel.Outcome, OutcomeSelected, fallbackReasons(sel))
	}
	if got := testNames(sel); len(got) != 1 || got[0] != "TestCheckout" {
		t.Fatalf("selected tests = %v, want [TestCheckout]", got)
	}
	if sel.TotalTests != 2 {
		t.Errorf("TotalTests = %d, want 2 (the indexed suite size)", sel.TotalTests)
	}
	if want := "^(TestCheckout)$"; sel.RunPattern() != want {
		t.Errorf("RunPattern() = %q, want %q", sel.RunPattern(), want)
	}
	if len(sel.ChangedSymbols) != 1 || sel.ChangedSymbols[0].QualifiedName != "billing.Checkout" {
		t.Errorf("ChangedSymbols = %+v, want just billing.Checkout", sel.ChangedSymbols)
	}
	if len(sel.Packages) != 1 || sel.Packages[0] != "billing" {
		t.Errorf("Packages = %v, want [billing]", sel.Packages)
	}
}

// A line outside every symbol span in an indexed file (a package-level var, an
// import block) widens to that package -- NOT to the whole suite, and NOT
// silently to nothing.
func TestSelect_ChangeOutsideAnySpanWidensToThePackage(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 3, End: 3}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if sel.Outcome != OutcomeSelected {
		t.Fatalf("outcome = %q, want %q", sel.Outcome, OutcomeSelected)
	}
	if got := testNames(sel); len(got) != 2 {
		t.Fatalf("selected tests = %v, want both package tests", got)
	}
	if len(sel.Widenings) == 0 {
		t.Error("a package widening must be reported, not applied silently")
	}
}

// ---------------------------------------------------------------------------
// The honesty rules. Each of these must produce run-all, never an empty
// selection: an empty selection means "run nothing", which ships broken code.
// ---------------------------------------------------------------------------

func TestSelect_UnindexedFileForcesRunAll(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/mystery.go"},
		lines: map[string][]LineRange{"billing/mystery.go": {{Start: 1, End: 4}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all for a file atlas has no symbols for", sel.Outcome)
	}
	if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonUnindexedFile {
		t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonUnindexedFile)
	}
}

func TestSelect_DependencyChangeForcesRunAll(t *testing.T) {
	for _, path := range []string{"go.mod", "go.sum", "Makefile", ".github/workflows/ci.yml"} {
		t.Run(path, func(t *testing.T) {
			git := &fakeGit{files: []string{path}, lines: map[string][]LineRange{path: {{Start: 1, End: 1}}}}
			ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
			sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))
			if !sel.RunAll() {
				t.Fatalf("%s must force run-all, got %q", path, sel.Outcome)
			}
			if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonBuildConfig {
				t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonBuildConfig)
			}
		})
	}
}

func TestSelect_TestInfrastructureChangeForcesRunAll(t *testing.T) {
	for _, path := range []string{"billing/testdata/golden.json", "internal/testutil/harness.go"} {
		t.Run(path, func(t *testing.T) {
			git := &fakeGit{files: []string{path}, lines: map[string][]LineRange{path: {{Start: 2, End: 2}}}}
			ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
			sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))
			if !sel.RunAll() {
				t.Fatalf("%s must force run-all, got %q", path, sel.Outcome)
			}
			if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonTestInfra {
				t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonTestInfra)
			}
		})
	}
}

// An empty test_coverage table for the frontier is the subtlest of the four:
// every TestsExecuting lookup returns nothing, so the naive answer is an empty
// selection -- "run nothing" -- when the truth is "I have no evidence at all".
func TestSelect_EmptyPerTestEvidenceForcesRunAll(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 12}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{}, count: 0}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all when the frontier carries no per-test rows", sel.Outcome)
	}
	if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonNoEvidence {
		t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonNoEvidence)
	}
}

func TestSelect_EmptyFrontierForcesRunAll(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 12}}},
	}
	in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, &fakeEvidence{count: 5})
	in.Frontier = store.CoverageFrontier{}
	sel := mustSelect(t, in)

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all with no coverage frontier", sel.Outcome)
	}
	if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonNoEvidence {
		t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonNoEvidence)
	}
}

func TestSelect_NoIndexedTestsForcesRunAll(t *testing.T) {
	prodOnly := []store.SymbolRow{
		{ID: 1, QualifiedName: "billing.Checkout", Kind: shared.KindFunc, FilePath: "billing/checkout.go", Line: 10, EndLine: intPtr(30)},
	}
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 12}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 1}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: prodOnly}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all when atlas has indexed no tests", sel.Outcome)
	}
	if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonNoTestsIndexed {
		t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonNoTestsIndexed)
	}
}

// ---------------------------------------------------------------------------
// The new-test rule: a test in the diff has no execution history *because it
// has never run*. Reading its empty evidence as "nothing to do" is precisely
// backwards.
// ---------------------------------------------------------------------------

func TestSelect_NewTestIsAlwaysSelected(t *testing.T) {
	syms := append(baseSymbols(), store.SymbolRow{
		ID: 12, QualifiedName: "billing.TestCheckout_Idempotent", Kind: shared.KindFunc,
		FilePath: "billing/checkout_test.go", Line: 40, EndLine: intPtr(60),
	})
	git := &fakeGit{
		files: []string{"billing/checkout_test.go"},
		lines: map[string][]LineRange{"billing/checkout_test.go": {{Start: 45, End: 48}}},
	}
	// Deliberately no evidence for symbol 12 -- it has never executed.
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: syms}, ev))

	if sel.Outcome != OutcomeSelected {
		t.Fatalf("outcome = %q, want %q (fallbacks %v)", sel.Outcome, OutcomeSelected, fallbackReasons(sel))
	}
	got := testNames(sel)
	if len(got) != 1 || got[0] != "TestCheckout_Idempotent" {
		t.Fatalf("selected = %v, want [TestCheckout_Idempotent]", got)
	}
	if !sel.SelectedTests[0].NoHistory {
		t.Error("a test absent from test_coverage must be flagged as having no execution history")
	}
}

// The converse of the rule above, and the easier one to get wrong: an EXISTING
// test that the diff merely edited has execution history, and mislabelling it
// as new would tell a reader to expect a first-ever run of something that has
// been green for months.
func TestSelect_EditedTestWithHistoryIsNotLabelledNew(t *testing.T) {
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}

	cases := map[string]*fakeGit{
		// Both a production symbol and its test changed. The changed-symbol
		// set is walked in qualified-name order, so the evidence path visits
		// TestCheckout BEFORE the changed-test path does -- an ordering a
		// naive "mark changed tests as new" would silently invert.
		"production and test together": {
			files: []string{"billing/checkout.go", "billing/checkout_test.go"},
			lines: map[string][]LineRange{
				"billing/checkout.go":      {{Start: 12, End: 12}},
				"billing/checkout_test.go": {{Start: 10, End: 10}},
			},
		},
		"test alone": {
			files: []string{"billing/checkout_test.go"},
			lines: map[string][]LineRange{"billing/checkout_test.go": {{Start: 10, End: 10}}},
		},
	}
	for name, git := range cases {
		t.Run(name, func(t *testing.T) {
			sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))
			for _, tst := range sel.SelectedTests {
				if tst.RunName == "TestCheckout" && tst.NoHistory {
					t.Fatalf("TestCheckout has execution history but was labelled new: %+v", tst)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reporting: reduction, uncovered symbols, and the age of the evidence.
// ---------------------------------------------------------------------------

func TestSelect_ReportsReductionAgainstTheIndexedSuite(t *testing.T) {
	// Three indexed tests, one selected: the reduction is the two the run
	// skips, not the one it keeps. The asymmetric fixture is deliberate --
	// a 1-of-2 case cannot tell the two readings apart.
	syms := append(baseSymbols(), store.SymbolRow{
		ID: 14, QualifiedName: "billing.TestLedger", Kind: shared.KindFunc,
		FilePath: "billing/ledger_test.go", Line: 8, EndLine: intPtr(20),
	})
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 12}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 3}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: syms}, ev))

	if sel.TotalTests != 3 || len(sel.SelectedTests) != 1 {
		t.Fatalf("selected %d of %d, want 1 of 3", len(sel.SelectedTests), sel.TotalTests)
	}
	if got := sel.Reduction(); got < 0.66 || got > 0.67 {
		t.Errorf("Reduction() = %v, want ~0.667 (the share of the suite skipped)", got)
	}
}

// A changed symbol no test recorded executing is a real, reportable finding --
// "nothing covers this" -- not a reason to run the whole suite.
func TestSelect_ChangedSymbolWithoutEvidenceIsReportedNotFallback(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/refund.go"},
		lines: map[string][]LineRange{"billing/refund.go": {{Start: 7, End: 7}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if sel.RunAll() {
		t.Fatalf("outcome = %q; an uncovered symbol is evidence of absence, not a fallback", sel.Outcome)
	}
	if len(sel.UncoveredSymbols) != 1 || sel.UncoveredSymbols[0].QualifiedName != "billing.Refund" {
		t.Fatalf("UncoveredSymbols = %+v, want billing.Refund", sel.UncoveredSymbols)
	}
}

func TestSelect_ReportsFrontierRunsAndAge(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 12}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if got := sel.Evidence.RunIDs; len(got) != 1 || got[0] != 7 {
		t.Fatalf("Evidence.RunIDs = %v, want [7]", got)
	}
	if sel.Evidence.Age != 3*time.Hour {
		t.Errorf("Evidence.Age = %v, want 3h -- the diff and the evidence are from different commits and the gap must be visible", sel.Evidence.Age)
	}
}

// ---------------------------------------------------------------------------
// Degenerate inputs.
// ---------------------------------------------------------------------------

func TestSelect_NoChangedFilesSelectsNothing(t *testing.T) {
	sel := mustSelect(t, newInputs(&fakeGit{}, &fakeSymbols{rows: baseSymbols()}, &fakeEvidence{count: 2}))
	if sel.Outcome != OutcomeNoChanges {
		t.Fatalf("outcome = %q, want %q", sel.Outcome, OutcomeNoChanges)
	}
	if sel.RunAll() {
		t.Error("an empty diff genuinely selects nothing; it must not be reported as a bail-out")
	}
	if sel.RunPattern() != "" {
		t.Errorf("RunPattern() = %q, want empty", sel.RunPattern())
	}
}

// Documentation-only changes must not bankrupt the reduction. The inert set is
// deliberately tiny and named, so a reader can audit exactly what atlas is
// willing to ignore.
func TestSelect_InertPathsDoNotForceRunAll(t *testing.T) {
	git := &fakeGit{
		files: []string{"README.md", "docs/commands/affected.md", "billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 12}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if sel.RunAll() {
		t.Fatalf("outcome = %q, want a narrowed selection: markdown cannot change behaviour", sel.Outcome)
	}
	if len(sel.InertFiles) != 2 {
		t.Errorf("InertFiles = %v, want the two markdown paths reported", sel.InertFiles)
	}
}

// A selected test whose qualified name yields no `go test -run` token cannot be
// expressed in the regex. Emitting a partial pattern would silently drop it, so
// the honest answer is to bail.
func TestSelect_UnexpressibleTestNameForcesRunAll(t *testing.T) {
	syms := append(baseSymbols(), store.SymbolRow{
		ID: 13, QualifiedName: "billing.helperNotATest", Kind: shared.KindFunc,
		FilePath: "billing/checkout_test.go", Line: 70, EndLine: intPtr(80),
	})
	git := &fakeGit{
		files: []string{"billing/checkout_test.go"},
		lines: map[string][]LineRange{"billing/checkout_test.go": {{Start: 72, End: 72}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: syms}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all when a changed test-file symbol is not a runnable test", sel.Outcome)
	}
	if got := strings.Join(fallbackReasons(sel), ","); !strings.Contains(got, ReasonUnrunnableTest) {
		t.Fatalf("fallback reasons = %v, want one of %s", got, ReasonUnrunnableTest)
	}
}

func TestSelect_RequiresASinceRef(t *testing.T) {
	in := newInputs(&fakeGit{}, &fakeSymbols{rows: baseSymbols()}, &fakeEvidence{count: 2})
	in.Since = ""
	if _, err := Select(context.Background(), in); err == nil {
		t.Fatal("Select with no --since must error rather than diff against nothing")
	}
}
