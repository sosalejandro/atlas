package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveNodeModulesPath_WalksUp verifies the auto-detect helper finds
// a node_modules/ at the repo root when called from a nested package
// directory (e.g. apps/web-patient/).
func TestResolveNodeModulesPath_WalksUp(t *testing.T) {
	root := t.TempDir()
	// Repo-style layout: root/apps/web-patient + root/node_modules/.
	nm := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	nested := filepath.Join(root, "apps", "web-patient")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got := resolveNodeModulesPath(nested)
	// Resolve symlinks/abs to normalise: both sides should refer to the
	// same on-disk directory. macOS TempDir lives under /var → /private/var
	// so a string compare would be brittle.
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("evalsymlinks got=%q: %v", got, err)
	}
	wantReal, err := filepath.EvalSymlinks(nm)
	if err != nil {
		t.Fatalf("evalsymlinks want=%q: %v", nm, err)
	}
	if gotReal != wantReal {
		t.Fatalf("resolveNodeModulesPath = %q, want %q", gotReal, wantReal)
	}
}

// TestResolveNodeModulesPath_PrefersClosest confirms that when several
// node_modules directories exist along the walk-up path, the closest one
// wins. This matches pnpm/npm workspace conventions where a per-package
// node_modules takes precedence over the root.
func TestResolveNodeModulesPath_PrefersClosest(t *testing.T) {
	root := t.TempDir()
	rootNM := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(rootNM, 0o755); err != nil {
		t.Fatalf("mkdir root nm: %v", err)
	}
	leaf := filepath.Join(root, "apps", "web-patient")
	leafNM := filepath.Join(leaf, "node_modules")
	if err := os.MkdirAll(leafNM, 0o755); err != nil {
		t.Fatalf("mkdir leaf nm: %v", err)
	}

	got := resolveNodeModulesPath(leaf)
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(leafNM)
	if gotReal != wantReal {
		t.Fatalf("expected leaf node_modules to win; got %q want %q",
			gotReal, wantReal)
	}
}

// TestResolveNodeModulesPath_PrefersTypescriptHost asserts that when two
// node_modules/ directories are on the walk-up path and only the outer one
// contains a top-level typescript/, the outer wins. This is the load-bearing
// regression guard for the Phase 9 dogfood failure: a leaf node_modules/
// that's missing typescript would otherwise mask a root install that has it.
func TestResolveNodeModulesPath_PrefersTypescriptHost(t *testing.T) {
	root := t.TempDir()
	// Root node_modules with typescript installed.
	rootNM := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(filepath.Join(rootNM, "typescript"), 0o755); err != nil {
		t.Fatalf("mkdir root nm/typescript: %v", err)
	}
	// Nested package with its own (empty) node_modules — should be skipped.
	leaf := filepath.Join(root, "apps", "web-patient")
	leafNM := filepath.Join(leaf, "node_modules")
	if err := os.MkdirAll(leafNM, 0o755); err != nil {
		t.Fatalf("mkdir leaf nm: %v", err)
	}

	got := resolveNodeModulesPath(leaf)
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(rootNM)
	if gotReal != wantReal {
		t.Fatalf("expected outer node_modules with typescript to win; got %q want %q",
			gotReal, wantReal)
	}
}

// TestResolveNodeModulesPath_PnpmLayout exercises the pnpm-virtual-store
// branch: nutrition-v2-go (and most pnpm monorepos) install typescript at
// node_modules/.pnpm/typescript@<v>/node_modules/typescript, NOT directly
// at node_modules/typescript. The helper must follow the .pnpm hop so the
// returned path actually satisfies the TS scanner's bridge candidate
// check (which joins <result>/typescript).
func TestResolveNodeModulesPath_PnpmLayout(t *testing.T) {
	root := t.TempDir()
	pnpmTS := filepath.Join(root, "node_modules", ".pnpm",
		"typescript@5.9.3", "node_modules", "typescript")
	if err := os.MkdirAll(pnpmTS, 0o755); err != nil {
		t.Fatalf("mkdir pnpm typescript: %v", err)
	}

	got := resolveNodeModulesPath(root)
	want := filepath.Join(root, "node_modules", ".pnpm",
		"typescript@5.9.3", "node_modules")
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(want)
	if gotReal != wantReal {
		t.Fatalf("pnpm path resolution wrong; got %q want %q", gotReal, wantReal)
	}
	// Final sanity check: the returned path + "/typescript" exists.
	if _, err := os.Stat(filepath.Join(got, "typescript")); err != nil {
		t.Fatalf("resolved path doesn't contain typescript/: %v", err)
	}
}

// TestResolveNodeModulesPath_PnpmPicksNewestVersion confirms the tie-break
// when several pnpm-virtual-store typescript versions coexist. We pick the
// lex-last as a stand-in for "newest"; this matters less than picking one
// deterministically.
func TestResolveNodeModulesPath_PnpmPicksNewestVersion(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"5.6.3", "5.8.2", "5.9.3"} {
		p := filepath.Join(root, "node_modules", ".pnpm",
			"typescript@"+v, "node_modules", "typescript")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	got := resolveNodeModulesPath(root)
	if !strings.Contains(got, "typescript@5.9.3") {
		t.Fatalf("expected typescript@5.9.3 to win; got %q", got)
	}
}

// TestResolveNodeModulesPath_NoneFound returns empty string when the walk
// terminates at the filesystem root without finding a node_modules/.
func TestResolveNodeModulesPath_NoneFound(t *testing.T) {
	// Use a sandboxed tempdir whose ancestors definitely don't include a
	// node_modules/. Since the helper walks all the way to "/", we need to
	// make sure no real node_modules exists on the path. We sidestep this
	// by checking the documented contract: when the immediate dir has no
	// match AND no ancestor matches under the tempdir, the helper either
	// returns empty (clean machine) OR a system-level node_modules (rare).
	// The test asserts the function does NOT panic and does NOT return the
	// scanRoot itself.
	root := t.TempDir()
	got := resolveNodeModulesPath(root)
	if got == root {
		t.Fatalf("helper must not return scanRoot itself; got %q", got)
	}
	// If something was found, it must end in node_modules and be a dir.
	if got != "" {
		if filepath.Base(got) != "node_modules" {
			t.Fatalf("found path doesn't end in node_modules: %q", got)
		}
		info, err := os.Stat(got)
		if err != nil || !info.IsDir() {
			t.Fatalf("found path isn't a real directory: %q err=%v", got, err)
		}
	}
}

// TestResolveNodeModulesPath_EmptyInput safeguards the helper against the
// empty-string seed callers might hand in when --root is also empty.
func TestResolveNodeModulesPath_EmptyInput(t *testing.T) {
	if got := resolveNodeModulesPath(""); got != "" {
		t.Fatalf("empty scanRoot must return \"\"; got %q", got)
	}
}

// TestResolveNodeModulesPath_IgnoresFileNamedNodeModules guards against a
// false positive when a file (not directory) happens to be named
// `node_modules` in the walk path.
func TestResolveNodeModulesPath_IgnoresFileNamedNodeModules(t *testing.T) {
	root := t.TempDir()
	bogus := filepath.Join(root, "node_modules")
	if err := os.WriteFile(bogus, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write bogus: %v", err)
	}
	got := resolveNodeModulesPath(root)
	if got == bogus {
		t.Fatalf("helper must reject a non-directory entry; got %q", got)
	}
}

// TestIndexProjectFromConfig_AutoDetectsNodeModules wires the full helper
// stack: a synthetic tmp project with a package.json + node_modules/
// containing a stubbed typescript/package.json. We exercise the
// orchestrator (codeindex.IndexProject) with no explicit
// --node-modules-path and confirm that:
//
//  1. The scan completes without a hard error.
//  2. The TS scanner does NOT emit the "no typescript install found"
//     warning that would surface from an empty NodeModulesPaths.
//
// This is the regression guard for the Phase 9 dogfood failure: init
// silently dropping all TS symbols because /tmp/atlas-tsscan-* couldn't
// resolve `typescript`.
func TestIndexProjectFromConfig_AutoDetectsNodeModules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"),
		[]byte(`{"name":"fixture","version":"0.0.0"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	tsDir := filepath.Join(root, "node_modules", "typescript")
	if err := os.MkdirAll(tsDir, 0o755); err != nil {
		t.Fatalf("mkdir typescript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsDir, "package.json"),
		[]byte(`{"name":"typescript","version":"5.0.0"}`), 0o644); err != nil {
		t.Fatalf("write ts package.json: %v", err)
	}

	// Reset package-level config singletons so the helper picks up clean
	// defaults rather than whatever a prior test populated.
	loaded = Config{}
	flags = globalFlags{}

	idx, warnings, err := indexProjectFromConfig(context.Background(), root, false, nil)
	if err != nil {
		t.Fatalf("indexProjectFromConfig: %v\nwarnings: %v", err, warnings)
	}
	if idx == nil {
		t.Fatal("nil index")
	}
	// The "no typescript install found" warning would indicate the
	// auto-detect short-circuit failed to wire the path through.
	for _, w := range idx.Warnings {
		if strings.Contains(w, "no typescript install found") {
			t.Fatalf("auto-detect failed to wire node_modules: warning surfaced: %q", w)
		}
	}
}

// TestEffectiveNodeModulesPaths_ExplicitWins confirms that any caller-
// supplied value is returned verbatim — auto-detect MUST NOT override an
// explicit --node-modules-path. This is the load-bearing rule for users
// who deliberately point atlas at a specific install in CI scripts.
func TestEffectiveNodeModulesPaths_ExplicitWins(t *testing.T) {
	root := t.TempDir()
	// An in-tree node_modules/typescript exists; auto-detect WOULD pick it.
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "typescript"), 0o755); err != nil {
		t.Fatalf("mkdir in-tree typescript: %v", err)
	}
	explicit := []string{"/opt/shared/node_modules"}

	got := effectiveNodeModulesPaths(root, explicit)
	if len(got) != 1 || got[0] != explicit[0] {
		t.Fatalf("effective = %v, want %v (explicit must override auto-detect)",
			got, explicit)
	}
}

// TestEffectiveNodeModulesPaths_AutoDetectFallback confirms that when no
// explicit path is supplied AND an in-tree typescript exists, auto-detect
// kicks in and returns the typescript-bearing node_modules. This is the
// regression guard for the Phase 9 silent-drop bug.
func TestEffectiveNodeModulesPaths_AutoDetectFallback(t *testing.T) {
	root := t.TempDir()
	nm := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(filepath.Join(nm, "typescript"), 0o755); err != nil {
		t.Fatalf("mkdir typescript: %v", err)
	}
	got := effectiveNodeModulesPaths(root, nil)
	if len(got) != 1 {
		t.Fatalf("effective = %v, want exactly 1 auto-detected entry", got)
	}
	gotReal, _ := filepath.EvalSymlinks(got[0])
	wantReal, _ := filepath.EvalSymlinks(nm)
	if gotReal != wantReal {
		t.Fatalf("auto-detect resolved %q, want %q", gotReal, wantReal)
	}
}

// TestEffectiveNodeModulesPaths_EmptyUserSuppliedTreatedAsAutoDetect
// guards the boundary case where a caller passes an explicit but empty
// slice (e.g. `--node-modules-path ""` parses to len 0). The effective
// path must still auto-detect rather than disabling the TS bridge.
func TestEffectiveNodeModulesPaths_EmptyUserSuppliedTreatedAsAutoDetect(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "typescript"), 0o755); err != nil {
		t.Fatalf("mkdir typescript: %v", err)
	}
	got := effectiveNodeModulesPaths(root, []string{})
	if len(got) != 1 {
		t.Fatalf("expected auto-detect to fire for empty slice; got %v", got)
	}
}

// TestIndexProjectFromConfig_FailsCleanlyWhenNoNodeModules verifies the
// promised graceful degradation: a project with package.json but no
// node_modules/ anywhere on the walk-up path must still complete the
// scan, returning an index (possibly with a TS warning) and no error.
func TestIndexProjectFromConfig_FailsCleanlyWhenNoNodeModules(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"),
		[]byte(`{"name":"fixture","version":"0.0.0"}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	// Drop a Go file so the Go scan has something to find.
	goSrc := []byte("package fixture\n\nfunc Hello() {}\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), goSrc, 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module fixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	loaded = Config{}
	flags = globalFlags{}

	idx, _, err := indexProjectFromConfig(context.Background(), root, false, nil)
	if err != nil {
		t.Fatalf("indexProjectFromConfig: %v", err)
	}
	if idx == nil {
		t.Fatal("nil index")
	}
	// The Go scan must still produce its symbols even when TS resolution
	// fails. This is the load-bearing "init never silently drops Go" promise.
	if len(idx.Symbols) == 0 {
		t.Fatalf("expected Go symbols even with no node_modules; got 0")
	}
}

// TestInitCmd_HasNodeModulesPathFlag verifies the flag is wired on the
// init command surface. A future refactor that drops the flag would
// re-introduce the Phase 9 silent-drop bug; this is the regression guard.
func TestInitCmd_HasNodeModulesPathFlag(t *testing.T) {
	c := newInitCmd()
	if c.Flags().Lookup("node-modules-path") == nil {
		t.Fatal("atlas init is missing --node-modules-path flag")
	}
}

// TestScanCmd_HasNodeModulesPathFlag mirrors the init check for scan.
func TestScanCmd_HasNodeModulesPathFlag(t *testing.T) {
	c := newScanCmd()
	if c.Flags().Lookup("node-modules-path") == nil {
		t.Fatal("atlas scan is missing --node-modules-path flag")
	}
}
