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
// Four measurement hazards shape every type in here, because a naive
// implementation of a trend line lies in all four ways:
//
//   - A denominator change is not a coverage change. Deleting a thousand
//     untested lines raises the number without a single new test. Every
//     score therefore travels with the size of the surface it was computed
//     over, IN THE SAME UNIT as the score — statements for the Tier B
//     coverage signal, because a denominator counted in symbols cannot see a
//     statement-level deletion and so guards nothing. Compare reports a
//     denominator move explicitly instead of presenting the delta as a
//     quality signal.
//
//   - Absent evidence is not a zero. A commit nobody ran the suite for has
//     no score, not a score of nought. That is a *float64 throughout, and a
//     comparison against an unmeasured BASELINE refuses rather than
//     fabricating a regression. The mirror case is not symmetrical: a head
//     that lost a measurement the baseline had fails the gate, or a change
//     that deletes the coverage step passes every gate there is.
//
//   - A blend is not a coverage number. The series records the audit's
//     coverage COMPONENT, never its re-normalised overall score — otherwise
//     a "coverage regression" fires on a stale annotation, and a real
//     coverage drop hides behind pattern compliance rising.
//
//   - A gate with no tolerance fails builds on noise. CompareOptions carries
//     one, with a default justified where it is declared.
//
// Dependency direction: trend imports store and audit; neither imports
// trend.
package trend
