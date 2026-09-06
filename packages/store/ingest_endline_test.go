package store

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Ingest must persist each symbol's source span. Coverage attribution keys
// executed statements to the symbol whose [line, end_line] range contains
// them; when end_line is NULL the ingest falls back to guessing the range
// from the next symbol's start line, so statements belonging to unindexed
// declarations are charged to the wrong symbol and the last symbol in a file
// absorbs every block to EOF (issues #84 / #85).
func TestIngest_PersistsEndLine(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	g := graph.New()
	g.AddNode(&graph.Node{Symbol: shared.Symbol{
		ID:       "pkg.Login",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/auth.go", Line: 10},
		EndLine:  42,
	}})
	idx := &codeindex.Index{
		Root:       ".",
		Graph:      g,
		Symbols:    []shared.Symbol{g.Nodes["pkg.Login"].Symbol},
		FileHashes: map[string]codeindex.FileHash{},
	}
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	rows, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d symbols, want 1", len(rows))
	}
	if rows[0].EndLine == nil {
		t.Fatal("end_line was not persisted (nil)")
	}
	if *rows[0].EndLine != 42 {
		t.Fatalf("end_line = %d, want 42", *rows[0].EndLine)
	}
}
