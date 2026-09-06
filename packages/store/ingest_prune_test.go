package store

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

func indexWithSymbols(syms ...shared.Symbol) *codeindex.Index {
	g := graph.New()
	for i := range syms {
		s := syms[i]
		g.AddNode(&graph.Node{Symbol: s})
	}
	return &codeindex.Index{Root: ".", Graph: g, Symbols: syms, FileHashes: map[string]codeindex.FileHash{}}
}

// A rescan of a CHANGED file must drop the symbols that no longer exist in
// it. Without pruning, a renamed or deleted function keeps its row — with its
// old line number — forever, and the coverage ingest keeps attributing
// executed statements to a span that no longer holds any code.
func TestIngest_PrunesSymbolsMissingFromRescannedFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	first := indexWithSymbols(
		shared.Symbol{ID: "pkg.OldName", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 10}, EndLine: 20},
		shared.Symbol{ID: "pkg.Keep", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 30}, EndLine: 40},
	)
	if _, err := s.Ingest(ctx, first); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	// The file was edited: OldName became NewName; Keep is untouched.
	second := indexWithSymbols(
		shared.Symbol{ID: "pkg.NewName", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 10}, EndLine: 20},
		shared.Symbol{ID: "pkg.Keep", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 30}, EndLine: 40},
	)
	if _, err := s.Ingest(ctx, second); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}

	rows, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[shared.SymbolID]bool{}
	for _, r := range rows {
		got[r.QualifiedName] = true
	}
	if got["pkg.OldName"] {
		t.Error("stale symbol pkg.OldName survived the rescan")
	}
	if !got["pkg.NewName"] || !got["pkg.Keep"] {
		t.Errorf("expected NewName + Keep after rescan, got %v", got)
	}
}

// Pruning must be scoped to the files the scan actually covered: a symbol in
// a file the scan never visited (a partial / filtered scan) must survive.
func TestIngest_KeepsSymbolsFromFilesNotInThisScan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.Ingest(ctx, indexWithSymbols(
		shared.Symbol{ID: "pkg.Other", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/other.go", Line: 5}, EndLine: 9},
	)); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if _, err := s.Ingest(ctx, indexWithSymbols(
		shared.Symbol{ID: "pkg.Auth", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 5}, EndLine: 9},
	)); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}

	rows, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d symbols, want 2 (a scan of one file must not delete another file's symbols): %+v", len(rows), rows)
	}
}

// A symbol that MOVED inside its file must have its stored position
// refreshed. INSERT OR IGNORE alone leaves the old line behind, so the
// coverage ingest keeps charging statements to a span the function no longer
// occupies — and `atlas trace` points at the wrong line.
func TestIngest_RefreshesMovedSymbolPosition(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.Ingest(ctx, indexWithSymbols(
		shared.Symbol{ID: "pkg.Login", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 10}, EndLine: 20},
	)); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if _, err := s.Ingest(ctx, indexWithSymbols(
		shared.Symbol{ID: "pkg.Login", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/auth.go", Line: 120}, EndLine: 140},
	)); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}

	rows, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d symbols, want 1", len(rows))
	}
	if rows[0].Line != 120 {
		t.Errorf("line = %d, want 120 (moved symbol keeps its stale position)", rows[0].Line)
	}
	if rows[0].EndLine == nil || *rows[0].EndLine != 140 {
		t.Errorf("end_line = %v, want 140", rows[0].EndLine)
	}
}

// Re-ingesting an index whose symbols already exist must resolve each one to
// ITS OWN surrogate id. SQLite leaves last_insert_rowid untouched when
// INSERT OR IGNORE skips a conflicting row, so reading the id back from
// LastInsertId returned whichever row was inserted just before in the loop —
// and every edge, feature link and coverage result written in that pass
// pointed at the wrong symbol.
func TestIngest_RescanResolvesExistingSymbolsToOwnIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	caller := shared.Symbol{ID: "pkg.Caller", Kind: shared.KindFunc,
		Position: shared.FilePosition{Path: "src/caller.go", Line: 10}, EndLine: 20}
	if _, err := s.Ingest(ctx, indexWithSymbols(caller)); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}

	// Second scan: Callee is new and is written FIRST, then Caller — whose
	// insert is ignored because it already exists. That ordering is what
	// exposes the bug: last_insert_rowid still points at Callee's brand-new
	// row, so Caller resolved to Callee's id and the edge collapsed onto
	// itself. Scanner output is map-ordered, so production hit this at random.
	callee := shared.Symbol{ID: "pkg.Callee", Kind: shared.KindFunc,
		Position: shared.FilePosition{Path: "src/callee.go", Line: 5}, EndLine: 9}
	idx := indexWithSymbols(callee, caller)
	idx.Graph.AddEdge(caller.ID, callee.ID)
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}

	byName := map[shared.SymbolID]int64{}
	rows, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		byName[r.QualifiedName] = r.ID
	}
	out, err := s.Edges().Out(ctx, byName["pkg.Caller"])
	if err != nil {
		t.Fatalf("Out: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d outgoing edges from pkg.Caller, want 1: %+v", len(out), out)
	}
	if out[0].ToID != byName["pkg.Callee"] {
		t.Errorf("edge target = %d, want pkg.Callee (%d)", out[0].ToID, byName["pkg.Callee"])
	}
}
