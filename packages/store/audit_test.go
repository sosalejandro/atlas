package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestAuditSnapshots_InsertList(t *testing.T) {
	s := openTestStore(t)
	_ = s.Features().Upsert(context.Background(), Feature{ID: "f", Title: "F"})
	aud := s.AuditSnapshots()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := aud.Insert(ctx, AuditSnapshot{
			FeatureID:       "f",
			Score:           70 + i,
			LayerScoresJSON: `{"handler":80}`,
			TakenAt:         time.Now().UTC().Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}

	out, err := aud.ListByFeature(ctx, "f", 10)
	if err != nil {
		t.Fatalf("ListByFeature: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("ListByFeature len = %d, want 3", len(out))
	}
	// Most recent first.
	if out[0].Score < out[1].Score {
		t.Errorf("ListByFeature not DESC by taken_at: %+v", out)
	}
}

func TestAuditSnapshots_InsertDefaultsJSON(t *testing.T) {
	s := openTestStore(t)
	_ = s.Features().Upsert(context.Background(), Feature{ID: "f", Title: "F"})

	id, err := s.AuditSnapshots().Insert(context.Background(), AuditSnapshot{
		FeatureID: "f", Score: 0,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id == 0 {
		t.Fatal("got 0 surrogate id")
	}
}

func TestAuditSnapshotRuns_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	runs := s.AuditSnapshotRuns()
	ctx := context.Background()

	id, err := runs.Insert(ctx, AuditSnapshotRun{
		ScoreJSON: `[{"feature_id":"a","score":42}]`,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id == 0 {
		t.Fatal("got 0 surrogate id")
	}

	got, err := runs.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	if got.ID != id {
		t.Errorf("ID = %d, want %d", got.ID, id)
	}
	if got.ScoreJSON != `[{"feature_id":"a","score":42}]` {
		t.Errorf("ScoreJSON = %q, want preserved", got.ScoreJSON)
	}
	if got.ComputedAt.IsZero() {
		t.Error("ComputedAt is zero (expected default CURRENT_TIMESTAMP)")
	}

	latest, err := runs.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID != id {
		t.Errorf("Latest ID = %d, want %d", latest.ID, id)
	}

	list, err := runs.List(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
}

func TestAuditSnapshotRuns_NotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.AuditSnapshotRuns().Get(ctx, 999); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Get(999) error = %v, want shared.ErrNotFound", err)
	}
	if _, err := s.AuditSnapshotRuns().Latest(ctx); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Latest empty error = %v, want shared.ErrNotFound", err)
	}
}

func TestAuditSnapshotRuns_InsertWithTime_OrdersDesc(t *testing.T) {
	s := openTestStore(t)
	runs := s.AuditSnapshotRuns()
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	ids := make([]int64, 3)
	for i := 0; i < 3; i++ {
		id, err := runs.Insert(ctx, AuditSnapshotRun{
			ComputedAt: base.Add(time.Duration(i) * time.Minute),
			ScoreJSON:  `[]`,
		})
		if err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
		ids[i] = id
	}

	list, err := runs.List(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List len = %d, want 3", len(list))
	}
	// Most recent first.
	if list[0].ComputedAt.Before(list[1].ComputedAt) {
		t.Errorf("List not DESC: %v vs %v", list[0].ComputedAt, list[1].ComputedAt)
	}
}
