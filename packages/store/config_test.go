package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestConfig_SetGetUpsert(t *testing.T) {
	s := openTestStore(t)
	cfg := s.Config()
	ctx := context.Background()

	if err := cfg.Set(ctx, "log.level", "debug"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := cfg.Get(ctx, "log.level")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "debug" {
		t.Errorf("Get = %q, want \"debug\"", got)
	}

	// Upsert path.
	if err := cfg.Set(ctx, "log.level", "info"); err != nil {
		t.Fatalf("Set #2: %v", err)
	}
	got, _ = cfg.Get(ctx, "log.level")
	if got != "info" {
		t.Errorf("after upsert Get = %q, want \"info\"", got)
	}

	// All ordering.
	if err := cfg.Set(ctx, "scan.default_scope", "src"); err != nil {
		t.Fatalf("Set scan.default_scope: %v", err)
	}
	all, err := cfg.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("All len = %d, want 2", len(all))
	}
	if all[0].Key != "log.level" || all[1].Key != "scan.default_scope" {
		t.Errorf("All ordering wrong: %+v", all)
	}
}

func TestConfig_GetMissing_ReturnsErrNotFound(t *testing.T) {
	cfg := openTestStore(t).Config()
	_, err := cfg.Get(context.Background(), "missing")
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
}

func TestConfig_Delete(t *testing.T) {
	cfg := openTestStore(t).Config()
	ctx := context.Background()

	_ = cfg.Set(ctx, "k", "v")
	if err := cfg.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := cfg.Delete(ctx, "k"); err != nil { // idempotent
		t.Fatalf("Delete idempotent: %v", err)
	}
}
