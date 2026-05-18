package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/coverage/gotest"
	"github.com/sosalejandro/atlas/packages/coverage/jest"
	"github.com/sosalejandro/atlas/packages/coverage/maestro"
	"github.com/sosalejandro/atlas/packages/coverage/playwright"
	"github.com/sosalejandro/atlas/packages/coverage/vitest"
	"github.com/sosalejandro/atlas/packages/store"
)

// newScanCoverageCmd is a smoke-test helper for Phase 5. It parses a
// single framework-tagged report file (or stdin) and writes the resulting
// run + per-test rows through store.Coverage().InsertRunWithResults.
//
// The user-facing `atlas cov sync` verb lands in Phase 7 alongside the
// rest of the CLI surface; this command is intentionally narrow: it
// covers one framework per invocation and prints the new run id.
func newScanCoverageCmd() *cobra.Command {
	var (
		framework string
		dbPath    string
		input     string
	)
	cmd := &cobra.Command{
		Use:   "scan-coverage",
		Short: "Ingest a single framework's test result report into the Atlas SQLite store (smoke-test wiring for Phase 5).",
		Long: `scan-coverage parses one test-framework report and writes its run + per-test rows through the Coverage port.

Supported frameworks (per --framework):
  go-test, playwright, vitest, jest, maestro

Input source: --input <path> (a file) or "-" to read from stdin.

This command is a Phase 5 smoke-test helper; the production verb is "atlas cov sync" (Phase 7).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScanCoverage(cmd, framework, dbPath, input)
		},
	}
	cmd.Flags().StringVar(&framework, "framework", "", "framework tag (go-test|playwright|vitest|jest|maestro)")
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the Atlas state SQLite file")
	cmd.Flags().StringVar(&input, "input", "-", "report file path, or '-' for stdin")
	_ = cmd.MarkFlagRequired("framework")
	_ = cmd.MarkFlagRequired("db")
	return cmd
}

// runScanCoverage is the implementation of the scan-coverage command,
// hoisted out of the cobra closure so funlen stays under the project
// budget (60 statements).
func runScanCoverage(cmd *cobra.Command, framework, dbPath, input string) error {
	parser, err := pickParser(framework)
	if err != nil {
		return err
	}
	r, closeFn, err := openInput(input)
	if err != nil {
		return err
	}
	defer closeFn()

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer func() { _ = s.Close() }()

	opts := coverage.IngestOptions{
		Framework: coverage.Framework(framework),
		Resolver:  s.Symbols(),
	}
	if input != "-" && input != "" {
		absPath := input
		opts.RawPath = &absPath
	}
	id, err := coverage.Ingest(ctx, s, parser, r, opts)
	if err != nil {
		return fmt.Errorf("scan-coverage: %w", err)
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "coverage run id=%d framework=%s\n", id, framework); err != nil {
		return fmt.Errorf("scan-coverage: write output: %w", err)
	}
	return nil
}

func pickParser(fw string) (coverage.Parser, error) {
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
	case "":
		return nil, fmt.Errorf("--framework is required")
	default:
		return nil, fmt.Errorf("unknown framework %q (supported: go-test, playwright, vitest, jest, maestro)", fw)
	}
}

func openInput(path string) (io.Reader, func(), error) {
	if path == "" || path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}
