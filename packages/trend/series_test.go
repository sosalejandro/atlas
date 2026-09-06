package trend

import (
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func day(n int) time.Time {
	return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

func TestProjectSeries_CarriesMeasuredAndUnmeasuredPoints(t *testing.T) {
	pts := []store.HistoryPoint{
		{CommitSHA: "a", MeasuredAt: day(0), Score: f64(70), Denominator: 100},
		{CommitSHA: "b", MeasuredAt: day(1), Score: nil, Denominator: 100},
		{CommitSHA: "c", MeasuredAt: day(2), Score: f64(75), Denominator: 110},
	}
	s := ProjectSeries(pts)
	if s.Scope != ScopeProject {
		t.Errorf("Scope = %q, want %q", s.Scope, ScopeProject)
	}
	if len(s.Points) != 3 {
		t.Fatalf("Points = %d, want 3 (an unmeasured point is still a point)", len(s.Points))
	}
	if s.Points[1].Measured() {
		t.Error("the middle point reported Measured, but it has no evidence")
	}
	// First and last MEASURED points bracket the series; the gap must not
	// pull either end.
	first, last, ok := s.Bounds()
	if !ok {
		t.Fatal("Bounds reported no measured points")
	}
	if first.CommitSHA != "a" || last.CommitSHA != "c" {
		t.Errorf("Bounds = %s..%s, want a..c", first.CommitSHA, last.CommitSHA)
	}
}

func TestProjectSeries_AllUnmeasuredHasNoBounds(t *testing.T) {
	s := ProjectSeries([]store.HistoryPoint{
		{CommitSHA: "a", MeasuredAt: day(0), Score: nil},
		{CommitSHA: "b", MeasuredAt: day(1), Score: nil},
	})
	if _, _, ok := s.Bounds(); ok {
		t.Error("Bounds succeeded on a series with no measured point")
	}
	if s.Latest() != nil {
		t.Error("Latest returned a point from an all-unmeasured series")
	}
}

func TestFeatureSeries_SelectsOneFeatureAndKeepsGapsAsGaps(t *testing.T) {
	pts := []store.HistoryPoint{
		{CommitSHA: "a", MeasuredAt: day(0), Score: f64(70), Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.x", Score: f64(60), Denominator: 40},
			{FeatureID: "f.y", Score: f64(90), Denominator: 10},
		}},
		// f.x is absent at commit b entirely — that is a gap, not a zero.
		{CommitSHA: "b", MeasuredAt: day(1), Score: f64(90), Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.y", Score: f64(90), Denominator: 10},
		}},
		{CommitSHA: "c", MeasuredAt: day(2), Score: f64(80), Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.x", Score: f64(65), Denominator: 42},
		}},
	}
	s := FeatureSeries(pts, shared.FeatureID("f.x"))
	if s.Scope != "f.x" {
		t.Errorf("Scope = %q, want f.x", s.Scope)
	}
	if len(s.Points) != 3 {
		t.Fatalf("Points = %d, want 3 (the commit with no f.x row is still on the axis)", len(s.Points))
	}
	if s.Points[1].Measured() {
		t.Error("commit b has no f.x row but reported Measured")
	}
	if s.Points[0].Score == nil || *s.Points[0].Score != 60 {
		t.Errorf("Points[0].Score = %v, want 60", s.Points[0].Score)
	}
	if s.Points[2].Denominator != 42 {
		t.Errorf("Points[2].Denominator = %d, want 42", s.Points[2].Denominator)
	}
}

func TestAssemble_ProjectScoreIsDenominatorWeighted(t *testing.T) {
	// An unweighted mean would score this 55; the 10-symbol feature must not
	// outvote the 90-symbol one.
	got := Assemble(AssembleInput{
		CommitSHA:  "abc",
		MeasuredAt: day(0),
		Features: []FeatureMeasurement{
			{FeatureID: "big", Score: f64(10), Denominator: 90},
			{FeatureID: "small", Score: f64(100), Denominator: 10},
		},
	})
	if got.Score == nil {
		t.Fatal("Score = nil, want a measured project score")
	}
	if *got.Score != 19 {
		t.Errorf("Score = %v, want 19 ((10*90 + 100*10)/100)", *got.Score)
	}
	if got.Denominator != 100 {
		t.Errorf("Denominator = %d, want 100", got.Denominator)
	}
	if len(got.Features) != 2 {
		t.Errorf("Features = %d, want 2", len(got.Features))
	}
}

// A feature with no coverage evidence must not drag the project score down as
// if it scored zero, nor inflate the denominator it was never measured over.
func TestAssemble_UnmeasuredFeaturesAreExcludedFromTheProjectScore(t *testing.T) {
	got := Assemble(AssembleInput{
		CommitSHA:  "abc",
		MeasuredAt: day(0),
		Features: []FeatureMeasurement{
			{FeatureID: "measured", Score: f64(80), Denominator: 50},
			{FeatureID: "unmeasured", Score: nil, Denominator: 50},
		},
	})
	if got.Score == nil || *got.Score != 80 {
		t.Errorf("Score = %v, want 80", got.Score)
	}
	if got.Denominator != 50 {
		t.Errorf("Denominator = %d, want 50 (the unmeasured half is not in scope)", got.Denominator)
	}
	// The unmeasured feature is still RECORDED, so `trend --feature` can show
	// the gap rather than pretending the feature did not exist.
	if len(got.Features) != 2 {
		t.Fatalf("Features = %d, want 2", len(got.Features))
	}
}

func TestAssemble_NoEvidenceAtAllYieldsANullScore(t *testing.T) {
	got := Assemble(AssembleInput{
		CommitSHA:  "abc",
		MeasuredAt: day(0),
		Features: []FeatureMeasurement{
			{FeatureID: "a", Score: nil, Denominator: 10},
		},
	})
	if got.Score != nil {
		t.Fatalf("Score = %v, want nil when nothing was measured", *got.Score)
	}
}

func TestAssemble_SortsFeaturesByIDForAStableRow(t *testing.T) {
	got := Assemble(AssembleInput{
		CommitSHA:  "abc",
		MeasuredAt: day(0),
		Features: []FeatureMeasurement{
			{FeatureID: "z", Score: f64(1), Denominator: 1},
			{FeatureID: "a", Score: f64(1), Denominator: 1},
		},
	})
	if got.Features[0].FeatureID != "a" {
		t.Errorf("Features[0] = %q, want a", got.Features[0].FeatureID)
	}
}
