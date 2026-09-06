package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

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

		skipTypedResolution bool
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

--skip-typed-resolution scans Go with the AST name heuristics alone
instead of type-checking through go/packages. Type checking is the
default because a name is not an answer to "which declaration does this
call bind to", and it degrades per package, so a tree that does not
compile still scans. The flag is the escape hatch for when the LOAD
itself is the problem: no Go toolchain on the machine, a build that
needs credentials to resolve modules, or a latency budget that cannot
absorb it. Expect call edges to move from the typed tier down to
name_resolved and syntactic -- 'atlas edges' will show it.

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
				return runScanSkipped(cmd, root, skippedPath)
			}
			return runScan(cmd, root, hashFiles, nodeModulesPaths, includeGenerated,
				skipTypedResolution)
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
	cmd.Flags().BoolVar(&skipTypedResolution, "skip-typed-resolution", false,
		"resolve Go calls by name only, without go/packages type checking "+
			"(escape hatch: no toolchain, or a load that cannot run here)")
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
//
// LedgerPresent is separate from Count because zero rows has two causes that
// mean opposite things — the last scan excluded nothing, or nothing ever
// wrote a ledger into this database — and a consumer that cannot tell them
// apart will read the second as the first.
type scanSkippedResult struct {
	DBPath          string `json:"db_path"`
	LedgerPresent   bool   `json:"ledger_present"`
	LedgerWrittenAt string `json:"ledger_written_at,omitempty"`
	// QueriedPath is the ledger key --skipped-path was normalised to, so a
	// miss can be read as "this key is absent" rather than "your spelling
	// was wrong".
	QueriedPath string                 `json:"queried_path,omitempty"`
	Count       int                    `json:"count"`
	Skipped     []store.SkippedFileRow `json:"skipped"`
}

func runScan(
	cmd *cobra.Command,
	rootArg string,
	hashFiles bool,
	nodeModulesPaths []string,
	includeGenerated bool,
	skipTypedResolution bool,
) error {
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

	idx, warnings, err := indexProjectFromConfig(ctx, rootDir, hashFiles, nodeModulesPaths,
		includeGenerated, withSkipTypedResolution(skipTypedResolution))
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
func runScanSkipped(cmd *cobra.Command, rootArg, filterPath string) error {
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
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	// Asked BEFORE the rows, because it decides what zero rows means.
	writtenAt, present, err := s.SkippedFiles().WrittenAt(ctx)
	if err != nil {
		return fmt.Errorf("read skipped ledger marker: %w", err)
	}

	key := normalizeLedgerPath(filterPath, rootDir)
	rows, err := readSkippedLedger(ctx, s, key)
	if err != nil {
		return err
	}

	res := scanSkippedResult{
		DBPath:        dbPath,
		LedgerPresent: present,
		QueriedPath:   key,
		Count:         len(rows),
		Skipped:       rows,
	}
	if present && !writtenAt.IsZero() {
		res.LedgerWrittenAt = writtenAt.Format(time.RFC3339)
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "scan",
			map[string]any{"skipped": true, "path": filterPath}, res, nil)
	}
	printSkippedText(cmd, res)
	return nil
}

// normalizeLedgerPath rewrites an operator-supplied path into the key space
// the ledger is written in: project-relative, slash-separated, cleaned.
//
// Matching the raw string is what makes the lookup useless in practice. The
// two spellings an operator actually produces are a shell-completed
// `./pkg/x.go` and the absolute path a jump-to-file gave them, and neither
// is ever a ledger key — so both miss, and a miss used to read as "this file
// is indexed".
func normalizeLedgerPath(input, rootDir string) string {
	if input == "" {
		return ""
	}
	p := input
	if filepath.IsAbs(p) {
		// Anchored on the scan root, which is what the ledger keys are
		// relative to. A path outside it stays absolute and misses — which
		// is the honest answer, since it was never walked.
		if absRoot, err := filepath.Abs(rootDir); err == nil {
			if rel, relErr := filepath.Rel(absRoot, p); relErr == nil &&
				!strings.HasPrefix(rel, "..") {
				p = rel
			}
		}
	}
	p = filepath.ToSlash(p)
	if cleaned := path.Clean(p); cleaned != "." {
		p = cleaned
	}
	return p
}

// readSkippedLedger returns the whole ledger, or the single entry for
// ledgerKey (already normalised by normalizeLedgerPath). A key with no entry
// is not an error: "the last scan did not exclude this" is a perfectly good
// answer to "why is it not indexed?", and an exit code would tell a script
// the query failed rather than that it got an answer.
func readSkippedLedger(ctx context.Context, s *store.Store, ledgerKey string) ([]store.SkippedFileRow, error) {
	if ledgerKey == "" {
		rows, err := s.SkippedFiles().List(ctx)
		if err != nil {
			return nil, fmt.Errorf("read skipped ledger: %w", err)
		}
		return rows, nil
	}
	row, err := s.SkippedFiles().Get(ctx, ledgerKey)
	if errors.Is(err, shared.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skipped ledger for %s: %w", ledgerKey, err)
	}
	return []store.SkippedFileRow{row}, nil
}

func printSkippedText(cmd *cobra.Command, r scanSkippedResult) {
	out := cmd.OutOrStdout()
	if r.Count == 0 {
		printSkippedNothing(out, r)
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

// printSkippedNothing renders the empty case, whose whole difficulty is that
// zero rows has two causes that mean opposite things.
//
// A store with no ledger marker has NOT told us the last scan excluded
// nothing; it has told us nothing at all — the database predates the ledger,
// or no scan has ever run against it. Printing "excluded no files" there
// hands the operator a measurement where there is only an absence, which is
// the one thing this command exists not to do.
func printSkippedNothing(out io.Writer, r scanSkippedResult) {
	if !r.LedgerPresent {
		fmt.Fprintf(out,
			"No exclusion ledger has been recorded in this database (db: %s)\n", r.DBPath)
		if r.QueriedPath != "" {
			fmt.Fprintf(out, "  Cannot determine whether %s was excluded. "+
				"Run 'atlas scan' to record a ledger.\n", r.QueriedPath)
			return
		}
		fmt.Fprintln(out, "  This is not the same as a scan that excluded nothing. "+
			"Run 'atlas scan' to record a ledger.")
		return
	}
	when := ""
	if r.LedgerWrittenAt != "" {
		when = " (recorded " + r.LedgerWrittenAt + ")"
	}
	if r.QueriedPath != "" {
		// Deliberately not "it is indexed": the ledger records only what the
		// walk excluded, so a path it does not mention may equally have been
		// outside the scan root, deleted, or spelled for another tree.
		fmt.Fprintf(out,
			"%s is not in the exclusion ledger written by the last scan%s (db: %s)\n",
			r.QueriedPath, when, r.DBPath)
		fmt.Fprintln(out, "  The last scan did not exclude it. Whether it was indexed "+
			"is a separate question this ledger does not answer.")
		return
	}
	fmt.Fprintf(out, "The last scan excluded no files%s (db: %s)\n", when, r.DBPath)
}
