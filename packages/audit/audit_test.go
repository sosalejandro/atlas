package audit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// openTestStore mirrors packages/store's test helper. We can't import the
// non-exported one, so we inline it.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "atlas-state.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedFeature creates a feature + N symbols, links them as impl. Returns
// the symbol surrogate IDs in declaration order. SymbolFile carries the
// repo-relative file path used for every linked symbol (sharing a file
// path makes the annotation lookup test deterministic).
type seedSpec struct {
	FeatureID  shared.FeatureID
	Title      string
	Kind       store.FeatureKind
	NumSymbols int
	SymbolFile string // shared file path for all symbols
}

func seedFeature(t *testing.T, s *store.Store, spec seedSpec) []int64 {
	t.Helper()
	ctx := context.Background()
	kind := spec.Kind
	if kind == "" {
		kind = store.FeatureKindFeature
	}
	if err := s.Features().Upsert(ctx, store.Feature{
		ID: spec.FeatureID, Title: spec.Title, Kind: kind,
	}); err != nil {
		t.Fatalf("Upsert %q: %v", spec.FeatureID, err)
	}
	if spec.NumSymbols == 0 {
		return nil
	}
	ids := make([]int64, 0, spec.NumSymbols)
	for i := 0; i < spec.NumSymbols; i++ {
		qn := shared.SymbolID(string(spec.FeatureID) + ".sym" + intStr(i))
		sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: qn,
			Kind:          shared.KindFunc,
			FilePath:      spec.SymbolFile,
			Line:          i + 1,
		})
		if err != nil {
			t.Fatalf("Insert symbol %q: %v", qn, err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: spec.FeatureID,
			SymbolID:  sid,
			Role:      store.RoleImpl,
			Source:    store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("Link %q→%d: %v", spec.FeatureID, sid, err)
		}
		ids = append(ids, sid)
	}
	return ids
}

func intStr(i int) string {
	// rune("0"+i) is fine for tests (only 0..9 in use).
	return string(rune('0' + i))
}

// seedCoverage writes a run + per-symbol pass/fail results.
func seedCoverage(t *testing.T, s *store.Store, framework store.Framework, statuses map[int64]store.CoverageStatus) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	results := make([]store.CoverageResult, 0, len(statuses))
	for sid, st := range statuses {
		v := sid
		results = append(results, store.CoverageResult{
			SymbolID: &v,
			Status:   st,
		})
	}
	id, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework:  framework,
		StartedAt:  now,
		FinishedAt: now,
	}, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}
	return id
}

// -----------------------------------------------------------------------------
// Coverage signal — unit tests
// -----------------------------------------------------------------------------

func TestCoverageSignal_AllPass(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 3, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass, ids[2]: store.StatusPass,
	})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalCoverage]; v != 100 {
		t.Errorf("coverage = %.1f, want 100", v)
	}
}

func TestCoverageSignal_PartialPass(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "auth.login", Title: "Login", NumSymbols: 4, SymbolFile: "auth/login.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
		ids[2]: store.StatusFail, ids[3]: store.StatusFail,
	})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalCoverage]; v != 50 {
		t.Errorf("coverage = %.1f, want 50", v)
	}
}

func TestCoverageSignal_AllSkipsTreatedAsNoSignal(t *testing.T) {
	// Edge-case-as-pressure-dimension: feature where every coverage row is
	// `skip` — must NOT yield score=0. The signal must be reported as
	// "unavailable" so the weighted average falls back to other signals.
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "feat.skipped", Title: "Skipped", NumSymbols: 2, SymbolFile: "x/y.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusSkip, ids[1]: store.StatusSkip,
	})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "feat.skipped")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if _, ok := got.Components[SignalCoverage]; ok {
		t.Errorf("coverage component present (%.1f); want unavailable for all-skip", got.Components[SignalCoverage])
	}
}

// -----------------------------------------------------------------------------
// Annotation freshness signal — unit tests
// -----------------------------------------------------------------------------

func TestFreshnessSignal_AllFresh(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedFeature(t, s, seedSpec{
		FeatureID: "billing.invoice", Title: "Invoice", NumSymbols: 1, SymbolFile: "billing/invoice.go",
	})
	if err := s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "billing/invoice.go", Line: 5, Kind: shared.AnnFeature,
		Value: "billing.invoice", Source: shared.SourceAtlas,
	}); err != nil {
		t.Fatalf("Upsert annotation: %v", err)
	}
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	a := New(s, Options{
		GitBlame: &fixedBlameSource{ts: now.Add(-2 * 24 * time.Hour)},
		Now:      func() time.Time { return now },
	})
	got, err := a.ScoreFeature(ctx, "billing.invoice")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalAnnotationFresh]; v != 100 {
		t.Errorf("freshness = %.1f, want 100", v)
	}
}

func TestFreshnessSignal_AllStale(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedFeature(t, s, seedSpec{
		FeatureID: "billing.invoice", Title: "Invoice", NumSymbols: 1, SymbolFile: "billing/invoice.go",
	})
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "billing/invoice.go", Line: 5, Kind: shared.AnnFeature,
		Value: "billing.invoice", Source: shared.SourceAtlas,
	})
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	a := New(s, Options{
		GitBlame: &fixedBlameSource{ts: now.Add(-365 * 24 * time.Hour)},
		Now:      func() time.Time { return now },
	})
	got, err := a.ScoreFeature(ctx, "billing.invoice")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalAnnotationFresh]; v != 0 {
		t.Errorf("freshness = %.1f, want 0", v)
	}
}

func TestFreshnessSignal_NoBlameSourceUnavailable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedFeature(t, s, seedSpec{
		FeatureID: "x", Title: "X", NumSymbols: 1, SymbolFile: "x/y.go",
	})
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "x/y.go", Line: 1, Kind: shared.AnnFeature, Value: "x",
		Source: shared.SourceAtlas,
	})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "x")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if _, ok := got.Components[SignalAnnotationFresh]; ok {
		t.Errorf("freshness component present without GitBlame source")
	}
}

// -----------------------------------------------------------------------------
// Pattern compliance signal — unit tests
// -----------------------------------------------------------------------------

func TestPatternSignal_FullMatch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedFeature(t, s, seedSpec{
		FeatureID: "agg.cart", Title: "Cart", NumSymbols: 1, SymbolFile: "cart/service.go",
	})
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "cart/service.go", Line: 1, Kind: shared.AnnAggregateService,
		Value: "cart", Source: shared.SourceAtlas,
	})
	// Pattern match: a symbol in the same file flagged canonical-service.
	sid, _ := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "CartService.Update", Kind: shared.KindMethod,
		FilePath: "cart/service.go", Line: 12,
	})
	_ = s.Symbols().SetPatternMatches(ctx, "CartService.Update",
		`[{"pattern":"canonical-service","symbol":"CartService.Update","confidence":1.0}]`)
	_ = sid

	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "agg.cart")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalPatternCompliance]; v != 100 {
		t.Errorf("pattern = %.1f, want 100", v)
	}
}

func TestPatternSignal_NoAggregateSkipsSignal(t *testing.T) {
	// Edge-case-as-pressure-dimension: feature with no aggregate-service
	// annotation must NOT register a pattern_compliance component.
	s := openTestStore(t)
	ctx := context.Background()
	seedFeature(t, s, seedSpec{
		FeatureID: "x", Title: "X", NumSymbols: 1, SymbolFile: "x/y.go",
	})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "x")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if _, ok := got.Components[SignalPatternCompliance]; ok {
		t.Errorf("pattern present without any aggregate annotation; components=%+v", got.Components)
	}
}

// -----------------------------------------------------------------------------
// Contract drift signal — unit tests
// -----------------------------------------------------------------------------

func TestContractDriftSignal_FreshContract(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// The feature ITSELF is a contract; contract drift counts against itself.
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	seedFeature(t, s, seedSpec{
		FeatureID: "contract.login", Title: "Login API", Kind: store.FeatureKindContract,
		NumSymbols: 1, SymbolFile: "x/y.go",
	})
	a := New(s, Options{
		Now: func() time.Time { return now },
	})
	got, err := a.ScoreFeature(ctx, "contract.login")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if v := got.Components[SignalContractDrift]; v != 100 {
		t.Errorf("drift = %.1f, want 100 (fresh contract just inserted)", v)
	}
}

// -----------------------------------------------------------------------------
// Combined-score tests
// -----------------------------------------------------------------------------

func TestScoreFeature_AllSignalsAvailable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "agg.cart", Title: "Cart", Kind: store.FeatureKindContract,
		NumSymbols: 2, SymbolFile: "cart/service.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusFail,
	})
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "cart/service.go", Line: 1, Kind: shared.AnnAggregateService,
		Value: "cart", Source: shared.SourceAtlas,
	})
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "cart/service.go", Line: 2, Kind: shared.AnnFeature,
		Value: "agg.cart", Source: shared.SourceAtlas,
	})
	_, _ = s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "CartService.Update", Kind: shared.KindMethod,
		FilePath: "cart/service.go", Line: 12,
	})
	_ = s.Symbols().SetPatternMatches(ctx, "CartService.Update",
		`[{"pattern":"canonical-service","symbol":"CartService.Update","confidence":1.0}]`)

	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	a := New(s, Options{
		GitBlame: &fixedBlameSource{ts: now.Add(-1 * 24 * time.Hour)},
		Now:      func() time.Time { return now },
	})
	got, err := a.ScoreFeature(ctx, "agg.cart")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// Expect all four signals present.
	for _, k := range []string{
		SignalCoverage, SignalAnnotationFresh,
		SignalPatternCompliance, SignalContractDrift,
	} {
		if _, ok := got.Components[k]; !ok {
			t.Errorf("missing %s component; got=%+v", k, got.Components)
		}
	}
	if got.Score <= 0 || got.Score >= 100 {
		t.Errorf("Score = %.2f, expected mid-range (one signal at 50%%, others at 100%%)", got.Score)
	}
}

func TestScoreFeature_SubsetSignalsAvailable(t *testing.T) {
	// Edge-case-as-pressure-dimension: feature where only ONE signal is
	// available — the weighted average must use ONLY that signal (no
	// bias toward zero from "missing" signals).
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "noagg", Title: "No Aggregate", NumSymbols: 2, SymbolFile: "x/y.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusPass, ids[1]: store.StatusPass,
	})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "noagg")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if got.Score != 100 {
		t.Errorf("Score = %.2f, want 100 (single available signal at 100%%)", got.Score)
	}
}

func TestScoreFeature_PatternHighCoverageZero(t *testing.T) {
	// Edge-case-as-pressure-dimension: pattern compliance 100%, coverage 0%
	// — score reflects the mix, NOT a flat 0.
	s := openTestStore(t)
	ctx := context.Background()
	ids := seedFeature(t, s, seedSpec{
		FeatureID: "agg.cart", Title: "Cart", NumSymbols: 1, SymbolFile: "cart/svc.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		ids[0]: store.StatusFail,
	})
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "cart/svc.go", Line: 1, Kind: shared.AnnAggregateService,
		Value: "cart", Source: shared.SourceAtlas,
	})
	_, _ = s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "CartSvc.Update", Kind: shared.KindMethod,
		FilePath: "cart/svc.go", Line: 4,
	})
	_ = s.Symbols().SetPatternMatches(ctx, "CartSvc.Update",
		`[{"pattern":"canonical-service","symbol":"CartSvc.Update","confidence":1.0}]`)

	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "agg.cart")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	// coverage(0) * 0.40 + pattern(100) * 0.25 → 25.0 (no other signals).
	// Renormalized over {coverage, pattern} weights (0.40 + 0.25 = 0.65):
	//   (0 * 0.40 + 100 * 0.25) / 0.65 ≈ 38.46
	if got.Score < 30 || got.Score > 50 {
		t.Errorf("Score = %.2f, want mid-range (~38) for mixed signals", got.Score)
	}
	if got.Components[SignalCoverage] != 0 {
		t.Errorf("coverage = %.1f, want 0", got.Components[SignalCoverage])
	}
	if got.Components[SignalPatternCompliance] != 100 {
		t.Errorf("pattern = %.1f, want 100", got.Components[SignalPatternCompliance])
	}
}

func TestScoreFeature_NoSymbolsLinked(t *testing.T) {
	// Edge-case-as-pressure-dimension: feature with zero linked symbols.
	// Documented behaviour: Score == 0, Reasons explains "no signals
	// available" — distinguishable from "0 because everything failed."
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.Features().Upsert(ctx, store.Feature{ID: "ghost", Title: "Ghost"})
	a := New(s, Options{})
	got, err := a.ScoreFeature(ctx, "ghost")
	if err != nil {
		t.Fatalf("ScoreFeature: %v", err)
	}
	if got.Score != 0 {
		t.Errorf("Score = %.2f, want 0 for no-symbols feature", got.Score)
	}
	if len(got.Reasons) == 0 {
		t.Errorf("Reasons empty for no-symbols feature; want an explanatory line")
	}
}

// -----------------------------------------------------------------------------
// ScoreAll ordering
// -----------------------------------------------------------------------------

func TestScoreAll_SortsAscending(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	goodIDs := seedFeature(t, s, seedSpec{
		FeatureID: "good", Title: "Good", NumSymbols: 1, SymbolFile: "good.go",
	})
	badIDs := seedFeature(t, s, seedSpec{
		FeatureID: "bad", Title: "Bad", NumSymbols: 2, SymbolFile: "bad.go",
	})
	seedCoverage(t, s, store.FrameworkGoTest, map[int64]store.CoverageStatus{
		goodIDs[0]: store.StatusPass,
		badIDs[0]:  store.StatusFail, badIDs[1]: store.StatusFail,
	})
	a := New(s, Options{})
	got, err := a.ScoreAll(ctx)
	if err != nil {
		t.Fatalf("ScoreAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2", len(got))
	}
	if got[0].FeatureID != "bad" {
		t.Errorf("worst feature first; got %q first, want %q", got[0].FeatureID, "bad")
	}
}

func TestScoreAll_NilStoreReturnsTypedError(t *testing.T) {
	a := New(nil, Options{})
	_, err := a.ScoreAll(context.Background())
	if err == nil || !errors.Is(err, errNilStore) {
		t.Fatalf("ScoreAll err = %v, want errNilStore", err)
	}
}
