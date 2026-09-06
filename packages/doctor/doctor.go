// Package doctor answers one question: is atlas's picture of this repo
// still true?
//
// Every number atlas prints -- a coverage percentage, an audit score, a
// sprint ranking -- is downstream of a scan and an ingest, and both go
// stale silently. A stale index does not error; it answers confidently
// about a repo that no longer exists. That is the worst failure mode an
// analysis tool has, because nothing in the output distinguishes it from
// a correct answer. doctor is the cheap command whose whole job is to say
// "your inputs are wrong" (issue #88).
//
// Two rules shape everything here:
//
//  1. Report EVERY check, not just the failures. A doctor that prints
//     nothing when healthy has taught the user nothing about what it
//     examined, and leaves them unable to tell "checked and fine" from
//     "never looked".
//
//  2. A check that cannot run says so. SeverityNotApplicable with a
//     stated reason -- never SeverityOK. Reporting "ok" for a check that
//     had no input to examine is precisely the confident-but-wrong answer
//     doctor exists to catch.
//
// Reads go through the store's ports wherever a port exists. The one
// exception is probe.go, which opens its own read-only handle for the
// facts no port exposes; see that file for why.
package doctor

import (
	"context"
	"fmt"
	"sort"
)

// Severity is a check's verdict. The zero value is deliberately not a
// valid severity: a Result that was never filled in must not read as ok.
type Severity string

const (
	// SeverityOK means the check ran, examined real input, and found
	// nothing wrong.
	SeverityOK Severity = "ok"

	// SeverityWarn means the check found something that degrades atlas's
	// answers without invalidating them.
	SeverityWarn Severity = "warn"

	// SeverityFail means the check found something that makes atlas's
	// answers untrustworthy.
	SeverityFail Severity = "fail"

	// SeverityNotApplicable means the check could not run because its
	// input does not exist yet (no coverage ingested, no features
	// declared). It always carries the reason in Finding. It is NOT a
	// pass: it is the honest "I did not look, and here is why".
	SeverityNotApplicable Severity = "n/a"
)

// rank orders severities for "worst wins" aggregation and for the
// --fail-on threshold comparison. NotApplicable ranks BELOW ok on
// purpose: it must never trip a gate on its own, because "no coverage
// ingested yet" is a normal state for a repo mid-adoption and a CI gate
// that fails on it would simply get switched off.
func (s Severity) rank() int {
	switch s {
	case SeverityNotApplicable:
		return 0
	case SeverityOK:
		return 1
	case SeverityWarn:
		return 2
	case SeverityFail:
		return 3
	default:
		return -1
	}
}

// AtLeast reports whether s is at or above threshold, i.e. whether a
// gate set at threshold should fire on it. A threshold that is not one of
// the two gateable severities never fires, so a caller that skipped
// ParseSeverity cannot accidentally build a gate that fails everything.
func (s Severity) AtLeast(threshold Severity) bool {
	if threshold.rank() < SeverityWarn.rank() {
		return false
	}
	return s.rank() >= threshold.rank()
}

// ParseSeverity resolves the --fail-on flag value. Only the two
// severities a gate can meaningfully sit at are accepted: "ok" and "n/a"
// as thresholds would fail every healthy repo.
func ParseSeverity(s string) (Severity, error) {
	switch Severity(s) {
	case SeverityWarn:
		return SeverityWarn, nil
	case SeverityFail:
		return SeverityFail, nil
	default:
		return "", fmt.Errorf("doctor: unknown severity %q (want \"warn\" or \"fail\")", s)
	}
}

// Result is one check's verdict, in the shape both the human output and
// the --json envelope render directly.
//
// Remediation is a concrete command, not advice: "the index is stale" is
// a diagnosis the user cannot act on, "atlas scan" is one they can. It is
// empty only when there is nothing to do.
type Result struct {
	Name        string   `json:"name"`
	Examines    string   `json:"examines"`
	Severity    Severity `json:"severity"`
	Finding     string   `json:"finding"`
	Remediation string   `json:"remediation,omitempty"`

	// Details carries the numbers behind Finding so a JSON consumer can
	// gate on them without parsing prose. Sample path lists in here are
	// capped (see maxSamples); the counts beside them stay exact.
	Details map[string]any `json:"details,omitempty"`
}

// Check is one question doctor asks. Implementations are stateless -- all
// input arrives through Env -- so the check set can be reordered, subset
// or run twice without surprise.
type Check interface {
	// Name is the stable identifier ("index.freshness"). JSON consumers
	// and CI gates key off it, so it does not change once shipped.
	Name() string

	// Examines is the one-line statement of what this check looks at,
	// printed whatever the verdict. It is what makes a clean report
	// informative rather than empty.
	Examines() string

	// Run returns the verdict. It returns an error only when the check
	// could not complete for a reason that is NOT "the input does not
	// exist yet" -- that case is a not-applicable Result, not an error.
	Run(ctx context.Context, env *Env) (Result, error)
}

// Counts is the per-severity tally. A struct rather than a map so the
// JSON key order is fixed and the output stays diffable across runs.
type Counts struct {
	OK            int `json:"ok"`
	Warn          int `json:"warn"`
	Fail          int `json:"fail"`
	NotApplicable int `json:"n/a"`
}

// Report is the whole run: every check's Result, in the order the checks
// were given, plus the aggregate a caller gates on.
type Report struct {
	Checks []Result `json:"checks"`
	Counts Counts   `json:"counts"`

	// Worst is the highest-ranked severity in Checks; SeverityOK for an
	// empty report.
	Worst Severity `json:"worst"`
}

// TrippedBy reports whether this report should fail a gate set at
// threshold. `atlas doctor` exits non-zero exactly when this is true.
func (r Report) TrippedBy(threshold Severity) bool {
	return r.Worst.AtLeast(threshold)
}

// DefaultChecks is the check set `atlas doctor` runs, ordered so the
// report reads outward from the thing everything else depends on: the
// index is upstream of every coverage number, which is upstream of every
// audit score. Schema comes last because it is a statement about the
// store, not about the repo.
func DefaultChecks() []Check {
	return []Check{
		indexFreshness{},
		coverageFreshness{},
		coverageAttribution{},
		featureLinkage{},
		schemaVersion{},
	}
}

// Run executes every check against env and aggregates the results.
//
// A check that returns an error is reported as SeverityFail -- not
// skipped, and not downgraded to not-applicable. An errored check means
// atlas could not verify its own state, which from the user's side is
// indistinguishable from a broken state; calling that "n/a" would let a
// broken store exit 0, which is the entire class of bug doctor exists to
// close.
//
// Run itself errors only on setup that fails before any check can be
// attempted.
func Run(ctx context.Context, env *Env, checks []Check) (Report, error) {
	if env == nil {
		return Report{}, fmt.Errorf("doctor: env is required")
	}
	e := env.withDefaults()
	// A probe that will not open is not fatal here: the checks that need
	// it degrade to their own not-applicable path carrying the reason,
	// and the checks that do not need it still run.
	e.probeErr = e.openProbe()
	defer e.closeProbe()

	rep := Report{Checks: make([]Result, 0, len(checks)), Worst: SeverityOK}
	for _, c := range checks {
		res, err := c.Run(ctx, e)
		if err != nil {
			res = Result{
				Severity: SeverityFail,
				Finding:  fmt.Sprintf("the check could not complete: %v", err),
			}
		}
		// Name and Examines are taken from the Check, never from the
		// Result, so a check cannot mislabel itself on one branch and
		// not another.
		res.Name = c.Name()
		res.Examines = c.Examines()
		if res.Severity == "" {
			res.Severity = SeverityFail
			res.Finding = "the check returned no verdict (bug in " + res.Name + ")"
		}
		rep.Checks = append(rep.Checks, res)
		rep.Counts.add(res.Severity)
		if res.Severity.rank() > rep.Worst.rank() {
			rep.Worst = res.Severity
		}
	}
	return rep, nil
}

func (c *Counts) add(s Severity) {
	switch s {
	case SeverityOK:
		c.OK++
	case SeverityWarn:
		c.Warn++
	case SeverityFail:
		c.Fail++
	case SeverityNotApplicable:
		c.NotApplicable++
	}
}

// maxSamples caps how many example paths a Result carries in Details.
// The counts beside them stay exact; the list exists to make the finding
// actionable, not to be an inventory. One `atlas scan` fixes all of them
// at once, so a wall of 4000 paths would only bury the number that
// matters.
const maxSamples = 10

// samples sorts and truncates a path list for inclusion in Details.
//
// Sorted so two runs over the same state produce byte-identical JSON,
// which is what makes the output diffable in CI. Always non-nil, so the
// empty case marshals as [] rather than null and a consumer can iterate
// it without a null check.
func samples(paths []string) []string {
	out := make([]string, 0, len(paths))
	out = append(out, paths...)
	sort.Strings(out)
	if len(out) > maxSamples {
		out = out[:maxSamples]
	}
	return out
}
