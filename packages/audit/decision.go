package audit

import (
	"context"
	"errors"
	"fmt"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// ---------------------------------------------------------------------------
// Decision coverage signal (issue #140)
// ---------------------------------------------------------------------------
//
// #127 built the CFG, taught `atlas flow` to decide branch outcomes from a
// statement profile, and stored the verdicts in `cfg_decision_coverage`. The
// audit went on scoring from statement coverage alone, so the strongest
// evidence atlas collects never reached the number anyone reads.
//
// Wiring it in is not mechanical, because scoreFromFeature re-normalises its
// weighted average over whichever signals are AVAILABLE. A signal that
// reports 0 when it has no data does not add a component — it lowers the score
// of every feature it cannot see, the moment anybody runs `atlas flow` once.
// Everything below exists to keep "unmeasured" and "measured badly" apart:
//
//   - no `cfg_decision_coverage` row on any surface symbol -> the signal is
//     UNAVAILABLE and there is no report at all, so the feature scores and
//     serialises exactly as it did before the signal existed;
//   - rows present but no decidable outcome (a symbol whose only branching is
//     `a && b`, whose operands share one profile counter) -> still
//     UNAVAILABLE, but a report is emitted saying so, because "we looked and
//     could not judge" is a different fact from "nobody looked";
//   - rows with decidable outcomes -> scored as taken/decidable, never
//     taken/total. The difference is the undetermined outcomes, and charging a
//     symbol for outcomes no instrumentation could observe would report a
//     blind spot as a test gap.

// decisionCoverageSignal scores the feature's branch outcomes.
//
// `surface` is the symbol set the statement-coverage signal was scored over
// (nil when that signal did not run, in which case the surface is resolved
// here). Scoring both halves over one surface is what lets blendWeights treat
// them as two readings of one question rather than two questions.
//
// Returns ok=false whenever the ratio does not exist. The report is returned
// independently of ok: it is non-nil as soon as ANY surface symbol carries a
// measurement, so an unscoreable-but-measured feature still says what happened.
func (a *auditImpl) decisionCoverageSignal(
	ctx context.Context,
	links []store.FeatureSymbolLink,
	frontier store.CoverageFrontier,
	surface map[int64]bool,
) (signalResult, *DecisionCoverageReport, bool, error) {
	if surface == nil {
		// The coverage signal never ran (no frontier, so nothing to resolve it
		// against). Decision coverage does not need a coverage run to exist —
		// `atlas flow measure` reads a profile straight off disk — so resolve
		// the surface against the empty frontier, which drops to the static
		// call-edge walk without touching the per-test tables.
		//
		// Recording the tier is not bookkeeping: `surface_source` is a
		// documented output field, and this is the one path where decision
		// coverage is the ONLY signal that resolved a surface. Leaving it
		// unset would blank the field in exactly the case a reader needs it to
		// know what the reported number was computed over.
		wanted, _, source, err := a.resolveWantedSet(ctx, links, frontier)
		if err != nil {
			return signalResult{}, nil, false, err
		}
		surface = wanted
		a.lastSurfaceSource = source
	}
	if len(surface) == 0 {
		return signalResult{}, nil, false, nil
	}

	rep := DecisionCoverageReport{}
	for sid := range surface {
		dc, measured, err := a.decisionCoverageFor(ctx, sid)
		if err != nil {
			return signalResult{}, nil, false, err
		}
		if !measured {
			// Never analysed. Not "no branch was taken" — an absent row is a
			// statement about `atlas flow`'s reach, not about the tests.
			rep.SymbolsUnmeasured++
			continue
		}
		rep.SymbolsMeasured++
		rep.OutcomesTotal += dc.OutcomesTotal
		rep.OutcomesDecidable += dc.OutcomesDecidable
		rep.OutcomesTaken += dc.OutcomesTaken
		rep.OutcomesUndetermined += dc.Undetermined()
	}

	switch {
	case rep.SymbolsMeasured == 0:
		// Nothing on this surface has ever been measured. No report either:
		// emitting one here would put a `decision_coverage` object into the
		// JSON of every feature in every store that has never run `atlas
		// flow`, which is a schema change for readers who gained no fact.
		return signalResult{}, nil, false, nil
	case rep.OutcomesDecidable == 0:
		// Measured, and unjudgeable. Unavailable, so the weighted average
		// re-normalises past it exactly as if the rows were absent — but the
		// report goes out, because an operator who just ran `atlas flow` over
		// this feature needs to see that it ran and found nothing to decide.
		return signalResult{}, &rep, false, nil
	}

	rep.Available = true
	rep.Percent = 100 * float64(rep.OutcomesTaken) / float64(rep.OutcomesDecidable)
	return signalResult{
		score: rep.Percent,
		note:  decisionCoverageNote(rep),
	}, &rep, true, nil
}

// decisionCoverageFor reads one symbol's measurement, memoised for the life of
// the Audit instance. The bool is "a row exists"; shared.ErrNotFound is the
// store's way of saying the symbol was never analysed, and it is an answer
// rather than a failure.
func (a *auditImpl) decisionCoverageFor(ctx context.Context, symbolID int64) (store.DecisionCoverage, bool, error) {
	if a.decisionCache == nil {
		a.decisionCache = make(map[int64]*store.DecisionCoverage)
	}
	if cached, seen := a.decisionCache[symbolID]; seen {
		if cached == nil {
			return store.DecisionCoverage{}, false, nil
		}
		return *cached, true, nil
	}
	dc, err := a.store.ControlFlow().GetDecisionCoverage(ctx, symbolID)
	if errors.Is(err, shared.ErrNotFound) {
		a.decisionCache[symbolID] = nil
		return store.DecisionCoverage{}, false, nil
	}
	if err != nil {
		return store.DecisionCoverage{}, false, fmt.Errorf("decision coverage (symbol %d): %w", symbolID, err)
	}
	a.decisionCache[symbolID] = &dc
	return dc, true, nil
}

// decisionCoverageNote is the operator-facing explanation.
//
// It names the undetermined count because without it the reader cannot tell an
// untested branch from one nothing could have judged, and those call for
// opposite actions: write a test, versus accept the limit of statement-level
// instrumentation. Score == 100 -> no note (topReasons drops zero-weight
// notes anyway).
func decisionCoverageNote(rep DecisionCoverageReport) signalNote {
	if rep.Percent >= 100 {
		return signalNote{}
	}
	msg := fmt.Sprintf("decision coverage: %d/%d decidable branch outcomes taken (%.0f%%)",
		rep.OutcomesTaken, rep.OutcomesDecidable, rep.Percent)
	if rep.OutcomesUndetermined > 0 {
		msg += fmt.Sprintf("; %d undetermined (not counted either way)", rep.OutcomesUndetermined)
	}
	if rep.SymbolsUnmeasured > 0 {
		msg += fmt.Sprintf("; %d of %d symbols unanalysed",
			rep.SymbolsUnmeasured, rep.SymbolsMeasured+rep.SymbolsUnmeasured)
	}
	return signalNote{weight: 100 - rep.Percent, message: msg}
}

// blendWeights returns the weight map scoreFromFeature blends with.
//
// WHY THE TWO COVERAGE SIGNALS SPLIT ONE BUDGET INSTEAD OF EACH DRAWING THEIR
// OWN.
//
// Statement and decision coverage answer the same question — is this feature's
// behaviour exercised? — at two resolutions, and in atlas they are derived
// from the SAME artefact: `atlas flow` decides a branch outcome by asking
// whether the statements on either side of it ran, using the counters `atlas
// cov` already ingested. They are not two independent witnesses. Adding
// decision coverage at its own full weight would give one measurement, counted
// twice, roughly 57% of a score that also has to carry pattern compliance and
// contract drift, and a feature could then be sunk or saved by its test suite
// alone. So the pair splits the configured coverage weight rather than adding
// to it.
//
// WHY THE SPLIT LEANS TOWARD DECISION COVERAGE (0.6, i.e. 1.5:1).
//
// Decision coverage subsumes the statement verdict over the branches it can
// judge: an outcome cannot be taken if the statements behind it never ran,
// while a statement can run with its branch only ever taken one way. 100%
// statement coverage is routinely compatible with half the error paths never
// being entered, which is the failure mode #127 was opened about — so weighting
// the two equally would understate the stronger evidence.
//
// It is not the whole story either. It is judged only over the DECIDABLE
// outcomes; short-circuit operands, and every symbol with no CFG row, fall
// outside it and are exactly what the statement half still sees. That is why
// the statement half keeps a real share rather than being replaced, and why
// the lean stops at 1.5:1 — at 3:1 or beyond a feature's score would swing on a
// signal that goes blind on `&&`.
//
// WHY THE SHARE IS SCALED BY HOW MUCH OF THE SURFACE WAS MEASURED.
//
// The availability rule keeps an unmeasured feature out of the signal
// entirely, but on its own it says nothing about a PARTIALLY measured one. The
// ratio already handles that correctly — it is computed over the measured
// symbols and reports the rest as symbols_unmeasured — and applying the full
// share to it would not: one symbol carrying a cfg row out of a hundred would
// move 0.24 of the 0.40 budget, 60% of everything the score says about
// testing, onto evidence covering 1% of the feature. The score would then
// swing on a reading whose own report admits it saw almost nothing.
//
// So the share is scaled by measured / (measured + unmeasured): the fraction
// of the surface the reading actually covers. This is proportional rather than
// a threshold on purpose — a cutoff would need a number nothing here can
// justify, and would make the score jump at whatever value that number took.
// The unmeasured remainder stays with statement coverage, which DID see those
// symbols; a fully measured surface scales by 1 and blends exactly as the
// paragraphs above describe.
//
// WHY THE UNAVAILABLE CASE RETURNS THE MAP UNTOUCHED.
//
// This is the property that decides whether the signal survives contact with a
// real repo. A feature with no decision-coverage rows must blend exactly the
// weights it always did, so that adopting `atlas flow` cannot move the score of
// anything it has not measured. Availability, not zero — the split only ever
// applies to features the signal can actually see.
func (a *auditImpl) blendWeights(available map[string]bool, rep *DecisionCoverageReport) map[string]float64 {
	if !available[SignalDecisionCoverage] {
		return a.opts.Weights
	}
	budget := a.opts.Weights[SignalVerification]
	if budget <= 0 {
		budget = defaultWeights()[SignalVerification]
	}
	share := a.opts.DecisionCoverageShare
	if share <= 0 || share >= 1 {
		share = defaultDecisionCoverageShare
	}
	share *= measuredSurfaceFraction(rep)

	out := make(map[string]float64, len(a.opts.Weights)+1)
	for k, v := range a.opts.Weights {
		out[k] = v
	}
	if !available[SignalVerification] {
		// `atlas flow` ran against a profile `atlas cov` never ingested. The
		// budget belongs to the question, not to either half of it, so the
		// half that CAN answer holds it — but only in proportion to the
		// surface it actually read. There is no other half to hand the
		// remainder to, so it goes unspent and weightedAverage re-normalises
		// it away, which is the same treatment an absent signal gets.
		out[SignalDecisionCoverage] = budget * measuredSurfaceFraction(rep)
		return out
	}
	out[SignalVerification] = budget * (1 - share)
	out[SignalDecisionCoverage] = budget * share
	return out
}

// measuredSurfaceFraction is how much of the feature's surface the decision
// reading covers: measured symbols over measured + unmeasured. It is the
// scaling factor blendWeights applies to the share.
//
// A nil report, or one whose surface is empty, returns 0 — but neither reaches
// blendWeights with the signal available, because a signal with no measured
// symbol is never available in the first place.
func measuredSurfaceFraction(rep *DecisionCoverageReport) float64 {
	if rep == nil {
		return 0
	}
	total := rep.SymbolsMeasured + rep.SymbolsUnmeasured
	if total <= 0 {
		return 0
	}
	return float64(rep.SymbolsMeasured) / float64(total)
}
