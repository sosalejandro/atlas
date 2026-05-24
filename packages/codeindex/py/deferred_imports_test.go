package pyscan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Scope tag constants — duplicated from packages/store/edges.go to
// avoid the test-only import cycle (store imports codeindex which
// imports codeindex/py, so codeindex/py cannot import store from a
// test file). Kept in lockstep with the originals; CI would break if
// they drift because the store-side integration test asserts on the
// same string values.
const (
	scopeModule       = "module"
	scopeFunction     = "function"
	scopeConditional  = "conditional"
	scopeTypeChecking = "type_checking"
	scopeTryGuard     = "try_guard"
)

// TestScanner_DeferredImports_AllScopesCaptured pins issue #16 regression:
// the Python scanner MUST emit one edge per import statement at any
// nesting depth, tagged with the lexical scope it was found in.
//
// Pre-fix the walker only descended through module.body, so anything
// inside a function body, ``if`` block, or ``try`` got silently
// dropped — producing systematic dead-code false positives.
//
// The fixture exercises every supported scope:
//
//   - module        plain ``import os`` at module scope
//   - function      ``from urllib.request import urlopen`` inside def
//   - type_checking ``from collections.abc import Iterator`` inside
//                   ``if TYPE_CHECKING:`` (priority over conditional)
//   - try_guard     ``from greenlet import greenlet`` inside
//                   ``try/except ImportError:``
//   - conditional   ``import json`` inside a plain ``if`` block
//
// Counter-fixture: an ``import sys`` inside ``except OSError:`` MUST
// NOT promote to try_guard — only ``ImportError`` / ``ModuleNotFoundError``
// handlers qualify (those are the runtime exceptions that signal a
// missing-optional-dependency pattern).
func TestScanner_DeferredImports_AllScopesCaptured(t *testing.T) {
	t.Parallel()
	skipIfNoPython(t)

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "sample.py"), deferredImportsFixture)

	s := NewScanner(Options{Logger: shared.NopLogger{}})
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Collect (to, meta) pairs for every import-kind edge so the
	// assertions can match by intent ("the urlopen import is tagged
	// function") without coupling to edge ordering.
	type key struct{ to, meta string }
	got := make(map[key]int)
	for _, e := range res.Edges {
		if e.Kind != "import" {
			continue
		}
		got[key{to: string(e.To), meta: e.Meta}]++
	}

	want := []key{
		// Module-level: ``import os`` at the top of the file.
		{to: "os", meta: scopeModule},
		// Deferred: ``from urllib.request import urlopen`` inside
		// ``def make_client():``.
		{to: "urllib.request.urlopen", meta: scopeFunction},
		// Type-checking: ``from collections.abc import Iterator``
		// inside ``if TYPE_CHECKING:``.
		{to: "collections.abc.Iterator", meta: scopeTypeChecking},
		// Try-guard: ``from greenlet import greenlet`` inside a
		// try-block whose except clause catches ImportError.
		{to: "greenlet.greenlet", meta: scopeTryGuard},
		// Plain conditional: ``import json`` inside a vanilla
		// ``if cfg:`` block (no TYPE_CHECKING and no try-guard).
		{to: "json", meta: scopeConditional},
	}
	for _, w := range want {
		if got[w] == 0 {
			t.Errorf("missing import edge: to=%q meta=%q; got=%v", w.to, w.meta, got)
		}
	}

	// Negative: ``import sys`` inside ``except OSError:`` MUST NOT
	// promote to try_guard — only ImportError-handlers qualify.
	// The walker should still emit the import (closes #16) but with
	// scope=conditional (the enclosing try is treated as a regular
	// block, not a guard, because no handler matches our allow-list).
	if got[key{to: "sys", meta: scopeTryGuard}] > 0 {
		t.Error("import sys inside except OSError should NOT be tagged try_guard")
	}
	// Affirmative on the negative: the sys import IS captured (any
	// scope), proving the walker descends into try blocks regardless
	// of handler shape.
	totalSys := 0
	for k, n := range got {
		if k.to == "sys" {
			totalSys += n
		}
	}
	if totalSys == 0 {
		t.Error("import sys inside try/except OSError was dropped entirely; expected at least one edge")
	}
}

// TestScanner_DeferredImports_BackCompatJSON guards the JSON wire
// contract: edges without a scope tag (every non-import edge today)
// MUST NOT include a "scope" key, so legacy atlas binaries that do
// strict-mode JSON decoding don't reject the envelope.
func TestScanner_DeferredImports_BackCompatJSON(t *testing.T) {
	t.Parallel()
	skipIfNoPython(t)

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "sample.py"), deferredImportsFixture)

	s := NewScanner(Options{Logger: shared.NopLogger{}})
	t.Cleanup(func() { _ = s.Close() })
	res, err := s.Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Every non-import edge MUST have empty Meta (the rawEdge.Scope
	// is omitted at the JSON layer for non-imports — the Go-side
	// mapToResult faithfully forwards "" to graph.Edge.Meta).
	for _, e := range res.Edges {
		if e.Kind != "import" && e.Meta != "" {
			t.Errorf("non-import edge has Meta=%q (want empty): %+v", e.Meta, e)
		}
	}

	// Every import edge MUST have a non-empty Meta — the scanner
	// emits scope=module by default for top-level imports, so any
	// empty Meta on an import edge indicates a regression.
	for _, e := range res.Edges {
		if e.Kind == "import" && e.Meta == "" {
			t.Errorf("import edge has empty Meta (want one of module/function/conditional/type_checking/try_guard): %+v", e)
		}
	}
}

// deferredImportsFixture is the issue #16 reproducer. The shape
// mirrors the verification script in the issue body so a manual
// "rerun atlas init against this file" smoke test matches the
// CI assertions byte-for-byte.
const deferredImportsFixture = `# Module-level import - scope=module
import os
from typing import TYPE_CHECKING


def make_client():
    # Deferred import inside function body - scope=function.
    # The pre-#16 scanner walked only module.body so this edge was
    # silently dropped.
    from urllib.request import urlopen
    return urlopen


if TYPE_CHECKING:
    # Type-checking-only import - scope=type_checking. Takes
    # priority over the enclosing if-block because the test pattern
    # is more specific.
    from collections.abc import Iterator


try:
    # Try-guard import - scope=try_guard because the except clause
    # catches ImportError. Standard optional-dependency pattern.
    from greenlet import greenlet
except ImportError:
    greenlet = None


cfg = True
if cfg:
    # Plain conditional import - scope=conditional. No TYPE_CHECKING
    # and no try-guard.
    import json


try:
    # NOT a try-guard - the except handler catches OSError, which
    # has nothing to do with optional-dependency probing. The walker
    # must still emit the edge (closes #16) but tag it as
    # scope=conditional (the try itself, like an if, introduces a
    # conditional execution context).
    import sys
except OSError:
    pass
`

// mustWriteFile mirrors the helper in scanner_test.go — kept local
// so this test file doesn't depend on test-file load ordering.
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
