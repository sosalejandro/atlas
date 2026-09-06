package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newScanCmd implements `atlas scan` — incremental re-scan that re-uses
// the file-hash cache so unchanged files aren't re-ingested.
func newScanCmd() *cobra.Command {
	var (
		root             string
		hashFiles        bool
		nodeModulesPaths []string
		includeGenerated bool
		showSkipped      bool
		skippedPath      string
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
looking for a node_modules/ sibling and uses the first hit.

--include-generated indexes machine-written files that would otherwise be
excluded. Exclusion is the default because generated statements execute
constantly and would dominate any coverage or complexity reading taken
over hand-written code; the flag is the escape hatch for "why did my
symbol disappear?". Which files count as generated is a property of the
codebase, so extra patterns belong under scan.generated in atlas.yaml
rather than on the command line.

--skipped does not scan. It reads back the exclusion ledger the last scan
wrote and answers "why is this file not indexed?" from the store, naming
the rule that claimed each file -- and, for a glob, the pattern that
matched. Exclusion is silent by design: a file that was never indexed does
not appear as uncovered or unlinked, it appears as nothing at all, so an
over-broad glob is invisible without this. --skipped-path narrows the
answer to one file.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The ledger read is a different verb wearing the same name: it
			// answers from the store and must not walk the tree, or the
			// answer would come from a scan the operator did not ask for.
			if showSkipped || skippedPath != "" {
				return runScanSkipped(cmd, skippedPath)
			}
			return runScan(cmd, root, hashFiles, nodeModulesPaths, includeGenerated)
		},
	}
	cmd.Flags().StringVar(&root, "root", "",
		"project root to scan (default: repo root or cwd)")
	cmd.Flags().BoolVar(&hashFiles, "hash-files", true,
		"compute SHA-256 of every scanned file (default: true)")
	cmd.Flags().StringSliceVar(&nodeModulesPaths, "node-modules-path", nil,
		"absolute path to a node_modules dir the TS scanner can borrow typescript from "+
			"(repeatable; auto-detected from the scan root when unset)")
	cmd.Flags().BoolVar(&includeGenerated, "include-generated", false,
		"index machine-written files instead of excluding them (see scan.generated in atlas.yaml)")
	cmd.Flags().BoolVar(&showSkipped, "skipped", false,
		"print the last scan's exclusion ledger instead of scanning")
	cmd.Flags().StringVar(&skippedPath, "skipped-path", "",
		"print the ledger entry for one file (implies --skipped)")
	return cmd
}

// scanResult is the JSON payload emitted by `atlas scan`. It embeds
// initResult so consumers can de-duplicate parser code (the `command`
// envelope field distinguishes the two), and adds the one count init has no
// reason to report: how many files this scan excluded from the index.
//
// That count is the pointer to `atlas scan --skipped`. Without it the only
// evidence that a rule dropped forty files is their absence, which reads
// exactly like a repo that never had them.
type scanResult struct {
	initResult
	FilesExcluded int `json:"files_excluded"`
}

// scanSkippedResult is the payload of the `--skipped` read path: the
// exclusion ledger as the last scan left it.
type scanSkippedResult struct {
	DBPath  string                 `json:"db_path"`
	Count   int                    `json:"count"`
	Skipped []store.SkippedFileRow `json:"skipped"`
}

func runScan(cmd *cobra.Command, rootArg string, hashFiles bool, nodeModulesPaths []string, includeGenerated bool) error {
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

	idx, warnings, err := indexProjectFromConfig(ctx, rootDir, hashFiles, nodeModulesPaths, includeGenerated)
	if err != nil {
		return err
	}

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	// The configured globs travel with the ingest so the ledger can record
	// WHICH pattern claimed each excluded file. The index carries the rule
	// but not the pattern, and the pattern is the half an operator can act
	// on.
	stats, err := s.Ingest(ctx, idx, store.IngestOptions{
		GeneratedGlobs: loaded.Scan.Generated,
	})
	if err != nil {
		return fmt.Errorf("ingest project: %w", err)
	}

	res := scanResult{
		initResult: initResult{
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
		},
		FilesExcluded: stats.SkippedFilesRecorded,
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
	if r.FilesExcluded > 0 {
		fmt.Fprintf(cmd.OutOrStdout(),
			"  files_excluded=%d (generated or ignored; see 'atlas scan --skipped')\n",
			r.FilesExcluded)
	}
	for _, w := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "  warning: %s\n", w)
	}
}

// runScanSkipped answers "why is this file not indexed?" from the store.
//
// It deliberately does not scan. The value of a persisted ledger is that it
// outlives the scan that wrote it: re-walking the tree here would answer
// with today's rules instead of the ones that produced the index the
// operator is looking at, and would make the answer cost a full scan.
func runScanSkipped(cmd *cobra.Command, filterPath string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	rows, err := readSkippedLedger(ctx, s, filterPath)
	if err != nil {
		return err
	}

	res := scanSkippedResult{DBPath: dbPath, Count: len(rows), Skipped: rows}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "scan",
			map[string]any{"skipped": true, "path": filterPath}, res, nil)
	}
	printSkippedText(cmd, res, filterPath)
	return nil
}

// readSkippedLedger returns the whole ledger, or the single entry for
// filterPath. A path with no entry is not an error: "this file is indexed"
// is a perfectly good answer to "why is it not indexed?", and an exit code
// would tell a script the query failed rather than that the file is fine.
func readSkippedLedger(ctx context.Context, s *store.Store, filterPath string) ([]store.SkippedFileRow, error) {
	if filterPath == "" {
		rows, err := s.SkippedFiles().List(ctx)
		if err != nil {
			return nil, fmt.Errorf("read skipped ledger: %w", err)
		}
		return rows, nil
	}
	row, err := s.SkippedFiles().Get(ctx, filepath.ToSlash(filterPath))
	if errors.Is(err, shared.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skipped ledger for %s: %w", filterPath, err)
	}
	return []store.SkippedFileRow{row}, nil
}

func printSkippedText(cmd *cobra.Command, r scanSkippedResult, filterPath string) {
	out := cmd.OutOrStdout()
	if r.Count == 0 {
		if filterPath != "" {
			fmt.Fprintf(out, "%s is not on the exclusion ledger: the last scan "+
				"either indexed it or never walked it (db: %s)\n", filterPath, r.DBPath)
			return
		}
		fmt.Fprintf(out, "The last scan excluded no files (db: %s)\n", r.DBPath)
		return
	}
	fmt.Fprintf(out, "Excluded from the index by the last scan (db: %s): %d file(s)\n",
		r.DBPath, r.Count)
	pathWidth, ruleWidth := 0, 0
	for _, row := range r.Skipped {
		if n := len(row.FilePath); n > pathWidth {
			pathWidth = n
		}
		if n := len(row.Rule); n > ruleWidth {
			ruleWidth = n
		}
	}
	for _, row := range r.Skipped {
		// The detail is the actionable half — the glob to narrow — so it
		// sits on the same line as the rule rather than behind --verbose.
		line := fmt.Sprintf("  %-*s  %-*s", pathWidth, row.FilePath, ruleWidth, row.Rule)
		if row.Detail != "" {
			line += "  " + row.Detail
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
}
