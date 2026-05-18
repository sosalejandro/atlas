package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
	"github.com/sosalejandro/atlas/packages/store"
)

// newInitCmd implements `atlas init` — first-time scan that creates
// `.atlas/atlas.db`, applies migrations, and ingests the project.
func newInitCmd() *cobra.Command {
	var (
		root             string
		importYAML       string
		hashFiles        bool
		nodeModulesPaths []string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialise the Atlas state DB and run the first scan",
		Long: `init performs a one-time scan of the project rooted at --root
(defaults to the git toplevel / cwd), opens (creating if needed) the
SQLite state DB, applies all pending migrations, and ingests the
codeindex.Index produced by the scan.

After init you can re-run incremental scans with 'atlas scan'.

--import-yaml is reserved for testreg compatibility: a directory of
YAML feature definitions may be ingested alongside the AST scan once
the import-yaml adapter lands. For now it is accepted but no-ops with
a warning so existing scripts don't break.

--node-modules-path lets you point the TypeScript scanner at a real
node_modules directory so it can resolve the embedded scanner.ts's
'typescript' dependency. When not supplied, init walks up from --root
looking for a node_modules/ sibling and uses the first hit. Explicit
values always win over the auto-detected path.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, root, importYAML, hashFiles, nodeModulesPaths)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root to scan (default: repo root or cwd)")
	cmd.Flags().StringVar(&importYAML, "import-yaml", "",
		"directory of testreg-format feature YAMLs to import (no-op in v1)")
	cmd.Flags().BoolVar(&hashFiles, "hash-files", true,
		"compute SHA-256 of every scanned file (default: true)")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; auto-detected from the scan root when unset)")
	return cmd
}

// initResult is the JSON payload emitted by `atlas init`.
type initResult struct {
	DBPath              string `json:"db_path"`
	Root                string `json:"root"`
	SymbolsInserted     int    `json:"symbols_inserted"`
	EdgesInserted       int    `json:"edges_inserted"`
	AnnotationsInserted int    `json:"annotations_inserted"`
	FileHashesUpserted  int    `json:"file_hashes_upserted"`
	PatternMatchesSet   int    `json:"pattern_matches_set"`
	FilesScanned        int    `json:"files_scanned"`
	FilesSkipped        int    `json:"files_skipped"`
	DurationMS          int64  `json:"duration_ms"`
}

func runInit(cmd *cobra.Command, rootArg, importYAML string, hashFiles bool, nodeModulesPaths []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	rootDir := rootArg
	if rootDir == "" {
		rootDir = loaded.repoRoot
	}

	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}

	idx, warnings, err := indexProjectFromConfig(ctx, rootDir, hashFiles, nodeModulesPaths)
	if err != nil {
		return err
	}

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		return fmt.Errorf("ingest project: %w", err)
	}

	if importYAML != "" {
		warnings = append(warnings,
			fmt.Sprintf("--import-yaml=%s: testreg YAML import is not implemented in v1; the directory was ignored",
				importYAML))
	}

	res := initResult{
		DBPath:              dbPath,
		Root:                rootDir,
		SymbolsInserted:     stats.SymbolsInserted,
		EdgesInserted:       stats.EdgesInserted,
		AnnotationsInserted: stats.AnnotationsInserted,
		FileHashesUpserted:  stats.FileHashesUpserted,
		PatternMatchesSet:   stats.PatternMatchesSet,
		FilesScanned:        stats.FilesScanned,
		FilesSkipped:        stats.FilesSkipped,
		DurationMS:          stats.Duration.Milliseconds(),
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "init",
			map[string]any{"root": rootDir, "import_yaml": importYAML, "hash_files": hashFiles},
			res, warnings)
	}
	printInitText(cmd, res, warnings)
	return nil
}

func printInitText(cmd *cobra.Command, r initResult, warnings []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "Atlas initialised %s (root: %s)\n", r.DBPath, r.Root)
	fmt.Fprintf(cmd.OutOrStdout(),
		"  symbols=%d edges=%d annotations=%d file_hashes=%d pattern_matches=%d\n",
		r.SymbolsInserted, r.EdgesInserted, r.AnnotationsInserted,
		r.FileHashesUpserted, r.PatternMatchesSet)
	fmt.Fprintf(cmd.OutOrStdout(),
		"  files_scanned=%d files_skipped=%d duration=%dms\n",
		r.FilesScanned, r.FilesSkipped, r.DurationMS)
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}

// indexProjectFromConfig runs codeindex.IndexProject with options derived
// from the loaded Config + the supplied hash-files toggle + caller-supplied
// (or auto-detected) node_modules paths for the TS scanner's typescript
// resolution.
//
// nodeModulesPaths from the caller (e.g. via --node-modules-path) always
// wins. When empty we walk up from rootDir looking for a node_modules/
// sibling and use the first hit. Missing node_modules is not fatal — the
// TS scanner degrades to a warning and the Go scan still completes.
//
// Returns the index, any orchestrator warnings, and the first hard error.
func indexProjectFromConfig(ctx context.Context, rootDir string, hashFiles bool, nodeModulesPaths []string) (
	*codeindex.Index, []string, error,
) {
	resolvedNM := effectiveNodeModulesPaths(rootDir, nodeModulesPaths)
	opts := codeindex.Options{
		SkipTS:    loaded.Scan.SkipTS,
		SkipDirs:  loaded.Scan.SkipDirs,
		HashFiles: hashFiles,
		TSOptions: tsscan.Options{
			NodeModulesPaths: resolvedNM,
		},
	}
	idx, err := codeindex.IndexProject(ctx, rootDir, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("index project %s: %w", rootDir, err)
	}
	return idx, idx.Warnings, nil
}

// effectiveNodeModulesPaths returns the node_modules paths that should be
// forwarded to the TS scanner. Caller-supplied values always win: when
// userSupplied is non-empty we return it verbatim. Only when it's empty do
// we fall back to walking up from scanRoot for a typescript-bearing
// node_modules — the auto-detect that closes the Phase 9 silent-drop bug
// for the common case where the user hasn't pinned a path.
//
// Returning nil (rather than an empty slice) when no auto-detect candidate
// exists lets the underlying TS scanner skip the NodeModulesPaths
// validation path entirely instead of logging "ignored entry" warnings for
// an empty list.
func effectiveNodeModulesPaths(scanRoot string, userSupplied []string) []string {
	if len(userSupplied) > 0 {
		return userSupplied
	}
	if auto := resolveNodeModulesPath(scanRoot); auto != "" {
		return []string{auto}
	}
	return nil
}

// resolveNodeModulesPath returns the closest node_modules/ directory walking
// up from scanRoot, preferring one that actually contains a top-level
// `typescript/` package (which is what the TS scanner's typescript-bridge
// looks for). Empty string when none is found. Caller is responsible for
// treating the empty string as "skip auto-detect" — the TS scanner itself
// degrades gracefully if no resolution root is available.
//
// Why the extra check: pnpm installs `typescript` inside
// `node_modules/.pnpm/typescript@<version>/node_modules/typescript`, NOT at
// `node_modules/typescript`. A naive "first node_modules wins" rule then
// hands the scanner a directory it can't resolve `typescript` from, which
// re-introduces the Phase 9 ERR_MODULE_NOT_FOUND silent-drop bug. When the
// closest node_modules lacks a top-level typescript/, we probe the
// `.pnpm/typescript@*/node_modules` inner layouts and return the first
// match. npm/yarn hoisting always satisfies the closest-wins case directly.
//
// We make scanRoot absolute first so the walk doesn't terminate prematurely
// when callers pass a relative path like "." or "./apps/web-patient".
func resolveNodeModulesPath(scanRoot string) string {
	if scanRoot == "" {
		return ""
	}
	abs, err := filepath.Abs(scanRoot)
	if err != nil {
		return ""
	}
	dir := abs
	var fallback string
	for {
		candidate := filepath.Join(dir, "node_modules")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			// Hoisted layout (npm / yarn / pnpm with shamefully-hoist) —
			// typescript is right inside node_modules/.
			if info2, err := os.Stat(filepath.Join(candidate, "typescript")); err == nil && info2.IsDir() {
				return candidate
			}
			// pnpm isolated layout — typescript lives inside
			// node_modules/.pnpm/typescript@<v>/node_modules/.
			if inner := findPnpmTypescriptDir(candidate); inner != "" {
				return inner
			}
			// Remember the first plain node_modules we saw as the fallback
			// for projects that don't have typescript installed at all.
			// The scanner can still bridge from caller-supplied paths or
			// degrade with a warning; an empty return would skip the
			// resolution-root wiring entirely.
			if fallback == "" {
				fallback = candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fallback
		}
		dir = parent
	}
}

// findPnpmTypescriptDir scans nodeModulesDir/.pnpm for the typescript@*
// virtual store directory and returns the inner node_modules path that
// contains the real typescript install. Returns empty string if no pnpm
// typescript install exists at this layer.
//
// We pick the lexicographically last match when multiple versions are
// present, which approximates "newest version wins" without paying for a
// semver parse. The TS scanner only needs *a* typescript install to
// satisfy the bare ESM import; the exact version it picks doesn't affect
// the scan result because scanner.ts uses the compiler API on the source
// files it owns, not on the resolved typescript module's types.
func findPnpmTypescriptDir(nodeModulesDir string) string {
	pnpmDir := filepath.Join(nodeModulesDir, ".pnpm")
	entries, err := os.ReadDir(pnpmDir)
	if err != nil {
		return ""
	}
	var best string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Match "typescript@<version>" exactly (no false positives like
		// "typescript-eslint@..."). The @ chars in pnpm dir names are
		// literal — they're not URL-escaped at this layer.
		if !strings.HasPrefix(name, "typescript@") {
			continue
		}
		inner := filepath.Join(pnpmDir, name, "node_modules")
		if info, err := os.Stat(filepath.Join(inner, "typescript")); err == nil && info.IsDir() {
			if name > best {
				best = name
			}
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(pnpmDir, best, "node_modules")
}
