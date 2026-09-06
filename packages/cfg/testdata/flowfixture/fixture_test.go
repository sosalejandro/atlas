package flowfixture

import "testing"

// The profile is committed as `fixture.coverprofile` rather than
// `fixture.cover.out`: the repo .gitignore excludes `*.out` as build output,
// and a golden fixture that vanishes on a fresh clone is a test suite that
// cannot run at all.
//
// TestFixture exercises a DELIBERATELY partial set of paths: the point of the
// fixture is that some outcomes are taken, some are not, and some cannot be
// judged either way from statement coverage. Regenerate the profile the
// analysis tests read with:
//
//	go test -vet=off -covermode=count \
//	    -coverprofile=packages/cfg/testdata/flowfixture/fixture.coverprofile \
//	    ./packages/cfg/testdata/flowfixture/
//
// -covermode=count (not the default `set`) matters: the successor-difference
// inference that recovers the false outcome of an if with no else needs
// execution COUNTS, not booleans.
func TestFixture(t *testing.T) {
	// Only the true arm of the outer if; the else-if is never evaluated.
	if got := Classify(1, true); got != "positive" {
		t.Fatalf("Classify = %q", got)
	}
	// Only the false path: the then arm never runs, and the statement after
	// the if is the only witness that the false path happened at all.
	if _, err := Guard(5); err != nil {
		t.Fatalf("Guard: %v", err)
	}
	// The loop runs, the inner if never matches — and because the if is
	// inside the loop, "never matched" is not something the profile can prove.
	if got := Sum([]int{1, 2, 3}, 99); got != 6 {
		t.Fatalf("Sum = %d", got)
	}
	if got := Label("a"); got != "alpha" {
		t.Fatalf("Label = %q", got)
	}
	if got := Kindly(1); got != "int" {
		t.Fatalf("Kindly = %q", got)
	}
	ch := make(chan int, 1)
	ch <- 7
	if v, ok := Await(ch, make(chan struct{})); !ok || v != 7 {
		t.Fatalf("Await = %d %v", v, ok)
	}
	if got := Retry(2); got != 2 {
		t.Fatalf("Retry = %d", got)
	}
	if got := Unreachable(); got != 1 {
		t.Fatalf("Unreachable = %d", got)
	}
	if got := Either(false, false); got != 1 {
		t.Fatalf("Either = %d", got)
	}
	if Both(func() bool { return false }, func() bool { return true }) {
		t.Fatal("Both = true")
	}
	// AlwaysThen is called ONLY on the true path, twice. That is the whole
	// point: the then arm and the statement after the if end up with the SAME
	// count, which is the one shape where "the successor ran" and "the
	// successor ran more often than the then arm" disagree.
	if got := AlwaysThen(3); got != 6 {
		t.Fatalf("AlwaysThen = %d", got)
	}
	if got := AlwaysThen(4); got != 8 {
		t.Fatalf("AlwaysThen = %d", got)
	}
	// Escapes takes the nested guard once and falls through once, so the
	// statement after the if has run fewer times than the then arm was
	// entered. No arithmetic over those counts recovers the false outcome.
	if got := Escapes(1, true); got != -1 {
		t.Fatalf("Escapes(bail) = %d", got)
	}
	if got := Escapes(1, false); got != 2 {
		t.Fatalf("Escapes = %d", got)
	}
	if got := Panics(1, false); got != 2 {
		t.Fatalf("Panics = %d", got)
	}
	if got := Breaking("a"); got != "alpha" {
		t.Fatalf("Breaking = %q", got)
	}
	if got := Jumping(1); got != "ok" {
		t.Fatalf("Jumping = %q", got)
	}
	// LoadUsers and friends are deliberately NOT called: the N+1 detector is
	// a static analysis and must not need execution data.
}
