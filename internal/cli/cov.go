package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/coverage/gotest"
	"github.com/sosalejandro/atlas/packages/coverage/jest"
	"github.com/sosalejandro/atlas/packages/coverage/maestro"
	"github.com/sosalejandro/atlas/packages/coverage/playwright"
	"github.com/sosalejandro/atlas/packages/coverage/vitest"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// newCovCmd builds the `atlas cov` command group with sync, status and diff
// subcommands.
func newCovCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cov",
		Short: "Test coverage ingestion + per-feature views",
		Long: "cov groups the coverage-ingest (sync), coverage-status and " +
			"patch-coverage (diff) verbs.",
	}
	cmd.AddCommand(newCovSyncCmd())
	cmd.AddCommand(newCovStatusCmd())
	cmd.AddCommand(newCovDiffCmd())
	return cmd
}

// newCovSyncCmd implements `atlas cov sync` — ingest one framework's test
// output into the SQLite store.
func newCovSyncCmd() *cobra.Command {
	var (
		framework string
		input     string
		perTest   string
		runGroup  string
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Ingest a test framework's report into the Atlas store",
		Long: `cov sync parses a test-framework report and writes the resulting
run + per-test rows through the Coverage port.

Supported frameworks (--framework):
  go-test, go-cover, playwright, vitest, jest, maestro, istanbul

go-cover and istanbul are STATEMENT-coverage tracks (Tier B): go-cover
ingests a Go coverprofile (go test -coverprofile); istanbul ingests a
front-end coverage-final.json (vitest/jest v8 JSON reporter). Both
attribute line-weighted covered/total statements to the symbols that ran.

When --framework is omitted, cov sync attempts to auto-detect from the
filename (.json patterns from each framework) and the file's top-level
shape. Failing detection is fatal — pass --framework explicitly.

Input source: --input <path> (a file) or "-" / unset for stdin.

A polyglot repo measures itself more than once per build. Pass the SAME
--run-group to every sync of one build (a git SHA, a CI run id) and the
audit reads those runs as one coverage frontier; without it each sync
stands alone and the last one to land is the only one scored.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if perTest != "" {
				return runCovSyncPerTest(cmd, framework, perTest, runGroup)
			}
			return runCovSync(cmd, framework, input, runGroup)
		},
	}
	cmd.Flags().StringVar(&framework, "framework", "",
		"framework tag (go-test|go-cover|playwright|vitest|jest|maestro|istanbul); auto-detected when omitted")
	cmd.Flags().StringVar(&input, "input", "-",
		"report file path, or '-' for stdin")
	cmd.Flags().StringVar(&perTest, "per-test", "",
		"directory of per-test coverprofiles named <TestSymbol>.out; records which symbols each test executed (go-cover only)")
	cmd.Flags().StringVar(&runGroup, "run-group", "",
		"correlation key (a git SHA, a CI run id) tying this sync to the other frameworks measured in the same build; the audit then scores them as one frontier instead of letting the last sync win")
	return cmd
}

// runCovSyncPerTest ingests a directory of per-test coverprofiles.
//
// Convention: one file per test, named `<qualified test symbol>.out` — e.g.
// `billing.TestCheckout.out`. That is what a collection shim writes when it
// clears and dumps coverage counters around each test
// (`runtime/coverage.ClearCounters` + `WriteCountersDir`, Go 1.20+), and it
// keeps the ingest a pure function of the filesystem rather than of any one
// test framework's reporting format.
//
// The result is both the ordinary union run AND per-test evidence, so a store
// ingested this way is a strict superset of one ingested whole-run.
func runCovSyncPerTest(cmd *cobra.Command, framework, dir, runGroup string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if framework != "" && framework != "go-cover" {
		return fmt.Errorf("cov sync: --per-test is only supported for --framework go-cover (got %q)", framework)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("cov sync (per-test): read %s: %w", dir, err)
	}

	profiles := make([]coverage.PerTestProfile, 0, len(entries))
	closers := make([]io.Closer, 0, len(entries))
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".out") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("cov sync (per-test): open %s: %w", e.Name(), err)
		}
		closers = append(closers, f)
		profiles = append(profiles, coverage.PerTestProfile{
			Test:    shared.SymbolID(strings.TrimSuffix(e.Name(), ".out")),
			Profile: f,
		})
	}
	if len(profiles) == 0 {
		return fmt.Errorf("cov sync (per-test): no *.out profiles in %s", dir)
	}

	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("cov sync (per-test): open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	stats, err := coverage.IngestGoProfilePerTest(ctx, s, coverage.RunMeta{Framework: store.FrameworkGoTest, Group: runGroup}, profiles)
	if err != nil {
		return fmt.Errorf("cov sync (per-test): %w", err)
	}

	res := covSyncResult{RunID: stats.RunID, Framework: "go-cover", Input: dir}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "cov.sync",
			map[string]any{"framework": "go-cover", "per_test": dir}, res, nil)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out,
		"per-test ingest complete  run_id=%d tests=%d rows=%d symbols_executed=%d\n",
		stats.RunID, stats.TestsIngested, stats.Rows, stats.SymbolsExecuted)
	if n := len(stats.TestsUnresolved); n > 0 {
		fmt.Fprintf(out, "  %d test name(s) matched no indexed symbol\n", n)
		if flags.Verbose {
			for i, name := range stats.TestsUnresolved {
				if i == maxGapLines {
					fmt.Fprintf(out, "    ... +%d more\n", n-maxGapLines)
					break
				}
				fmt.Fprintf(out, "    %s\n", name)
			}
		}
	}
	renderAttributionGaps(cmd, covAttribution{
		StmtsUnattributed: stats.StmtsUnattributed,
		Gaps:              stats.Gaps,
	})
	return nil
}

// covSyncResult is the JSON payload for `atlas cov sync`.
type covSyncResult struct {
	RunID     int64  `json:"run_id"`
	Framework string `json:"framework"`
	Input     string `json:"input,omitempty"`

	// Attribution is populated for the statement-coverage frameworks
	// (go-cover, istanbul). It answers "how much of what ran did atlas
	// actually attribute?" — the question issue #85 was filed about.
	Attribution *covAttribution `json:"attribution,omitempty"`
}

// covAttribution reports how much of the ingested profile atlas could place
// on a symbol, and enumerates what it could not.
type covAttribution struct {
	FilesInProfile    int                `json:"files_in_profile"`
	FilesMatched      int                `json:"files_matched"`
	FilesUnmatched    int                `json:"files_unmatched"`
	StmtsAttributed   int                `json:"stmts_attributed"`
	StmtsUnattributed int                `json:"stmts_unattributed"`
	Gaps              []coverage.FileGap `json:"gaps,omitempty"`
}

// maxGapLines caps the human-readable gap list. --json always carries the
// full set; the terminal gets the biggest losses plus a "+N more" line.
const maxGapLines = 25

// renderAttributionGaps prints the attribution summary and, with --verbose,
// the per-file gap list. Silent when every statement was attributed.
func renderAttributionGaps(cmd *cobra.Command, a covAttribution) {
	if a.StmtsUnattributed == 0 && a.FilesUnmatched == 0 {
		return
	}
	out := cmd.OutOrStdout()
	total := a.StmtsAttributed + a.StmtsUnattributed
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(a.StmtsUnattributed) / float64(total)
	}
	fmt.Fprintf(out,
		"attribution gap: %d/%d statements (%.1f%%) in %d file(s) could not be charged to a symbol\n",
		a.StmtsUnattributed, total, pct, len(a.Gaps))
	if !flags.Verbose {
		fmt.Fprintf(out, "  re-run with --verbose (or --json) to list them\n")
		return
	}
	for i, g := range a.Gaps {
		if i == maxGapLines {
			fmt.Fprintf(out, "  ... +%d more\n", len(a.Gaps)-maxGapLines)
			break
		}
		fmt.Fprintf(out, "  %6d stmts  %-22s %s\n", g.Stmts, g.Reason, g.Path)
	}
}

func runCovSync(cmd *cobra.Command, framework, input, runGroup string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Resolve framework — explicit flag wins; otherwise sniff the filename.
	if framework == "" && input != "" && input != "-" {
		framework = sniffFramework(input)
	}
	if framework == "" {
		return fmt.Errorf("cov sync: --framework is required when input is stdin or auto-detection fails")
	}

	r, closeFn, err := openCovInput(input)
	if err != nil {
		return err
	}
	defer closeFn()

	dbPath, err := resolveDBPath(loaded, flags.DBPath)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("cov sync: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	// go-cover: a `go test -coverprofile` profile. Unlike the framework
	// parsers (which key results to TEST functions), this attributes REAL
	// production-code execution to the symbols that ran, via source span.
	// Persisted under the 'go-test' framework tag — a coverprofile IS
	// go-test coverage, and this avoids a CHECK-constraint migration.
	if framework == "go-cover" {
		stats, err := coverage.IngestGoProfile(ctx, s, coverage.RunMeta{Framework: store.FrameworkGoTest, Group: runGroup}, r)
		if err != nil {
			return fmt.Errorf("cov sync (go-cover): %w", err)
		}
		attr := covAttribution{
			FilesInProfile:    stats.FilesInProfile,
			FilesMatched:      stats.FilesMatched,
			FilesUnmatched:    stats.FilesUnmatched,
			StmtsAttributed:   stats.StmtsAttributed,
			StmtsUnattributed: stats.StmtsUnattributed,
			Gaps:              stats.Gaps,
		}
		res := covSyncResult{RunID: stats.RunID, Framework: framework, Input: input, Attribution: &attr}
		if flags.JSON {
			return emitJSON(stdoutOrJSON(cmd), "cov.sync",
				map[string]any{"framework": framework, "input": input}, res, nil)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"coverprofile ingest complete  run_id=%d blocks=%d files=%d/%d unmatched=%d symbols_executed=%d stmts=%d/%d\n",
			stats.RunID, stats.BlocksParsed, stats.FilesMatched, stats.FilesInProfile,
			stats.FilesUnmatched, stats.SymbolsExecuted,
			stats.StmtsAttributed, stats.StmtsAttributed+stats.StmtsUnattributed)
		renderAttributionGaps(cmd, attr)
		return nil
	}

	// istanbul: a front-end `coverage-final.json` (vitest/jest v8 JSON
	// reporter). Like go-cover this is a STATEMENT-coverage source, not a
	// pass/fail framework — it attributes line-weighted covered/total
	// statements to the FE symbols that ran (file_path apps/web-*/src/...).
	// Persisted under the 'vitest' framework tag — istanbul coverage IS
	// vitest/jest coverage, and this avoids a CHECK-constraint migration
	// (the same trick go-cover uses with the 'go-test' tag).
	if framework == string(coverage.FrameworkIstanbul) {
		stats, err := coverage.IngestIstanbul(ctx, s, coverage.RunMeta{Framework: store.FrameworkVitest, Group: runGroup}, r)
		if err != nil {
			return fmt.Errorf("cov sync (istanbul): %w", err)
		}
		attr := covAttribution{
			FilesInProfile:    stats.FilesInReport,
			FilesMatched:      stats.FilesMatched,
			FilesUnmatched:    stats.FilesUnmatched,
			StmtsAttributed:   stats.StmtsAttributed,
			StmtsUnattributed: stats.StmtsUnattributed,
			Gaps:              stats.Gaps,
		}
		res := covSyncResult{RunID: stats.RunID, Framework: framework, Input: input, Attribution: &attr}
		if flags.JSON {
			return emitJSON(stdoutOrJSON(cmd), "cov.sync",
				map[string]any{"framework": framework, "input": input}, res, nil)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"istanbul ingest complete  run_id=%d stmts=%d files=%d/%d symbols_covered=%d unmatched=%d\n",
			stats.RunID, stats.StmtsParsed, stats.FilesMatched, stats.FilesInReport, stats.SymbolsCovered, stats.FilesUnmatched)
		renderAttributionGaps(cmd, attr)
		return nil
	}

	parser, err := pickCovParser(framework)
	if err != nil {
		return err
	}

	opts := coverage.IngestOptions{
		Framework: coverage.Framework(framework),
		Resolver:  s.Symbols(),
		RunGroup:  runGroup,
	}
	if input != "-" && input != "" {
		p := input
		opts.RawPath = &p
	}

	id, err := coverage.Ingest(ctx, s, parser, r, opts)
	if err != nil {
		return fmt.Errorf("cov sync: %w", err)
	}

	res := covSyncResult{RunID: id, Framework: framework, Input: input}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "cov.sync",
			map[string]any{"framework": framework, "input": input}, res, nil)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"coverage ingest complete  run_id=%d framework=%s\n", id, framework)
	return nil
}

func pickCovParser(fw string) (coverage.Parser, error) {
	switch coverage.Framework(fw) {
	case coverage.FrameworkGoTest:
		return coverage.ParseFunc(gotest.Parse), nil
	case coverage.FrameworkPlaywright:
		return coverage.ParseFunc(playwright.Parse), nil
	case coverage.FrameworkVitest:
		return coverage.ParseFunc(vitest.Parse), nil
	case coverage.FrameworkJest:
		return coverage.ParseFunc(jest.Parse), nil
	case coverage.FrameworkMaestro:
		return coverage.ParseFunc(maestro.Parse), nil
	default:
		return nil, fmt.Errorf("unknown framework %q (supported: go-test, playwright, vitest, jest, maestro)", fw)
	}
}

func openCovInput(path string) (io.Reader, func(), error) {
	if path == "" || path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// sniffFramework guesses the framework from a file name. Best-effort —
// most CI scripts pass a friendly name like `playwright-results.json`,
// `vitest-output.json`, `gotest.json`. Returns "" when nothing matches;
// callers then surface a clear "framework required" error.
func sniffFramework(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case base == "coverage-final.json",
		strings.HasSuffix(base, "coverage-final.json"),
		strings.Contains(base, "istanbul"):
		return string(coverage.FrameworkIstanbul)
	case strings.HasSuffix(base, ".cover"),
		strings.HasSuffix(base, "cover.out"),
		strings.Contains(base, "coverprofile"),
		strings.Contains(base, "go-cover"):
		return "go-cover"
	case strings.Contains(base, "playwright"):
		return string(coverage.FrameworkPlaywright)
	case strings.Contains(base, "vitest"):
		return string(coverage.FrameworkVitest)
	case strings.Contains(base, "jest"):
		return string(coverage.FrameworkJest)
	case strings.Contains(base, "maestro"):
		return string(coverage.FrameworkMaestro)
	case strings.Contains(base, "gotest"),
		strings.Contains(base, "go-test"),
		strings.HasSuffix(base, ".gotest.json"):
		return string(coverage.FrameworkGoTest)
	}
	return ""
}

// --- cov status -----------------------------------------------------------

// newCovStatusCmd implements `atlas cov status [--feature <id>]` — show
// per-feature coverage counts from the latest coverage run.
func newCovStatusCmd() *cobra.Command {
	var opts covStatusOpts
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Per-feature coverage view from the current coverage frontier",
		Long: `cov status summarises pass/fail/skip counts grouped by feature_id
over the current coverage FRONTIER -- the same runs the audit scores. With
--feature the output is filtered to one feature only.

The frontier is resolved from the newest run outward: a run synced with
--run-group brings its whole group along, an ungrouped run stands alone.
So a polyglot build that tagged every sync shows one combined picture,
and one that did not shows only its last sync -- which is exactly what
the audit will score.

--group breaks the frontier down into the runs that compose it, which is
how you check that every framework in a build actually landed under the
same key.

--gaps additionally reports the frontier's ATTRIBUTION accounting: how
much of the coverage reports atlas could charge to a symbol, and which
files it could not. That is read back from the store, so the blind spot
is inspectable long after the ingest that measured it.

CARRYFORWARD (issue #136). A symbol that NO run in the frontier measured
-- because a CI job failed, timed out or was skipped -- would otherwise
drop out of the picture entirely, and coverage would go UP because
testing went DOWN. Instead the last build that did measure it stands in,
marked as carried. pass/fail/skip stay OBSERVED counts, so a CI gate
reading pass_rate keeps reading this build's own measurement; the carried
counts sit beside them. --carry=false restores the pre-#136 reading.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCovStatus(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.feature, "feature", "",
		"restrict output to one feature id")
	cmd.Flags().BoolVar(&opts.gaps, "gaps", false,
		"report the attribution accounting and the files whose execution could not be attributed")
	cmd.Flags().BoolVar(&opts.group, "group", false,
		"break the frontier down into the runs that compose it")
	cmd.Flags().BoolVar(&opts.carry, "carry", true,
		"stand in for symbols this build did not measure with the last build that did; --carry=false reads the frontier alone")
	cmd.Flags().IntVar(&opts.carryBuilds, "carry-builds", 0,
		fmt.Sprintf("how many builds back a carried measurement may come from and still count as evidence (0 = %d)", store.DefaultCarryBuilds))
	cmd.Flags().DurationVar(&opts.carryMaxAge, "carry-max-age", 0,
		fmt.Sprintf("wall-clock bound on the same window, for repos that build rarely (0 = %s)", store.DefaultCarryMaxAge))
	return cmd
}

// covStatusOpts is the flag set of `cov status`. Collected into a struct
// because the carry window added three more of them and a six-argument
// runCovStatus is a signature nobody can read a call to.
type covStatusOpts struct {
	feature     string
	gaps        bool
	group       bool
	carry       bool
	carryBuilds int
	carryMaxAge time.Duration
}

// covStatusResult is the JSON payload for `atlas cov status`.
type covStatusResult struct {
	// RunID is the run the frontier was resolved FROM -- the newest in the
	// store. It stays for compatibility with readers written before run
	// groups; RunIDs is the honest answer once a frontier can span runs.
	RunID  int64   `json:"run_id"`
	RunIDs []int64 `json:"run_ids"`
	// Group is the run group the frontier was read under, absent when the
	// newest run carried none and therefore stands alone.
	Group    *string               `json:"group,omitempty"`
	Runs     []covStatusRunRow     `json:"runs,omitempty"`
	Features []covStatusFeatureRow `json:"features"`

	// Attribution is populated by --gaps from what the ingest persisted on
	// the run (schema 0011). Absent when the flag is off; present-but-zeroed
	// never happens — a run that recorded no accounting is reported as such
	// rather than as a perfect 0-of-0 attribution.
	Attribution *covStatusAttribution `json:"attribution,omitempty"`

	// Carry reports what this build did not measure and had to inherit
	// (issue #136). Always present so a consumer can tell "carryforward was
	// off" from "carryforward found nothing to carry"; the two mean very
	// different things when a number looks suspiciously stable.
	Carry covStatusCarry `json:"carry"`
}

// covStatusCarry is the frontier-level carry accounting.
//
// The distinction between Evidence and DenominatorOnly is the answer to
// "does a carried result count as covered?". Evidence carries are recent and
// span-verified and stand in for this build's reading; denominator-only
// carries are too old or belong to a symbol whose span moved, so they hold
// the symbol's place with zero credit. Neither is an observed measurement,
// which is why the per-feature pass/fail/skip counts stay observed-only.
type covStatusCarry struct {
	Enabled   bool   `json:"enabled"`
	MaxBuilds int    `json:"max_builds"`
	MaxAge    string `json:"max_age"`

	Results         int `json:"results"`
	Evidence        int `json:"evidence"`
	DenominatorOnly int `json:"denominator_only"`

	// Sources names the builds the carried results came from, so a reader can
	// go and look at why those builds are still supplying this one.
	Sources []covStatusCarrySource `json:"sources,omitempty"`
}

// covStatusCarrySource is one build that this frontier is still borrowing
// from, and how far back it is.
type covStatusCarrySource struct {
	Group      string `json:"group"`
	BuildsBack int    `json:"builds_back"`
	Results    int    `json:"results"`
}

// covStatusAttribution is the persisted answer to "how much of what ran can
// atlas actually see?" — the run-level counters plus the per-file enumeration
// behind them. This is what a CI gate ("fail if unattributed > 10%") reads.
type covStatusAttribution struct {
	Recorded          bool `json:"recorded"`
	FilesInReport     int  `json:"files_in_report"`
	FilesMatched      int  `json:"files_matched"`
	FilesUnmatched    int  `json:"files_unmatched"`
	StmtsAttributed   int  `json:"stmts_attributed"`
	StmtsUnattributed int  `json:"stmts_unattributed"`
	// GapsTruncated is how many gap files did not fit the store's per-run
	// cap. The statement totals above stay exact regardless, so a non-zero
	// value narrows the enumeration, never the accounting.
	GapsTruncated int                 `json:"gaps_truncated"`
	Gaps          []store.CoverageGap `json:"gaps"`
}

// covStatusRunRow is one run of the frontier, emitted by --group. It answers
// "did every framework in this build land under the same key?", which is the
// one question a mis-tagged CI job makes urgent.
type covStatusRunRow struct {
	RunID      int64     `json:"run_id"`
	Framework  string    `json:"framework"`
	FinishedAt time.Time `json:"finished_at"`
	Results    int       `json:"results"`
}

// covStatusFeatureRow is one feature's rollup.
//
// Passed/Failed/Skipped/Total/PassRate are OBSERVED counts -- this build's own
// measurement, unchanged by carryforward. That is deliberate: a CI gate reads
// pass_rate, and a gate exists to catch the build that stopped measuring, so a
// carry that satisfied the gate would defeat it. Carried/CarriedEvidence sit
// beside them and say how much of the feature's picture came from an earlier
// build instead.
type covStatusFeatureRow struct {
	FeatureID *shared.FeatureID `json:"feature_id,omitempty"`
	Passed    int               `json:"passed"`
	Failed    int               `json:"failed"`
	Skipped   int               `json:"skipped"`
	Total     int               `json:"total"`
	PassRate  float64           `json:"pass_rate"`

	Carried         int `json:"carried,omitempty"`
	CarriedEvidence int `json:"carried_evidence,omitempty"`
}

func runCovStatus(cmd *cobra.Command, opts covStatusOpts) error {
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
		return fmt.Errorf("cov status: open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	// Read the frontier rather than the newest row: status that summarised a
	// different set of runs than the audit scores would be reporting on
	// something no other command acts on.
	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		return fmt.Errorf("cov status: resolve frontier: %w", err)
	}
	if frontier.Empty() {
		return fmt.Errorf("cov status: no coverage runs in the store yet - run 'atlas cov sync' first")
	}

	// Resolve through the carry port, not ListFrontierResults, for the same
	// reason status reads the frontier at all: it has to summarise what the
	// audit scores. The audit carries; status that did not would disagree with
	// it exactly on the builds where the disagreement matters.
	resolved, err := s.CoverageCarry().Resolve(ctx, frontier, store.CarryOptions{
		Disabled:  !opts.carry,
		MaxBuilds: opts.carryBuilds,
		MaxAge:    opts.carryMaxAge,
	})
	if err != nil {
		return fmt.Errorf("cov status: resolve frontier results: %w", err)
	}

	rows := aggregateCovStatus(resolved.Observed, opts.feature)
	rows = applyCovCarryCounts(rows, resolved, opts.feature)
	res := covStatusResult{
		RunID:    frontier.Newest,
		RunIDs:   frontier.RunIDs(),
		Group:    frontier.Group,
		Features: rows,
		Carry:    covCarrySummary(resolved, opts.carry),
	}
	if opts.group {
		res.Runs = frontierRunRows(frontier, resolved.Observed)
	}
	if opts.gaps {
		attr, err := loadCovAttribution(ctx, s, frontier)
		if err != nil {
			return err
		}
		res.Attribution = &attr
	}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "cov.status", map[string]any{
			"feature": opts.feature, "gaps": opts.gaps, "group": opts.group,
			"carry": opts.carry, "carry_builds": opts.carryBuilds,
			"carry_max_age": opts.carryMaxAge.String(),
		}, res, nil)
	}
	printCovStatusText(cmd, frontier, rows)
	printCovCarry(cmd, res.Carry)
	if res.Runs != nil {
		printCovFrontierRuns(cmd, res.Runs)
	}
	if res.Attribution != nil {
		printCovAttribution(cmd, frontier, *res.Attribution)
	}
	return nil
}

// applyCovCarryCounts folds the carried results into the per-feature rows
// WITHOUT touching the observed counts.
//
// A feature whose every symbol went unmeasured has no observed row at all, so
// the carried results have to be able to create one; otherwise the build in
// which a whole language's job died would show a shorter feature list rather
// than a list of features nobody measured, and "shorter list" is not something
// anyone reads as a problem.
func applyCovCarryCounts(
	rows []covStatusFeatureRow,
	resolved store.ResolvedCoverage,
	filter string,
) []covStatusFeatureRow {
	if len(resolved.Carried) == 0 {
		return rows
	}
	index := make(map[shared.FeatureID]int, len(rows))
	for i, r := range rows {
		var fid shared.FeatureID
		if r.FeatureID != nil {
			fid = *r.FeatureID
		}
		index[fid] = i
	}
	for _, c := range resolved.Carried {
		var fid shared.FeatureID
		if c.FeatureID != nil {
			fid = *c.FeatureID
		}
		if filter != "" && string(fid) != filter {
			continue
		}
		i, ok := index[fid]
		if !ok {
			row := covStatusFeatureRow{}
			if fid != "" {
				id := fid
				row.FeatureID = &id
			}
			rows = append(rows, row)
			i = len(rows) - 1
			index[fid] = i
		}
		rows[i].Carried++
		if c.Mode == store.CarryEvidence {
			rows[i].CarriedEvidence++
		}
	}
	return rows
}

// covCarrySummary rolls the carries up to the frontier level, including the
// window that produced them: a bound whose value is invisible is a bound
// nobody can reason about when a number looks wrong.
func covCarrySummary(resolved store.ResolvedCoverage, enabled bool) covStatusCarry {
	out := covStatusCarry{
		Enabled:   enabled,
		MaxBuilds: resolved.Window.MaxBuilds,
		MaxAge:    resolved.Window.MaxAge.String(),
		Results:   len(resolved.Carried),
	}
	bySource := map[string]*covStatusCarrySource{}
	for _, c := range resolved.Carried {
		if c.Mode == store.CarryEvidence {
			out.Evidence++
		} else {
			out.DenominatorOnly++
		}
		src := bySource[c.FromGroup]
		if src == nil {
			src = &covStatusCarrySource{Group: c.FromGroup, BuildsBack: c.BuildsBack}
			bySource[c.FromGroup] = src
		}
		src.Results++
	}
	for _, src := range bySource {
		out.Sources = append(out.Sources, *src)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Group < out.Sources[j].Group })
	return out
}

// printCovCarry states the inheritance in the terminal. It prints even when
// nothing was carried, because "this build measured everything it was asked
// to" is the reassuring half of the same fact.
func printCovCarry(cmd *cobra.Command, c covStatusCarry) {
	out := cmd.OutOrStdout()
	if !c.Enabled {
		fmt.Fprintln(out, "carryforward: off (--carry=false); symbols this build did not measure are simply absent")
		return
	}
	if c.Results == 0 {
		fmt.Fprintln(out, "carryforward: nothing carried; every symbol in the picture was measured by this build")
		return
	}
	fmt.Fprintf(out,
		"carryforward: %d result(s) carried (%d as evidence, %d holding the denominator only), window %d builds / %s\n",
		c.Results, c.Evidence, c.DenominatorOnly, c.MaxBuilds, c.MaxAge)
	for _, src := range c.Sources {
		fmt.Fprintf(out, "  %d from build %q (%d build(s) back)\n", src.Results, src.Group, src.BuildsBack)
	}
	fmt.Fprintln(out,
		"  pass/fail/skip above are OBSERVED counts - a CI gate should read those, not the carried ones")
}

// frontierRunRows counts each run's contribution to the pooled result set, so
// a run that landed in the group but carried nothing is visible as such
// rather than merely absent.
func frontierRunRows(f store.CoverageFrontier, results []store.CoverageResult) []covStatusRunRow {
	counts := make(map[int64]int, len(f.Runs))
	for _, r := range results {
		counts[r.RunID]++
	}
	rows := make([]covStatusRunRow, 0, len(f.Runs))
	for _, r := range f.Runs {
		rows = append(rows, covStatusRunRow{
			RunID:      r.ID,
			Framework:  string(r.Framework),
			FinishedAt: r.FinishedAt,
			Results:    counts[r.ID],
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RunID < rows[j].RunID })
	return rows
}

func printCovFrontierRuns(cmd *cobra.Command, rows []covStatusRunRow) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "frontier runs (%d):\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(out, "  run %-6d %-12s %s  results=%d\n",
			r.RunID, r.Framework, r.FinishedAt.UTC().Format(time.RFC3339), r.Results)
	}
}

// loadCovAttribution reads the frontier's persisted attribution accounting.
// The counters live on each run row; the per-file enumeration behind them is
// a separate read per run, because a run with a large blind spot can carry
// hundreds of gap rows that the default view never wants.
//
// Across a frontier the counters SUM. Each run accounts for its own report,
// and the reports do not overlap -- go-cover measures Go files, istanbul
// measures the front end -- so the sum is the build's total blind spot, which
// is the number a CI gate wants. Runs carrying no accounting contribute
// nothing and cannot dilute it.
func loadCovAttribution(ctx context.Context, s *store.Store, f store.CoverageFrontier) (covStatusAttribution, error) {
	var out covStatusAttribution
	for _, run := range f.Runs {
		// A run predating schema 0011, or one from a framework with no
		// statement coverage at all, leaves every counter at zero. Reporting
		// that as 0-of-0 attributed would read as "nothing was lost", which
		// is the opposite of what it means.
		if run.FilesInReport == 0 && run.StmtsAttributed == 0 && run.StmtsUnattributed == 0 {
			continue
		}
		rows, err := s.CoverageGaps().List(ctx, run.ID)
		if err != nil {
			return covStatusAttribution{}, fmt.Errorf("cov status: list gaps %d: %w", run.ID, err)
		}
		out.Recorded = true
		out.FilesInReport += run.FilesInReport
		out.FilesMatched += run.FilesMatched
		out.FilesUnmatched += run.FilesUnmatched
		out.StmtsAttributed += run.StmtsAttributed
		out.StmtsUnattributed += run.StmtsUnattributed
		out.GapsTruncated += run.GapsTruncated
		out.Gaps = append(out.Gaps, rows...)
	}
	if out.Gaps == nil {
		out.Gaps = []store.CoverageGap{}
	}
	// Largest loss first: the enumeration is capped in the terminal, so the
	// rows a reader actually sees must be the ones that cost the most.
	sort.SliceStable(out.Gaps, func(i, j int) bool { return out.Gaps[i].Stmts > out.Gaps[j].Stmts })
	return out, nil
}

// printCovAttribution renders the accounting under the feature rollup. The
// terminal list is capped at maxGapLines; --json carries every stored row.
func printCovAttribution(cmd *cobra.Command, f store.CoverageFrontier, a covStatusAttribution) {
	out := cmd.OutOrStdout()
	if !a.Recorded {
		fmt.Fprintf(out,
			"frontier %v has no attribution metadata on any run (ingested before "+
				"schema 0011, or by a framework without statement coverage)\n",
			f.RunIDs())
		return
	}
	total := a.StmtsAttributed + a.StmtsUnattributed
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(a.StmtsUnattributed) / float64(total)
	}
	fmt.Fprintf(out,
		"attribution: %d/%d statements (%.1f%%) unattributed, files %d/%d matched\n",
		a.StmtsUnattributed, total, pct, a.FilesMatched, a.FilesInReport)
	if a.GapsTruncated > 0 {
		fmt.Fprintf(out,
			"  %d gap file(s) stored; %d more were dropped at the store's cap of %d\n",
			len(a.Gaps), a.GapsTruncated, store.MaxRunGapRows)
	}
	for i, g := range a.Gaps {
		if i == maxGapLines {
			fmt.Fprintf(out, "  ... +%d more\n", len(a.Gaps)-maxGapLines)
			break
		}
		fmt.Fprintf(out, "  %6d stmts  %-22s %s\n", g.Stmts, g.Reason, g.Path)
	}
}

func aggregateCovStatus(rs []store.CoverageResult, filter string) []covStatusFeatureRow {
	type acc struct {
		pass, fail, skip int
	}
	bucket := map[shared.FeatureID]*acc{}
	keyNil := acc{}
	for _, r := range rs {
		// Determine the feature key for this result.
		var fid shared.FeatureID
		if r.FeatureID != nil {
			fid = *r.FeatureID
		}
		if filter != "" && string(fid) != filter {
			continue
		}
		a := bucket[fid]
		if a == nil {
			a = &acc{}
			bucket[fid] = a
		}
		switch r.Status {
		case store.StatusPass:
			a.pass++
		case store.StatusFail:
			a.fail++
		case store.StatusSkip:
			a.skip++
		}
		_ = keyNil
	}
	out := make([]covStatusFeatureRow, 0, len(bucket))
	for fid, a := range bucket {
		total := a.pass + a.fail + a.skip
		rate := 0.0
		if total > 0 {
			rate = float64(a.pass) / float64(total)
		}
		row := covStatusFeatureRow{
			Passed: a.pass, Failed: a.fail, Skipped: a.skip,
			Total: total, PassRate: rate,
		}
		if fid != "" {
			id := fid
			row.FeatureID = &id
		}
		out = append(out, row)
	}
	return out
}

func printCovStatusText(cmd *cobra.Command, f store.CoverageFrontier, rows []covStatusFeatureRow) {
	if f.Group != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Coverage frontier %q (%d runs, newest %d)\n",
			*f.Group, len(f.Runs), f.Newest)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Coverage run %d (ungrouped)\n", f.Newest)
	}
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  (no results)")
		return
	}
	for _, r := range rows {
		name := "<unassigned>"
		if r.FeatureID != nil {
			name = string(*r.FeatureID)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"  %-40s  pass=%d fail=%d skip=%d  (%.0f%%)\n",
			name, r.Passed, r.Failed, r.Skipped, r.PassRate*100)
	}
}
