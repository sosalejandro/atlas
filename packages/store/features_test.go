package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func mustPtr(s string) *string { return &s }

func TestFeatures_UpsertGetList(t *testing.T) {
	feats := openTestStore(t).Features()
	ctx := context.Background()

	if err := feats.Upsert(ctx, Feature{
		ID:    "auth.login",
		Title: "User login",
		Owner: mustPtr("@auth-team"),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	f, err := feats.Get(ctx, "auth.login")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.Title != "User login" || f.Owner == nil || *f.Owner != "@auth-team" {
		t.Errorf("unexpected feature: %+v", f)
	}
	if f.Kind != FeatureKindFeature {
		t.Errorf("default Kind = %q, want %q", f.Kind, FeatureKindFeature)
	}

	// Upsert refreshes title; original created_at preserved.
	if err := feats.Upsert(ctx, Feature{ID: "auth.login", Title: "Login v2"}); err != nil {
		t.Fatalf("Upsert refresh: %v", err)
	}
	refreshed, _ := feats.Get(ctx, "auth.login")
	if refreshed.Title != "Login v2" {
		t.Errorf("refreshed.Title = %q, want \"Login v2\"", refreshed.Title)
	}
	if !refreshed.CreatedAt.Equal(f.CreatedAt) {
		t.Errorf("CreatedAt drift after upsert: before=%v after=%v", f.CreatedAt, refreshed.CreatedAt)
	}
}

func TestFeatures_GetMissing(t *testing.T) {
	feats := openTestStore(t).Features()
	_, err := feats.Get(context.Background(), "nope")
	if !errors.Is(err, shared.ErrFeatureNotFound) {
		t.Fatalf("Get(nope) err = %v, want ErrFeatureNotFound", err)
	}
}

func TestFeatures_List_FiltersByKind(t *testing.T) {
	feats := openTestStore(t).Features()
	ctx := context.Background()

	_ = feats.Upsert(ctx, Feature{ID: "a", Title: "A", Kind: FeatureKindFeature})
	_ = feats.Upsert(ctx, Feature{ID: "b", Title: "B", Kind: FeatureKindContract})

	kind := FeatureKindContract
	out, err := feats.List(ctx, FeatureFilter{Kind: &kind})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].ID != "b" {
		t.Errorf("List(Kind=contract) = %+v, want exactly [b]", out)
	}
}

func TestFeatures_DeleteCascadesFeatureSymbols(t *testing.T) {
	s := openTestStore(t)
	feats := s.Features()
	syms := s.Symbols()
	links := s.FeatureSymbols()
	ctx := context.Background()

	_ = feats.Upsert(ctx, Feature{ID: "auth.login", Title: "Login"})
	id, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.LoginHandler", Kind: shared.KindFunc,
		FilePath: "src/handler.go", Line: 10,
	})
	if err != nil {
		t.Fatalf("Insert symbol: %v", err)
	}
	if err := links.Link(ctx, FeatureSymbolLink{
		FeatureID: "auth.login", SymbolID: id, Role: RoleImpl,
	}); err != nil {
		t.Fatalf("Link: %v", err)
	}

	if err := feats.Delete(ctx, "auth.login"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if out, _ := links.ListByFeature(ctx, "auth.login"); len(out) != 0 {
		t.Errorf("feature_symbols not cascaded: %+v", out)
	}
}
