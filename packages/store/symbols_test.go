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

// TestSymbols_SetAndFindByPattern covers the Phase 6f pattern_matches
// column round-trip: SetPatternMatches persists JSON, FindByPattern
// reads only symbols whose JSON contains the requested pattern token.
func TestSymbols_SetAndFindByPattern(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	// Three symbols: one outbox-append, one canonical-service, one with
	// no recogniser hits at all.
	for _, sym := range []SymbolRow{
		{QualifiedName: "S.OutboxOnly", Kind: shared.KindMethod, FilePath: "src/a.go", Line: 10},
		{QualifiedName: "S.Canonical", Kind: shared.KindMethod, FilePath: "src/a.go", Line: 30},
		{QualifiedName: "S.NoMatches", Kind: shared.KindMethod, FilePath: "src/b.go", Line: 5},
	} {
		if _, err := syms.Insert(ctx, sym); err != nil {
			t.Fatalf("Insert %s: %v", sym.QualifiedName, err)
		}
	}

	// Persist JSON payloads with the closed-enum pattern tokens.
	outboxJSON := `[{"pattern":"outbox-append","symbol":"S.OutboxOnly","position":{"path":"src/a.go","line":10},"confidence":1.0}]`
	if err := syms.SetPatternMatches(ctx, "S.OutboxOnly", outboxJSON); err != nil {
		t.Fatalf("SetPatternMatches outbox: %v", err)
	}
	canonJSON := `[{"pattern":"canonical-service","symbol":"S.Canonical","position":{"path":"src/a.go","line":30},"confidence":1.0}]`
	if err := syms.SetPatternMatches(ctx, "S.Canonical", canonJSON); err != nil {
		t.Fatalf("SetPatternMatches canonical: %v", err)
	}

	// FindByPattern → outbox-append must return exactly the outbox row.
	outbox, err := syms.FindByPattern(ctx, "outbox-append")
	if err != nil {
		t.Fatalf("FindByPattern outbox: %v", err)
	}
	if len(outbox) != 1 || outbox[0].QualifiedName != "S.OutboxOnly" {
		t.Fatalf("FindByPattern outbox = %+v", outbox)
	}
	if outbox[0].PatternMatches == nil || *outbox[0].PatternMatches != outboxJSON {
		t.Errorf("returned PatternMatches roundtrip mismatch: %+v", outbox[0].PatternMatches)
	}

	canon, err := syms.FindByPattern(ctx, "canonical-service")
	if err != nil {
		t.Fatalf("FindByPattern canonical: %v", err)
	}
	if len(canon) != 1 || canon[0].QualifiedName != "S.Canonical" {
		t.Fatalf("FindByPattern canonical = %+v", canon)
	}

	// Unknown pattern → empty result, no error.
	empty, err := syms.FindByPattern(ctx, "saga-step-order")
	if err != nil {
		t.Fatalf("FindByPattern unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("FindByPattern unknown = %d rows, want 0", len(empty))
	}

	// Empty input clears the column.
	if err := syms.SetPatternMatches(ctx, "S.OutboxOnly", ""); err != nil {
		t.Fatalf("SetPatternMatches clear: %v", err)
	}
	after, err := syms.FindByPattern(ctx, "outbox-append")
	if err != nil {
		t.Fatalf("FindByPattern after clear: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("FindByPattern after clear = %d rows, want 0", len(after))
	}

	// "null" input is also treated as clear (matches json.Marshal(nil) → "null").
	_ = syms.SetPatternMatches(ctx, "S.Canonical", "null")
	stillThere, _ := syms.FindByPattern(ctx, "canonical-service")
	if len(stillThere) != 0 {
		t.Errorf(`SetPatternMatches "null" should clear; got %d rows`, len(stillThere))
	}
}

// TestSymbols_FindByPattern_NoSubstringCollision covers the precision
// pressure dim — `outbox-append` must NOT match a stored
// `outbox-append-extended` payload (the LIKE bound includes the closing
// quote so this is impossible by construction; the test guards the
// contract).
func TestSymbols_FindByPattern_NoSubstringCollision(t *testing.T) {
	syms := openTestStore(t).Symbols()
	ctx := context.Background()

	if _, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: "S.Long",
		Kind:          shared.KindMethod,
		FilePath:      "src/a.go",
		Line:          1,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// JSON with a longer pattern name — intentionally avoiding the
	// `outbox-append` exact spelling.
	longJSON := `[{"pattern":"outbox-append-extended","symbol":"S.Long","position":{"path":"src/a.go","line":1},"confidence":0.9}]`
	if err := syms.SetPatternMatches(ctx, "S.Long", longJSON); err != nil {
		t.Fatalf("SetPatternMatches: %v", err)
	}

	res, err := syms.FindByPattern(ctx, "outbox-append")
	if err != nil {
		t.Fatalf("FindByPattern: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("substring-collision leak: FindByPattern(outbox-append) returned %d rows", len(res))
	}
}

// TestSymbols_FindByPattern_EmptyPattern guards the input-validation path.
func TestSymbols_FindByPattern_EmptyPattern(t *testing.T) {
	syms := openTestStore(t).Symbols()
	if _, err := syms.FindByPattern(context.Background(), ""); err == nil {
		t.Fatal("FindByPattern(\"\") should error")
	}
}
