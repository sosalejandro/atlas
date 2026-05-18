package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestSymbols_InsertIdempotent(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	id1, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Foo",
		Kind:          shared.KindFunc,
		FilePath:      "src/foo.go",
		Line:          7,
	})
	if err != nil {
		t.Fatalf("Insert #1: %v", err)
	}
	id2, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Foo",
		Kind:          shared.KindFunc,
		FilePath:      "src/foo.go",
		Line:          7,
	})
	if err != nil {
		t.Fatalf("Insert #2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("re-Insert returned different surrogate id: %d vs %d", id1, id2)
	}
}

func TestSymbols_NormalizesAuditKindsToFunc(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	// shared.KindHandler is an audit-layer value not in the §5.4 CHECK set;
	// the adapter must collapse it so the row is acceptable.
	_, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "pkg.Handler",
		Kind:          shared.KindHandler,
		FilePath:      "src/x.go",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("Insert with KindHandler: %v", err)
	}
	r, err := syms.FindByQualifiedName(ctx, "pkg.Handler")
	if err != nil {
		t.Fatalf("FindByQualifiedName: %v", err)
	}
	if r.Kind != shared.KindFunc {
		t.Errorf("normalized kind = %q, want %q", r.Kind, shared.KindFunc)
	}
}

func TestSymbols_FindByQualifiedName_Missing(t *testing.T) {
	syms := openTestStore(t).Symbols()
	_, err := syms.FindByQualifiedName(context.Background(), "nope")
	if !errors.Is(err, shared.ErrSymbolNotFound) {
		t.Fatalf("FindByQualifiedName(nope) err = %v, want ErrSymbolNotFound", err)
	}
}

func TestSymbols_ListFilter(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	for _, sym := range []SymbolRow{
		{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1, Package: mustPtr("pkgA")},
		{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1, Package: mustPtr("pkgA")},
		{QualifiedName: "p.C", Kind: shared.KindFunc, FilePath: "src/c.go", Line: 1, Package: mustPtr("pkgB")},
	} {
		if _, err := syms.Insert(ctx, sym); err != nil {
			t.Fatalf("Insert %s: %v", sym.QualifiedName, err)
		}
	}

	out, err := syms.List(ctx, SymbolFilter{Package: "pkgA"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("List(pkgA) len = %d, want 2", len(out))
	}
}

func TestSymbols_DeleteByFile_CascadesEdges(t *testing.T) {
	s := openTestStore(t)
	syms := s.Symbols()
	edges := s.Edges()
	ctx := context.Background()

	a, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1})
	b, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1})
	if _, err := edges.Insert(ctx, EdgeRow{
		FromID: a, ToID: b, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 2,
	}); err != nil {
		t.Fatalf("edges Insert: %v", err)
	}

	if err := syms.DeleteByFile(ctx, "src/a.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	if out, _ := edges.Out(ctx, a); len(out) != 0 {
		t.Errorf("edges not cascaded after symbol delete: %+v", out)
	}
}
