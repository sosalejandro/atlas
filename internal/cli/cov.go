package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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

// newCovCmd builds the `atlas cov` command group with sync + status
// subcommands.
func newCovCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cov",
		Short: "Test coverage ingestion + per-feature views",
		Long:  "cov groups the coverage-ingest (sync) and coverage-status verbs.",
	}
	cmd.AddCommand(newCovSyncCmd())
	cmd.AddCommand(newCovStatusCmd())
	return cmd
}

// newCovSyncCmd implements `atlas cov sync` — ingest one framework's test
// output into the SQLite store.
func newCovSyncCmd() *cobra.Command {
	var (
		framework string
		input     string
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Ingest a test framework's report into the Atlas store",
		Long: `cov sync parses a test-framework report and writes the resulting
run + per-test rows through the Coverage port.

Supported frameworks (--framework):
  go-test, playwright, vitest, jest, maestro

When --framework is omitted, cov sync attempts to auto-detect from the
filename (.json patterns from each framework) and the file's top-level
shape. Failing detection is fatal — pass --framework explicitly.

Input source: --input <path> (a file) or "-" / unset for stdin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCovSync(cmd, framework, input)
		},
	}
	cmd.Flags().StringVar(&framework, "framework", "",
		"framework tag (go-test|playwright|vitest|jest|maestro); auto-detected when omitted")
	cmd.Flags().StringVar(&input, "input", "-",
		"report file path, or '-' for stdin")
	return cmd
}

// covSyncResult is the JSON payload for `atlas cov sync`.
type covSyncResult struct {
	RunID     int64  `json:"run_id"`
	Framework string `json:"framework"`
	Input     string `json:"input,omitempty"`
}

func runCovSync(cmd *cobra.Command, framework, input string) error {
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

	parser, err := pickCovParser(framework)
	if err != nil {
		return err
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

	opts := coverage.IngestOptions{
		Framework: coverage.Framework(framework),
		Resolver:  s.Symbols(),
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
	var feature string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Per-feature coverage view from the latest coverage run",
		Long: `cov status pulls the most recent coverage run from the store and
summarises pass/fail/skip counts grouped by feature_id. With --feature
the output is filtered to one feature only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCovStatus(cmd, feature)
		},
	}
	cmd.Flags().StringVar(&feature, "feature", "",
		"restrict output to one feature id")
	return cmd
}

// covStatusResult is the JSON payload for `atlas cov status`.
type covStatusResult struct {
	RunID    int64                  `json:"run_id"`
	Features []covStatusFeatureRow  `json:"features"`
}

type covStatusFeatureRow struct {
	FeatureID *shared.FeatureID `json:"feature_id,omitempty"`
	Passed    int               `json:"passed"`
	Failed    int               `json:"failed"`
	Skipped   int               `json:"skipped"`
	Total     int               `json:"total"`
	PassRate  float64           `json:"pass_rate"`
}

func runCovStatus(cmd *cobra.Command, feature string) error {
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

	runs, err := s.Coverage().ListRuns(ctx, "")
	if err != nil {
		return fmt.Errorf("cov status: list runs: %w", err)
	}
	if len(runs) == 0 {
		return fmt.Errorf("cov status: no coverage runs in the store yet — run 'atlas cov sync' first")
	}
	latest := runs[0]
	for _, r := range runs[1:] {
		if r.FinishedAt.After(latest.FinishedAt) {
			latest = r
		}
	}

	results, err := s.Coverage().ListResults(ctx, latest.ID)
	if err != nil {
		return fmt.Errorf("cov status: list results %d: %w", latest.ID, err)
	}

	rows := aggregateCovStatus(results, feature)
	res := covStatusResult{RunID: latest.ID, Features: rows}
	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "cov.status",
			map[string]any{"feature": feature}, res, nil)
	}
	printCovStatusText(cmd, latest, rows)
	return nil
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

func printCovStatusText(cmd *cobra.Command, run store.CoverageRun, rows []covStatusFeatureRow) {
	fmt.Fprintf(cmd.OutOrStdout(), "Coverage run %d (%s, finished %s)\n",
		run.ID, run.Framework, run.FinishedAt.Format("2006-01-02 15:04:05"))
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
