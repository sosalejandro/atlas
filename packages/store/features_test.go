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

// Locks in that the IDs-filter raw scan path returns the same Feature
// shape as the sqlc-backed fast paths — both decode the nullable cols
// (owner, deprecated_since, introduced_in) via the shared *string
// convention so a future schema column add can't break only one branch.
func TestFeatures_List_FiltersByIDs_RoundTripsNullableCols(t *testing.T) {
	feats := openTestStore(t).Features()
	ctx := context.Background()

	if err := feats.Upsert(ctx, Feature{
		ID:              "auth.login",
		Title:           "Login",
		Owner:           mustPtr("@auth-team"),
		DeprecatedSince: mustPtr("v2.0"),
		IntroducedIn:    mustPtr("v0.1"),
	}); err != nil {
		t.Fatalf("Upsert(owned): %v", err)
	}
	if err := feats.Upsert(ctx, Feature{
		ID:    "auth.logout",
		Title: "Logout",
		// no owner / deprecated / introduced — exercises the NULL path.
	}); err != nil {
		t.Fatalf("Upsert(bare): %v", err)
	}

	out, err := feats.List(ctx, FeatureFilter{
		IDs: []shared.FeatureID{"auth.login", "auth.logout"},
	})
	if err != nil {
		t.Fatalf("List(IDs): %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}

	byID := map[shared.FeatureID]Feature{out[0].ID: out[0], out[1].ID: out[1]}

	owned := byID["auth.login"]
	if owned.Owner == nil || *owned.Owner != "@auth-team" {
		t.Errorf("owned.Owner = %+v, want @auth-team", owned.Owner)
	}
	if owned.DeprecatedSince == nil || *owned.DeprecatedSince != "v2.0" {
		t.Errorf("owned.DeprecatedSince = %+v, want v2.0", owned.DeprecatedSince)
	}
	if owned.IntroducedIn == nil || *owned.IntroducedIn != "v0.1" {
		t.Errorf("owned.IntroducedIn = %+v, want v0.1", owned.IntroducedIn)
	}

	bare := byID["auth.logout"]
	if bare.Owner != nil {
		t.Errorf("bare.Owner = %+v, want nil", bare.Owner)
	}
	if bare.DeprecatedSince != nil || bare.IntroducedIn != nil {
		t.Errorf("bare nullable cols not nil: dep=%+v intro=%+v", bare.DeprecatedSince, bare.IntroducedIn)
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
