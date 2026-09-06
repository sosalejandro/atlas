// Package flowfixture is the golden fixture for the CFG builder and the
// decision-coverage analysis. It lives under testdata so the go tool does
// not build it as part of the module; the tests parse it with go/parser and
// analyse it against fixture.cover.out, a real `go test -coverprofile` run
// over fixture_test.go.
//
// Every function here exists to pin one construct the builder has to get
// right. Do not "tidy" them: the line numbers are asserted, and the shapes
// (an if whose then-arm returns, a loop with no break, a switch with no
// default) are chosen because each one lands in a different branch of the
// decidability rules in coverage.go.
package flowfixture

import "errors"

// Classify covers if / else-if / else and a short-circuit `&&`.
func Classify(n int, ok bool) string {
	if n > 0 && ok {
		return "positive"
	} else if n < 0 {
		return "negative"
	}
	return "zero"
}

// Guard is an if with no else whose then-arm returns. The false outcome has
// no counter of its own; it is observable only because the statement after
// the if has one, and only because the if is not inside a loop.
func Guard(n int) (int, error) {
	if n < 0 {
		return 0, errors.New("negative")
	}
	return n * 2, nil
}

// Sum ranges over a collection and returns early from inside the loop. The
// inner if is in a loop, so its false outcome is NOT decidable by
// differencing counts, and the loop's own exit outcome is not decidable
// either because the body can leave by return.
func Sum(xs []int, stop int) int {
	total := 0
	for _, x := range xs {
		if x == stop {
			return total
		}
		total += x
	}
	return total
}

// Label is a switch with a default: every outcome has a body, so every
// outcome is decidable.
func Label(kind string) string {
	switch kind {
	case "a":
		return "alpha"
	case "b":
		return "beta"
	default:
		return "other"
	}
}

// Kindly is a type switch with no default. The "matched nothing" outcome
// leaves no counter anywhere, which is the case the analysis must refuse
// rather than assume.
func Kindly(v any) string {
	switch v.(type) {
	case int:
		return "int"
	case string:
		return "string"
	}
	return "unknown"
}

// Await covers select and defer. The defer must not add a branch arm.
func Await(ch <-chan int, done <-chan struct{}) (int, bool) {
	defer func() { _ = recover() }()
	select {
	case v := <-ch:
		return v, true
	case <-done:
		return 0, false
	}
}

// Retry is a while-shaped for whose body cannot leave early, so the loop's
// exit outcome IS decidable from the statement that follows it.
func Retry(attempts int) int {
	i := 0
	for i < attempts {
		i++
	}
	return i
}

// Unreachable has a statement no path can reach — the structural finding,
// as distinct from an untested one. (`go vet` flags this too; the fixture is
// compiled with -vet=off when regenerating the profile.)
func Unreachable() int {
	n := 1
	return n
	n++
	return n
}

// Either covers `||` and a bare `for {}` with a break.
func Either(a, b bool) int {
	n := 0
	if a || b {
		n = 1
	}
	for {
		n++
		break
	}
	return n
}

// Both puts a short-circuit operator OUTSIDE condition position. It is still
// a branch — b is not called when a returns false — and still invisible to
// statement coverage, which is why the builder models it too.
func Both(a, b func() bool) bool {
	return a() && b()
}
