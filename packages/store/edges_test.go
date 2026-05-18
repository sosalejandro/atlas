package store

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestEdges_InsertIdempotent(t *testing.T) {
	s := openTestStore(t)
	syms := s.Symbols()
	edges := s.Edges()
	ctx := context.Background()

	a, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1})
	b, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1})

	row := EdgeRow{FromID: a, ToID: b, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 7}
	id1, err := edges.Insert(ctx, row)
	if err != nil {
		t.Fatalf("Insert #1: %v", err)
	}
	id2, err := edges.Insert(ctx, row)
	if err != nil {
		t.Fatalf("Insert #2: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("re-Insert returned different surrogate id: %d vs %d", id1, id2)
	}
}

func TestEdges_OutIn(t *testing.T) {
	s := openTestStore(t)
	syms := s.Symbols()
	edges := s.Edges()
	ctx := context.Background()

	a, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1})
	b, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1})
	c, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.C", Kind: shared.KindFunc, FilePath: "src/c.go", Line: 1})

	for _, e := range []EdgeRow{
		{FromID: a, ToID: b, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 5},
		{FromID: a, ToID: c, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 6},
		{FromID: b, ToID: c, Kind: EdgeKindCall, FilePath: "src/b.go", Line: 5},
	} {
		if _, err := edges.Insert(ctx, e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	out, err := edges.Out(ctx, a)
	if err != nil {
		t.Fatalf("Out: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("Out(a) len = %d, want 2", len(out))
	}

	in, err := edges.In(ctx, c)
	if err != nil {
		t.Fatalf("In: %v", err)
	}
	if len(in) != 2 {
		t.Errorf("In(c) len = %d, want 2", len(in))
	}
}

func TestEdges_Walk_RecursiveCTE(t *testing.T) {
	s := openTestStore(t)
	syms := s.Symbols()
	edges := s.Edges()
	ctx := context.Background()

	// Chain: A -> B -> C -> D
	a, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.A", Kind: shared.KindFunc, FilePath: "src/a.go", Line: 1})
	b, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.B", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 1})
	c, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.C", Kind: shared.KindFunc, FilePath: "src/c.go", Line: 1})
	d, _ := syms.Insert(ctx, SymbolRow{QualifiedName: "p.D", Kind: shared.KindFunc, FilePath: "src/d.go", Line: 1})

	for _, e := range []EdgeRow{
		{FromID: a, ToID: b, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 2},
		{FromID: b, ToID: c, Kind: EdgeKindCall, FilePath: "src/b.go", Line: 2},
		{FromID: c, ToID: d, Kind: EdgeKindCall, FilePath: "src/c.go", Line: 2},
	} {
		if _, err := edges.Insert(ctx, e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	walk, err := edges.Walk(ctx, a, 10)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(walk) != 3 {
		t.Fatalf("Walk len = %d, want 3; got %+v", len(walk), walk)
	}
	if walk[len(walk)-1].Depth != 3 {
		t.Errorf("max depth = %d, want 3", walk[len(walk)-1].Depth)
	}

	// maxDepth caps the chain.
	walk2, err := edges.Walk(ctx, a, 2)
	if err != nil {
		t.Fatalf("Walk(maxDepth=2): %v", err)
	}
	if len(walk2) != 2 {
		t.Errorf("Walk(maxDepth=2) len = %d, want 2", len(walk2))
	}
}
