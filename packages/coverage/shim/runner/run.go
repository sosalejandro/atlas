package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/coverage/shim"
)

// Package is one Go package the suite covers, as `go list` reports it, plus
// the package clause its tests are declared in — which is what a test
// symbol's id is built from, and which is "<pkg>_test" for a suite that
// lives in the external test package.
type Package struct {
	ImportPath string `json:"import_path"`
	Dir        string `json:"dir"`
	Clause     string `json:"clause,omitempty"`
}

// Degradation is one package that could not be collected per test, and why.
// It is a first-class result rather than a log line: a feature surface
// derived from a suite with silent holes in it is worse than no surface.
type Degradation struct {
	Package string `json:"package"`
	Reason  string `json:"reason"`
	// Source is "plan" when atlas decided before the run (a static read of
	// the tests) and "runtime" when the shim decided during it.
	Source string `json:"source"`
}

// Options configure a collection run.
type Options struct {
	// Dir is the module directory the command runs in.
	Dir string
	// Packages are the patterns whose tests are analysed for the plan.
	// Defaults to ./... — note this is independent of the packages the
	// command itself runs, which atlas does not try to parse out of it.
	Packages []string
	// Command is the suite command, e.g. ["go", "test", "./..."].
	Command []string
	// Work is the counter root. A temporary directory when empty, removed
	// again unless Keep is set.
	Work string
	// Out is where coverprofiles are written. Defaults to <Work>/profiles.
	Out string
	// CoverPkg is the -coverpkg value injected when the command has none.
	// The default (./...) is what makes cross-package attribution possible:
	// without it a test only ever appears to execute its own package.
	CoverPkg string
	// Serialize collects packages whose tests call t.Parallel() per test
	// anyway, spending their concurrency to get the finer grain.
	Serialize bool
	// Keep leaves Work in place after the run.
	Keep bool

	Stdout io.Writer
	Stderr io.Writer
}

// Result is what a collection run produced.
type Result struct {
	// Command is what actually ran, coverage flags included.
	Command []string
	// ExitCode is the suite's exit code. Non-zero means tests failed; the
	// evidence collected up to that point is still returned, because a
	// failing suite is exactly when you want to know what ran.
	ExitCode int
	Profiles []Profile
	Reports  []shim.Report
	Degraded []Degradation
	// Packages are the analysed packages, each carrying the package clause
	// its tests are declared in.
	Packages []Package
	Warnings []string
	// Work is the counter root, whether or not it survives the call.
	Work string
	// Cleanup removes a temporary work directory, the profiles under it
	// included. Always non-nil, and a no-op when the caller supplied Work
	// or asked to Keep it. Call it once the profiles have been read: they
	// live under Work, so Run cannot tidy up behind itself.
	Cleanup func()
}

// planFileName is the plan the shim reads out of the work directory.
const planFileName = "plan.json"

// Run analyses the packages, runs the suite with the shim armed, and turns
// the counter snapshots into coverprofiles.
func Run(ctx context.Context, opts Options) (Result, error) {
	if len(opts.Command) == 0 {
		return Result{}, errors.New("runner: no command to run")
	}
	work, cleanup := opts.Work, func() {}
	if work == "" {
		dir, err := os.MkdirTemp("", "atlas-cov-")
		if err != nil {
			return Result{}, fmt.Errorf("runner: create work dir: %w", err)
		}
		work = dir
		if !opts.Keep {
			cleanup = func() { _ = os.RemoveAll(dir) }
		}
	}
	// Every early return below this point is a failure, and a failed run's
	// snapshots are of no use to anyone.
	failed := true
	defer func() {
		if failed {
			cleanup()
		}
	}()
	out := opts.Out
	if out == "" {
		out = filepath.Join(work, "profiles")
	}

	patterns := opts.Packages
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	pkgs, err := ListPackages(ctx, opts.Dir, patterns)
	if err != nil {
		return Result{}, err
	}
	plan, degraded, err := PlanFor(pkgs, opts.Serialize)
	if err != nil {
		return Result{}, err
	}
	planPath := filepath.Join(work, planFileName)
	if err := shim.WritePlan(planPath, plan); err != nil {
		return Result{}, err
	}

	command := withCoverageFlags(opts.Command, opts.CoverPkg)
	code, err := execute(ctx, opts, command, work, planPath)
	if err != nil {
		return Result{}, err
	}

	reports, err := shim.ReadReports(work)
	if err != nil {
		return Result{}, err
	}
	profiles, warnings, err := Convert(ctx, work, out, reports)
	if err != nil {
		return Result{}, err
	}

	failed = false
	res := Result{
		Command:  command,
		Work:     work,
		Cleanup:  cleanup,
		Packages: pkgs,
		ExitCode: code,
		Profiles: profiles,
		Reports:  reports,
		Degraded: mergeDegradations(degraded, reports),
		Warnings: warnings,
	}
	return res, nil
}

// ListPackages resolves the patterns to import paths and directories.
func ListPackages(ctx context.Context, dir string, patterns []string) ([]Package, error) {
	args := append([]string{"list", "-f", "{{.ImportPath}}\t{{.Dir}}"}, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("runner: go list %s: %w: %s",
				strings.Join(patterns, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("runner: go list %s: %w", strings.Join(patterns, " "), err)
	}
	var out []Package
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		path, pkgDir, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		out = append(out, Package{ImportPath: path, Dir: pkgDir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ImportPath < out[j].ImportPath })
	return out, nil
}

// PlanFor decides each package's collection mode from a static read of its
// tests, and returns the degradations that decision implies. It also fills
// in each package's clause, which the analysis has in hand and which the
// caller would otherwise have to re-read every test file to learn.
func PlanFor(pkgs []Package, serialize bool) (shim.Plan, []Degradation, error) {
	plan := shim.Plan{
		Version:     shim.PlanVersion,
		DefaultMode: shim.ModePerTest,
		Packages:    map[string]shim.PackagePlan{},
	}
	var degraded []Degradation
	for i, p := range pkgs {
		a, err := Analyze(p.Dir)
		if err != nil {
			return shim.Plan{}, nil, err
		}
		pkgs[i].Clause = a.Clause
		parallel := a.ParallelTests()
		if len(parallel) == 0 || serialize {
			continue
		}
		reason := fmt.Sprintf("%d of %d tests call t.Parallel() (%s); collecting them one at a time "+
			"would take away the concurrency they asked for",
			len(parallel), len(a.Tests), strings.Join(parallel, ", "))
		plan.Packages[p.ImportPath] = shim.PackagePlan{Mode: shim.ModePerPackage, Reason: reason}
		degraded = append(degraded, Degradation{Package: p.ImportPath, Reason: reason, Source: "plan"})
	}
	return plan, degraded, nil
}

// mergeDegradations folds the modes the shim chose at runtime into the ones
// atlas planned. The runtime is the authority — it is the only side that
// knows whether the counters could actually be cleared.
func mergeDegradations(planned []Degradation, reports []shim.Report) []Degradation {
	out := make([]Degradation, 0, len(planned))
	seen := map[string]bool{}
	for _, r := range reports {
		if r.Mode == shim.ModePerTest {
			continue
		}
		out = append(out, Degradation{Package: r.Package, Reason: r.Reason, Source: "runtime"})
		seen[r.Package] = true
	}
	for _, d := range planned {
		if !seen[d.Package] {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
}

// execute runs the suite with the shim armed. A non-zero exit is a result,
// not an error: the tests failed, and what they covered before failing is
// still evidence.
func execute(ctx context.Context, opts Options, command []string, work, planPath string) (int, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...) //nolint:gosec // the command is the user's own.
	cmd.Dir = opts.Dir
	cmd.Stdout, cmd.Stderr = opts.Stdout, opts.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	abs, err := filepath.Abs(work)
	if err != nil {
		return 0, fmt.Errorf("runner: resolve work dir: %w", err)
	}
	absPlan, err := filepath.Abs(planPath)
	if err != nil {
		return 0, fmt.Errorf("runner: resolve plan path: %w", err)
	}
	cmd.Env = append(os.Environ(), shim.EnvDir+"="+abs, shim.EnvPlan+"="+absPlan)

	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), nil
	default:
		return 0, fmt.Errorf("runner: run %s: %w", strings.Join(command, " "), err)
	}
}

// withCoverageFlags adds the flags per-test collection cannot work without,
// leaving any the caller already chose alone:
//
//   - -covermode=atomic, the only mode whose counters the runtime will clear;
//   - -coverpkg, without which a test appears to execute only its own package;
//   - -count=1, because a cached test result runs no binary and therefore
//     writes no counters.
//
// Only a `go test` command is touched. Anything else (a Makefile, a wrapper
// script) is run verbatim and left to pass its own flags — the shim will
// report the degradation if they are missing.
func withCoverageFlags(command []string, coverPkg string) []string {
	if len(command) < 2 || filepath.Base(command[0]) != "go" || command[1] != "test" {
		return command
	}
	if coverPkg == "" {
		coverPkg = "./..."
	}
	add := []string{}
	for flag, value := range map[string]string{
		"-covermode": "-covermode=atomic",
		"-coverpkg":  "-coverpkg=" + coverPkg,
		"-count":     "-count=1",
	} {
		if !hasFlag(command[2:], flag) {
			add = append(add, value)
		}
	}
	sort.Strings(add) // map iteration order must not reach the command line.

	out := make([]string, 0, len(command)+len(add))
	out = append(out, command[:2]...)
	out = append(out, add...)
	return append(out, command[2:]...)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		a = strings.TrimPrefix(a, "-")
		if a == strings.TrimPrefix(flag, "-") || strings.HasPrefix(a, strings.TrimPrefix(flag, "-")+"=") {
			return true
		}
	}
	return false
}
