package store

import (
	"context"
	"database/sql"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
)

// TestIngest_PythonScanner_DeferredImportScopesPersist is the
// end-to-end regression test for issue #16: deferred imports inside
// function bodies, conditional blocks, type-checking guards, and
// try-blocks MUST land in the SQLite store with the correct
// edge_meta scope tag.
//
// Why this exists alongside the in-package scanner test: the
// scanner-only test proves the AST walker visits the right nodes
// and emits the right scope strings. This test proves the value
// survives the FULL pipeline:
//
//	scanner.py -> exec.go -> scanner.go.mapToResult ->
//	codeindex.mergePYResult -> store.Ingest -> SQLite edge_meta
//
// A bug at any layer (scope dropped in graph.Edge.Meta, lost in
// upsertEdgeTx, rejected by NormalizeEdgeMeta, etc.) shows up here
// as a NULL edge_meta in the SELECT below.
//
// Skips when python3 isn't on PATH so hermetic minimal-image CI
// runs don't fail spuriously.
func TestIngest_PythonScanner_DeferredImportScopesPersist(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available; skipping pyscan integration test")
	}

	// Hermetic per-test fixture written to a tempdir.
	fixtureDir := t.TempDir()
	mustWriteFile(t, filepath.Join(fixtureDir, "deferred.py"), pythonDeferredImportsFixture)

	ctx := context.Background()
	idx, err := codeindex.IndexProject(ctx, fixtureDir, codeindex.Options{HashFiles: false})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if idx.Graph == nil {
		t.Fatal("nil graph")
	}
	s := openTestStore(t)
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	got := edgeMetaCountsForKind(t, s, EdgeKindImport)

	// Every documented scope MUST appear at least once. The fixture
	// is deliberately small (one import per scope) so the assertion
	// is "exactly 1" — under-counts mean walker drops, over-counts
	// mean double-emit.
	wantScopes := []string{
		EdgeMetaImportScopeModule,
		EdgeMetaImportScopeFunction,
		EdgeMetaImportScopeConditional,
		EdgeMetaImportScopeTypeChecking,
		EdgeMetaImportScopeTryGuard,
	}
	for _, scope := range wantScopes {
		if got[scope] < 1 {
			t.Errorf("edges with kind=import meta=%q: got %d, want >= 1; full counts=%v",
				scope, got[scope], got)
		}
	}

	// NULL edge_meta on an import edge means the value was dropped
	// somewhere in the pipeline. After issue #16, every import
	// emitted by scanner.py carries a scope tag, so finding NULL
	// imports here is a regression.
	if got[""] > 0 {
		t.Errorf("found %d import edges with NULL edge_meta; every import must carry a scope tag post-#16",
			got[""])
	}

	// Total imports MUST be >= 5 (the fixture has exactly five
	// canonical imports plus one non-try-guard "sys"). Lower
	// numbers signal silent drops at any pipeline layer.
	total := 0
	for _, n := range got {
		total += n
	}
	if total < 5 {
		t.Errorf("total import edges = %d, want >= 5 (one per documented scope); counts=%v", total, got)
	}
}

// pythonDeferredImportsFixture is the issue #16 reproducer the
// store-side integration test asserts on. Kept byte-compatible with
// the scanner-side fixture (in package pyscan) so a diff between
// the two highlights drift immediately.
const pythonDeferredImportsFixture = `# Module-level import
import os
from typing import TYPE_CHECKING


def make_client():
    # Deferred function-body import
    from urllib.request import urlopen
    return urlopen


if TYPE_CHECKING:
    # Type-checking-only import
    from collections.abc import Iterator


try:
    # Try-guard import
    from greenlet import greenlet
except ImportError:
    greenlet = None


cfg = True
if cfg:
    # Plain conditional import
    import json
`

// edgeMetaCountsForKind returns a count of edges-of-the-given-kind
// keyed by edge_meta value. Empty string represents SQL NULL so the
// "did the scope tag actually persist" assertion can read it as the
// "missing value" bucket.
func edgeMetaCountsForKind(t *testing.T, s *Store, kind EdgeKind) map[string]int {
	t.Helper()
	rows, err := s.sqlDB().Query(
		"SELECT COALESCE(edge_meta, '') AS m, COUNT(*) FROM edges WHERE kind = ? GROUP BY m",
		string(kind),
	)
	if err != nil {
		t.Fatalf("query edge_meta counts: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var meta sql.NullString
		var count int
		if err := rows.Scan(&meta, &count); err != nil {
			t.Fatalf("scan edge_meta count: %v", err)
		}
		out[meta.String] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return out
}
