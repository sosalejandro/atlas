// Package atlastest holds the generators and invariant checks the property
// layer is built from. It lives beside the packages it serves rather than
// inside any one of them because the invariants it encodes are not local:
// "attributed + unattributed == total" is a statement about the coverage
// ingest, but the symbol spans it consumes come from the scanner and the ids
// come from the store, and a generator only one package can reach gets forked
// the moment a second package needs it.
//
// # Why a property layer exists at all
//
// Every bug this repo has shipped and then written a regression test for was
// an invariant violation, not a wrong answer to one specific question:
//
//   - Issue #85: a colliding short name made a whole file's symbols vanish, so
//     every statement in it was charged to nobody. The example test that would
//     have caught it is "two packages both declare Chat.MarkLoaded" — and
//     nobody writes that example until after the incident. The invariant is
//     "every declaration is either indexed or reported as dropped".
//   - The duplicate-coverprofile bug: `-coverpkg=./...` repeats every block
//     once per tested package, so raw summation inflated totals. The example
//     test is "a profile with duplicate blocks"; the invariant is "the
//     statements a run accounts for equal the statements the profile holds".
//   - Issue #97 / PR #99: map-iteration order changed the output. No example
//     test fails for that, because the example passed on the run you wrote it
//     on. The invariant is "the same input renders the same bytes".
//
// So the generators here produce ADVERSARIAL input by construction —
// colliding names, duplicated blocks, unindexed files, spans that touch and
// spans that nest — and the checks in invariants.go state the property the
// code has to hold for all of it. An example test finds a bug only if you
// already guessed it; a property finds the class.
//
// # Dependencies
//
// This package deliberately imports nothing from atlas, and nothing outside
// the standard library. Two reasons.
//
// First, an internal test (`package coverage`) cannot import a helper that
// imports `packages/coverage` — that is an import cycle. Keeping this package
// atlas-free is what lets a property test live in the same package as the
// unexported function whose invariant it is checking, instead of being pushed
// out to an external `_test` package where that function is unreachable.
//
// Second, generating profile TEXT rather than parsed blocks means the property
// exercises the real parser and the real merge step, not a hand-built
// approximation of what they are believed to produce.
//
// # Reproducing a failure
//
// Every generator is driven by an explicit seed, and every property test
// reports the seed it failed on. Re-run one case with:
//
//	go test ./packages/coverage -run TestProperty_Attribution/seed=1234
//
// The generators are pure functions of the seed, so the case is byte-identical
// on any machine and any Go version whose math/rand/v2 PCG is unchanged.
package atlastest
