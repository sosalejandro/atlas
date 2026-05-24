package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
)

// TestIngest_PythonScanner_EdgeKindsLandInStore is the regression test for
// issue #57 (Python scanner: edges silently dropped — Go orchestrator
// strips kind field).
//
// Why this test exists: the original bug shipped in v0.3.0 because the
// existing tests cover scanner.py in isolation (its emitted edges are
// correct) and the store's Ingest in isolation (it persists what it's
// given), but no test exercised the FULL pipeline:
//
//	scanner.py  →  exec.go (subprocess)  →  scanner.go (mapToResult)
//	            →  codeindex.mergePYResult  →  store.Ingest  →  SQLite
//
// The bug was a four-line dropped field deep inside scanner.go that no
// single-layer test could catch. This integration test pins the contract
// by running every layer end-to-end against a fixture that exercises the
// four Python edge kinds (call / inheritance / decorator / import) and
// asserting they all land in the edges table with their kind preserved.
//
// The test auto-skips when python3 is unavailable so it doesn't block
// hermetic CI runs on minimal containers. Scanner.go already returns a
// warning rather than an error in that case, but this test would yield
// a misleading "0 edges" failure rather than a clean skip without the
// guard.
func TestIngest_PythonScanner_EdgeKindsLandInStore(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; skipping pyscan integration test")
	}

	// Per-test fixture written to a tempdir so the test is hermetic and
	// can run in parallel with itself across packages without colliding.
	fixtureDir := t.TempDir()
	mustWriteFile(t, filepath.Join(fixtureDir, "sample.py"), pythonFixtureSource)

	ctx := context.Background()
	idx, err := codeindex.IndexProject(ctx, fixtureDir, codeindex.Options{HashFiles: false})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if idx.Graph == nil {
		t.Fatal("nil graph")
	}
	if len(idx.Graph.Edges) == 0 {
		t.Fatalf("scanner produced 0 edges; expected ≥4. warnings=%v", idx.Warnings)
	}

	s := openTestStore(t)
	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	// The fixture should produce ≥4 edges in the store (one per kind).
	if stats.EdgesInserted < 4 {
		t.Errorf("EdgesInserted = %d, want >=4 (one per Python edge kind)", stats.EdgesInserted)
	}

	// Per-kind breakdown — every Python kind the fixture exercises MUST
	// land with the correct kind preserved.
	got := edgeCountsByKind(t, s)
	wantKinds := []EdgeKind{
		EdgeKindCall,
		EdgeKindInheritance,
		EdgeKindDecorator,
		EdgeKindImport,
	}
	for _, k := range wantKinds {
		if got[k] == 0 {
			t.Errorf("edges with kind=%q = 0, want >=1; full counts=%v", k, got)
		}
	}
}

// pythonFixtureSource exercises all four Python edge kinds the scanner
// emits:
//
//   - import:      `from typing import List`         → sample → typing.List
//   - inheritance: `class Derived(Base)`             → sample.Derived → sample.Base
//   - decorator:   `@register def helper()`          → sample.helper → sample.register
//   - call:        `helper()` inside Derived.hello   → sample.Derived.hello → sample.helper
//
// The fixture intentionally keeps every relationship in-module so the
// pyEdgeResolver promotes raw `helper` / `Base` / `register` targets to
// fully-qualified ids before they hit the store. Cross-module edges are
// covered by the scanner_test.go fixture; this test focuses on the
// regression surface (post-resolve persistence).
const pythonFixtureSource = `from typing import List

def register(fn):
    return fn

class Base:
    def hello(self):
        pass

class Derived(Base):
    def hello(self):
        helper()

@register
def helper():
    pass
`

// TestIngest_PythonScanner_CallEdgesPreserveCallerIdentity is the
// regression test for issue #18 (call edges lose caller identity —
// every from_symbol_id collapsed to the module-level stub).
//
// Why this test exists: the pre-fix scanner used `ast.walk(func)`
// inside `_emit_call_edges`, which descended into nested `def` /
// `class` bodies. Any call made by an inner closure was therefore
// attributed both to the inner symbol AND to the outer enclosing
// function. End-to-end, the store saw caller identity collapsing —
// the duplicated edges all shared file_path + kind with edges already
// attributed correctly to the inner scope, so trace-walks could no
// longer trust `from_symbol_id` to mean "this exact function makes
// this call".
//
// The integration test (existing) only checked kind preservation, not
// per-function attribution. This test pins the contract by asserting:
//
//  1. Calls inside a method body land with from_symbol_id pointing at
//     the method's qualified id (NOT the enclosing class, NOT the
//     module).
//  2. Calls inside a nested closure land ONLY under the inner scope,
//     NOT duplicated under the outer scope.
//  3. The number of distinct from_symbol_ids in the call-edges table
//     is strictly > 1 (the old bug collapsed everything to id=1).
func TestIngest_PythonScanner_CallEdgesPreserveCallerIdentity(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; skipping pyscan integration test")
	}

	fixtureDir := t.TempDir()
	mustWriteFile(t, filepath.Join(fixtureDir, "sample.py"), callerIdentityFixtureSource)

	ctx := context.Background()
	idx, err := codeindex.IndexProject(ctx, fixtureDir, codeindex.Options{HashFiles: false})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	s := openTestStore(t)
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	callers := callEdgeCallersByCallee(t, s)

	// AC #1: method-body call lands under the method's qualified id.
	if got := callers["sample.helper"]; !contains(got, "sample.Derived.method_a") {
		t.Errorf("expected sample.helper to be called by sample.Derived.method_a; got callers=%v", got)
	}

	// AC #2: nested-closure call lands ONLY under the inner closure,
	// NOT duplicated under the enclosing method.
	leafCallers := callers["sample.leaf_only"]
	if !contains(leafCallers, "sample.Derived.method_b.inner") {
		t.Errorf("expected sample.leaf_only to be called by sample.Derived.method_b.inner; got callers=%v", leafCallers)
	}
	if contains(leafCallers, "sample.Derived.method_b") {
		t.Errorf(
			"regression: sample.leaf_only is incorrectly attributed to enclosing scope "+
				"sample.Derived.method_b — issue #18 nested-closure caller-identity collapse "+
				"has returned. Callers seen: %v",
			leafCallers,
		)
	}

	// AC #3: distinct from_symbol_ids in call edges is strictly > 1.
	// Pre-fix the bug collapsed every edge to the module stub (count=1);
	// this assertion is the broad-stroke guard against re-collapse.
	if n := distinctCallFromCount(t, s); n <= 1 {
		t.Errorf("distinct call-edge from_symbol_id = %d; expected > 1 (issue #18 regression — caller identity lost)", n)
	}

	// AC #4: NO call edge points from a module-kind symbol. Module
	// nodes own only `import` edges; if one ever lands on a call edge
	// it means an emitter regressed back to `from_=module_id`.
	if rows := callEdgesFromModuleSymbols(t, s); len(rows) > 0 {
		t.Errorf("call edges incorrectly attributed to module-kind callers: %v", rows)
	}
}

// callerIdentityFixtureSource exercises both halves of issue #18:
//
//   - sample.Derived.method_a calls sample.helper at the method body
//     level → must attribute to the METHOD, not the class, not the module.
//
//   - sample.Derived.method_b defines an inner closure `inner()` whose
//     body calls sample.leaf_only → must attribute to the INNER closure
//     ONLY, not duplicated under method_b (the pre-fix bug).
//
//   - sample.outer is a module-level function whose body defines a
//     nested `inside()` that calls sample.helper → must attribute to
//     sample.outer.inside ONLY, not duplicated under sample.outer.
const callerIdentityFixtureSource = `def helper():
    pass

def leaf_only():
    pass

class Base:
    pass

class Derived(Base):
    def method_a(self):
        helper()

    def method_b(self):
        def inner():
            leaf_only()
        inner()

def outer():
    def inside():
        helper()
    inside()
`

// callEdgeCallersByCallee returns a map of callee qualified_name → list
// of caller qualified_names, restricted to edges with kind='call'.
func callEdgeCallersByCallee(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	rows, err := s.sqlDB().Query(`
		SELECT st.qualified_name AS callee, sf.qualified_name AS caller
		FROM edges e
		JOIN symbols sf ON sf.id = e.from_symbol_id
		JOIN symbols st ON st.id = e.to_symbol_id
		WHERE e.kind = 'call'
	`)
	if err != nil {
		t.Fatalf("query call edges: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var callee, caller string
		if err := rows.Scan(&callee, &caller); err != nil {
			t.Fatalf("scan call edge: %v", err)
		}
		out[callee] = append(out[callee], caller)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

// distinctCallFromCount returns the cardinality of from_symbol_id
// values across all call edges. The pre-fix bug collapsed this to 1.
func distinctCallFromCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	row := s.sqlDB().QueryRow(`SELECT COUNT(DISTINCT from_symbol_id) FROM edges WHERE kind='call'`)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("distinct from_symbol_id: %v", err)
	}
	return n
}

// callEdgesFromModuleSymbols returns any rows where a call-edge's
// from_symbol_id resolves to a symbol whose qualified_name has no dot
// (i.e. a top-level module). The Atlas Python schema stores module
// symbols as kind=func (the rawKindToSymbolKind fallback) but their
// qualified_name is always dot-free at the top level, which lets the
// test detect a regression without depending on kind labeling.
func callEdgesFromModuleSymbols(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.sqlDB().Query(`
		SELECT DISTINCT sf.qualified_name
		FROM edges e
		JOIN symbols sf ON sf.id = e.from_symbol_id
		WHERE e.kind = 'call' AND instr(sf.qualified_name, '.') = 0
	`)
	if err != nil {
		t.Fatalf("query module-caller call edges: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var qn string
		if err := rows.Scan(&qn); err != nil {
			t.Fatalf("scan module caller: %v", err)
		}
		out = append(out, qn)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

// contains is a tiny helper to keep the assertion bodies readable.
func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func edgeCountsByKind(t *testing.T, s *Store) map[EdgeKind]int {
	t.Helper()
	rows, err := s.sqlDB().Query("SELECT kind, COUNT(*) FROM edges GROUP BY kind")
	if err != nil {
		t.Fatalf("query edge counts: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[EdgeKind]int{}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			t.Fatalf("scan edge count: %v", err)
		}
		out[EdgeKind(kind)] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
