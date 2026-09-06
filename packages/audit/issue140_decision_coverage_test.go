package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/store"
)

// Issue #140: decision coverage is a signal of its own, blended alongside
// statement coverage and never folded into it.
//
// Every test here exists because of a specific way the integration could go
// wrong, and each names the wrong answer it rejects.

// seedDecision writes one symbol's decision-coverage measurement, the way
// `atlas flow measure` would.
func seedDecision(t *testing.T, s *store.Store, symbolID int64, total, decidable, taken int) {
	t.Helper()
	err := s.ControlFlow().SetDecisionCoverage(context.Background(), store.DecisionCoverage{
		SymbolID:          symbolID,
		OutcomesTotal:     total,
		OutcomesDecidable: decidable,
		OutcomesTaken:     taken,
		Source:            "cover.out",
	})
	if err != nil {
		t.Fatalf("SetDecisionCoverage(symbol %d): %v", symbolID, err)
	}
}

// TestDecisionCoverage_UnmeasuredFeatureScoresExactlyAsBefore is the
// regression this whole issue is about.
//
// scoreFromFeature re-normalises over the AVAILABLE signals, so a signal that
// reports 0 when it has no data does not merely add a component — it drags
// every previously-scored feature down the moment anyone runs `atlas flow`.
// That is how a signal gets switched off in the first week. A feature whose
// symbols carry no cfg_decision_coverage row must therefore report the signal
// UNAVAILABLE, and its score must be bit-for-bit what it was.
//
// The second half is the part with teeth: another feature in the SAME store
// adopts decision coverage, and the unmeasured feature must not move.
func TestDecisionCoverage_UnmeasuredFeatureScoresExactlyAsBefore(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	measured := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 2, SymbolFile: "auth/login.go",
	})
	unmeasured := seedFeature(t, s, seedSpec{
		FeatureID: "billing.charge", Title: "Charge", NumSymbols: 4, SymbolFile: "billing/charge.go",
	})
	// billing.charge: 2 of 4 impl symbols executed -> statement coverage 50.
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		measured[0]:   store.StatusPass,
		measured[1]:   store.StatusPass,
		unmeasured[0]: store.StatusPass,
		unmeasured[1]: store.StatusPass,
		unmeasured[2]: store.StatusFail,
		unmeasured[3]: store.StatusFail,
	})

	before, err := New(s, Options{}).ScoreFeature(ctx, "billing.charge")
	if err != nil {
		t.Fatalf("ScoreFeature (before): %v", err)
	}
	if before.Score != 50 {
		t.Fatalf("baseline score = %.4f, want 50 (2 of 4 impl symbols passing)", before.Score)
	}
	if _, ok := before.Components[SignalDecisionCoverage]; ok {
		t.Errorf("decision_coverage component present (%.2f) for a store with no cfg rows; "+
			"an unmeasured signal must be absent, not zero", before.Components[SignalDecisionCoverage])
	}
	if before.Decision != nil {
		t.Errorf("Decision report = %+v for a store with no cfg rows; want nil", before.Decision)
	}

	// Someone runs `atlas flow` — but only over auth.login's symbols.
	seedDecision(t, s, measured[0], 4, 4, 4)
	seedDecision(t, s, measured[1], 4, 4, 4)

	after, err := New(s, Options{}).ScoreFeature(ctx, "billing.charge")
	if err != nil {
		t.Fatalf("ScoreFeature (after): %v", err)
	}
	if after.Score != before.Score {
		t.Errorf("unmeasured feature moved from %.4f to %.4f when a NEIGHBOUR adopted decision coverage; "+
			"want no movement at all", before.Score, after.Score)
	}
	if len(after.Components) != len(before.Components) {
		t.Errorf("components changed: before %v, after %v", before.Components, after.Components)
	}
	for k, v := range before.Components {
		if after.Components[k] != v {
			t.Errorf("component %q moved from %.4f to %.4f", k, v, after.Components[k])
		}
	}

	// And the feature that WAS measured must actually carry the signal —
	// otherwise this test would pass against an implementation that does
	// nothing at all.
	live, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature auth.login: %v", err)
	}
	if v, ok := live.Components[SignalDecisionCoverage]; !ok || v != 100 {
		t.Errorf("auth.login decision_coverage = %.2f (present=%v), want 100", v, ok)
	}
}

// TestDecisionCoverage_UndeterminedMovesNeitherNumeratorNorDenominator pins
// the verdict #139 went out of its way to make first-class.
//
// An undetermined outcome is one no statement-coverage profile can judge (the
// operands of `a && b` share a counter). Counting it as untaken would report a
// tested branch as a gap; counting it as taken would invent evidence. It has
// to leave the ratio entirely — which is exactly what taken/decidable does,
// and what taken/total would not.
func TestDecisionCoverage_UndeterminedMovesNeitherNumeratorNorDenominator(t *testing.T) {
	cases := []struct {
		name                      string
		total, decidable, taken   int
		wantComponent             float64
		wantUndetermined          int
		wantTakenAndDecidableSeen [2]int
	}{
		{
			// Two of the four outcomes are unjudgeable; both judgeable ones
			// were taken. taken/total would say 50 — a fabricated gap.
			name:  "half undetermined, all decidable taken",
			total: 4, decidable: 2, taken: 2,
			wantComponent:             100,
			wantUndetermined:          2,
			wantTakenAndDecidableSeen: [2]int{2, 2},
		},
		{
			// Nothing undetermined: the same taken count is now genuinely
			// half the outcomes.
			name:  "nothing undetermined",
			total: 4, decidable: 4, taken: 2,
			wantComponent:             50,
			wantUndetermined:          0,
			wantTakenAndDecidableSeen: [2]int{2, 4},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			ids := seedFeature(t, s, seedSpec{
				FeatureID: "auth.login", Title: "Login", NumSymbols: 1, SymbolFile: "auth/login.go",
			})
			seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
				ids[0]: store.StatusPass,
			})
			seedDecision(t, s, ids[0], tc.total, tc.decidable, tc.taken)

			got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
			if err != nil {
				t.Fatalf("ScoreFeature: %v", err)
			}
			if v := got.Components[SignalDecisionCoverage]; v != tc.wantComponent {
				t.Errorf("decision_coverage = %.4f, want %.4f", v, tc.wantComponent)
			}
			d := got.Decision
			if d == nil {
				t.Fatal("Decision report is nil; want a report for a measured feature")
			}
			if !d.Available {
				t.Error("Decision.Available = false, want true")
			}
			if d.OutcomesUndetermined != tc.wantUndetermined {
				t.Errorf("OutcomesUndetermined = %d, want %d", d.OutcomesUndetermined, tc.wantUndetermined)
			}
			if d.OutcomesTaken != tc.wantTakenAndDecidableSeen[0] {
				t.Errorf("OutcomesTaken = %d, want %d", d.OutcomesTaken, tc.wantTakenAndDecidableSeen[0])
			}
			if d.OutcomesDecidable != tc.wantTakenAndDecidableSeen[1] {
				t.Errorf("OutcomesDecidable = %d, want %d", d.OutcomesDecidable, tc.wantTakenAndDecidableSeen[1])
			}
		})
	}
}

// TestDecisionCoverage_AllUndeterminedIsUnavailableNotZero covers the case a
// naive taken/decidable would divide by zero on: every outcome the symbol has
// is unjudgeable. "No judgement was possible" is not 0%, and must not score
// like it — the feature has to land on the same number as if the row were
// absent, while still SAYING that the measurement happened.
func TestDecisionCoverage_AllUndeterminedIsUnavailableNotZero(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 2, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusFail,
	})

	before, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature (before): %v", err)
	}

	// Both symbols are nothing but short-circuit operands: measured, and
	// unjudgeable.
	seedDecision(t, s, ids[0], 2, 0, 0)
	seedDecision(t, s, ids[1], 1, 0, 0)

	after, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature (after): %v", err)
	}
	if after.Score != before.Score {
		t.Errorf("score moved from %.4f to %.4f on an all-undetermined measurement; "+
			"an unjudgeable signal must not score", before.Score, after.Score)
	}
	if v, ok := after.Components[SignalDecisionCoverage]; ok {
		t.Errorf("decision_coverage component = %.2f; an unjudgeable signal must not be scored at all", v)
	}
	d := after.Decision
	if d == nil {
		t.Fatal("Decision report is nil; the measurement happened and must be visible even though it is unscoreable")
	}
	if d.Available {
		t.Error("Decision.Available = true with zero decidable outcomes")
	}
	if d.OutcomesUndetermined != 3 {
		t.Errorf("OutcomesUndetermined = %d, want 3", d.OutcomesUndetermined)
	}
	if d.SymbolsMeasured != 2 {
		t.Errorf("SymbolsMeasured = %d, want 2", d.SymbolsMeasured)
	}
}

// TestDecisionCoverage_SplitsTheCoverageBudget pins the weighting.
//
// The two signals read the SAME profile at two resolutions, so the pair splits
// the coverage weight rather than adding to it; the split favours decision
// coverage. With statement=50 and decision=100 the arithmetic is
// (0.16*50 + 0.24*100) / 0.40 = 80. Summing at equal weight would give 75, and
// leaving decision out entirely would give 50 — so this number distinguishes
// all three.
func TestDecisionCoverage_SplitsTheCoverageBudget(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 4, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
		ids[2]: store.StatusFail, ids[3]: store.StatusFail,
	})
	for _, id := range ids {
		seedDecision(t, s, id, 2, 2, 2)
	}

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalCoverage]; v != 50 {
		t.Fatalf("statement coverage = %.4f, want 50", v)
	}
	if v := got.Components[SignalDecisionCoverage]; v != 100 {
		t.Fatalf("decision coverage = %.4f, want 100", v)
	}
	if got.Score != 80 {
		t.Errorf("blended score = %.4f, want 80 "+
			"(0.16*50 + 0.24*100 over a 0.40 budget)", got.Score)
	}

	// The split is an Option, not a constant baked into the algorithm.
	even, err := New(s, Options{DecisionCoverageShare: 0.5}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature (even split): %v", err)
	}
	if even.Score != 75 {
		t.Errorf("blended score at share=0.5 = %.4f, want 75", even.Score)
	}
}

// TestDecisionCoverage_TakesWholeBudgetWhenStatementCoverageIsAbsent covers a
// store that ran `atlas flow` against a profile it never ingested through
// `atlas cov`. The coverage budget belongs to the question "is this feature's
// behaviour exercised?"; when only one half can answer it, that half holds the
// whole budget rather than leaving it unspent.
func TestDecisionCoverage_TakesWholeBudgetWhenStatementCoverageIsAbsent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 2, SymbolFile: "auth/login.go",
	})
	seedDecision(t, s, ids[0], 4, 4, 3)
	seedDecision(t, s, ids[1], 4, 4, 1)

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if _, ok := got.Components[SignalCoverage]; ok {
		t.Fatalf("statement coverage present with no coverage run: %.2f", got.Components[SignalCoverage])
	}
	// 4 of 8 decidable outcomes taken.
	if v := got.Components[SignalDecisionCoverage]; v != 50 {
		t.Errorf("decision coverage = %.4f, want 50", v)
	}
	if got.Score != 50 {
		t.Errorf("score = %.4f, want 50 (decision coverage is the only available signal)", got.Score)
	}
}

// TestDecisionCoverage_UnmeasuredSymbolsLeaveTheRatioAlone applies the
// availability rule one level down. A feature whose surface is half analysed
// is scored on the half that was, and reports the other half as unmeasured;
// treating an absent row as "no branch taken" would penalise the feature for
// the analyser's coverage rather than the tests'.
func TestDecisionCoverage_UnmeasuredSymbolsLeaveTheRatioAlone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 4, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
		ids[2]: store.StatusPass, ids[3]: store.StatusPass,
	})
	seedDecision(t, s, ids[0], 2, 2, 2)
	seedDecision(t, s, ids[1], 2, 2, 1)

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// 3 of the 4 decidable outcomes that exist were taken. Had the two
	// unmeasured symbols contributed a zero each, this would be lower.
	if v := got.Components[SignalDecisionCoverage]; v != 75 {
		t.Errorf("decision coverage = %.4f, want 75 (3/4 decidable outcomes over the MEASURED symbols)", v)
	}
	d := got.Decision
	if d == nil {
		t.Fatal("Decision report is nil")
	}
	if d.SymbolsMeasured != 2 || d.SymbolsUnmeasured != 2 {
		t.Errorf("symbols measured/unmeasured = %d/%d, want 2/2", d.SymbolsMeasured, d.SymbolsUnmeasured)
	}
}

// TestDecisionCoverage_ReasonNamesTheUndeterminedOutcomes checks the operator-
// facing half: a decision-coverage gap has to say how many outcomes nothing
// could judge, or the reader cannot tell an untested branch from a blind spot.
func TestDecisionCoverage_ReasonNamesTheUndeterminedOutcomes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 1, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass,
	})
	seedDecision(t, s, ids[0], 6, 4, 1)

	got, err := New(s, Options{}).ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	found := ""
	for _, r := range got.Reasons {
		if strings.HasPrefix(r, "decision coverage") {
			found = r
		}
	}
	if found == "" {
		t.Fatalf("no decision-coverage reason in %v", got.Reasons)
	}
	for _, want := range []string{"1/4", "2 undetermined"} {
		if !strings.Contains(found, want) {
			t.Errorf("reason %q does not mention %q", found, want)
		}
	}
}
