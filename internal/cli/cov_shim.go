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
	"github.com/sosalejandro/atlas/packages/coverage/shim/runner"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// registerCovShimCmds attaches the per-test collection verbs to the existing
// `cov` group. They are registered from the root rather than from inside
// newCovCmd so that the pair — and the doc comments explaining what they do
// to someone's test suite — stays in one file next to nothing else.
func registerCovShimCmds(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == "cov" {
			c.AddCommand(newCovShimCmd())
			c.AddCommand(newCovRunCmd())
			return
		}
	}
}

// --- cov shim init --------------------------------------------------------

// newCovShimCmd builds `atlas cov shim`, whose only verb is init.
func newCovShimCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shim",
		Short: "Manage the per-test coverage collection shim",
		Long: `shim manages the generated TestMain that lets a Go suite emit one
coverage snapshot per test instead of one per process.

The Go runtime writes coverage counters once, at exit, so nothing in a
plain 'go test' run says which test executed which line. The shim clears
and snapshots the counters around each test (runtime/coverage, Go 1.20+),
which is what 'atlas cov sync --per-test' ingests.`,
	}
	cmd.AddCommand(newCovShimInitCmd())
	return cmd
}

func newCovShimInitCmd() *cobra.Command {
	var packages []string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write the TestMain shim into the selected packages",
		Long: `shim init writes ` + runner.ShimFileName + ` into every selected package that
has tests and no TestMain of its own.

It is idempotent: a second run reports 'unchanged' and rewrites nothing.
Two kinds of package are left alone, and both are reported:

  - one that already declares TestMain — rewriting a suite's process
    entry point is not something a tool should do quietly. Opt in by
    hand instead: call shim.Run(m) from the TestMain you have.
  - one with no test functions, where a TestMain would only make the go
    tool build a test binary with nothing to run.

The generated file is inert unless ATLAS_COV_DIR is set, so committing it
does not change what 'go test' does.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCovShimInit(cmd, packages)
		},
	}
	cmd.Flags().StringSliceVar(&packages, "package", []string{"./..."},
		"package patterns to write the shim into")
	return cmd
}

// covShimInitResult is the JSON payload for `atlas cov shim init`.
type covShimInitResult struct {
	Packages []runner.InitResult `json:"packages"`
}

func runCovShimInit(cmd *cobra.Command, patterns []string) error {
	ctx := cmdContext(cmd)
	pkgs, err := runner.ListPackages(ctx, loaded.repoRoot, patterns)
	if err != nil {
		return fmt.Errorf("cov shim init: %w", err)
	}

	results := make([]runner.InitResult, 0, len(pkgs))
	for _, p := range pkgs {
		res, err := runner.InitDir(p.Dir)
		if err != nil {
			return fmt.Errorf("cov shim init: %w", err)
		}
		res.Dir = p.ImportPath
		results = append(results, res)
	}

	if flags.JSON {
		return emitJSON(stdoutOrJSON(cmd), "cov.shim.init",
			map[string]any{"package": patterns}, covShimInitResult{Packages: results}, nil)
	}
	printShimInit(cmd.OutOrStdout(), results)
	return nil
}

func printShimInit(out io.Writer, results []runner.InitResult) {
	counts := map[runner.InitStatus]int{}
	for _, r := range results {
		counts[r.Status]++
		switch {
		case r.Status == runner.StatusUnchanged && !flags.Verbose:
			continue
		case r.Reason != "":
			fmt.Fprintf(out, "  %-9s %s  (%s)\n", r.Status, r.Dir, r.Reason)
		default:
			fmt.Fprintf(out, "  %-9s %s\n", r.Status, r.Dir)
		}
	}
	fmt.Fprintf(out, "shim init: %d created, %d updated, %d unchanged, %d skipped\n",
		counts[runner.StatusCreated], counts[runner.StatusUpdated],
		counts[runner.StatusUnchanged], counts[runner.StatusSkipped])
	if counts[runner.StatusCreated]+counts[runner.StatusUpdated] > 0 {
		fmt.Fprintf(out, "the shim imports %s — run 'go get github.com/sosalejandro/atlas' if your module does not require it yet\n",
			shimImportPath)
	}
}

// shimImportPath is what the generated file imports. Named here only for
// the hint above; the generator owns the source it writes.
const shimImportPath = "github.com/sosalejandro/atlas/packages/coverage/shim"

// --- cov run --------------------------------------------------------------

type covRunFlags struct {
	packages  []string
	command   []string
	coverPkg  string
	out       string
	work      string
	serialize bool
	keep      bool
	noIngest  bool
	runGroup  string
}

func newCovRunCmd() *cobra.Command {
	var f covRunFlags
	cmd := &cobra.Command{
		Use:   "run [flags] -- <command>",
		Short: "Run a test suite with per-test coverage collection and ingest it",
		Long: `run executes a test suite with the shim armed, turns the counter
snapshots it leaves behind into one coverprofile per test, and ingests
them as per-test evidence (the same rows 'cov sync --per-test' writes).

  atlas cov run -- go test ./...

A 'go test' command is given the flags per-test collection cannot work
without, unless it already carries them:

  -covermode=atomic   the only counter mode the runtime will clear
  -coverpkg=./...     without it a test only appears to execute its own
                      package, which makes cross-package attribution — the
                      entire point — impossible
  -count=1            a cached test result runs no binary, so it writes
                      no counters

Any other command is run verbatim.

Packages whose tests call t.Parallel() are collected per PACKAGE, not per
test, and the degradation is reported: collecting them one test at a time
would silently take away the concurrency those tests asked for. Pass
--serialize to spend that concurrency and get the finer grain anyway.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.command = args
			return runCovRun(cmd, f)
		},
	}
	cmd.Flags().StringSliceVar(&f.packages, "package", []string{"./..."},
		"package patterns to analyse when planning collection")
	cmd.Flags().StringVar(&f.coverPkg, "coverpkg", "./...",
		"-coverpkg value injected into a 'go test' command that has none")
	cmd.Flags().StringVar(&f.out, "out", "",
		"also write the per-test profiles here, named <test symbol>.out for 'cov sync --per-test'")
	cmd.Flags().StringVar(&f.work, "work", "",
		"directory for counter snapshots (a temporary one by default)")
	cmd.Flags().BoolVar(&f.serialize, "serialize", false,
		"collect packages with t.Parallel() tests per test anyway, one test at a time")
	cmd.Flags().BoolVar(&f.keep, "keep", false,
		"keep the counter snapshots after the run")
	cmd.Flags().BoolVar(&f.noIngest, "no-ingest", false,
		"collect and convert only; do not write to the store")
	cmd.Flags().StringVar(&f.runGroup, "run-group", "",
		"correlation key tying this run to the other frameworks measured in the same build; the audit then scores them as one frontier")
	return cmd
}

// covRunResult is the JSON payload for `atlas cov run`.
type covRunResult struct {
	Command   []string             `json:"command"`
	ExitCode  int                  `json:"exit_code"`
	Profiles  int                  `json:"profiles"`
	Degraded  []runner.Degradation `json:"degraded,omitempty"`
	Work      string               `json:"work,omitempty"`
	RunID     int64                `json:"run_id,omitempty"`
	Tests     int                  `json:"tests_ingested,omitempty"`
	Rows      int                  `json:"rows,omitempty"`
	Symbols   int                  `json:"symbols_executed,omitempty"`
	Unmatched []shared.SymbolID    `json:"tests_unresolved,omitempty"`
}

func runCovRun(cmd *cobra.Command, f covRunFlags) error {
	ctx := cmdContext(cmd)
	command := f.command
	if len(command) == 0 {
		command = []string{"go", "test", "./..."}
	}
	// A work directory the caller named is theirs to keep, whether or not
	// they also passed --keep.
	kept := f.keep || f.work != ""

	res, err := runner.Run(ctx, runner.Options{
		Dir:       loaded.repoRoot,
		Packages:  f.packages,
		Command:   command,
		Work:      f.work,
		CoverPkg:  f.coverPkg,
		Serialize: f.serialize,
		Keep:      kept,
		Stdout:    cmd.ErrOrStderr(), // the suite's own output is not this command's result
		Stderr:    cmd.ErrOrStderr(),
	})
	if err != nil {
		return fmt.Errorf("cov run: %w", err)
	}
	// The profiles live under the work directory, so it survives exactly
	// as long as this function needs them.
	defer res.Cleanup()

	out := covRunResult{
		Command:  res.Command,
		ExitCode: res.ExitCode,
		Profiles: len(res.Profiles),
		Degraded: res.Degraded,
	}
	if kept {
		out.Work = res.Work
	}
	if err := consumeProfiles(ctx, cmd, res, f, &out); err != nil {
		return err
	}

	if flags.JSON {
		if err := emitJSON(stdoutOrJSON(cmd), "cov.run",
			map[string]any{"command": command, "serialize": f.serialize}, out, res.Warnings); err != nil {
			return err
		}
	} else {
		printCovRun(cmd, out, res)
	}
	// A suite that failed must fail this command too: `atlas cov run -- go
	// test ./...` stands in for `go test ./...` in a CI script, and a green
	// wrapper around a red suite is the worst thing it could be. The
	// evidence collected before the failure is already ingested.
	if res.ExitCode != 0 {
		return fmt.Errorf("cov run: the suite failed (exit %d)", res.ExitCode)
	}
	return nil
}

// consumeProfiles names each per-test profile after the test symbol it
// belongs to, then writes it where the flags asked: into the store, into
// --out, or both.
//
// The naming is candidate-based rather than assumed because the scanner
// gives a test the id "<package clause>.<TestName>" only while that short
// name is free; a name that collided is indexed package-qualified instead.
// A test that matches neither is still handed to the ingest under its
// primary candidate, so it comes back in TestsUnresolved rather than
// disappearing.
func consumeProfiles(ctx context.Context, cmd *cobra.Command, res runner.Result, f covRunFlags, out *covRunResult) error {
	// The store answers both questions — what is this test's symbol id, and
	// where does the evidence go — so it is opened once for both, and not at
	// all when neither is being asked.
	var s *store.Store
	if !f.noIngest {
		dbPath, err := resolveDBPath(loaded, flags.DBPath)
		if err != nil {
			return err
		}
		s, err = store.Open(ctx, dbPath)
		if err != nil {
			return fmt.Errorf("cov run: open store %s: %w", dbPath, err)
		}
		defer func() { _ = s.Close() }()
	}

	pkgs := map[string]runner.Package{}
	for _, p := range res.Packages {
		pkgs[p.ImportPath] = p
	}

	profiles := make([]coverage.PerTestProfile, 0, len(res.Profiles))
	closers := make([]io.Closer, 0, len(res.Profiles))
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	for _, p := range res.Profiles {
		if p.Test == "" {
			continue // a degraded package's package-wide profile: no test to charge it to.
		}
		id := resolveTestSymbol(ctx, s, pkgs[p.Package], p.Test)
		if f.out != "" {
			if err := copyProfile(p.Path, filepath.Join(f.out, string(id)+".out")); err != nil {
				return fmt.Errorf("cov run: %w", err)
			}
		}
		if s == nil {
			continue
		}
		file, err := os.Open(p.Path)
		if err != nil {
			return fmt.Errorf("cov run: open profile %s: %w", p.Path, err)
		}
		closers = append(closers, file)
		profiles = append(profiles, coverage.PerTestProfile{Test: id, Profile: file})
	}
	if s == nil {
		return nil
	}
	if len(profiles) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"cov run: no per-test profiles to ingest — has 'atlas cov shim init' been run for these packages?")
		return nil
	}

	stats, err := coverage.IngestGoProfilePerTest(ctx, s, coverage.RunMeta{Framework: store.FrameworkGoTest, Group: f.runGroup}, profiles)
	if err != nil {
		return fmt.Errorf("cov run: %w", err)
	}
	out.RunID = stats.RunID
	out.Tests = stats.TestsIngested
	out.Rows = stats.Rows
	out.Symbols = stats.SymbolsExecuted
	out.Unmatched = stats.TestsUnresolved
	return nil
}

// resolveTestSymbol picks the id the scanner would have indexed this test
// under, preferring one that actually exists in the store. A nil store — no
// ingest was asked for — means the primary candidate, unverified.
func resolveTestSymbol(ctx context.Context, s *store.Store, pkg runner.Package, test string) shared.SymbolID {
	primary := shared.SymbolID(test)
	if pkg.Clause != "" {
		primary = shared.SymbolID(pkg.Clause + "." + test)
	}
	candidates := []shared.SymbolID{primary}
	if rel, err := filepath.Rel(loaded.repoRoot, pkg.Dir); err == nil && !strings.HasPrefix(rel, "..") {
		candidates = append(candidates, shared.SymbolID(filepath.ToSlash(rel)+"."+test))
	}
	if s == nil {
		return primary
	}
	for _, c := range candidates {
		if _, err := s.Symbols().FindByQualifiedName(ctx, c); err == nil {
			return c
		}
	}
	return primary
}

func copyProfile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(to), err)
	}
	b, err := os.ReadFile(from)
	if err != nil {
		return fmt.Errorf("read %s: %w", from, err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", to, err)
	}
	return nil
}

func printCovRun(cmd *cobra.Command, out covRunResult, res runner.Result) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "cov run: %s\n", strings.Join(out.Command, " "))
	fmt.Fprintf(w, "  %d per-test profile(s) from %d package(s), suite exit %d\n",
		out.Profiles, len(res.Reports), out.ExitCode)
	if out.RunID != 0 {
		fmt.Fprintf(w, "  ingested run_id=%d tests=%d rows=%d symbols_executed=%d\n",
			out.RunID, out.Tests, out.Rows, out.Symbols)
	}
	if n := len(out.Unmatched); n > 0 {
		fmt.Fprintf(w, "  %d test name(s) matched no indexed symbol — re-run 'atlas scan' if the graph is stale\n", n)
		if flags.Verbose {
			for _, name := range out.Unmatched {
				fmt.Fprintf(w, "    %s\n", name)
			}
		}
	}
	for _, d := range out.Degraded {
		fmt.Fprintf(w, "  degraded (%s) %s: %s\n", d.Source, d.Package, d.Reason)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "  warning: %s\n", warn)
	}
	if out.Work != "" {
		fmt.Fprintf(w, "  counter snapshots kept in %s\n", out.Work)
	}
}

// cmdContext is cmd.Context() with the never-nil guarantee the RunE bodies
// in this package all assume.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
