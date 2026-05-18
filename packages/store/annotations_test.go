package store

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestAnnotations_UpsertList(t *testing.T) {
	a := openTestStore(t).Annotations()
	ctx := context.Background()

	rows := []AnnotationRow{
		{FilePath: "src/foo.go", Line: 10, Kind: shared.AnnFeature, Value: "auth.login", Source: shared.SourceAtlas},
		{FilePath: "src/foo.go", Line: 11, Kind: shared.AnnOwner, Value: "@auth-team", Source: shared.SourceAtlas},
		{FilePath: "src/bar.go", Line: 5, Kind: shared.AnnFeature, Value: "meals.create", Source: shared.SourceTestreg},
	}
	for _, r := range rows {
		if err := a.Upsert(ctx, r); err != nil {
			t.Fatalf("Upsert %+v: %v", r, err)
		}
	}

	out, err := a.ListByFile(ctx, "src/foo.go")
	if err != nil {
		t.Fatalf("ListByFile: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("foo.go rows = %d, want 2", len(out))
	}
}

func TestAnnotations_DedupeOnReUpsert(t *testing.T) {
	a := openTestStore(t).Annotations()
	ctx := context.Background()

	row := AnnotationRow{
		FilePath: "src/foo.go", Line: 10, Kind: shared.AnnFeature,
		Value: "auth.login", Source: shared.SourceAtlas,
	}
	for i := 0; i < 3; i++ {
		if err := a.Upsert(ctx, row); err != nil {
			t.Fatalf("Upsert #%d: %v", i, err)
		}
	}
	out, _ := a.ListByFile(ctx, "src/foo.go")
	if len(out) != 1 {
		t.Errorf("dedupe failed: %d rows after 3 upserts", len(out))
	}
}

func TestAnnotations_SkipsUnknownKind(t *testing.T) {
	// AnnAPI is not part of the §5.11 CHECK set — adapter must no-op
	// instead of returning a constraint-violation error.
	a := openTestStore(t).Annotations()
	if err := a.Upsert(context.Background(), AnnotationRow{
		FilePath: "src/x.go", Line: 1, Kind: shared.AnnAPI, Value: "GET /x",
	}); err != nil {
		t.Fatalf("Upsert AnnAPI: %v", err)
	}
	out, _ := a.ListByFile(context.Background(), "src/x.go")
	if len(out) != 0 {
		t.Errorf("AnnAPI should have been skipped, got %+v", out)
	}
}

func TestAnnotations_DeleteByFile(t *testing.T) {
	a := openTestStore(t).Annotations()
	ctx := context.Background()
	_ = a.Upsert(ctx, AnnotationRow{
		FilePath: "src/foo.go", Line: 10, Kind: shared.AnnFeature,
		Value: "auth.login", Source: shared.SourceAtlas,
	})
	if err := a.DeleteByFile(ctx, "src/foo.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	out, _ := a.ListByFile(ctx, "src/foo.go")
	if len(out) != 0 {
		t.Errorf("DeleteByFile left rows: %+v", out)
	}
}
