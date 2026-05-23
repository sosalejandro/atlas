package tsscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// TestScanner_TSFilesButNoTypescript is the regression test for
// sosalejandro/atlas-internal#19 — the Minca-AI silent-skip bug. When a
// project has .ts/.tsx files but the `typescript` npm package is not
// reachable (no node_modules, no caller-supplied path), the scanner MUST:
//
//  1. Return no error (TS scan is best-effort, never fatal).
//  2. Return zero symbols / zero edges (nothing was scanned).
//  3. Return exactly one warning describing what happened.
//  4. Include the count of skipped files in the warning so the user can
//     gauge impact at a glance.
//  5. Include actionable fix guidance (npm ci / --node-modules-path).
//
// Pre-fix the scanner spawned Node, hit ERR_MODULE_NOT_FOUND, and the
// orchestrator wrapped a 300-line stack trace in a single "ts scan: ..."
// warning that didn't mention the impact OR the fix. Users saw "0 TS
// symbols" indistinguishable from "no TS in this project".
func TestScanner_TSFilesButNoTypescript_EmitsActionableWarning(t *testing.T) {
	t.Parallel()
	skipIfNoNode(t)

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "sample.ts"),
		"export function hello(name: string): string {\n  return `hello ${name}`;\n}\n")
	mustWriteFile(t, filepath.Join(root, "page.tsx"),
		"export function Page() { return null; }\n")

	s := NewScanner(Options{Logger: shared.NopLogger{}})
	res, err := s.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan returned error (should degrade to warning): %v", err)
	}
	if res == nil {
		t.Fatal("Scan returned nil result")
	}
	if len(res.Symbols) != 0 || len(res.Edges) != 0 {
		t.Fatalf("expected zero symbols/edges; got %d/%d",
			len(res.Symbols), len(res.Edges))
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("expected exactly one warning; got %d: %v",
			len(res.Warnings), res.Warnings)
	}
	w := res.Warnings[0]
	// The warning is multi-line and human-targeted; assert each load-bearing
	// element rather than the full string so future copy-edits don't churn
	// the test.
	mustContain(t, w, "TypeScript scanner: could not initialize")
	mustContain(t, w, "'typescript' package not found")
	mustContain(t, w, ".ts/.tsx files found but not scanned: 2")
	mustContain(t, w, "npm ci")
	mustContain(t, w, "--node-modules-path")
	mustContain(t, w, "Searched")
}

// TestScanner_NoTSFiles_NoWarning confirms the silent-skip path — when there
// is no typescript module AND no .ts/.tsx files to scan, the scanner emits
// no warning (because emitting one would be noise for a Go-only project that
// happens to live next to atlas's "scan everything" entry point).
func TestScanner_NoTSFiles_NoWarning(t *testing.T) {
	t.Parallel()
	skipIfNoNode(t)

	root := t.TempDir()
	// Drop a non-TS file so the directory isn't empty (otherwise the walker
	// short-circuits before exercising the count path).
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n")

	s := NewScanner(Options{Logger: shared.NopLogger{}})
	res, err := s.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if res == nil {
		t.Fatal("Scan returned nil result")
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("expected zero warnings for no-TS project; got: %v", res.Warnings)
	}
}

// TestProbeTypescriptModule_FoundInProject verifies the probe returns true
// when typescript lives in the scanned project's node_modules.
func TestProbeTypescriptModule_FoundInProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "node_modules", "typescript"))

	found, searched := probeTypescriptModule(root, nil)
	if !found {
		t.Fatalf("expected found=true; got false (searched=%v)", searched)
	}
	if len(searched) == 0 || !strings.HasSuffix(searched[0], "node_modules") {
		t.Fatalf("expected searched to include <root>/node_modules; got %v", searched)
	}
}

// TestProbeTypescriptModule_FoundInExtraPath verifies the probe consults
// caller-supplied NodeModulesPaths in the documented order (project root
// first, extras second).
func TestProbeTypescriptModule_FoundInExtraPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	extra := t.TempDir()
	mustMkdirAll(t, filepath.Join(extra, "node_modules", "typescript"))

	found, searched := probeTypescriptModule(root, []string{filepath.Join(extra, "node_modules")})
	if !found {
		t.Fatalf("expected found=true; got false (searched=%v)", searched)
	}
	if len(searched) != 2 {
		t.Fatalf("expected two searched paths (project + extra); got %d: %v",
			len(searched), searched)
	}
}

// TestProbeTypescriptModule_NotFound verifies the probe reports false +
// returns the searched paths when no typescript install exists anywhere.
func TestProbeTypescriptModule_NotFound(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	found, searched := probeTypescriptModule(root, nil)
	if found {
		t.Fatal("expected found=false for empty tree")
	}
	if len(searched) != 1 {
		t.Fatalf("expected one searched path; got %d: %v", len(searched), searched)
	}
}

// TestCountTSSourceFiles_Basic exercises the .ts/.tsx counter on a small
// tree with one of each extension plus a .d.ts (excluded) and node_modules
// (skipped).
func TestCountTSSourceFiles_SkipsDTSAndNodeModules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "")
	mustWriteFile(t, filepath.Join(root, "b.tsx"), "")
	mustWriteFile(t, filepath.Join(root, "c.mts"), "")
	mustWriteFile(t, filepath.Join(root, "d.cts"), "")
	mustWriteFile(t, filepath.Join(root, "types.d.ts"), "")        // excluded
	mustMkdirAll(t, filepath.Join(root, "node_modules"))
	mustWriteFile(t, filepath.Join(root, "node_modules", "x.ts"), "") // skipped

	got, err := countTSSourceFiles(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if got != 4 {
		t.Fatalf("expected 4 (a.ts, b.tsx, c.mts, d.cts); got %d", got)
	}
}

// TestCountTSSourceFiles_HonoursContextCancel makes sure a cancelled ctx
// short-circuits the walk so a multi-million-file tree doesn't ignore the
// caller's deadline.
func TestCountTSSourceFiles_HonoursContextCancel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.ts"), "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := countTSSourceFiles(ctx, root, nil)
	if err == nil {
		t.Fatal("expected ctx.Err() to propagate; got nil")
	}
}

// ---------------------------------------------------------------------------
// helpers — tiny, package-local, no external test deps.
// ---------------------------------------------------------------------------

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected warning to contain %q; got:\n%s", needle, haystack)
	}
}
