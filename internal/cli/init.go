package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/store"
)

// newInitCmd implements `atlas init` — first-time scan that creates
// `.atlas/atlas.db`, applies migrations, and ingests the project.
func newInitCmd() *cobra.Command {
	var (
		root       string
		importYAML string
		hashFiles  bool
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
a warning so existing scripts don't break.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, root, importYAML, hashFiles)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root to scan (default: repo root or cwd)")
	cmd.Flags().StringVar(&importYAML, "import-yaml", "",
		"directory of testreg-format feature YAMLs to import (no-op in v1)")
	cmd.Flags().BoolVar(&hashFiles, "hash-files", true,
		"compute SHA-256 of every scanned file (default: true)")
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

func runInit(cmd *cobra.Command, rootArg, importYAML string, hashFiles bool) error {
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

	idx, warnings, err := indexProjectFromConfig(ctx, rootDir, hashFiles)
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
// from the loaded Config + the supplied hash-files toggle.
//
// Returns the index, any orchestrator warnings, and the first hard error.
func indexProjectFromConfig(ctx context.Context, rootDir string, hashFiles bool) (
	*codeindex.Index, []string, error,
) {
	opts := codeindex.Options{
		SkipTS:    loaded.Scan.SkipTS,
		SkipDirs:  loaded.Scan.SkipDirs,
		HashFiles: hashFiles,
	}
	idx, err := codeindex.IndexProject(ctx, rootDir, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("index project %s: %w", rootDir, err)
	}
	return idx, idx.Warnings, nil
}
