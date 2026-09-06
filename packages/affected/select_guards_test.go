package affected

import (
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// ---------------------------------------------------------------------------
// The run-all output guards (#90 finding 3). Asserting the OUTCOME is not
// enough: a partial SelectedTests list beside outcome="run-all" is exactly
// what a runner reads as a -run pattern, so CI would run the subset while
// believing it ran everything. These assert the two cleared fields directly,
// from fixtures that HAVE a partial selection to discard -- a fixture with
// nothing selected would pass with the guards deleted.
// ---------------------------------------------------------------------------

func TestSelect_RunAllDiscardsThePartialSelection(t *testing.T) {
	// checkout.go alone selects TestCheckout; go.mod alongside it forces
	// run-all. Anything left in SelectedTests is that partial answer leaking.
	git := &fakeGit{
		files: []string{"billing/checkout.go", "go.mod"},
		lines: map[string][]LineRange{
			"billing/checkout.go": {{Start: 12, End: 14}},
			"go.mod":              {{Start: 3, End: 3}},
		},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q; the fixture must bail for this test to mean anything", sel.Outcome)
	}
	if sel.SelectedTests != nil {
		t.Errorf("SelectedTests = %+v on the run-all path; a runner reads that as a -run subset", sel.SelectedTests)
	}
	if sel.RunPattern() != "" {
		t.Errorf("RunPattern() = %q on the run-all path, want empty", sel.RunPattern())
	}
	if sel.Reduction() != 0 {
		t.Errorf("Reduction() = %v on the run-all path, want 0", sel.Reduction())
	}
	// The changed symbols DO survive: they are what the reader looks at to
	// understand the bail-out. Only the executable answer is discarded.
	if len(sel.ChangedSymbols) == 0 {
		t.Error("ChangedSymbols was discarded too; the reader loses the reason for the bail-out")
	}
}

// The same guard on the other field, from a fixture whose partial selection
// spans two packages -- so a leaked Packages list would be visibly wrong.
func TestSelect_RunAllDiscardsThePackageList(t *testing.T) {
	syms := append(baseSymbols(), store.SymbolRow{
		ID: 20, QualifiedName: "ledger.TestPost", Kind: shared.KindFunc,
		FilePath: "ledger/post_test.go", Line: 8, EndLine: intPtr(20),
	})
	git := &fakeGit{
		files: []string{"billing/checkout.go", "billing/refund.go", "internal/testutil/harness.go"},
		lines: map[string][]LineRange{
			"billing/checkout.go":          {{Start: 12, End: 14}},
			"billing/refund.go":            {{Start: 7, End: 7}},
			"internal/testutil/harness.go": {{Start: 1, End: 1}},
		},
	}
	// Checkout is executed by billing.TestCheckout, Refund by ledger.TestPost:
	// without the guard, Packages would be [billing ledger].
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {20}}, count: 3}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: syms}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all", sel.Outcome)
	}
	if sel.Packages != nil {
		t.Errorf("Packages = %v on the run-all path; a runner reads that as the packages to test", sel.Packages)
	}
	if sel.SelectedTests != nil {
		t.Errorf("SelectedTests = %+v on the run-all path, want none", sel.SelectedTests)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks, fuzz targets and examples (#90 finding 7).
// ---------------------------------------------------------------------------

// Fuzz targets and examples ARE dispatched by `go test -run` and ARE part of
// what a plain `go test ./...` executes, so touching one selects it.
func TestSelect_FuzzAndExampleAreSelectableWhenTouched(t *testing.T) {
	syms := append(baseSymbols(),
		store.SymbolRow{ID: 30, QualifiedName: "billing.FuzzParse", Kind: shared.KindFunc,
			FilePath: "billing/fuzz_test.go", Line: 10, EndLine: intPtr(20)},
		store.SymbolRow{ID: 31, QualifiedName: "billing.ExampleCheckout", Kind: shared.KindFunc,
			FilePath: "billing/example_test.go", Line: 10, EndLine: intPtr(20)},
	)
	for _, tc := range []struct{ file, want string }{
		{"billing/fuzz_test.go", "FuzzParse"},
		{"billing/example_test.go", "ExampleCheckout"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			git := &fakeGit{
				files: []string{tc.file},
				lines: map[string][]LineRange{tc.file: {{Start: 12, End: 12}}},
			}
			ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
			sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: syms}, ev))

			if sel.Outcome != OutcomeSelected {
				t.Fatalf("outcome = %q, want %s selected (fallbacks %v)", sel.Outcome, tc.want, fallbackReasons(sel))
			}
			if got := testNames(sel); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("selected = %v, want [%s]", got, tc.want)
			}
			if sel.TotalTests != 4 {
				t.Errorf("TotalTests = %d, want 4: fuzz targets and examples run under `go test ./...`", sel.TotalTests)
			}
		})
	}
}

// A benchmark is NOT: `go test -run '^(BenchmarkX)$'` matches nothing without
// -bench, so naming one in the pattern would produce a green run that executed
// nothing. Touching one bails instead of pretending.
func TestSelect_ChangedBenchmarkForcesRunAll(t *testing.T) {
	syms := append(baseSymbols(), store.SymbolRow{
		ID: 32, QualifiedName: "billing.BenchmarkCheckout", Kind: shared.KindFunc,
		FilePath: "billing/bench_test.go", Line: 10, EndLine: intPtr(20),
	})
	git := &fakeGit{
		files: []string{"billing/bench_test.go"},
		lines: map[string][]LineRange{"billing/bench_test.go": {{Start: 12, End: 12}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: syms}, ev))

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all: a benchmark cannot be expressed in a -run pattern", sel.Outcome)
	}
	if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonUnrunnableTest {
		t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonUnrunnableTest)
	}
	if sel.TotalTests != 2 {
		t.Errorf("TotalTests = %d, want 2: a benchmark is not run by a plain `go test ./...`", sel.TotalTests)
	}
}
