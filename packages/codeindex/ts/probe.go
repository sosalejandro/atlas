package tsscan

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// probeTypescriptModule reports whether the embedded scanner.ts will be able
// to resolve `import 'typescript'` at runtime, given the scanned project's
// own node_modules and any caller-supplied fallbacks. The boolean return is
// true when the module is reachable; the second return is the (deterministic,
// de-duplicated) list of directories we looked under — surfaced so the
// no-typescript warning can tell the user where atlas searched.
//
// The candidate ordering mirrors bridgeTypescript: project-root first
// (closest-wins for monorepos), caller-supplied paths second (atlas's own
// node_modules in tests; an explicit --node-modules-path on the CLI).
//
// This is a pure read — no symlinks created, no env mutated. It exists so
// (*Scanner).Scan can short-circuit to a clean, actionable warning instead
// of spawning Node and letting it crash with a 300-line ERR_MODULE_NOT_FOUND
// stack trace that users have to grep through to understand what went wrong.
func probeTypescriptModule(projectRoot string, nodeModulesPaths []string) (bool, []string) {
	seen := make(map[string]bool)
	var searched []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		searched = append(searched, p)
	}
	add(filepath.Join(projectRoot, "node_modules"))
	for _, nm := range nodeModulesPaths {
		add(nm)
	}
	for _, dir := range searched {
		if st, err := os.Stat(filepath.Join(dir, "typescript")); err == nil && st.IsDir() {
			return true, searched
		}
	}
	return false, searched
}

// countTSSourceFiles walks rootDir and returns the number of .ts / .tsx /
// .mts / .cts files (declaration files .d.ts excluded — they're not source
// the scanner would extract symbols from).
//
// Always honours the standard skip set (node_modules, vendor, dist, build,
// hidden directories) plus any caller-supplied extras. ctx cancellation
// short-circuits the walk so a multi-million-file tree doesn't ignore a
// caller-cancelled scan.
//
// Returns (count, error). Walk errors are tolerated per-entry (returning
// nil to filepath.WalkDir so the walk continues); only a context error
// surfaces as a non-nil return.
//
// Lives in the tsscan package — not codeindex — because the warning that
// consumes it is composed here and surfacing the count through an out-param
// would leak walker mechanics into the orchestrator.
func countTSSourceFiles(ctx context.Context, rootDir string, extraSkipDirs map[string]bool) (int, error) {
	skip := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		"out":          true,
		".next":        true,
		".turbo":       true,
	}
	for d := range extraSkipDirs {
		skip[d] = true
	}
	count := 0
	walkErr := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			// Unreadable subtrees are skipped silently — they wouldn't have
			// been scanned anyway and surfacing them here would noise up the
			// warning the caller is about to compose.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			// Always descend into the root itself even if its name is in
			// the skip set (e.g. user scans a directory literally named
			// "dist"). Otherwise honour the skip set + hidden dirs.
			if path == rootDir {
				return nil
			}
			if skip[name] || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		// .d.ts is a declaration file — present in stripped typescript
		// distributions and node_modules type packages. Not source the
		// scanner would symbol-ise, so exclude it from the count to avoid
		// over-reporting "skipped" files.
		if strings.HasSuffix(name, ".d.ts") {
			return nil
		}
		if hasAnyTSExt(name) {
			count++
		}
		return nil
	})
	if walkErr != nil {
		return count, fmt.Errorf("count .ts files under %s: %w", rootDir, walkErr)
	}
	return count, nil
}

// hasAnyTSExt returns true when name ends in a TypeScript source extension
// the scanner would attempt to parse (.ts, .tsx, .mts, .cts). Centralised
// so the count + (future) glob-include logic stays consistent.
func hasAnyTSExt(name string) bool {
	switch {
	case strings.HasSuffix(name, ".ts"):
		return true
	case strings.HasSuffix(name, ".tsx"):
		return true
	case strings.HasSuffix(name, ".mts"):
		return true
	case strings.HasSuffix(name, ".cts"):
		return true
	}
	return false
}

// formatMissingTypescriptWarning composes the user-facing warning emitted
// when the TS scanner cannot initialise because the `typescript` module is
// not reachable. The message is multi-line on purpose — each line answers a
// different question the user has when they see the warning:
//
//	line 1: what happened (scanner did not run)
//	line 2: why (no typescript module found)
//	line 3: how big the impact is (N files skipped)
//	line 4: where atlas looked (so the user can verify their layout)
//	line 5: how to fix it (npm ci or explicit --node-modules-path)
//	line 6: how to re-run after fixing
//
// Single helper so the JSON `warnings[]` payload and the human stderr text
// stay byte-identical — machine consumers and humans see the same string.
func formatMissingTypescriptWarning(skippedCount int, searched []string) string {
	var b strings.Builder
	b.WriteString("TypeScript scanner: could not initialize.\n")
	b.WriteString("       Reason: 'typescript' package not found.\n")
	b.WriteString(fmt.Sprintf("       .ts/.tsx files found but not scanned: %d\n", skippedCount))
	if len(searched) > 0 {
		b.WriteString("       Searched (in order):\n")
		for _, p := range searched {
			b.WriteString(fmt.Sprintf("         - %s\n", p))
		}
	}
	b.WriteString("       Fix: cd <project>; npm ci  (or pass --node-modules-path <dir>)\n")
	b.WriteString("       Re-run: atlas scan")
	return b.String()
}
