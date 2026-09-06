// Package trend turns the per-commit history rows written into
// `coverage_history` into a series a human can read and a gate CI can fail
// on (issue #92).
//
// Atlas otherwise reports a snapshot. The question a team actually asks is
// "is this getting better or worse", and the gate that matters is "did this
// PR make it worse" — answerable without ever agreeing an absolute
// threshold, which is what makes it adoptable on a legacy codebase where an
// absolute gate is hopeless.
//
// Three measurement hazards shape every type in here, because a naive
// implementation of a trend line lies in all three ways:
//
//   - A denominator change is not a coverage change. Deleting a thousand
//     untested lines raises the number without a single new test. Every
//     score therefore travels with the size of the surface it was computed
//     over, and Compare reports a denominator move explicitly instead of
//     presenting the delta as a quality signal.
//
//   - Absent evidence is not a zero. A commit nobody ran the suite for has
//     no score, not a score of nought. That is a *float64 throughout, and a
//     comparison against an unmeasured point refuses rather than fabricating
//     a regression.
//
//   - A gate with no tolerance fails builds on noise. CompareOptions carries
//     one, with a default justified where it is declared.
//
// Dependency direction: trend imports store and audit; neither imports
// trend.
package trend
