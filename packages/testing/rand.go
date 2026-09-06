package atlastest

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// DefaultCases is how many seeds a property runs under a plain `go test`.
//
// The number is a budget, not a belief about coverage: the whole property
// layer has to stay inside the seconds a developer will tolerate before they
// start running `go test -short` and stop seeing the failures. Depth beyond
// this comes from `go test -fuzz`, which reuses the same generators through
// the Fuzz* entry points and is where a long adversarial search belongs.
const DefaultCases = 64

// Rand is a seeded source with the few shaping helpers the generators need.
// It wraps math/rand/v2's PCG rather than the global source so a failing case
// is reproducible from its seed alone — the seed is the entire bug report.
type Rand struct {
	*rand.Rand
	seed uint64
}

// New returns a Rand for one case. PCG is a specified algorithm with a
// specified seeding, so the same seed yields the same case on any machine.
func New(seed uint64) *Rand {
	return &Rand{Rand: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), seed: seed}
}

// Seed returns the seed this Rand was built from, for failure messages.
func (r *Rand) Seed() uint64 { return r.seed }

// IntRange returns a value in [lo, hi]. It panics on an inverted range
// because that is always a generator bug, and a generator bug that silently
// narrows to a constant makes the property look like it is passing.
func (r *Rand) IntRange(lo, hi int) int {
	if hi < lo {
		panic(fmt.Sprintf("atlastest: IntRange(%d, %d): inverted range", lo, hi))
	}
	return lo + r.IntN(hi-lo+1)
}

// Chance reports true with probability num/den.
func (r *Rand) Chance(num, den int) bool { return r.IntN(den) < num }

// Pick returns a uniformly chosen element of xs, or the zero value when xs is
// empty (callers that cannot tolerate the zero value check len first).
func Pick[T any](r *Rand, xs []T) T {
	var zero T
	if len(xs) == 0 {
		return zero
	}
	return xs[r.IntN(len(xs))]
}

// ForEachSeed runs fn once per seed as a named subtest, so a failure names the
// exact case ("seed=41") and `-run TestX/seed=41` replays only it.
//
// Subtests are used rather than a bare loop for one reason that matters in
// practice: without them the first failing seed aborts the run under t.Fatalf
// and you never learn whether the property fails for one input or for all of
// them, which is the difference between a fixture bug and a real defect.
func ForEachSeed(t *testing.T, cases int, fn func(t *testing.T, r *Rand)) {
	t.Helper()
	if cases <= 0 {
		cases = DefaultCases
	}
	for i := range cases {
		seed := uint64(i) + 1
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			fn(t, New(seed))
		})
	}
}
