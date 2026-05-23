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
