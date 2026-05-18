package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestFileHashes_UpsertGet(t *testing.T) {
	fh := openTestStore(t).FileHashes()
	ctx := context.Background()

	row := FileHashRow{
		FilePath:    "src/foo.go",
		ContentHash: "abc123",
		ModTime:     time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		LastScanned: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := fh.Upsert(ctx, row); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := fh.Get(ctx, "src/foo.go")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ContentHash != "abc123" {
		t.Errorf("ContentHash = %q, want abc123", got.ContentHash)
	}

	// Upsert updates fields.
	row.ContentHash = "def456"
	if err := fh.Upsert(ctx, row); err != nil {
		t.Fatalf("Upsert refresh: %v", err)
	}
	got, _ = fh.Get(ctx, "src/foo.go")
	if got.ContentHash != "def456" {
		t.Errorf("after refresh ContentHash = %q, want def456", got.ContentHash)
	}
}

func TestFileHashes_GetMissing(t *testing.T) {
	fh := openTestStore(t).FileHashes()
	_, err := fh.Get(context.Background(), "nope")
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Get(nope) err = %v, want ErrNotFound", err)
	}
}

func TestFileHashes_ListOrdered(t *testing.T) {
	fh := openTestStore(t).FileHashes()
	ctx := context.Background()

	for _, p := range []string{"b.go", "a.go", "c.go"} {
		if err := fh.Upsert(ctx, FileHashRow{FilePath: p, ContentHash: "h"}); err != nil {
			t.Fatalf("Upsert %s: %v", p, err)
		}
	}
	out, err := fh.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 || out[0].FilePath != "a.go" || out[2].FilePath != "c.go" {
		t.Errorf("List ordering wrong: %+v", out)
	}
}
