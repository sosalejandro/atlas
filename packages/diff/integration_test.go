package diff

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// TestIntegration_StoreRoundTrip verifies the end-to-end persistence path:
// build two synthetic atlas Snapshots, write them to a real SQLite store
// via Snapshots.Capture, then load them back via Engine.ComputeFromStore
// and assert the structured delta surfaces what we put in.
//
// This is the spec's "one integration test" requirement: capture A,
// mutate, capture B, compute diff, assert at least the modified file's
// symbol delta surfaces.
func TestIntegration_StoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "atlas-state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	// --- Build snapshot A ---
	idxA := &codeindex.Index{
		Root:           "/tmp/p",
		Graph:          graph.New(),
		FileHashes:     map[string]codeindex.FileHash{},
		SymbolLangs:    map[shared.SymbolID]string{},
		PatternMatches: map[shared.SymbolID][]patterns.Match{},
	}
	symA1 := shared.Symbol{
		ID:        "pkg.Foo",
		Kind:      shared.KindFunc,
		Position:  shared.FilePosition{Path: "pkg/foo.go", Line: 10},
		Signature: "func Foo() error",
	}
	symA2 := shared.Symbol{
		ID:        "pkg.Bar",
		Kind:      shared.KindFunc,
		Position:  shared.FilePosition{Path: "pkg/bar.go", Line: 20},
		Signature: "func Bar()",
	}
	idxA.Symbols = []shared.Symbol{symA1, symA2}
	idxA.Graph.AddNode(&graph.Node{Symbol: symA1})
	idxA.Graph.AddNode(&graph.Node{Symbol: symA2})
	idxA.Graph.AddEdge("pkg.Foo", "pkg.Bar")
	idxA.Annotations = []shared.Annotation{
		{Kind: shared.AnnFeature, IDs: []string{"pkg.foo"}, Source: shared.SourceAtlas, Position: shared.FilePosition{Path: "pkg/foo.go", Line: 9}},
	}

	auditA := []FeatureHealth{
		{FeatureID: "pkg.foo", Score: 90},
	}

	indexJSONA, err := EncodeIndexJSON(idxA)
	if err != nil {
		t.Fatalf("EncodeIndexJSON A: %v", err)
	}
	auditJSONA, err := EncodeAuditJSON(auditA)
	if err != nil {
		t.Fatalf("EncodeAuditJSON A: %v", err)
	}
	var auditPtrA *string
	if auditJSONA != "" {
		auditPtrA = &auditJSONA
	}

	idA, err := s.Snapshots().Capture(ctx, store.CaptureInput{
		GitRef:    "commit-a",
		IndexJSON: indexJSONA,
		AuditJSON: auditPtrA,
	})
	if err != nil {
		t.Fatalf("Snapshots.Capture A: %v", err)
	}

	// --- Build snapshot B: modify pkg/foo.go (shift line + signature),
	// drop pkg.Bar, add a new symbol, and worsen audit score.
	idxB := &codeindex.Index{
		Root:           "/tmp/p",
		Graph:          graph.New(),
		FileHashes:     map[string]codeindex.FileHash{},
		SymbolLangs:    map[shared.SymbolID]string{},
		PatternMatches: map[shared.SymbolID][]patterns.Match{},
	}
	symB1 := shared.Symbol{
		ID:        "pkg.Foo",
		Kind:      shared.KindFunc,
		Position:  shared.FilePosition{Path: "pkg/foo.go", Line: 15}, // moved
		Signature: "func Foo(ctx context.Context) error",             // signature change
	}
	symB3 := shared.Symbol{
		ID:        "pkg.Baz",
		Kind:      shared.KindFunc,
		Position:  shared.FilePosition{Path: "pkg/baz.go", Line: 1},
		Signature: "func Baz()",
	}
	idxB.Symbols = []shared.Symbol{symB1, symB3}
	idxB.Graph.AddNode(&graph.Node{Symbol: symB1})
	idxB.Graph.AddNode(&graph.Node{Symbol: symB3})
	idxB.Graph.AddEdge("pkg.Foo", "pkg.Baz")
	idxB.Annotations = []shared.Annotation{
		{Kind: shared.AnnFeature, IDs: []string{"pkg.foo"}, Source: shared.SourceAtlas, Position: shared.FilePosition{Path: "pkg/foo.go", Line: 9}},
	}

	auditB := []FeatureHealth{
		{FeatureID: "pkg.foo", Score: 75}, // -15 above noise floor
	}

	indexJSONB, err := EncodeIndexJSON(idxB)
	if err != nil {
		t.Fatalf("EncodeIndexJSON B: %v", err)
	}
	auditJSONB, err := EncodeAuditJSON(auditB)
	if err != nil {
		t.Fatalf("EncodeAuditJSON B: %v", err)
	}
	var auditPtrB *string
	if auditJSONB != "" {
		auditPtrB = &auditJSONB
	}

	idB, err := s.Snapshots().Capture(ctx, store.CaptureInput{
		GitRef:    "commit-b",
		IndexJSON: indexJSONB,
		AuditJSON: auditPtrB,
	})
	if err != nil {
		t.Fatalf("Snapshots.Capture B: %v", err)
	}

	// --- Diff via ComputeFromStore ---
	eng := NewEngine(s, Options{})
	d, err := eng.ComputeFromStore(ctx, idA, idB)
	if err != nil {
		t.Fatalf("ComputeFromStore: %v", err)
	}

	// pkg.Foo: shifted line + signature change → Changed
	var sawFooChanged bool
	for _, c := range d.Symbols.Changed {
		if c.ID == "pkg.Foo" {
			sawFooChanged = true
			if c.Before.Position.Line == c.After.Position.Line {
				t.Errorf("expected line shift; both %d", c.Before.Position.Line)
			}
		}
	}
	if !sawFooChanged {
		t.Errorf("pkg.Foo (modified file's symbol) must surface in Symbols.Changed; got %+v", d.Symbols.Changed)
	}

	// pkg.Bar removed, pkg.Baz added.
	var sawBarRemoved, sawBazAdded bool
	for _, sym := range d.Symbols.Removed {
		if sym.ID == "pkg.Bar" {
			sawBarRemoved = true
		}
	}
	for _, sym := range d.Symbols.Added {
		if sym.ID == "pkg.Baz" {
			sawBazAdded = true
		}
	}
	if !sawBarRemoved || !sawBazAdded {
		t.Errorf("expected pkg.Bar in Removed and pkg.Baz in Added; got removed=%+v added=%+v",
			d.Symbols.Removed, d.Symbols.Added)
	}

	// Edges: old pkg.Foo→pkg.Bar removed, new pkg.Foo→pkg.Baz added.
	if len(d.Edges.Added) != 1 || d.Edges.Added[0].To != "pkg.Baz" {
		t.Errorf("edges.Added: %+v", d.Edges.Added)
	}
	if len(d.Edges.Removed) != 1 || d.Edges.Removed[0].To != "pkg.Bar" {
		t.Errorf("edges.Removed: %+v", d.Edges.Removed)
	}

	// Audit: -15 surfaces above default floor of 5.
	if len(d.Audit.Changed) != 1 || d.Audit.Changed[0].FeatureID != "pkg.foo" {
		t.Fatalf("audit.Changed: %+v", d.Audit.Changed)
	}
	if d.Audit.Changed[0].Delta != -15 {
		t.Errorf("expected delta -15, got %d", d.Audit.Changed[0].Delta)
	}

	// ARef / BRef plumbed through.
	if d.ARef != "commit-a" || d.BRef != "commit-b" {
		t.Errorf("ARef/BRef: got %q/%q", d.ARef, d.BRef)
	}
}

// TestIntegration_ComputeFromStore_NotFound verifies the path returns a
// clear error when one of the snapshot ids is missing.
func TestIntegration_ComputeFromStore_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "atlas-state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	eng := NewEngine(s, Options{})
	if _, err := eng.ComputeFromStore(context.Background(), 1, 2); err == nil {
		t.Fatal("expected error for missing snapshot ids")
	}
}
