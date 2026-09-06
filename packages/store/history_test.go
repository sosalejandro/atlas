package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

func f64(v float64) *float64 { return &v }

// at returns a deterministic timestamp N days after a fixed epoch. The
// history series is ordered by measured_at, so every test that asserts an
// ordering needs distinguishable, non-now timestamps.
func at(days int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
}

func TestHistory_RecordAndGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.History().Record(ctx, HistoryPoint{
		CommitSHA:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MeasuredAt:  at(0),
		Score:       f64(72.5),
		Denominator: 120,
		Features: []HistoryFeaturePoint{
			{FeatureID: "feat.a", Score: f64(80), Denominator: 100},
			{FeatureID: "feat.b", Score: nil, Denominator: 20},
		},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id == 0 {
		t.Fatal("Record returned id=0")
	}

	got, err := s.History().Get(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Score == nil || *got.Score != 72.5 {
		t.Errorf("Score = %v, want 72.5", got.Score)
	}
	if got.Denominator != 120 {
		t.Errorf("Denominator = %d, want 120", got.Denominator)
	}
	if !got.MeasuredAt.Equal(at(0)) {
		t.Errorf("MeasuredAt = %v, want %v", got.MeasuredAt, at(0))
	}
	if len(got.Features) != 2 {
		t.Fatalf("Features = %d rows, want 2", len(got.Features))
	}
	// Ordered by feature id so the series is stable between reads.
	if got.Features[0].FeatureID != shared.FeatureID("feat.a") {
		t.Errorf("Features[0] = %q, want feat.a", got.Features[0].FeatureID)
	}
	// A nil per-feature score must round-trip as nil, NOT as 0 — that
	// distinction is the whole point of the column being nullable.
	if got.Features[1].Score != nil {
		t.Errorf("Features[1].Score = %v, want nil", *got.Features[1].Score)
	}
}

// A point whose coverage evidence was absent records a NULL score. Reading
// it back as 0 would make `trend` plot a cliff that never happened.
func TestHistory_NullScoreRoundTrips(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.History().Record(ctx, HistoryPoint{
		CommitSHA:   "b1",
		MeasuredAt:  at(1),
		Score:       nil,
		Denominator: 42,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := s.History().Get(ctx, "b1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Score != nil {
		t.Fatalf("Score = %v, want nil", *got.Score)
	}
}

// Two measurements of the same commit are ONE history point. A CI job that
// retries must not double the series.
func TestHistory_RecordIsIdempotentPerCommit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first, err := s.History().Record(ctx, HistoryPoint{
		CommitSHA:   "c0ffee",
		MeasuredAt:  at(0),
		Score:       f64(50),
		Denominator: 10,
		Features:    []HistoryFeaturePoint{{FeatureID: "x", Score: f64(50), Denominator: 10}},
	})
	if err != nil {
		t.Fatalf("Record #1: %v", err)
	}
	second, err := s.History().Record(ctx, HistoryPoint{
		CommitSHA:   "c0ffee",
		MeasuredAt:  at(2),
		Score:       f64(61),
		Denominator: 11,
		Features:    []HistoryFeaturePoint{{FeatureID: "x", Score: f64(61), Denominator: 11}},
	})
	if err != nil {
		t.Fatalf("Record #2: %v", err)
	}
	if first != second {
		t.Errorf("Record re-measured the same commit into a new row: %d then %d", first, second)
	}

	page, err := s.History().List(ctx, HistoryFilter{})
	pts := page.Points
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("List = %d points, want 1 (re-measure must replace)", len(pts))
	}
	if pts[0].Score == nil || *pts[0].Score != 61 {
		t.Errorf("Score = %v, want the re-measured 61", pts[0].Score)
	}
	if len(pts[0].Features) != 1 {
		t.Fatalf("Features = %d, want 1 (stale child rows must not survive)", len(pts[0].Features))
	}
	if pts[0].Features[0].Denominator != 11 {
		t.Errorf("feature denominator = %d, want the re-measured 11", pts[0].Features[0].Denominator)
	}
}

func TestHistory_ListOrdersOldestFirstAndHonoursSince(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i, sha := range []string{"s0", "s1", "s2"} {
		if _, err := s.History().Record(ctx, HistoryPoint{
			CommitSHA:   sha,
			MeasuredAt:  at(i),
			Score:       f64(float64(10 * i)),
			Denominator: 5,
		}); err != nil {
			t.Fatalf("Record %s: %v", sha, err)
		}
	}

	allPage, err := s.History().List(ctx, HistoryFilter{})
	all := allPage.Points
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List = %d, want 3", len(all))
	}
	// A series reads left to right in time.
	if all[0].CommitSHA != "s0" || all[2].CommitSHA != "s2" {
		t.Errorf("List order = %q..%q, want s0..s2", all[0].CommitSHA, all[2].CommitSHA)
	}

	recentPage, err := s.History().List(ctx, HistoryFilter{Since: at(1)})
	recent := recentPage.Points
	if err != nil {
		t.Fatalf("List(Since): %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("List(Since=day1) = %d, want 2", len(recent))
	}
	if recent[0].CommitSHA != "s1" {
		t.Errorf("List(Since) first = %q, want s1", recent[0].CommitSHA)
	}
}

func TestHistory_ListLimitKeepsTheNewest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i, sha := range []string{"l0", "l1", "l2", "l3"} {
		if _, err := s.History().Record(ctx, HistoryPoint{
			CommitSHA: sha, MeasuredAt: at(i), Score: f64(1), Denominator: 1,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	// A capped series must be the most RECENT window, still oldest-first.
	gotPage, err := s.History().List(ctx, HistoryFilter{Limit: 2})
	got := gotPage.Points
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(Limit=2) = %d points, want 2", len(got))
	}
	if got[0].CommitSHA != "l2" || got[1].CommitSHA != "l3" {
		t.Errorf("List(Limit=2) = %q,%q; want l2,l3", got[0].CommitSHA, got[1].CommitSHA)
	}
}

func TestHistory_GetMissingIsNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.History().Get(context.Background(), "nope"); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want shared.ErrNotFound", err)
	}
}

func TestHistory_ResolvePrefix(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, sha := range []string{"abc123deadbeef", "abd999feedface"} {
		if _, err := s.History().Record(ctx, HistoryPoint{
			CommitSHA: sha, MeasuredAt: at(0), Score: f64(1), Denominator: 1,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, err := s.History().Resolve(ctx, "abc")
	if err != nil {
		t.Fatalf("Resolve(abc): %v", err)
	}
	if got != "abc123deadbeef" {
		t.Errorf("Resolve(abc) = %q", got)
	}
	// An ambiguous prefix must fail loudly rather than pick one.
	if _, err := s.History().Resolve(ctx, "ab"); err == nil {
		t.Error("Resolve(ab) succeeded on an ambiguous prefix, want error")
	}
	if _, err := s.History().Resolve(ctx, "zz"); !errors.Is(err, shared.ErrNotFound) {
		t.Errorf("Resolve(zz) = %v, want shared.ErrNotFound", err)
	}
}

func TestHistory_PruneDropsOldPointsAndTheirFeatures(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i, sha := range []string{"p0", "p1", "p2"} {
		if _, err := s.History().Record(ctx, HistoryPoint{
			CommitSHA:   sha,
			MeasuredAt:  at(i),
			Score:       f64(1),
			Denominator: 1,
			Features:    []HistoryFeaturePoint{{FeatureID: "f", Score: f64(1), Denominator: 1}},
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	n, err := s.History().Prune(ctx, at(2))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("Prune = %d rows, want 2", n)
	}

	var orphans int
	if err := s.sqlDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM coverage_history_features fh
		 WHERE NOT EXISTS (SELECT 1 FROM coverage_history h WHERE h.id = fh.history_id)`,
	).Scan(&orphans); err != nil {
		t.Fatalf("orphan count: %v", err)
	}
	if orphans != 0 {
		t.Errorf("prune left %d orphaned per-feature rows", orphans)
	}
}

// --limit 0 is documented as "no cap", but an uncapped List is served up to
// defaultHistoryListLimit. Returning that page as if it were the whole series
// is a lie the reader cannot detect: the points a cap discards are the
// OLDEST, which is exactly where a long-run trend is read from.
func TestHistory_ListReportsTruncationRatherThanHidingIt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i, sha := range []string{"t0", "t1", "t2", "t3"} {
		if _, err := s.History().Record(ctx, HistoryPoint{
			CommitSHA: sha, MeasuredAt: at(i), Score: f64(1), Denominator: 1,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	capped, err := s.History().List(ctx, HistoryFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List(Limit=2): %v", err)
	}
	if !capped.Truncated {
		t.Error("Truncated = false for 2 of 4 points; the omitted two are invisible")
	}
	if capped.Cap != 2 {
		t.Errorf("Cap = %d, want 2", capped.Cap)
	}
	if len(capped.Points) != 2 {
		t.Errorf("Points = %d, want exactly the cap (the +1 probe row must not leak)", len(capped.Points))
	}
	// The cap keeps the RECENT window; the flag describes what it dropped.
	if capped.Points[0].CommitSHA != "t2" || capped.Points[1].CommitSHA != "t3" {
		t.Errorf("capped window = %q,%q; want t2,t3", capped.Points[0].CommitSHA, capped.Points[1].CommitSHA)
	}

	// A window that holds everything is not truncated, and still names the
	// cap it was served under so a caller can see the headroom.
	whole, err := s.History().List(ctx, HistoryFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if whole.Truncated {
		t.Error("Truncated = true for 4 points under the default cap")
	}
	if whole.Cap != defaultHistoryListLimit {
		t.Errorf("Cap = %d, want the default %d", whole.Cap, defaultHistoryListLimit)
	}
}
