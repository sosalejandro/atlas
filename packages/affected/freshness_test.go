package affected

import (
	"context"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/indexfresh"
)

// ---------------------------------------------------------------------------
// Index staleness (#90 finding 1). The line numbers come from HEAD; the spans
// come from whenever `atlas scan` last ran. Joining them when they disagree
// does not degrade gracefully -- it names whichever symbol USED to own those
// lines, so the wrong tests run and the right ones are omitted, both silently.
// ---------------------------------------------------------------------------

// The headline case. Lines 12-14 sit squarely inside billing.Checkout's stored
// span, so an unchecked join answers "TestCheckout" with total confidence. It
// is not entitled to: the file changed after the scan, so lines 12-14 may now
// be anywhere in it. The answer must widen instead of resolving to a symbol.
func TestSelect_StaleSpansWidenInsteadOfResolvingToASymbol(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev)
	in.Freshness = freshnessAt("billing/checkout.go", indexfresh.StateStale)
	sel := mustSelect(t, in)

	if sel.Outcome != OutcomeSelected {
		t.Fatalf("outcome = %q, want a widened selection (fallbacks %v)", sel.Outcome, fallbackReasons(sel))
	}
	if got := testNames(sel); len(got) != 2 {
		t.Fatalf("selected = %v, want both package tests: a stale span may not name one symbol", got)
	}
	if got := widenReasons(sel); len(got) != 1 || got[0] != WideningStaleIndex {
		t.Fatalf("widening reasons = %v, want [%s]", got, WideningStaleIndex)
	}
	if !sel.Widenings[0].StaleIndex() {
		t.Error("a stale-index widening must report StaleIndex(): the remedy is `atlas scan`, not a wider diff")
	}
	if sel.Widenings[0].Path != "billing/checkout.go" {
		t.Errorf("widening path = %q, want the file that forced it", sel.Widenings[0].Path)
	}
	for _, cs := range sel.ChangedSymbols {
		if !cs.Widened {
			t.Errorf("%s is marked as directly changed, but no line number was trusted to place it", cs.QualifiedName)
		}
	}
}

// The stale file's own test file is untouched, so its tests must not be
// claimed as changed -- while remaining reachable through evidence.
func TestSelect_StaleWideningKeepsTheRightTestsForTheRightReason(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev)
	in.Freshness = freshnessAt("billing/checkout.go", indexfresh.StateStale)
	sel := mustSelect(t, in)

	if got := changedNames(sel); len(got) != 2 || got[0] != "billing.Checkout" || got[1] != "billing.Refund" {
		t.Fatalf("changed symbols = %v, want only the package's production symbols", got)
	}
	for _, tst := range sel.SelectedTests {
		for _, why := range tst.Why {
			if why == WhyChanged {
				t.Errorf("%s claims %q, but the diff touched no test file", tst.RunName, WhyChanged)
			}
		}
	}
}

func TestSelect_DeletedAndUnhashedFilesWidenToo(t *testing.T) {
	cases := map[string]struct {
		state      indexfresh.State
		wantReason string
	}{
		"deleted from the tree but still indexed": {indexfresh.StateDeleted, WideningDeletedFile},
		"indexed with no content hash to check":   {indexfresh.StateAbsent, WideningUnverifiableSpans},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			git := &fakeGit{
				files: []string{"billing/checkout.go"},
				lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
			}
			ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
			in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev)
			in.Freshness = freshnessAt("billing/checkout.go", tc.state)
			sel := mustSelect(t, in)

			if got := widenReasons(sel); len(got) != 1 || got[0] != tc.wantReason {
				t.Fatalf("widening reasons = %v, want [%s]", got, tc.wantReason)
			}
			if got := testNames(sel); len(got) != 2 {
				t.Fatalf("selected = %v, want the whole package's tests", got)
			}
		})
	}
}

// A file the check could not read is weaker still: there is no verdict to
// widen on, so nothing may be narrowed.
func TestSelect_UnverifiableSpansForceRunAll(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev)
	in.Freshness = freshnessAt("billing/checkout.go", indexfresh.StateUnreadable)
	sel := mustSelect(t, in)

	if !sel.RunAll() {
		t.Fatalf("outcome = %q, want run-all when freshness could not be established", sel.Outcome)
	}
	if got := fallbackReasons(sel); len(got) != 1 || got[0] != ReasonUnverifiableSpans {
		t.Fatalf("fallback reasons = %v, want [%s]", got, ReasonUnverifiableSpans)
	}
}

var errFreshness = errors.New("hash: permission denied")

func TestSelect_FreshnessErrorIsNotSwallowed(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev)
	in.Freshness = &fakeFreshness{err: errFreshness}

	_, err := Select(context.Background(), in)
	if err == nil {
		t.Fatal("a failed freshness check must surface as an error, not as a confident subset")
	}
	if !errors.Is(err, errFreshness) {
		t.Errorf("error = %v, want it to wrap the underlying cause", err)
	}
}

func TestSelect_RequiresAFreshnessSource(t *testing.T) {
	in := newInputs(&fakeGit{}, &fakeSymbols{rows: baseSymbols()}, &fakeEvidence{count: 2})
	in.Freshness = nil
	if _, err := Select(context.Background(), in); err == nil {
		t.Fatal("Select without a freshness source must refuse: the span join would be unchecked")
	}
}

// Inert and build-config paths never reach a span join, so they are not hashed.
func TestSelect_OnlySourcePathsAreClassifiedForFreshness(t *testing.T) {
	git := &fakeGit{
		files: []string{"README.md", "billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	fresh := &fakeFreshness{}
	in := newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev)
	in.Freshness = fresh
	mustSelect(t, in)

	if len(fresh.asked) != 1 || fresh.asked[0] != "billing/checkout.go" {
		t.Errorf("classified %v, want only the source path", fresh.asked)
	}
}

// ---------------------------------------------------------------------------
// Widening provenance (#90 finding 2).
// ---------------------------------------------------------------------------

// Widening to a package must not sweep in tests from test files the diff never
// opened. They are not changed, they are already selected on their own
// evidence when they execute something that changed, and including them
// inflates the -run pattern with tests nothing links to the change.
func TestSelect_PackageWideningSkipsUntouchedTestFiles(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]LineRange{"billing/checkout.go": {{Start: 3, End: 3}}}, // an import block
	}
	// No evidence for Refund, so TestRefund could only arrive by widening.
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	for _, cs := range sel.ChangedSymbols {
		if cs.IsTest {
			t.Errorf("widening claimed %s (%s) as changed, but the diff never touched a test file",
				cs.QualifiedName, cs.FilePath)
		}
	}
	if got := testNames(sel); len(got) != 1 || got[0] != "TestCheckout" {
		t.Fatalf("selected = %v, want only the test the evidence names", got)
	}
}

// The other half: when the widened file IS a test file, its own tests come
// along -- but the reason recorded must not say the diff edited them.
func TestSelect_WidenedTestIsNotLabelledAsEditedByTheDiff(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout_test.go"},
		lines: map[string][]LineRange{"billing/checkout_test.go": {{Start: 3, End: 3}}}, // outside every span
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	var checkout *SelectedTest
	for i := range sel.SelectedTests {
		if sel.SelectedTests[i].RunName == "TestCheckout" {
			checkout = &sel.SelectedTests[i]
		}
	}
	if checkout == nil {
		t.Fatalf("TestCheckout must still be selected when its own file widens; got %v", testNames(sel))
	}
	widened := false
	for _, why := range checkout.Why {
		if why == WhyChanged {
			t.Fatalf("Why = %v; the diff edited an import block, not TestCheckout itself", checkout.Why)
		}
		if why == WhyWidened {
			widened = true
		}
	}
	if !widened {
		t.Fatalf("Why = %v, want it to carry %q", checkout.Why, WhyWidened)
	}
}

// A test the diff really did edit keeps the direct claim.
func TestSelect_DirectlyEditedTestSaysSo(t *testing.T) {
	git := &fakeGit{
		files: []string{"billing/checkout_test.go"},
		lines: map[string][]LineRange{"billing/checkout_test.go": {{Start: 10, End: 10}}},
	}
	ev := &fakeEvidence{tests: map[int64][]int64{1: {10}, 2: {11}}, count: 2}
	sel := mustSelect(t, newInputs(git, &fakeSymbols{rows: baseSymbols()}, ev))

	if len(sel.SelectedTests) != 1 || sel.SelectedTests[0].RunName != "TestCheckout" {
		t.Fatalf("selected = %v, want [TestCheckout]", testNames(sel))
	}
	if got := sel.SelectedTests[0].Why; len(got) == 0 || got[0] != WhyChanged {
		t.Fatalf("Why = %v, want it to lead with %q", got, WhyChanged)
	}
}
