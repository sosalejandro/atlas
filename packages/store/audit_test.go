package store

import (
	"context"
	"testing"
	"time"
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
