package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Environment variables the shim reads. They are the whole interface between
// `atlas cov run` and a test binary it does not link against: a shim built
// into someone's repo at version N has to keep working with an atlas binary
// at version N+1, so this contract stays small and additive.
const (
	// EnvDir names the output root. Unset means "do nothing" — the shim is
	// opt-in per invocation, not just per package.
	EnvDir = "ATLAS_COV_DIR"
	// EnvPlan names a JSON plan file (see Plan). Optional; without it every
	// package is collected per test.
	EnvPlan = "ATLAS_COV_PLAN"
)

// Mode is the granularity one package's coverage was collected at.
type Mode string

const (
	// ModePerTest: one counter snapshot per top-level test.
	ModePerTest Mode = "per-test"
	// ModePerPackage: one counter snapshot for the whole package. The
	// fallback whenever per-test evidence would be a guess.
	ModePerPackage Mode = "per-package"
)

// PackageCounterName is the leaf directory a per-package snapshot lands in.
// Go test names always begin with "Test", so it cannot collide with one.
const PackageCounterName = "_package"

// Sub-directories of the output root. Meta-data and counters are kept apart
// because `go tool covdata` matches a counter file to its meta file by hash,
// not by location, and one meta file per package serves every test in it.
const (
	metaSubdir     = "meta"
	countersSubdir = "counters"
	reportsSubdir  = "reports"
	reportFile     = "shim.json"
)

// CountersRoot is the directory every counter snapshot lives under, whatever
// package or test wrote it. The collecting side walks this, and only this.
func CountersRoot(root string) string {
	return filepath.Join(root, countersSubdir)
}

// MetaDir is where a package's coverage meta-data file goes.
func MetaDir(root, pkg string) string {
	return filepath.Join(root, metaSubdir, filepath.FromSlash(pkg))
}

// CounterDir is where one test's counter snapshot goes.
func CounterDir(root, pkg, test string) string {
	return filepath.Join(root, countersSubdir, filepath.FromSlash(pkg), test)
}

// PackageCounterDir is where a degraded package's single snapshot goes.
func PackageCounterDir(root, pkg string) string {
	return CounterDir(root, pkg, PackageCounterName)
}

// SplitCounterDir is CounterDir's inverse: it recovers (package, test) from a
// directory under root, and reports whether the path is a counter directory
// at all. The collecting side walks the tree it did not create, so a path it
// does not recognise must be rejected rather than parsed optimistically.
func SplitCounterDir(root, dir string) (pkg, test string, ok bool) {
	rel, err := filepath.Rel(filepath.Join(root, countersSubdir), dir)
	if err != nil {
		return "", "", false
	}
	slashed := filepath.ToSlash(rel)
	if slashed == "." || strings.HasPrefix(slashed, "../") {
		return "", "", false
	}
	pkg, test = path.Split(slashed)
	pkg = strings.TrimSuffix(pkg, "/")
	if pkg == "" || test == "" {
		return "", "", false
	}
	return pkg, test, true
}

// ReportPath is where a package's Report goes.
func ReportPath(root, pkg string) string {
	return filepath.Join(root, reportsSubdir, filepath.FromSlash(pkg), reportFile)
}

// Plan tells the shim, per package, how coverage may be collected. It is
// written by `atlas cov run` from a static read of the test sources — the
// test binary itself cannot see that a test calls t.Parallel() until it is
// too late to matter.
type Plan struct {
	Version     int                    `json:"version"`
	DefaultMode Mode                   `json:"default_mode"`
	Packages    map[string]PackagePlan `json:"packages,omitempty"`
}

// PackagePlan is one package's entry in a Plan.
type PackagePlan struct {
	Mode Mode `json:"mode"`
	// Reason explains a non-default mode in the words a human needs to see
	// in the diagnostic. Empty for the default.
	Reason string `json:"reason,omitempty"`
}

// PlanVersion is the plan format's version. The shim rejects nothing on a
// mismatch — a newer plan read by an older shim still yields per-test for
// packages it understands — but the field makes a future break diagnosable.
const PlanVersion = 1

// For returns the plan for one package, falling back to the default mode.
func (p Plan) For(pkg string) PackagePlan {
	if pp, ok := p.Packages[pkg]; ok && pp.Mode != "" {
		return pp
	}
	mode := p.DefaultMode
	if mode == "" {
		mode = ModePerTest
	}
	return PackagePlan{Mode: mode}
}

// WritePlan serialises a plan, creating the parent directory.
func WritePlan(path string, p Plan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("shim: create plan dir: %w", err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("shim: marshal plan: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("shim: write plan %s: %w", path, err)
	}
	return nil
}

// LoadPlan reads a plan. A missing file is not an error: the shim is usable
// with nothing but EnvDir set, and then everything is per-test.
func LoadPlan(path string) (Plan, error) {
	if path == "" {
		return Plan{Version: PlanVersion, DefaultMode: ModePerTest}, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Plan{Version: PlanVersion, DefaultMode: ModePerTest}, nil
	}
	if err != nil {
		return Plan{}, fmt.Errorf("shim: read plan %s: %w", path, err)
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return Plan{}, fmt.Errorf("shim: parse plan %s: %w", path, err)
	}
	return p, nil
}

// Report is what one package's shim run leaves behind for the collecting
// side. It is the authority on whether that package's per-test directories
// mean anything: a report in per-package mode invalidates them, however many
// of them happen to be on disk.
type Report struct {
	Package string `json:"package"`
	Mode    Mode   `json:"mode"`
	// Reason is why the mode is not per-test. Always set when it isn't.
	Reason string `json:"reason,omitempty"`
	// CoverMode is `go test -covermode`, recorded because "atomic" is a
	// precondition for per-test collection and its absence is the single
	// most likely reason a run produced nothing.
	CoverMode string `json:"cover_mode,omitempty"`
	// Tests are the top-level tests that were snapshotted, in run order.
	Tests []string `json:"tests,omitempty"`
	// Errors are non-fatal failures (a snapshot that could not be written,
	// say). Surfaced rather than swallowed: they are missing evidence.
	Errors []string `json:"errors,omitempty"`
}

// WriteReport persists one package's report under root.
func WriteReport(root string, r Report) error {
	path := ReportPath(root, r.Package)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("shim: create report dir: %w", err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("shim: marshal report: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("shim: write report %s: %w", path, err)
	}
	return nil
}

// ReadReports collects every report under root, sorted by package so callers
// render the same diagnostics in the same order on every run.
func ReadReports(root string) ([]Report, error) {
	dir := filepath.Join(root, reportsSubdir)
	var out []Report
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != reportFile {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		var r Report
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		out = append(out, r)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("shim: read reports under %s: %w", dir, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out, nil
}
