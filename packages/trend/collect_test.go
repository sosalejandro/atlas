package trend

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// stubScorer stands in for packages/audit so Collect can be tested on the
// thing it actually owns — turning scores into a point — without building a
// full coverage frontier for every case.
type stubScorer struct {
	healths []audit.FeatureHealth
	err     error
}

func (s stubScorer) ScoreAll(context.Context) ([]audit.FeatureHealth, error) {
	return s.healths, s.err
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedFeature creates a feature with n linked impl symbols, which is the
// denominator Collect reports for it.
func seedFeature(t *testing.T, s *store.Store, id string, n int) {
	t.Helper()
	ctx := context.Background()
	if err := s.Features().Upsert(ctx, store.Feature{
		ID: shared.FeatureID(id), Title: id, Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("upsert feature %s: %v", id, err)
	}
	for i := 0; i < n; i++ {
		symID, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(id + "." + string(rune('a'+i))),
			Kind:          shared.KindFunc,
			FilePath:      id + ".go",
			Line:          i + 1,
		})
		if err != nil {
			t.Fatalf("insert symbol: %v", err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: shared.FeatureID(id),
			SymbolID:  symID,
			Role:      store.RoleImpl,
			Source:    store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("link symbol: %v", err)
		}
	}
}

func covered(score float64) audit.FeatureHealth {
	return audit.FeatureHealth{
		Score:      score,
		Components: map[string]float64{audit.SignalVerification: score},
	}
}

func TestCollect_DenominatorIsTheLinkedSurface(t *testing.T) {
	s := openStore(t)
	seedFeature(t, s, "f.big", 3)
	seedFeature(t, s, "f.small", 1)

	big, small := covered(60), covered(100)
	big.FeatureID, small.FeatureID = "f.big", "f.small"

	got, err := Collect(context.Background(), s,
		stubScorer{healths: []audit.FeatureHealth{big, small}},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Denominator != 4 {
		t.Errorf("Denominator = %d, want 4 (3 + 1 linked symbols)", got.Denominator)
	}
	if got.Score == nil || *got.Score != 70 {
		t.Errorf("Score = %v, want 70 ((60*3 + 100*1)/4)", got.Score)
	}
	if len(got.Features) != 2 {
		t.Fatalf("Features = %d, want 2", len(got.Features))
	}
}

// A feature the audit scored WITHOUT a coverage signal has no coverage
// evidence. Recording its blended score as a coverage point would make the
// series jump the moment coverage arrives, for no change in the code.
func TestCollect_FeatureWithoutACoverageSignalIsUnmeasured(t *testing.T) {
	s := openStore(t)
	seedFeature(t, s, "f.blind", 2)

	blind := audit.FeatureHealth{
		FeatureID: "f.blind",
		Score:     42,
		// No SignalCoverage component: the audit re-normalised over the
		// other signals, which is a different measurement entirely.
		Components: map[string]float64{audit.SignalPatternCompliance: 42},
	}

	got, err := Collect(context.Background(), s,
		stubScorer{healths: []audit.FeatureHealth{blind}},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Score != nil {
		t.Errorf("project Score = %v, want nil (no coverage evidence anywhere)", *got.Score)
	}
	if len(got.Features) != 1 {
		t.Fatalf("Features = %d, want 1", len(got.Features))
	}
	if got.Features[0].Score != nil {
		t.Errorf("feature Score = %v, want nil", *got.Features[0].Score)
	}
	// The surface is still known even when the score is not — that is what
	// lets a later comparison notice the denominator moved.
	if got.Features[0].Denominator != 2 {
		t.Errorf("feature Denominator = %d, want 2", got.Features[0].Denominator)
	}
}

func TestCollect_RequiresACommitSHA(t *testing.T) {
	s := openStore(t)
	if _, err := Collect(context.Background(), s, stubScorer{},
		CollectOptions{MeasuredAt: day(0)}); err == nil {
		t.Fatal("Collect with no commit sha succeeded, want an error")
	}
}

func TestCollect_PropagatesScorerFailure(t *testing.T) {
	s := openStore(t)
	boom := errors.New("boom")
	_, err := Collect(context.Background(), s, stubScorer{err: boom},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestCollect_DefaultsMeasuredAtToNow(t *testing.T) {
	s := openStore(t)
	before := time.Now().UTC().Add(-time.Second)
	got, err := Collect(context.Background(), s, stubScorer{},
		CollectOptions{CommitSHA: "abc"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.MeasuredAt.Before(before) {
		t.Errorf("MeasuredAt = %v, want a fresh timestamp", got.MeasuredAt)
	}
}
