package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/grunnr/packages/coverage/shim/runner"
)

func TestAnalyze(t *testing.T) {
	cases := []struct {
		name         string
		dir          string
		wantClause   string
		wantTests    []string
		wantParallel []string
		wantTestMain bool
		wantOurs     bool
	}{
		{
			name:       "sequential package with a generated shim",
			dir:        "../testdata/fixture/billing",
			wantClause: "billing",
			// A t.Parallel() inside a subtest closure is not the package's
			// own parallelism: it finishes inside its parent's run.
			wantTests:    []string{"TestRefund", "TestTotal"},
			wantParallel: nil,
			wantTestMain: true,
			wantOurs:     true,
		},
		{
			name:         "parallel package",
			dir:          "../testdata/fixture/shipping",
			wantClause:   "shipping",
			wantTests:    []string{"TestRate", "TestZone"},
			wantParallel: []string{"TestRate", "TestZone"},
			wantTestMain: true,
			wantOurs:     true,
		},
		{
			name:       "hand-written TestMain is recognised as not ours",
			dir:        "testdata/foreignmain",
			wantClause: "foreignmain",
			// The helper-called t.Parallel() is deliberately NOT detected:
			// see the fixture's comment.
			wantTests:    []string{"TestParallelViaHelper", "TestSomething"},
			wantTestMain: true,
			wantOurs:     false,
		},
		{
			name:         "external test package keeps its own clause",
			dir:          "testdata/externaltests",
			wantClause:   "externaltests_test",
			wantTests:    []string{"TestOther", "TestThing"},
			wantParallel: []string{"TestThing"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runner.Analyze(tc.dir)
			if err != nil {
				t.Fatalf("Analyze(%s): %v", tc.dir, err)
			}
			if got.Clause != tc.wantClause {
				t.Errorf("clause = %q, want %q", got.Clause, tc.wantClause)
			}
			if names := got.TestNames(); !equal(names, tc.wantTests) {
				t.Errorf("tests = %v, want %v", names, tc.wantTests)
			}
			if par := got.ParallelTests(); !equal(par, tc.wantParallel) {
				t.Errorf("parallel = %v, want %v", par, tc.wantParallel)
			}
			if (got.TestMain != nil) != tc.wantTestMain {
				t.Errorf("TestMain = %v, want present=%v", got.TestMain, tc.wantTestMain)
			}
			if got.TestMain != nil && got.TestMain.Generated != tc.wantOurs {
				t.Errorf("TestMain.Generated = %v, want %v", got.TestMain.Generated, tc.wantOurs)
			}
		})
	}
}

// A directory with no test files at all is not an error — `--package ./...`
// will hand the analyser plenty of them.
func TestAnalyze_NoTests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := runner.Analyze(dir)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(got.Tests) != 0 || got.Clause != "x" {
		t.Fatalf("got %+v, want no tests and clause x", got)
	}
}

// The generated file must be exactly the one the fixture commits: the
// fixture is the proof that this shape compiles and runs, and it is only
// proof of anything if the generator still emits it byte for byte.
func TestGenerate_MatchesTheCommittedFixture(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("../testdata/fixture/billing", runner.ShimFileName))
	if err != nil {
		t.Fatalf("read fixture shim: %v", err)
	}
	if got := runner.Generate("billing"); string(got) != string(want) {
		t.Fatalf("Generate(billing) =\n%s\nwant\n%s", got, want)
	}
}

func TestInitDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thing.go", "package thing\n")
	write(t, dir, "thing_test.go", "package thing\n\nimport \"testing\"\n\nfunc TestThing(t *testing.T) {}\n")

	first, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("InitDir: %v", err)
	}
	if first.Status != runner.StatusCreated {
		t.Fatalf("first InitDir = %q (%s), want created", first.Status, first.Reason)
	}

	// Idempotency is the acceptance criterion: a second init writes nothing.
	before := stat(t, first.Path)
	second, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("second InitDir: %v", err)
	}
	if second.Status != runner.StatusUnchanged {
		t.Fatalf("second InitDir = %q, want unchanged", second.Status)
	}
	if after := stat(t, first.Path); after != before {
		t.Fatalf("unchanged init rewrote the file (%v -> %v)", before, after)
	}
}

func TestInitDir_RefusesToTouchAForeignTestMain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thing.go", "package thing\n")
	write(t, dir, "thing_test.go",
		"package thing\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n"+
			"func TestMain(m *testing.M) { os.Exit(m.Run()) }\n\nfunc TestThing(t *testing.T) {}\n")

	got, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("InitDir: %v", err)
	}
	if got.Status != runner.StatusSkipped {
		t.Fatalf("status = %q, want skipped", got.Status)
	}
	if !strings.Contains(got.Reason, "TestMain") {
		t.Fatalf("reason = %q, want it to name the existing TestMain", got.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, runner.ShimFileName)); !os.IsNotExist(err) {
		t.Fatalf("a shim file was written next to someone else's TestMain")
	}
}

// A stale shim — ours, but from an older grunnr — is refreshed rather than
// left to rot, and that is reported as a change, not as a no-op.
func TestInitDir_RewritesAStaleGeneratedShim(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thing.go", "package thing\n")
	write(t, dir, "thing_test.go", "package thing\n\nimport \"testing\"\n\nfunc TestThing(t *testing.T) {}\n")
	stale := strings.Replace(string(runner.Generate("thing")), "atlascov.Run(m)", "atlascov.RunOld(m)", 1)
	write(t, dir, runner.ShimFileName, stale)

	got, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("InitDir: %v", err)
	}
	if got.Status != runner.StatusUpdated {
		t.Fatalf("status = %q, want updated", got.Status)
	}
	b, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != string(runner.Generate("thing")) {
		t.Fatalf("stale shim was not refreshed:\n%s", b)
	}
}

// A package with no tests gets no shim: a TestMain in a package that has no
// tests to run is noise, and it would make `go test` compile a test binary
// where none existed.
func TestInitDir_SkipsPackagesWithoutTests(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thing.go", "package thing\n")

	got, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("InitDir: %v", err)
	}
	if got.Status != runner.StatusSkipped || !strings.Contains(got.Reason, "no test") {
		t.Fatalf("got %+v, want skipped for having no tests", got)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func stat(t *testing.T, path string) [2]int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return [2]int64{fi.Size(), fi.ModTime().UnixNano()}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The upgrade case: a package whose shim was written by the binary back when
// it was called atlas.
//
// Everything about that file is grunnr's own work -- but the marker it
// carries names the old product, and after the rename the detector only knew
// the new spelling. Analyze then reported the TestMain as hand-written, and
// InitDir took the refusal branch: "atlas_shim_test.go already declares
// TestMain; call shim.Run(m) from it to opt in". Safe, wrong, and entirely
// plausible to a user who would have had no reason to doubt it.
//
// The file must be adopted and refreshed in place. Not renamed to
// ShimFileName: writing the canonical name while the old file stayed on disk
// would leave the package with two TestMain declarations and no build.
func TestInitDir_AdoptsAShimWrittenBeforeTheRename(t *testing.T) {
	const legacyName = "atlas_shim_test.go"
	dir := t.TempDir()
	write(t, dir, "thing.go", "package thing\n")
	write(t, dir, "thing_test.go", "package thing\n\nimport \"testing\"\n\nfunc TestThing(t *testing.T) {}\n")

	legacy := strings.Replace(string(runner.Generate("thing")),
		"`grunnr cov shim init`", "`atlas cov shim init`", 1)
	if !strings.Contains(legacy, "`atlas cov shim init`") {
		t.Fatal("fixture did not take: the generated marker no longer has the shape this test rewrites")
	}
	write(t, dir, legacyName, legacy)

	got, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("InitDir: %v", err)
	}
	if got.Status == runner.StatusSkipped {
		t.Fatalf("a shim grunnr wrote under its old name was refused as foreign: %s", got.Reason)
	}
	if got.Status != runner.StatusUpdated {
		t.Fatalf("status = %q, want updated", got.Status)
	}
	if filepath.Base(got.Path) != legacyName {
		t.Errorf("shim was written to %s, want the existing %s -- a second file means two TestMain",
			filepath.Base(got.Path), legacyName)
	}
	if _, err := os.Stat(filepath.Join(dir, runner.ShimFileName)); err == nil {
		t.Errorf("a second shim %s was created beside the legacy one; the package will not compile",
			runner.ShimFileName)
	}
	b, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != string(runner.Generate("thing")) {
		t.Errorf("the adopted shim was not refreshed to the current template:\n%s", b)
	}
}

// New packages get the current name. Pinned separately from the adoption case
// above so that "we kept the old file" can never quietly become "we always
// write the old name".
func TestInitDir_NewPackagesGetTheCurrentShimName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thing.go", "package thing\n")
	write(t, dir, "thing_test.go", "package thing\n\nimport \"testing\"\n\nfunc TestThing(t *testing.T) {}\n")

	got, err := runner.InitDir(dir)
	if err != nil {
		t.Fatalf("InitDir: %v", err)
	}
	if filepath.Base(got.Path) != "grunnr_shim_test.go" {
		t.Errorf("a fresh package got shim %q, want grunnr_shim_test.go", filepath.Base(got.Path))
	}
	if strings.Contains(string(runner.Generate("thing")), "`atlas cov shim init`") {
		t.Error("the generated marker still names the old product")
	}
}
