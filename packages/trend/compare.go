package trend

import (
	"fmt"
	"math"
	"sort"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Verdict is the outcome of comparing one scope between two commits.
type Verdict string

const (
	// VerdictImproved: the score rose by more than the tolerance.
	VerdictImproved Verdict = "improved"
	// VerdictUnchanged: the move is inside the tolerance band.
	VerdictUnchanged Verdict = "unchanged"
	// VerdictRegressed: the score fell by more than the tolerance. This is
	// the only verdict that fails the gate.
	VerdictRegressed Verdict = "regressed"
	// VerdictNoEvidence: neither side carries a measurement this commit
	// could have lost — the baseline was never measured, or nothing was
	// measured on either side. Explicitly NOT a gate failure: failing a
	// build because nobody ever ran the suite on the baseline punishes the
	// wrong PR.
	VerdictNoEvidence Verdict = "no_evidence"
	// VerdictUnmeasured: the BASELINE carried a measurement and this commit
	// does not. That is not the innocent case, and collapsing the two is how
	// a PR that breaks measurement outright sails through the gate — delete
	// the coverage step and every scope reports "no evidence", which used to
	// pass. It FAILS the gate.
	VerdictUnmeasured Verdict = "unmeasured"
	// VerdictNew: the feature exists at head and not at base.
	VerdictNew Verdict = "new"
	// VerdictRemoved: the feature existed at base and not at head.
	VerdictRemoved Verdict = "removed"
)

// DefaultMaxRegression is the score drop, in points, that the gate tolerates
// before calling a regression.
//
// Why 0.5 and not 0. Two noise sources sit under a coverage score. Float
// accumulation over a few thousand weighted terms in unspecified map order
// contributes on the order of 1e-12 points, which is irrelevant. What
// actually moves the number between two runs of the SAME code is measurement
// jitter: a timing-sensitive test that skips a branch, a parallel scheduler
// that changes which init path executes, a flaky integration test excluded on
// a retry. That is empirically a fraction of a point on a repo of any size,
// and a gate that fires on it gets switched off within a week — which costs
// far more coverage than it ever protected.
//
// 0.5 is the smallest round number above that jitter. It is a floor on
// SENSITIVITY, not a licence to lose half a point per PR: the per-feature
// comparison runs at the same tolerance, so a single feature falling off a
// cliff still fails the gate no matter how flat the headline stays. Teams
// wanting a strict gate pass --max-regression 0, which this code honours
// exactly (a zero option value is a real zero, not "unset" — see
// withDefaults).
const DefaultMaxRegression = 0.5

// DefaultDenominatorTolerance is the fraction by which the measured surface
// may move between two points before the comparison flags the delta as
// measuring different things.
//
// 2% because a normal PR touching a handful of symbols moves the denominator
// well under it, while the changes that actually invalidate a comparison —
// deleting a package, a scanner change that alters what counts as a symbol,
// a feature gaining or losing coverage evidence wholesale — clear it easily.
const DefaultDenominatorTolerance = 0.02

// CompareOptions tunes the gate.
//
// The zero value means "take the defaults", which forces a wrinkle: a caller
// asking for a genuinely zero tolerance cannot express it as a zero field.
// MaxRegression is therefore a *float64 — nil takes the default, and a
// pointer to 0 is an honest zero-tolerance gate.
type CompareOptions struct {
	MaxRegression        *float64
	DenominatorTolerance *float64
}

func (o CompareOptions) maxRegression() float64 {
	if o.MaxRegression == nil {
		return DefaultMaxRegression
	}
	return math.Abs(*o.MaxRegression)
}

func (o CompareOptions) denominatorTolerance() float64 {
	if o.DenominatorTolerance == nil {
		return DefaultDenominatorTolerance
	}
	return math.Abs(*o.DenominatorTolerance)
}

// Comparison is one scope's delta between two commits.
//
// Delta is nil exactly when Verdict is one of the three "nothing to compare"
// values. DenominatorMoved says the two scores were computed over materially
// different surfaces, which makes Delta a fact about the measurement rather
// than about quality. The delta is still reported when that happens —
// suppressing it would hide real news — but never without the flag.
type Comparison struct {
	Scope string `json:"scope"`

	BaseScore *float64 `json:"base_score"`
	HeadScore *float64 `json:"head_score"`
	Delta     *float64 `json:"delta"`

	BaseDenominator  int64   `json:"base_denominator"`
	HeadDenominator  int64   `json:"head_denominator"`
	DenominatorDelta int64   `json:"denominator_delta"`
	DenominatorShift float64 `json:"denominator_shift"`
	DenominatorMoved bool    `json:"denominator_moved"`

	Verdict Verdict `json:"verdict"`
	Note    string  `json:"note,omitempty"`
}

// Report is the full result of comparing two history points: the headline
// project comparison, every feature's comparison worst-first, and the gate
// verdict.
type Report struct {
	BaseCommit string `json:"base_commit"`
	HeadCommit string `json:"head_commit"`

	Project  Comparison   `json:"project"`
	Features []Comparison `json:"features"`

	// Regressed is true when the project OR any single feature fell by more
	// than the tolerance. The per-feature term is the point of the whole
	// exercise: a PR that drops one capability from 80 to 40 must fail even
	// while the repo-wide average stays respectable.
	Regressed bool `json:"regressed"`

	// Unmeasured is true when the project or any single feature was measured
	// at the baseline and is not measured now. It is reported separately from
	// Regressed because it is a different event with a different fix: the
	// score did not fall, the measurement disappeared.
	Unmeasured bool `json:"unmeasured"`

	// Failed is THE GATE — Regressed or Unmeasured. Both must fail: a change
	// that deletes the coverage step scores no worse than one that deletes
	// the tests, and a gate that only reads Regressed passes it.
	Failed bool `json:"failed"`

	// Warnings carry the caveats a reader must see before trusting the
	// numbers: absent evidence, and denominators that moved.
	Warnings []string `json:"warnings,omitempty"`

	// MaxRegression and DenominatorTolerance record the thresholds the
	// verdicts were reached under, so a CI log explains itself without the
	// reader reconstructing the invocation.
	MaxRegression        float64 `json:"max_regression"`
	DenominatorTolerance float64 `json:"denominator_tolerance"`
}

// Compare produces the delta report between two history points.
//
// base is the older side (the `--compare-to` target), head the newer.
func Compare(base, head store.HistoryPoint, opts CompareOptions) Report {
	tol := opts.maxRegression()
	denomTol := opts.denominatorTolerance()

	rep := Report{
		BaseCommit:           base.CommitSHA,
		HeadCommit:           head.CommitSHA,
		MaxRegression:        tol,
		DenominatorTolerance: denomTol,
	}

	rep.Project = compareScope(ScopeProject,
		&scopeSide{score: base.Score, denominator: base.Denominator},
		&scopeSide{score: head.Score, denominator: head.Denominator},
		tol, denomTol)
	rep.Features = compareFeatures(base, head, tol, denomTol)

	rep.Regressed, rep.Unmeasured = gateVerdicts(rep.Project, rep.Features)
	rep.Failed = rep.Regressed || rep.Unmeasured
	rep.Warnings = collectWarnings(base, head, rep)
	return rep
}

// gateVerdicts folds the project headline and every feature into the two
// gate-failing conditions. Both scan every scope: the whole point of the
// per-feature term is that one capability can fall (or stop being measured)
// while the headline stays flat.
func gateVerdicts(project Comparison, features []Comparison) (regressed, unmeasured bool) {
	for _, c := range append([]Comparison{project}, features...) {
		switch c.Verdict {
		case VerdictRegressed:
			regressed = true
		case VerdictUnmeasured:
			unmeasured = true
		}
	}
	return regressed, unmeasured
}

// scopeSide is one end of a comparison. A nil *scopeSide means the scope did
// not exist on that side at all (a feature added or deleted), which is a
// different fact from existing with no score.
type scopeSide struct {
	score       *float64
	denominator int64
}

// compareScope is the single place a verdict is decided, shared by the
// project headline and every feature so the two can never drift apart.
func compareScope(scope string, base, head *scopeSide, tol, denomTol float64) Comparison {
	c := Comparison{Scope: scope}

	switch {
	case base == nil && head == nil:
		c.Verdict = VerdictNoEvidence
		c.Note = "absent on both sides"
		return c
	case base == nil:
		c.HeadScore, c.HeadDenominator = head.score, head.denominator
		c.Verdict = VerdictNew
		c.Note = "no baseline at the compared commit"
		return c
	case head == nil:
		c.BaseScore, c.BaseDenominator = base.score, base.denominator
		c.Verdict = VerdictRemoved
		c.Note = "present at the baseline, absent now"
		return c
	}

	c.BaseScore, c.BaseDenominator = base.score, base.denominator
	c.HeadScore, c.HeadDenominator = head.score, head.denominator
	c.DenominatorDelta = head.denominator - base.denominator
	c.DenominatorShift = relativeShift(base.denominator, head.denominator)
	c.DenominatorMoved = math.Abs(c.DenominatorShift) > denomTol

	if base.score == nil || head.score == nil {
		// Absent evidence is not a zero, so there is no delta to judge —
		// but WHICH side is absent decides whether this is innocent. A
		// baseline nobody measured is not this PR's fault; a head that
		// produced no measurement when the baseline had one is a
		// measurement this change destroyed, and must not pass quietly.
		c.Verdict = VerdictNoEvidence
		if base.score != nil && head.score == nil {
			c.Verdict = VerdictUnmeasured
		}
		c.Note = missingEvidenceNote(base.score == nil, head.score == nil)
		return c
	}

	delta := *head.score - *base.score
	c.Delta = &delta
	switch {
	case delta < -tol:
		c.Verdict = VerdictRegressed
	case delta > tol:
		c.Verdict = VerdictImproved
	default:
		c.Verdict = VerdictUnchanged
	}
	if c.DenominatorMoved {
		c.Note = fmt.Sprintf(
			"measured surface changed %d -> %d (%+.1f%%); the delta is not a like-for-like quality signal",
			base.denominator, head.denominator, c.DenominatorShift*100)
	}
	return c
}

// relativeShift is the denominator change as a signed fraction of the base.
// A base of zero cannot express a ratio: growing from nothing is reported as
// a full shift so it is flagged, and staying at nothing as none.
func relativeShift(base, head int64) float64 {
	if base == 0 {
		if head == 0 {
			return 0
		}
		return 1
	}
	return float64(head-base) / float64(base)
}

func missingEvidenceNote(baseMissing, headMissing bool) string {
	switch {
	case baseMissing && headMissing:
		return "no coverage evidence at either commit"
	case baseMissing:
		return "no coverage evidence at the baseline commit"
	default:
		return "the baseline was measured and this commit was not; the measurement was lost, not the coverage"
	}
}

// compareFeatures produces one Comparison per feature seen on either side,
// ordered worst-delta first so the cliff is the first line a human reads.
// Comparisons with no delta (new, removed, no evidence) sort last: they are
// context, not findings.
func compareFeatures(base, head store.HistoryPoint, tol, denomTol float64) []Comparison {
	baseByID := indexFeatures(base)
	headByID := indexFeatures(head)

	ids := make([]shared.FeatureID, 0, len(baseByID)+len(headByID))
	seen := make(map[shared.FeatureID]bool, len(baseByID)+len(headByID))
	for _, m := range []map[shared.FeatureID]*scopeSide{baseByID, headByID} {
		for id := range m {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}

	out := make([]Comparison, 0, len(ids))
	for _, id := range ids {
		out = append(out, compareScope(string(id), baseByID[id], headByID[id], tol, denomTol))
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := out[i].Delta, out[j].Delta
		switch {
		case di != nil && dj != nil && *di != *dj:
			return *di < *dj
		case di != nil && dj == nil:
			return true
		case di == nil && dj != nil:
			return false
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

func indexFeatures(p store.HistoryPoint) map[shared.FeatureID]*scopeSide {
	out := make(map[shared.FeatureID]*scopeSide, len(p.Features))
	for _, f := range p.Features {
		out[f.FeatureID] = &scopeSide{score: f.Score, denominator: f.Denominator}
	}
	return out
}

// collectWarnings surfaces the caveats that must reach the reader even when
// they skim only the headline: a point with no evidence, and surfaces that
// moved. Per-feature noise is deliberately summarised rather than enumerated
// — a hundred new features should not bury the one that regressed.
func collectWarnings(base, head store.HistoryPoint, rep Report) []string {
	var warnings []string
	if !base.Measured() {
		warnings = append(warnings, fmt.Sprintf(
			"baseline %s has no coverage evidence; its score is unknown, not zero", short(base.CommitSHA)))
	}
	if !head.Measured() {
		if base.Measured() {
			warnings = append(warnings, fmt.Sprintf(
				"%s produced no coverage measurement while the baseline had one; the gate fails on a lost measurement, not on a lost score",
				short(head.CommitSHA)))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"%s has no coverage evidence; its score is unknown, not zero", short(head.CommitSHA)))
		}
	}
	if rep.Project.DenominatorMoved {
		warnings = append(warnings, fmt.Sprintf(
			"measured surface changed %d -> %d symbols (%+.1f%%): compare the denominators before reading the delta as quality",
			rep.Project.BaseDenominator, rep.Project.HeadDenominator, rep.Project.DenominatorShift*100))
	}
	if moved := countDenominatorMoves(rep.Features); moved > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d feature(s) changed measured surface by more than %.0f%%", moved, rep.DenominatorTolerance*100))
	}
	return warnings
}

func countDenominatorMoves(cs []Comparison) int {
	n := 0
	for _, c := range cs {
		if c.DenominatorMoved {
			n++
		}
	}
	return n
}

// short truncates a sha for log lines while leaving short refs alone.
func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
