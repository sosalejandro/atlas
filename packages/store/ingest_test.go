package store

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// buildTestIndex returns a minimal codeindex.Index suitable for asserting
// on Ingest's per-table side effects.
//
//	src/handler.go:    pkg.LoginHandler -> pkg.LoginService
//	src/service.go:    pkg.LoginService -> pkg.UserRepo
//	src/repo.go:       pkg.UserRepo
//	annotations:       @atlas:feature auth.login at handler.go:9
func buildTestIndex(t *testing.T) *codeindex.Index {
	t.Helper()

	g := graph.New()

	handler := &graph.Node{Symbol: shared.Symbol{
		ID:       "pkg.LoginHandler",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/handler.go", Line: 10},
		Package:  "github.com/example/pkg",
	}}
	service := &graph.Node{Symbol: shared.Symbol{
		ID:       "pkg.LoginService",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/service.go", Line: 20},
		Package:  "github.com/example/pkg",
	}}
	repo := &graph.Node{Symbol: shared.Symbol{
		ID:       "pkg.UserRepo",
		Kind:     shared.KindFunc,
		Position: shared.FilePosition{Path: "src/repo.go", Line: 30},
		Package:  "github.com/example/pkg",
	}}
	g.AddNode(handler)
	g.AddNode(service)
	g.AddNode(repo)
	g.AddEdge("pkg.LoginHandler", "pkg.LoginService")
	g.AddEdge("pkg.LoginService", "pkg.UserRepo")

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	return &codeindex.Index{
		Root:        "/tmp/project",
		GeneratedAt: now,
		Graph:       g,
		Symbols:     []shared.Symbol{handler.Symbol, service.Symbol, repo.Symbol},
		Annotations: []shared.Annotation{
			{
				Kind:     shared.AnnFeature,
				IDs:      []string{"auth.login"},
				Source:   shared.SourceAtlas,
				Position: shared.FilePosition{Path: "src/handler.go", Line: 9},
				Raw:      "auth.login",
			},
		},
		FileHashes: map[string]codeindex.FileHash{
			"src/handler.go": {Path: "src/handler.go", SHA256: "h1", ModTime: now, LastScanned: now},
			"src/service.go": {Path: "src/service.go", SHA256: "h2", ModTime: now, LastScanned: now},
			"src/repo.go":    {Path: "src/repo.go", SHA256: "h3", ModTime: now, LastScanned: now},
		},
	}
}

func TestIngest_HappyPath(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	stats, err := s.Ingest(ctx, buildTestIndex(t))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if stats.SymbolsInserted != 3 {
		t.Errorf("SymbolsInserted = %d, want 3", stats.SymbolsInserted)
	}
	if stats.EdgesInserted != 2 {
		t.Errorf("EdgesInserted = %d, want 2", stats.EdgesInserted)
	}
	if stats.AnnotationsInserted != 1 {
		t.Errorf("AnnotationsInserted = %d, want 1", stats.AnnotationsInserted)
	}
	if stats.FileHashesUpserted != 3 {
		t.Errorf("FileHashesUpserted = %d, want 3", stats.FileHashesUpserted)
	}

	// Cross-check by reading back via ports.
	syms, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("Symbols List: %v", err)
	}
	if len(syms) != 3 {
		t.Errorf("symbols rows = %d, want 3", len(syms))
	}

	handler, err := s.Symbols().FindByQualifiedName(ctx, "pkg.LoginHandler")
	if err != nil {
		t.Fatalf("FindByQualifiedName handler: %v", err)
	}
	out, err := s.Edges().Out(ctx, handler.ID)
	if err != nil {
		t.Fatalf("Edges Out: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("handler outgoing edges = %d, want 1", len(out))
	}

	walk, err := s.Edges().Walk(ctx, handler.ID, 10)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	// Handler -> Service (depth 1) and Service -> Repo (depth 2).
	if len(walk) != 2 {
		t.Errorf("walk len = %d, want 2: %+v", len(walk), walk)
	}

	annRows, err := s.Annotations().ListByFile(ctx, "src/handler.go")
	if err != nil {
		t.Fatalf("annotations ListByFile: %v", err)
	}
	if len(annRows) != 1 || annRows[0].Value != "auth.login" {
		t.Errorf("annotations = %+v, want one row with value auth.login", annRows)
	}
}

func TestIngest_Idempotent_NoDuplicates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	idx := buildTestIndex(t)

	first, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest #1: %v", err)
	}
	if first.SymbolsInserted == 0 {
		t.Fatal("first Ingest inserted no symbols")
	}

	second, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest #2: %v", err)
	}

	// Second pass: all files unchanged → skipped → zero new rows.
	if second.SymbolsInserted != 0 {
		t.Errorf("Ingest #2 SymbolsInserted = %d, want 0 (file hashes unchanged)", second.SymbolsInserted)
	}
	if second.EdgesInserted != 0 {
		t.Errorf("Ingest #2 EdgesInserted = %d, want 0", second.EdgesInserted)
	}
	if second.AnnotationsInserted != 0 {
		t.Errorf("Ingest #2 AnnotationsInserted = %d, want 0", second.AnnotationsInserted)
	}
	if second.FilesSkipped != 3 {
		t.Errorf("Ingest #2 FilesSkipped = %d, want 3", second.FilesSkipped)
	}

	// Row counts identical to first pass.
	syms, _ := s.Symbols().List(ctx, SymbolFilter{})
	if len(syms) != 3 {
		t.Errorf("symbols after re-ingest = %d, want 3", len(syms))
	}
	var edgeCount int
	if err := s.sqlDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM edges`).Scan(&edgeCount); err != nil {
		t.Fatal(err)
	}
	if edgeCount != 2 {
		t.Errorf("edges after re-ingest = %d, want 2", edgeCount)
	}
	var annCount int
	if err := s.sqlDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM annotations`).Scan(&annCount); err != nil {
		t.Fatal(err)
	}
	if annCount != 1 {
		t.Errorf("annotations after re-ingest = %d, want 1", annCount)
	}
}

func TestIngest_ChangedFile_RewritesRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	idx := buildTestIndex(t)

	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest #1: %v", err)
	}

	// Bump handler.go's hash to simulate a content change. The file_hashes
	// row already exists for handler.go from the first ingest, but now the
	// SHA256 in the new index differs.
	mut := idx.FileHashes["src/handler.go"]
	mut.SHA256 = "h1-updated"
	idx.FileHashes["src/handler.go"] = mut

	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest #2: %v", err)
	}
	if stats.FilesSkipped != 2 {
		t.Errorf("FilesSkipped = %d, want 2 (service + repo unchanged)", stats.FilesSkipped)
	}
	// Handler's symbol already exists by qualified_name — INSERT OR IGNORE
	// collapses to no-op so SymbolsInserted is 0, BUT the file_hashes row
	// for handler.go was refreshed.
	got, err := s.FileHashes().Get(ctx, "src/handler.go")
	if err != nil {
		t.Fatalf("FileHashes Get: %v", err)
	}
	if got.ContentHash != "h1-updated" {
		t.Errorf("ContentHash = %q, want \"h1-updated\"", got.ContentHash)
	}
}

func TestIngest_NilIndex(t *testing.T) {
	if _, err := openTestStore(t).Ingest(context.Background(), nil); err == nil {
		t.Fatal("Ingest(nil) expected error, got nil")
	}
}

// TestIngest_PersistsPatternMatches covers the Phase 6f wire-up: when an
// Index carries PatternMatches, Ingest writes them onto the corresponding
// symbol row and FindByPattern reads them back.
func TestIngest_PersistsPatternMatches(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	idx := buildTestIndex(t)
	idx.PatternMatches = map[shared.SymbolID][]patterns.Match{
		"pkg.LoginService": {
			{
				Pattern:    patterns.PatternCanonicalService,
				Symbol:     "pkg.LoginService",
				Position:   shared.FilePosition{Path: "src/service.go", Line: 20},
				Detail:     "synthetic",
				Confidence: 1.0,
			},
		},
		"pkg.UserRepo": {
			{
				Pattern:    patterns.PatternOutboxAppend,
				Symbol:     "pkg.UserRepo",
				Position:   shared.FilePosition{Path: "src/repo.go", Line: 30},
				Detail:     "synthetic",
				Confidence: 1.0,
			},
		},
	}

	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.PatternMatchesSet != 2 {
		t.Errorf("PatternMatchesSet = %d, want 2", stats.PatternMatchesSet)
	}

	canon, err := s.Symbols().FindByPattern(ctx, patterns.PatternCanonicalService)
	if err != nil {
		t.Fatalf("FindByPattern canonical: %v", err)
	}
	if len(canon) != 1 || canon[0].QualifiedName != "pkg.LoginService" {
		t.Fatalf("FindByPattern canonical = %+v", canon)
	}
	if canon[0].PatternMatches == nil {
		t.Fatal("returned row has nil PatternMatches")
	}

	outbox, err := s.Symbols().FindByPattern(ctx, patterns.PatternOutboxAppend)
	if err != nil {
		t.Fatalf("FindByPattern outbox: %v", err)
	}
	if len(outbox) != 1 || outbox[0].QualifiedName != "pkg.UserRepo" {
		t.Fatalf("FindByPattern outbox = %+v", outbox)
	}

	// A symbol with no matches must not surface.
	noMatch, _ := s.Symbols().FindByPattern(ctx, patterns.PatternEventRecorderEmbed)
	if len(noMatch) != 0 {
		t.Errorf("FindByPattern event-recorder = %d, want 0", len(noMatch))
	}
}
