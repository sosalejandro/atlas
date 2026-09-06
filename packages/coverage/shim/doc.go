// Package shim is the test-side half of atlas's per-test coverage
// collection: the library a generated TestMain calls so that a Go suite
// produces one coverage snapshot per test instead of one per process.
//
//	func TestMain(m *testing.M) { shim.Run(m) }   // atlas cov shim init
//
// Why a shim is needed at all: the Go runtime writes coverage counters once,
// at process exit, so a whole `go test ./...` run yields a single profile.
// The per-test evidence atlas derives feature surfaces from (issue #104,
// `atlas cov sync --per-test`) simply has no producer without this.
//
// # How it works
//
// Counters live in one global counter space, so isolating a test means
// emptying that space before it runs and photographing it afterwards:
// runtime/coverage.ClearCounters + WriteCountersDir (Go 1.20+). To keep the
// snapshot honest, the shim runs ONE top-level test per m.Run — a suite that
// ran all its tests at once could not be taken apart afterwards, because
// nothing in the counter data says which test incremented which counter.
//
// Three runtime facts shape the loop, all of them learned the hard way:
//
//   - ClearCounters and WriteCountersDir only work under -covermode=atomic;
//     under any other mode they refuse, and the shim degrades rather than
//     recording counters it knows are shared.
//   - The coverage runtime is not initialised until the first m.Run returns,
//     so the loop opens with a warm-up run that matches no test (^$). That
//     also gets cmd/go's test log created while testing's own numRun counter
//     is still 1, which the later runs then append to.
//   - Only top-level tests are separable. Subtests (including parallel ones)
//     finish inside their parent's run, so they are attributed to it.
//
// # Opt-in, and what "degraded" means
//
// The shim is inert unless ATLAS_COV_DIR names an output root: an unset
// environment is a plain `go test`. `atlas cov run` sets it, along with
// ATLAS_COV_PLAN — a per-package plan naming the packages that must NOT be
// collected per test, chiefly the ones whose tests call t.Parallel() and
// would otherwise be robbed of the concurrency they asked for. Such a
// package is collected as a single per-package snapshot and reports the
// degradation; nothing false is written to the store.
//
// Every package leaves a Report behind (`<root>/reports/<pkg>/shim.json`)
// saying which mode it actually ran in and why, so the collecting side never
// has to infer trustworthiness from the shape of the directory tree.
package shim
