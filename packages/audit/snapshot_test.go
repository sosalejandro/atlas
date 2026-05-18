package audit

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestPersistSnapshot_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := New(s, Options{Now: func() time.Time { return now }})

	scores := []FeatureHealth{
		{
			FeatureID:  "auth.login",
			Score:      72.5,
			Components: map[string]float64{SignalCoverage: 80, SignalContractDrift: 50},
			Reasons:    []string{"coverage: 4/5 symbols passing (80%)"},
			SampledAt:  now,
		},
		{
			FeatureID:  "auth.logout",
			Score:      0,
			Components: map[string]float64{},
			Reasons:    []string{"no audit signals available"},
			SampledAt:  now,
		},
	}

	id, err := a.PersistSnapshot(ctx, scores)
	if err != nil {
		t.Fatalf("PersistSnapshot: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero snapshot id")
	}

	got, err := a.LoadSnapshot(ctx, id)
	if err != nil {
		t.Fatalf("LoadSnapshot(%d): %v", id, err)
	}
	if !reflect.DeepEqual(got, scores) {
		t.Errorf("LoadSnapshot mismatch:\n got=%+v\nwant=%+v", got, scores)
	}
}

func TestPersistSnapshot_EmptySliceWritesAndLoads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := New(s, Options{})

	id, err := a.PersistSnapshot(ctx, nil)
	if err != nil {
		t.Fatalf("PersistSnapshot(nil): %v", err)
	}
	out, err := a.LoadSnapshot(ctx, id)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty slice; got %+v", out)
	}
}

func TestLoadSnapshot_NotFound(t *testing.T) {
	s := openTestStore(t)
	a := New(s, Options{})
	_, err := a.LoadSnapshot(context.Background(), 999)
	if err == nil {
		t.Fatal("LoadSnapshot(999): expected error, got nil")
	}
	if !errors.Is(err, shared.ErrNotFound) {
		t.Errorf("err = %v, want wrapped shared.ErrNotFound", err)
	}
}

// Sanity check that the snapshot id obtained from PersistSnapshot matches
// what the store-level adapter would return on its own. Guards against the
// audit package silently using a different identifier convention.
func TestPersistSnapshot_IDMatchesStoreSurface(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := New(s, Options{})

	id, err := a.PersistSnapshot(ctx, []FeatureHealth{
		{FeatureID: "x", Score: 50},
	})
	if err != nil {
		t.Fatalf("PersistSnapshot: %v", err)
	}
	row, err := s.AuditSnapshotRuns().Get(ctx, id)
	if err != nil {
		t.Fatalf("store Get: %v", err)
	}
	if row.ID != id {
		t.Errorf("id mismatch: audit=%d store=%d", id, row.ID)
	}
}
