package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

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

// TestAuditSnapshots_TableIsDropped confirms migration 0006 (closes #21)
// removed the legacy audit_snapshots table — the per-feature shape was never
// written to in production. The drop migration is destructive so the test
// guards against an accidental revert (e.g. someone re-adding the CREATE
// TABLE to 0001 during a rebase).
func TestAuditSnapshots_TableIsDropped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var name string
	err := s.sqlDB().
		QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'audit_snapshots'`).
		Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows querying audit_snapshots, got name=%q err=%v", name, err)
	}
}
