package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/store"
)

// newScanCmd implements `atlas scan` — incremental re-scan that re-uses
// the file-hash cache so unchanged files aren't re-ingested.
func newScanCmd() *cobra.Command {
	var (
		root             string
		hashFiles        bool
		nodeModulesPaths []string
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Re-scan the project (incremental via file hashes)",
		Long: `scan walks the project root, indexes every source file, and writes
the resulting symbols / edges / annotations / pattern matches to the
SQLite state DB. Files whose SHA-256 matches the cached hash are
skipped to avoid pointless re-writes.

The first scan after 'atlas init' will report files_skipped=0 because
every file is fresh; subsequent scans become incremental as more files
stabilise.

--node-modules-path mirrors 'atlas init': point the TypeScript scanner
at a real node_modules directory so the embedded scanner.ts can resolve
its 'typescript' dependency. When unset, scan walks up from --root
looking for a node_modules/ sibling and uses the first hit.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScan(cmd, root, hashFiles, nodeModulesPaths)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root to scan (default: repo root or cwd)")
	cmd.Flags().BoolVar(&hashFiles, "hash-files", true,
		"compute SHA-256 of every scanned file (default: true)")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; auto-detected from the scan root when unset)")
	return cmd
}

// scanResult is the JSON payload emitted by `atlas scan`. Identical shape
// to initResult so consumers can de-duplicate parser code; the `command`
// envelope field distinguishes the two.
type scanResult = initResult

func runScan(cmd *cobra.Command, rootArg string, hashFiles bool, nodeModulesPaths []string) error {
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

	res := scanResult{
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
		return emitJSON(stdoutOrJSON(cmd), "scan",
			map[string]any{"root": rootDir, "hash_files": hashFiles},
			res, warnings)
	}
	printScanText(cmd, res, warnings)
	return nil
}

func printScanText(cmd *cobra.Command, r scanResult, warnings []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "Atlas scan complete (root: %s, db: %s)\n", r.Root, r.DBPath)
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
