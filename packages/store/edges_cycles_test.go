package store

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// seedImportCycle inserts two symbols in two files plus an import
// edge from the first to the second, returning the (from, to)
// surrogate ids. Used by every ListImportEdges test as the minimum
// viable fixture.
func seedImportCycle(t *testing.T, s *Store, fromQN, fromFile, toQN, toFile, scope string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	syms := s.Symbols()
	edges := s.Edges()

	from, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: shared.SymbolID(fromQN), Kind: shared.KindFunc,
		FilePath: fromFile, Line: 1,
	})
	if err != nil {
		t.Fatalf("insert from: %v", err)
	}
	to, err := syms.Insert(ctx, SymbolRow{
		QualifiedName: shared.SymbolID(toQN), Kind: shared.KindFunc,
		FilePath: toFile, Line: 1,
	})
	if err != nil {
		t.Fatalf("insert to: %v", err)
	}
	if _, err := edges.Insert(ctx, EdgeRow{
		FromID: from, ToID: to, Kind: EdgeKindImport,
		FilePath: fromFile, Line: 1, Meta: scope,
	}); err != nil {
		t.Fatalf("insert edge from->to: %v", err)
	}
	return from, to
}

// TestListImportEdges_NoFilter returns every import edge regardless
// of scope when Filter.Scopes is empty — the `--scope-filter=all`
// mode the cycles verb advertises.
func TestListImportEdges_NoFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a, b := seedImportCycle(t, s, "pkg.a", "a.py", "pkg.b", "b.py", EdgeMetaImportScopeModule)
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: b, ToID: a, Kind: EdgeKindImport,
		FilePath: "b.py", Line: 1, Meta: EdgeMetaImportScopeFunction,
	}); err != nil {
		t.Fatalf("insert reverse edge: %v", err)
	}

	rows, err := s.Edges().ListImportEdges(ctx, ImportEdgeFilter{})
	if err != nil {
		t.Fatalf("ListImportEdges: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %#v", len(rows), rows)
	}
	// Output is ordered by (from_file, to_file, line).
	if rows[0].FromFile != "a.py" || rows[0].ToFile != "b.py" {
		t.Fatalf("first row mismatch: %+v", rows[0])
	}
	if rows[0].Scope != EdgeMetaImportScopeModule {
		t.Fatalf("scope mismatch on row 0: got %q want %q", rows[0].Scope, EdgeMetaImportScopeModule)
	}
	if rows[1].Scope != EdgeMetaImportScopeFunction {
		t.Fatalf("scope mismatch on row 1: got %q want %q", rows[1].Scope, EdgeMetaImportScopeFunction)
	}
}

// TestListImportEdges_ScopeFilter exercises the most common
// production query: "only module-scoped imports" — the default the
// cycles verb uses when surfacing real load-time cycles
// (deferred-import workarounds stay hidden).
func TestListImportEdges_ScopeFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a, b := seedImportCycle(t, s, "pkg.a", "a.py", "pkg.b", "b.py", EdgeMetaImportScopeModule)
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: b, ToID: a, Kind: EdgeKindImport,
		FilePath: "b.py", Line: 42, Meta: EdgeMetaImportScopeFunction,
	}); err != nil {
		t.Fatalf("insert reverse edge: %v", err)
	}

	rows, err := s.Edges().ListImportEdges(ctx, ImportEdgeFilter{
		Scopes: []string{EdgeMetaImportScopeModule},
	})
	if err != nil {
		t.Fatalf("ListImportEdges: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 module-scoped row, got %d: %#v", len(rows), rows)
	}
	if rows[0].Scope != EdgeMetaImportScopeModule {
		t.Fatalf("scope mismatch: %+v", rows[0])
	}
}

// TestListImportEdges_OnlyImportKind asserts non-import edges (call,
// inheritance, decorator, ...) never leak into the projection — only
// the kind='import' subgraph is the source of cycle truth.
func TestListImportEdges_OnlyImportKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a, b := seedImportCycle(t, s, "pkg.a", "a.py", "pkg.b", "b.py", EdgeMetaImportScopeModule)
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: b, ToID: a, Kind: EdgeKindCall,
		FilePath: "b.py", Line: 5,
	}); err != nil {
		t.Fatalf("insert call edge: %v", err)
	}

	rows, err := s.Edges().ListImportEdges(ctx, ImportEdgeFilter{})
	if err != nil {
		t.Fatalf("ListImportEdges: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 import row (call edge filtered), got %d: %#v", len(rows), rows)
	}
}

// TestListImportEdges_RejectsInvalidScope confirms a junk scope
// filter returns an empty slice rather than degrading to "every
// cycle" — a guard against accidentally widening the query when a
// CLI flag value doesn't match any vocabulary term.
func TestListImportEdges_RejectsInvalidScope(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	seedImportCycle(t, s, "pkg.a", "a.py", "pkg.b", "b.py", EdgeMetaImportScopeModule)

	rows, err := s.Edges().ListImportEdges(ctx, ImportEdgeFilter{
		Scopes: []string{"garbage_scope_value"},
	})
	if err != nil {
		t.Fatalf("ListImportEdges: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty result for invalid scope, got %d: %#v", len(rows), rows)
	}
}

// TestListImportEdges_OrderingDeterministic locks in the (from_file,
// to_file, line) ORDER BY contract so JSON snapshot diffs stay
// reproducible across re-runs.
func TestListImportEdges_OrderingDeterministic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Insert in non-sorted order on purpose.
	seedImportCycle(t, s, "pkg.z", "z.py", "pkg.a", "a.py", EdgeMetaImportScopeModule)
	seedImportCycle(t, s, "pkg.m", "m.py", "pkg.b", "b.py", EdgeMetaImportScopeModule)

	rows, err := s.Edges().ListImportEdges(ctx, ImportEdgeFilter{})
	if err != nil {
		t.Fatalf("ListImportEdges: %v", err)
	}

	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.FromFile + "->" + r.ToFile
	}
	expected := append([]string(nil), got...)
	sort.Strings(expected)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("expected sorted output, got %v want %v", got, expected)
	}
}
