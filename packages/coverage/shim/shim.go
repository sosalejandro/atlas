package shim

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/coverage"
	"runtime/debug"
	"strings"
	"testing"
)

// Run is the entry point a generated TestMain calls. It never returns: like
// any TestMain it owns the process exit code, and here that code has to
// survive several m.Run calls rather than just one.
//
// With ATLAS_COV_DIR unset it is exactly `os.Exit(m.Run())` — collection is
// opt-in per invocation, so a repo that has committed the shim still runs a
// perfectly ordinary `go test`.
func Run(m *testing.M) {
	os.Exit(run(m, os.Getenv, os.Stderr))
}

// run is Run without the exit, so it can be exercised end to end.
func run(m *testing.M, getenv func(string) string, stderr io.Writer) int {
	root := getenv(EnvDir)
	if root == "" {
		return m.Run()
	}

	plan, err := LoadPlan(getenv(EnvPlan))
	if err != nil {
		// A plan we cannot read is not a reason to fail someone's test run;
		// it costs granularity, and that is what the default gives.
		fmt.Fprintf(stderr, "atlas shim: %v (continuing per-test)\n", err)
	}

	s, err := newSuite(m)
	if err != nil {
		fmt.Fprintf(stderr, "atlas shim: %v (running the suite unchanged)\n", err)
		return m.Run()
	}

	pkg := packageUnderTest()
	pp := plan.For(pkg)
	code, rep := collect(collectArgs{
		Root:   root,
		Pkg:    pkg,
		Mode:   pp.Mode,
		Reason: pp.Reason,
		Filter: s.filter,
	}, s, goCounters{})

	if err := WriteReport(root, rep); err != nil {
		fmt.Fprintf(stderr, "atlas shim: %v\n", err)
	}
	return code
}

// suite is the slice of *testing.M the collector drives. Behind the seam so
// the choreography can be tested without a compiled test binary.
type suite interface {
	// List returns the top-level test names matching pattern, in the order
	// the binary would run them.
	List(pattern string) ([]string, error)
	// Run runs the tests matching pattern and returns the exit code.
	Run(pattern string) int
}

// counters is the slice of runtime/coverage the collector drives.
type counters interface {
	Mode() string
	Clear() error
	WriteCounters(dir string) error
	WriteMeta(dir string) error
}

// coverModeAtomic is the only counter mode the runtime will clear: under
// "set" and "count" the counters are written non-atomically, and clearing
// them from under a running goroutine could corrupt them.
const coverModeAtomic = "atomic"

// warmupPattern matches no test. The first m.Run is spent on it because the
// coverage runtime does not compute its meta-data hash — and so cannot write
// meta or clear counters — until a run has completed.
const warmupPattern = "^$"

type collectArgs struct {
	Root   string
	Pkg    string
	Mode   Mode
	Reason string
	// Filter is the caller's own -run pattern, preserved so the shim never
	// widens a selection the developer narrowed.
	Filter string
}

// collect runs the suite at the finest granularity that is defensible and
// returns the exit code plus the report describing what it actually did.
//
// The degradation ladder is deliberate: every rung is something detected at
// runtime, and each one prefers a coarser truth to a finer guess.
func collect(a collectArgs, s suite, c counters) (int, Report) {
	rep := Report{Package: a.Pkg, Mode: ModePerTest, CoverMode: c.Mode()}

	code := s.Run(warmupPattern)

	mode, reason := a.Mode, a.Reason
	var tests []string
	if mode != ModePerPackage {
		var err error
		tests, err = s.List(listPattern(a.Filter))
		switch {
		case err != nil:
			mode, reason = ModePerPackage, fmt.Sprintf("could not enumerate this package's tests: %v", err)
		case len(tests) == 0:
			mode, reason = ModePerPackage, "package declares no top-level tests"
		case c.Mode() != coverModeAtomic:
			mode, reason = ModePerPackage, fmt.Sprintf(
				"-covermode=%q cannot be cleared between tests (atomic can)", c.Mode())
		}
	}

	if mode == ModePerPackage {
		rep.Mode, rep.Reason = ModePerPackage, reason
		if rc := s.Run(a.Filter); rc != 0 {
			code = rc
		}
		snapshot(&rep, c, PackageCounterDir(a.Root, a.Pkg))
		writeMeta(&rep, c, a.Root)
		return code, rep
	}

	writeMeta(&rep, c, a.Root)
	rep.Tests = tests
	for _, name := range tests {
		if err := c.Clear(); err != nil && reason == "" {
			// Counters that would not clear mean this test's snapshot also
			// contains its predecessors'. The suite still has to finish —
			// it is the developer's test run, not ours — but nothing it
			// produces from here is per-test evidence any more.
			reason = fmt.Sprintf("clearing counters failed mid-suite: %v", err)
		}
		if rc := s.Run(testPattern(name)); rc != 0 {
			code = rc
		}
		snapshot(&rep, c, CounterDir(a.Root, a.Pkg, name))
	}

	if reason != "" {
		rep.Mode, rep.Reason, rep.Tests = ModePerPackage, reason, nil
		// The counters were never cleared, so what is in them now IS the
		// package-wide total — the exact thing per-package mode wants.
		snapshot(&rep, c, PackageCounterDir(a.Root, a.Pkg))
	}
	return code, rep
}

// listPattern turns the caller's -run filter into a -list filter. Empty
// means "every test", which is what an absent -run means too.
func listPattern(filter string) string {
	if filter == "" {
		return ".*"
	}
	return filter
}

// testPattern selects exactly one top-level test. Subtests come along with
// it: -run matches path element by element, and an absent element matches
// everything, which is why a subtest is attributed to its parent.
func testPattern(name string) string {
	return "^" + regexp.QuoteMeta(name) + "$"
}

func snapshot(rep *Report, c counters, dir string) {
	if err := c.WriteCounters(dir); err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("write counters %s: %v", dir, err))
	}
}

func writeMeta(rep *Report, c counters, root string) {
	if err := c.WriteMeta(MetaDir(root, rep.Package)); err != nil {
		rep.Errors = append(rep.Errors, fmt.Sprintf("write meta-data: %v", err))
	}
}

// --- the real *testing.M and runtime/coverage behind those seams ----------

const (
	runFlag  = "test.run"
	listFlag = "test.list"
)

type mSuite struct {
	m      *testing.M
	run    flag.Value
	list   flag.Value
	filter string
}

// newSuite fails only when the testing flags are absent, which means this is
// not a test binary at all. Once they are in hand, setting them cannot fail
// (they are plain string flags), so the collector's seam needs no errors.
func newSuite(m *testing.M) (*mSuite, error) {
	r, l := flag.Lookup(runFlag), flag.Lookup(listFlag)
	if r == nil || l == nil {
		return nil, fmt.Errorf("shim: -%s/-%s not registered; is this a test binary?", runFlag, listFlag)
	}
	return &mSuite{m: m, run: r.Value, list: l.Value, filter: r.Value.String()}, nil
}

func (s *mSuite) Run(pattern string) int {
	_ = s.run.Set(pattern) //nolint:errcheck // a string flag's Set never fails.
	return s.m.Run()
}

// List asks the test binary itself which tests it has, rather than parsing
// the sources: the binary is the only authority on what was compiled in
// (build tags, generated tests). testing prints the list to stdout and
// returns before running anything, so the answer costs one redirected m.Run.
func (s *mSuite) List(pattern string) ([]string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("shim: pipe for test listing: %w", err)
	}
	// Drain concurrently: a package with enough tests to fill the pipe
	// buffer would otherwise deadlock the listing it is producing.
	type readResult struct {
		out []byte
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		b, err := io.ReadAll(r)
		done <- readResult{b, err}
	}()

	saved := os.Stdout
	os.Stdout = w
	_ = s.list.Set(pattern) //nolint:errcheck // a string flag's Set never fails.
	s.m.Run()
	_ = s.list.Set("") //nolint:errcheck // ditto.
	os.Stdout = saved
	_ = w.Close()

	res := <-done
	_ = r.Close()
	if res.err != nil {
		return nil, fmt.Errorf("shim: read test listing: %w", res.err)
	}
	return testNames(string(res.out)), nil
}

// testNames keeps only the Test functions. -test.list also prints
// benchmarks, fuzz targets and examples, none of which the coverage story
// is about — and all of which are distinguishable by the prefix the testing
// package requires of them.
func testNames(listing string) []string {
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "Test") {
			out = append(out, name)
		}
	}
	return out
}

// goCounters is the production counters implementation.
type goCounters struct{}

func (goCounters) Mode() string { return testing.CoverMode() }

func (goCounters) Clear() error {
	if err := coverage.ClearCounters(); err != nil {
		return fmt.Errorf("clear counters: %w", err)
	}
	return nil
}

func (goCounters) WriteCounters(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := coverage.WriteCountersDir(dir); err != nil {
		return fmt.Errorf("write counters to %s: %w", dir, err)
	}
	return nil
}

func (goCounters) WriteMeta(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := coverage.WriteMetaDir(dir); err != nil {
		return fmt.Errorf("write meta-data to %s: %w", dir, err)
	}
	return nil
}

// packageUnderTest is the import path of the package being tested. A test
// binary's build info names it with a ".test" suffix, which is also how the
// binary itself is named — hence the same trim on both paths.
func packageUnderTest() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Path != "" {
		return strings.TrimSuffix(bi.Path, ".test")
	}
	return strings.TrimSuffix(filepath.Base(os.Args[0]), ".test")
}
