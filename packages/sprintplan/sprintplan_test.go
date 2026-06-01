package sprintplan

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

// openTestStore is the same helper used by audit_test.go.
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

// seedFeatureWithSymbols mirrors the audit_test helper. Returns symbol IDs.
func seedFeatureWithSymbols(t *testing.T, s *store.Store, featureID shared.FeatureID, n int, file string) []int64 {
	t.Helper()
	ctx := context.Background()
	if err := s.Features().Upsert(ctx, store.Feature{ID: featureID, Title: string(featureID)}); err != nil {
		t.Fatalf("Upsert %q: %v", featureID, err)
	}
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		// qualified names must be globally unique across symbols, so prefix.
		qn := shared.SymbolID(string(featureID) + ".s" + intStr(i))
		sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: qn, Kind: shared.KindFunc,
			FilePath: file, Line: i + 1,
		})
		if err != nil {
			t.Fatalf("Insert symbol %q: %v", qn, err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: featureID, SymbolID: sid,
			Role: store.RoleImpl, Source: store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("Link: %v", err)
		}
		ids = append(ids, sid)
	}
	return ids
}

func intStr(i int) string {
	// Tests only use 0..19; one rune is sufficient when < 10, otherwise
	// fall back to a two-rune representation.
	if i < 10 {
		return string(rune('0' + i))
	}
	tens := i / 10
	ones := i % 10
	return string([]rune{rune('0' + tens), rune('0' + ones)})
}

// -----------------------------------------------------------------------------
// Rank ordering & stability
// -----------------------------------------------------------------------------

func TestRank_StabilitySameInputSameOrder(t *testing.T) {
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()
	// Three features at different scores via coverage.
	// Use dot-namespaced IDs so they are not filtered as phantoms.
	low := seedFeatureWithSymbols(t, s, "cov.low", 2, "a.go")
	mid := seedFeatureWithSymbols(t, s, "cov.mid", 2, "b.go")
	high := seedFeatureWithSymbols(t, s, "cov.high", 2, "c.go")
	now := time.Now().UTC()
	results := []store.CoverageResult{
		{SymbolID: ptrInt64(low[0]), Status: store.StatusFail},
		{SymbolID: ptrInt64(low[1]), Status: store.StatusFail},
		{SymbolID: ptrInt64(mid[0]), Status: store.StatusPass},
		{SymbolID: ptrInt64(mid[1]), Status: store.StatusFail},
		{SymbolID: ptrInt64(high[0]), Status: store.StatusPass},
		{SymbolID: ptrInt64(high[1]), Status: store.StatusPass},
	}
	_, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}

	p := New(s, a, Options{Now: func() time.Time { return now }})
	first, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank #1: %v", err)
	}
	second, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank #2: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("len mismatch: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].FeatureID != second[i].FeatureID || first[i].Priority != second[i].Priority {
			t.Errorf("instability at [%d]: %+v vs %+v", i, first[i], second[i])
		}
	}
	// And: the lowest-score feature must be the highest priority.
	if first[0].FeatureID != "cov.low" {
		t.Errorf("Rank ordering wrong: first = %q, want %q", first[0].FeatureID, "cov.low")
	}
}

// -----------------------------------------------------------------------------
// Cost bucket boundaries
// -----------------------------------------------------------------------------

func TestCost_BucketBoundaries(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, CostS}, {1, CostS}, {3, CostS},
		{4, CostM}, {10, CostM}, {15, CostM},
		{16, CostL}, {50, CostL},
	}
	for _, tc := range cases {
		got := costBucket(tc.n)
		if got != tc.want {
			t.Errorf("costBucket(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestRank_EmitsCostLabel(t *testing.T) {
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()
	// Use dot-namespaced IDs so they are not filtered as phantoms.
	seedFeatureWithSymbols(t, s, "cost.small", 2, "a.go")  // S
	seedFeatureWithSymbols(t, s, "cost.medium", 8, "b.go") // M
	seedFeatureWithSymbols(t, s, "cost.large", 20, "c.go") // L
	p := New(s, a, Options{})
	got, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	costByID := make(map[shared.FeatureID]string)
	for _, item := range got {
		costByID[item.FeatureID] = item.Cost
	}
	want := map[shared.FeatureID]string{
		"cost.small":  CostS,
		"cost.medium": CostM,
		"cost.large":  CostL,
	}
	for id, w := range want {
		if costByID[id] != w {
			t.Errorf("cost[%q] = %q, want %q", id, costByID[id], w)
		}
	}
}

// -----------------------------------------------------------------------------
// TopN boundary behaviours
// -----------------------------------------------------------------------------

func TestTopN_Zero(t *testing.T) {
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	seedFeatureWithSymbols(t, s, "topn.x", 1, "a.go")
	p := New(s, a, Options{})
	got, err := p.TopN(context.Background(), 0)
	if err != nil {
		t.Fatalf("TopN(0): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestTopN_GreaterThanLen(t *testing.T) {
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	seedFeatureWithSymbols(t, s, "topn.x", 1, "a.go")
	seedFeatureWithSymbols(t, s, "topn.y", 1, "b.go")
	p := New(s, a, Options{})
	got, err := p.TopN(context.Background(), 99)
	if err != nil {
		t.Fatalf("TopN(99): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

// -----------------------------------------------------------------------------
// Edge-cases-as-pressure-dimensions
// -----------------------------------------------------------------------------

func TestRank_NoFeaturesEmptyOutput(t *testing.T) {
	// Edge-case-as-pressure-dimension: zero features in the store.
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	p := New(s, a, Options{})
	got, err := p.Rank(context.Background())
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestRank_AllScoresMaxStableByID(t *testing.T) {
	// Edge-case-as-pressure-dimension: every feature scores 100 (all-pass
	// coverage) → Priority = 0 for all; stable sort breaks ties by id.
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()
	now := time.Now().UTC()
	// Use dot-namespaced IDs (required — bare names are treated as phantoms).
	// Prefixed with "tier." to stay in the valid namespace while giving
	// each feature a recognisable alphabetical sort key.
	a1 := seedFeatureWithSymbols(t, s, "tier.alpha", 1, "alpha.go")
	a2 := seedFeatureWithSymbols(t, s, "tier.bravo", 1, "bravo.go")
	a3 := seedFeatureWithSymbols(t, s, "tier.charlie", 1, "charlie.go")
	_, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, []store.CoverageResult{
		{SymbolID: ptrInt64(a1[0]), Status: store.StatusPass},
		{SymbolID: ptrInt64(a2[0]), Status: store.StatusPass},
		{SymbolID: ptrInt64(a3[0]), Status: store.StatusPass},
	})
	if err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}
	p := New(s, a, Options{Now: func() time.Time { return now }})
	got, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for _, item := range got {
		if item.Priority != 0 {
			t.Errorf("Priority(%q) = %.2f, want 0", item.FeatureID, item.Priority)
		}
	}
	// Stable sort by id when priorities tie.
	want := []shared.FeatureID{"tier.alpha", "tier.bravo", "tier.charlie"}
	for i, w := range want {
		if got[i].FeatureID != w {
			t.Errorf("got[%d] = %q, want %q (tie-break by id)", i, got[i].FeatureID, w)
		}
	}
}

func TestRank_NoRecencyMeansLowerPriority(t *testing.T) {
	// Edge-case-as-pressure-dimension: no annotation activity → recency
	// term contributes 0; the priority drops compared to a feature WITH
	// recent activity.
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// Two features at identical zero coverage scores.
	// Use dot-namespaced IDs so they are not filtered as phantoms.
	dead := seedFeatureWithSymbols(t, s, "recency.dead", 1, "dead.go")
	hot := seedFeatureWithSymbols(t, s, "recency.hot", 1, "hot.go")
	_, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, []store.CoverageResult{
		{SymbolID: ptrInt64(dead[0]), Status: store.StatusFail},
		{SymbolID: ptrInt64(hot[0]), Status: store.StatusFail},
	})
	if err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}
	// Only `recency.hot` gets an annotation site.
	_ = s.Annotations().Upsert(ctx, store.AnnotationRow{
		FilePath: "hot.go", Line: 1, Kind: shared.AnnFeature,
		Value: "recency.hot", Source: shared.SourceAtlas,
	})

	p := New(s, a, Options{
		Now:      func() time.Time { return now },
		GitBlame: &recentBlame{ts: now.Add(-1 * 24 * time.Hour)},
	})
	got, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	byID := make(map[shared.FeatureID]SprintItem, len(got))
	for _, item := range got {
		byID[item.FeatureID] = item
	}
	if byID["recency.hot"].Priority <= byID["recency.dead"].Priority {
		t.Errorf("expected recency.hot > recency.dead in priority; hot=%.2f dead=%.2f",
			byID["recency.hot"].Priority, byID["recency.dead"].Priority)
	}
}

// -----------------------------------------------------------------------------
// Reasons emit a Cost label and explanation
// -----------------------------------------------------------------------------

func TestRank_ReasonsAndCostPresent(t *testing.T) {
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()
	seedFeatureWithSymbols(t, s, "reasons.x", 4, "x.go")
	p := New(s, a, Options{})
	got, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Cost == "" {
		t.Errorf("Cost empty: %+v", got[0])
	}
	if len(got[0].Reasons) == 0 {
		t.Errorf("Reasons empty: %+v", got[0])
	}
}

// -----------------------------------------------------------------------------
// Phantom / noise feature ID filtering (#80)
// -----------------------------------------------------------------------------

// TestRank_PhantomIDsExcludedFromBacklog verifies that feature IDs which do
// not conform to the canonical dot-namespaced FeatureID grammar (single tokens
// like "a", "is", "package", or em-dashes "—") are silently excluded from the
// sprint backlog even when they are already persisted in the store.
//
// This is the regression test for issue #80: "sprint backlog is dominated by
// phantom/noise features, making it non-actionable".
func TestRank_PhantomIDsExcludedFromBacklog(t *testing.T) {
	s := openTestStore(t)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()

	// Phantom IDs — all lack a dot-namespaced segment (noise from @testreg).
	phantoms := []shared.FeatureID{"a", "is", "package", "—" /* em-dash */}
	// Real IDs — well-formed dot-namespaced names.
	real := []shared.FeatureID{"auth.login", "pantry.add-item"}

	// Seed all into the store so Rank must see them.
	for _, id := range phantoms {
		if err := s.Features().Upsert(ctx, store.Feature{ID: id, Title: string(id)}); err != nil {
			t.Fatalf("Upsert phantom %q: %v", id, err)
		}
	}
	for _, id := range real {
		seedFeatureWithSymbols(t, s, id, 1, string(id)+".go")
	}

	p := New(s, a, Options{})
	got, err := p.Rank(ctx)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}

	// Build a set of returned IDs.
	returned := make(map[shared.FeatureID]bool, len(got))
	for _, item := range got {
		returned[item.FeatureID] = true
	}

	// Assert: no phantom ID appears in the output.
	for _, id := range phantoms {
		if returned[id] {
			t.Errorf("phantom ID %q should be excluded from sprint backlog but was included", id)
		}
	}

	// Assert: all real IDs are present.
	for _, id := range real {
		if !returned[id] {
			t.Errorf("real ID %q is missing from sprint backlog", id)
		}
	}

	// Assert: only real IDs are present (exact count).
	if got, want := len(got), len(real); got != want {
		t.Errorf("backlog len = %d, want %d (only real features)", got, want)
	}
}

// TestRank_PhantomIDsTable is a table-driven companion that exercises each
// phantom shape individually so a future regression pinpoints the offending
// ID class.
func TestRank_PhantomIDsTable(t *testing.T) {
	cases := []struct {
		id      shared.FeatureID
		isValid bool
		desc    string
	}{
		{"a", false, "single letter — stop-word"},
		{"is", false, "two-letter stop-word"},
		{"package", false, "Go keyword without dot"},
		{"—", false, "em-dash"},
		{"tags.", false, "trailing dot"},
		{".bad", false, "leading dot"},
		{"Bad.feature", false, "uppercase segment"},
		{"auth.login", true, "valid dot-namespaced"},
		{"pantry.add-item", true, "valid kebab segment"},
		{"batch-sessions.cook", true, "valid multi-segment kebab"},
		{"meal-prep.13-items", true, "valid segment with digits"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.id), func(t *testing.T) {
			t.Parallel()
			got := shared.IsValidFeatureID(tc.id)
			if got != tc.isValid {
				t.Errorf("IsValidFeatureID(%q) = %v, want %v (%s)",
					tc.id, got, tc.isValid, tc.desc)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Defensive wiring
// -----------------------------------------------------------------------------

func TestNew_NilStoreOrAudit(t *testing.T) {
	p := New(nil, nil, Options{})
	if _, err := p.Rank(context.Background()); !errors.Is(err, errNilStore) {
		t.Fatalf("Rank err = %v, want errNilStore", err)
	}
	if _, err := p.TopN(context.Background(), 5); !errors.Is(err, errNilStore) {
		t.Fatalf("TopN err = %v, want errNilStore", err)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func ptrInt64(v int64) *int64 { return &v }

// recentBlame is a deterministic GitBlameSource returning a fixed timestamp.
type recentBlame struct{ ts time.Time }

func (r *recentBlame) AuthorDate(context.Context, string, int) (time.Time, error) {
	return r.ts, nil
}
