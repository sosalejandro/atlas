package trend

import (
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func f64(v float64) *float64 { return &v }

func point(sha string, score *float64, denom int64, feats ...store.HistoryFeaturePoint) store.HistoryPoint {
	return store.HistoryPoint{
		CommitSHA:   sha,
		MeasuredAt:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		Score:       score,
		Denominator: denom,
		Features:    feats,
	}
}

func feat(id string, score *float64, denom int64) store.HistoryFeaturePoint {
	return store.HistoryFeaturePoint{
		FeatureID:   shared.FeatureID(id),
		Score:       score,
		Denominator: denom,
	}
}

func TestCompare_RegressionBeyondToleranceFailsTheGate(t *testing.T) {
	base := point("base", f64(80), 100, feat("f.a", f64(80), 100))
	head := point("head", f64(60), 100, feat("f.a", f64(60), 100))

	rep := Compare(base, head, CompareOptions{})
	if !rep.Regressed {
		t.Fatal("Regressed = false, want true for a 20-point drop")
	}
	if rep.Project.Verdict != VerdictRegressed {
		t.Errorf("project verdict = %q, want %q", rep.Project.Verdict, VerdictRegressed)
	}
	if rep.Project.Delta == nil || *rep.Project.Delta != -20 {
		t.Errorf("delta = %v, want -20", rep.Project.Delta)
	}
}

// Float noise must not fail builds: the sum of a few thousand ratios in
// unspecified order is not bit-identical between runs.
func TestCompare_ToleranceAbsorbsNoise(t *testing.T) {
	base := point("base", f64(80.0), 100)
	head := point("head", f64(79.7), 100)

	rep := Compare(base, head, CompareOptions{})
	if rep.Regressed {
		t.Fatal("Regressed = true for a 0.3-point move inside the default tolerance")
	}
	if rep.Project.Verdict != VerdictUnchanged {
		t.Errorf("verdict = %q, want %q", rep.Project.Verdict, VerdictUnchanged)
	}

	// A caller who wants a zero-tolerance gate must get one: the option is a
	// pointer precisely so an explicit 0 is distinguishable from "unset".
	strict := Compare(base, head, CompareOptions{MaxRegression: f64(0)})
	if !strict.Regressed {
		t.Error("MaxRegression=0 did not catch a 0.3-point drop")
	}
}

// The headline number can be flat while one feature falls off a cliff. That
// case is the entire reason the gate exists.
func TestCompare_PerFeatureRegressionFailsEvenWhenProjectIsFlat(t *testing.T) {
	base := point("base", f64(80), 200,
		feat("f.good", f64(80), 100),
		feat("f.bad", f64(80), 100),
	)
	// f.bad falls off a cliff while f.good climbs by exactly as much, so the
	// project headline stays at 80 and only the per-feature view sees it.
	head := point("head", f64(80), 200,
		feat("f.good", f64(120), 100),
		feat("f.bad", f64(40), 100),
	)

	rep := Compare(base, head, CompareOptions{})
	if !rep.Regressed {
		t.Fatal("Regressed = false; a feature fell 80 -> 40 with a flat project score")
	}
	// Worst first, so the cliff is the first thing a human reads.
	if rep.Features[0].Scope != "f.bad" {
		t.Errorf("Features[0] = %q, want f.bad (worst delta first)", rep.Features[0].Scope)
	}
	if rep.Features[0].Verdict != VerdictRegressed {
		t.Errorf("f.bad verdict = %q, want %q", rep.Features[0].Verdict, VerdictRegressed)
	}
}

// A denominator move means the two scores are not measuring the same thing.
// The comparison must say so rather than present the delta as quality.
func TestCompare_DenominatorShiftIsReportedNotSwallowed(t *testing.T) {
	base := point("base", f64(50), 1000)
	head := point("head", f64(70), 400)

	rep := Compare(base, head, CompareOptions{})
	if !rep.Project.DenominatorMoved {
		t.Fatal("DenominatorMoved = false for a 1000 -> 400 surface change")
	}
	if rep.Project.DenominatorDelta != -600 {
		t.Errorf("DenominatorDelta = %d, want -600", rep.Project.DenominatorDelta)
	}
	if len(rep.Warnings) == 0 {
		t.Error("a denominator shift produced no warning")
	}
	// The delta is still reported — suppressing it would hide real news.
	if rep.Project.Delta == nil || *rep.Project.Delta != 20 {
		t.Errorf("delta = %v, want 20", rep.Project.Delta)
	}
}

// A denominator that moved by a statement or two is not news.
func TestCompare_TrivialDenominatorMoveIsNotFlagged(t *testing.T) {
	rep := Compare(point("base", f64(50), 1000), point("head", f64(50), 1005), CompareOptions{})
	if rep.Project.DenominatorMoved {
		t.Error("a 0.5% denominator move was flagged")
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}
}

// Absent evidence is not a zero. Comparing against it must refuse, not fail
// the build with a fabricated -80.
func TestCompare_UnmeasuredSideIsIncomparable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		base, head store.HistoryPoint
	}{
		{"base unmeasured", point("base", nil, 100), point("head", f64(80), 100)},
		{"head unmeasured", point("base", f64(80), 100), point("head", nil, 100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Compare(tc.base, tc.head, CompareOptions{})
			if rep.Regressed {
				t.Error("Regressed = true against a point with no coverage evidence")
			}
			if rep.Project.Verdict != VerdictNoEvidence {
				t.Errorf("verdict = %q, want %q", rep.Project.Verdict, VerdictNoEvidence)
			}
			if rep.Project.Delta != nil {
				t.Errorf("delta = %v, want nil", *rep.Project.Delta)
			}
			if len(rep.Warnings) == 0 {
				t.Error("missing evidence produced no warning")
			}
		})
	}
}

func TestCompare_NewAndRemovedFeaturesAreNotRegressions(t *testing.T) {
	base := point("base", f64(80), 100, feat("f.gone", f64(80), 100))
	head := point("head", f64(80), 100, feat("f.new", f64(80), 100))

	rep := Compare(base, head, CompareOptions{})
	if rep.Regressed {
		t.Fatal("Regressed = true; a renamed/added/deleted feature is not a quality drop")
	}
	verdicts := map[string]Verdict{}
	for _, c := range rep.Features {
		verdicts[c.Scope] = c.Verdict
	}
	if verdicts["f.new"] != VerdictNew {
		t.Errorf("f.new verdict = %q, want %q", verdicts["f.new"], VerdictNew)
	}
	if verdicts["f.gone"] != VerdictRemoved {
		t.Errorf("f.gone verdict = %q, want %q", verdicts["f.gone"], VerdictRemoved)
	}
}

func TestCompare_ImprovementIsNotARegression(t *testing.T) {
	rep := Compare(point("base", f64(60), 100), point("head", f64(75), 100), CompareOptions{})
	if rep.Regressed {
		t.Fatal("Regressed = true on a 15-point improvement")
	}
	if rep.Project.Verdict != VerdictImproved {
		t.Errorf("verdict = %q, want %q", rep.Project.Verdict, VerdictImproved)
	}
}
