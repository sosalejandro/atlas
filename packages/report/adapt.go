package report

import (
	"fmt"
	"sort"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/diagnose"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Anchor is the source position a finding about a non-positional subject is
// reported at. A feature is not a place in the file system; the anchor is the
// place a reviewer should look, usually the feature's primary implementation
// symbol.
type Anchor struct {
	Path    string
	Line    int
	EndLine int
}

// AuditThresholds turn a continuous health score into the three-level severity
// CI renders. Both are exclusive lower bounds on the score.
//
// The bands are the caller's policy, not this package's: a repo mid-migration
// gates at 40 and a mature one at 80, and hard-coding either would make the
// annotation say something the team did not decide.
type AuditThresholds struct {
	// ErrorBelow: scores under this are errors. 0 disables the band, so a
	// zero-value AuditThresholds reports nothing as an error.
	ErrorBelow float64

	// WarnBelow: scores under this (and at or above ErrorBelow) are
	// warnings. Scores at or above it produce no finding at all — a healthy
	// feature is not news, and annotating it would bury the unhealthy ones.
	WarnBelow float64
}

// FromAudit converts feature health scores into findings.
//
// Features scoring at or above WarnBelow are dropped, and so are features with
// no anchor: a feature whose annotation links to no indexed symbol has no line
// to hang a warning on, and picking an arbitrary one would point the reviewer
// at unrelated code. The caller is expected to report the unanchored count
// separately rather than let those features vanish — see `atlas report`'s
// warnings.
func FromAudit(healths []audit.FeatureHealth, anchors map[shared.FeatureID]Anchor, t AuditThresholds) []Finding {
	out := make([]Finding, 0, len(healths))
	for _, h := range healths {
		sev, ok := auditSeverity(h.Score, t)
		if !ok {
			continue
		}
		anchor, ok := anchors[h.FeatureID]
		if !ok || anchor.Path == "" {
			continue
		}
		out = append(out, Finding{
			RuleID:   RuleFeatureUncovered,
			Severity: sev,
			Path:     anchor.Path,
			Line:     anchor.Line,
			EndLine:  anchor.EndLine,
			// The score is deliberately absent from the identity: it
			// moves on every coverage run, and folding it in would make
			// GitHub re-report the same unhealthy feature every push.
			Identity: "feature:" + string(h.FeatureID),
			Message:  auditMessage(h),
		})
	}
	return Sort(out)
}

func auditSeverity(score float64, t AuditThresholds) (Severity, bool) {
	switch {
	case t.ErrorBelow > 0 && score < t.ErrorBelow:
		return SeverityError, true
	case t.WarnBelow > 0 && score < t.WarnBelow:
		return SeverityWarning, true
	default:
		return "", false
	}
}

// auditMessage names the score and the weakest signal behind it. "scores 31"
// alone tells a reviewer nothing actionable; "coverage 12.5" tells them where
// to start.
func auditMessage(h audit.FeatureHealth) string {
	msg := fmt.Sprintf("feature %s scores %.1f/100", h.FeatureID, h.Score)
	if name, val, ok := weakestSignal(h.Components); ok {
		msg += fmt.Sprintf(" (weakest signal: %s %.1f)", name, val)
	}
	if len(h.Reasons) > 0 {
		msg += " — " + h.Reasons[0]
	}
	return msg
}

// weakestSignal returns the lowest-scoring component, ties broken by name so
// the message is stable between runs over identical data.
func weakestSignal(components map[string]float64) (string, float64, bool) {
	if len(components) == 0 {
		return "", 0, false
	}
	names := make([]string, 0, len(components))
	for k := range components {
		names = append(names, k)
	}
	sort.Strings(names)
	best := names[0]
	for _, n := range names[1:] {
		if components[n] < components[best] {
			best = n
		}
	}
	return best, components[best], true
}

// FromCoverageGaps converts a run's unattributed-execution rows into findings.
//
// minStmts drops the long tail: a file with three unattributed statements is
// rounding error, and a PR comment listing hundreds of them hides the file that
// lost four hundred.
//
// Note that these paths are frequently NOT repo-relative — a Go coverprofile
// names files by import path — which is the point of the rule: atlas could not
// map the file to the checkout. NormalizePaths drops what it cannot resolve,
// so those gaps survive in the sticky comment (which needs no line anchor) and
// not in SARIF (which does).
func FromCoverageGaps(gaps []store.CoverageGap, minStmts int) []Finding {
	out := make([]Finding, 0, len(gaps))
	for _, g := range gaps {
		if g.Stmts < minStmts {
			continue
		}
		out = append(out, Finding{
			RuleID:   RuleCoverageUnattributed,
			Severity: SeverityNote,
			Path:     g.Path,
			// A gap is a property of the whole file, so it is anchored at
			// the top of it rather than at a line the gap does not have.
			Line: 1,
			// The statement count is excluded from the identity on
			// purpose: the same blind spot growing from 118 to 130
			// statements is the same blind spot.
			Identity: "gap:" + g.Path,
			Message: fmt.Sprintf("%s executed but charged to no indexed symbol (reason: %s)",
				plural(g.Stmts, "statement"), g.Reason),
		})
	}
	return Sort(out)
}

// FromDeadCode converts dead-code candidates into findings.
//
// They are notes, never warnings: FindDead's own documentation calls the output
// a candidate list rather than a verdict, and a note is the level that says
// "look at this" without failing anyone's gate.
func FromDeadCode(candidates []store.DeadCodeCandidate) []Finding {
	out := make([]Finding, 0, len(candidates))
	for _, c := range candidates {
		end := 0
		if c.Symbol.EndLine != nil {
			end = *c.Symbol.EndLine
		}
		kind := c.EdgeKind
		if kind == "" {
			kind = "any"
		}
		out = append(out, Finding{
			RuleID:   RuleDeadCode,
			Severity: SeverityNote,
			Path:     c.Symbol.FilePath,
			Line:     c.Symbol.Line,
			EndLine:  end,
			Identity: "symbol:" + string(c.Symbol.QualifiedName),
			Message: fmt.Sprintf("%s has 0 incoming %s edges (dead-code candidate; dynamic dispatch and plugin entry points are invisible here)",
				c.Symbol.QualifiedName, kind),
		})
	}
	return Sort(out)
}

// FromDiagnose converts ranked diagnosis matches into findings, dropping
// anything below minConfidence.
//
// Diagnoses are notes by construction: they are a ranking over "where would
// this symptom come from", and treating a ranking as a defect would fail a
// build over a guess.
func FromDiagnose(matches []diagnose.Match, minConfidence float64) []Finding {
	out := make([]Finding, 0, len(matches))
	for _, m := range matches {
		if m.Confidence < minConfidence {
			continue
		}
		out = append(out, Finding{
			RuleID:   RuleDiagnosis,
			Severity: SeverityNote,
			Path:     m.Symbol.Position.Path,
			Line:     m.Symbol.Position.Line,
			EndLine:  m.Symbol.EndLine,
			// Confidence stays out of the identity: it drifts as the
			// candidate set grows, and a drifting fingerprint means the
			// same diagnosis is reported as new on every run.
			Identity: "symbol:" + string(m.Symbol.ID),
			Message: fmt.Sprintf("%s (confidence %.2f): %s",
				m.Symbol.ID, m.Confidence, m.Reason),
		})
	}
	return Sort(out)
}
