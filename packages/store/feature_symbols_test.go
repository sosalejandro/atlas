package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestFeatureSymbols_LinkIdempotent(t *testing.T) {
	s := openTestStore(t)
	feats := s.Features()
	syms := s.Symbols()
	links := s.FeatureSymbols()
	ctx := context.Background()

	_ = feats.Upsert(ctx, Feature{ID: "auth.login", Title: "Login"})
	id, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.Handler", Kind: shared.KindFunc, FilePath: "src/h.go", Line: 1})

	row := FeatureSymbolLink{FeatureID: "auth.login", SymbolID: id, Role: RoleImpl}
	if err := links.Link(ctx, row); err != nil {
		t.Fatalf("Link #1: %v", err)
	}
	if err := links.Link(ctx, row); err != nil {
		t.Fatalf("Link #2: %v", err)
	}

	out, err := links.ListByFeature(ctx, "auth.login")
	if err != nil {
		t.Fatalf("ListByFeature: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("got %d link rows, want 1: %+v", len(out), out)
	}
}

func TestFeatureSymbols_RoleSeparate(t *testing.T) {
	s := openTestStore(t)
	_ = s.Features().Upsert(context.Background(), Feature{ID: "f", Title: "F"})
	id, _ := s.Symbols().Insert(context.Background(), SymbolRow{
		QualifiedName: "p.X", Kind: shared.KindFunc, FilePath: "src/x.go", Line: 1,
	})

	links := s.FeatureSymbols()
	ctx := context.Background()
	if err := links.Link(ctx, FeatureSymbolLink{FeatureID: "f", SymbolID: id, Role: RoleImpl}); err != nil {
		t.Fatal(err)
	}
	if err := links.Link(ctx, FeatureSymbolLink{FeatureID: "f", SymbolID: id, Role: RoleTest}); err != nil {
		t.Fatal(err)
	}

	out, _ := links.ListByFeature(ctx, "f")
	if len(out) != 2 {
		t.Errorf("want 2 rows (impl + test), got %d: %+v", len(out), out)
	}
}

func TestFeatureSymbols_UnlinkMissing(t *testing.T) {
	links := openTestStore(t).FeatureSymbols()
	err := links.Unlink(context.Background(), "nope", 9999, RoleImpl)
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Unlink missing err = %v, want ErrNotFound", err)
	}
}
