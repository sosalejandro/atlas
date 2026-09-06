package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/affected"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// affectedFixture is a repo with a real store behind it: two production
// symbols, two tests, the per-test coverage rows a `cov sync --per-test` would
// have left, AND the files on disk with the content hashes a scan would have
// recorded. The files are real because the command re-hashes every changed
// path before it joins a line number against a stored span; a fixture with no
// files on disk exercises only the degraded path.
//
// The git side is faked: the point under test is the wiring from store rows to
// a -run pattern, not git's own diff machinery.
type affectedFixture struct {
	root   string
	dbPath string
}

func newAffectedFixture(t *testing.T) *affectedFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	fix := &affectedFixture{root: dir, dbPath: filepath.Join(dir, ".atlas", "atlas.db")}
	// A git repo so `git rev-parse --show-toplevel` anchors repoRoot on the
	// fixture rather than on whatever tree the test binary was built in.
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v\n%s", err, out)
	}
	fix.seed(t)
	return fix
}

// indexedFiles are the paths the fixture writes to disk and records hashes
// for: every file the seeded symbols live in.
var indexedFiles = []string{
	"billing/checkout.go", "billing/refund.go",
	"billing/checkout_test.go", "billing/refund_test.go",
}

// writeFile writes body under the fixture root and returns its sha256, which
// is exactly what the scanner records in file_hashes.
func (f *affectedFixture) writeFile(t *testing.T, rel, body string) string {
	t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// touch rewrites a file WITHOUT updating its recorded hash — an edit that
// landed after the last `atlas scan`, which is what makes the stored spans
// describe a version of the file that no longer exists.
func (f *affectedFixture) touch(t *testing.T, rel string) {
	t.Helper()
	f.writeFile(t, rel, "package billing\n\n// edited after the scan\n")
}

func (f *affectedFixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	end := func(v int) *int { return &v }
	ids := map[shared.SymbolID]int64{}
	for _, row := range []store.SymbolRow{
		{QualifiedName: "billing.Checkout", Kind: shared.KindFunc, FilePath: "billing/checkout.go", Line: 10, EndLine: end(30)},
		{QualifiedName: "billing.Refund", Kind: shared.KindFunc, FilePath: "billing/refund.go", Line: 5, EndLine: end(20)},
		{QualifiedName: "billing.TestCheckout", Kind: shared.KindFunc, FilePath: "billing/checkout_test.go", Line: 8, EndLine: end(25)},
		{QualifiedName: "billing.TestRefund", Kind: shared.KindFunc, FilePath: "billing/refund_test.go", Line: 8, EndLine: end(25)},
	} {
		id, err := s.Symbols().Insert(ctx, row)
		if err != nil {
			t.Fatalf("Symbols.Insert(%s): %v", row.QualifiedName, err)
		}
		ids[row.QualifiedName] = id
	}

	finished := time.Now().UTC().Add(-90 * time.Minute)
	runID, err := s.Coverage().InsertRun(ctx, store.CoverageRun{
		Framework:  store.FrameworkGoTest,
		StartedAt:  finished,
		FinishedAt: finished,
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if err := s.TestCoverage().Insert(ctx, runID, []store.TestExecution{
		{TestSymbolID: ids["billing.TestCheckout"], SymbolID: ids["billing.Checkout"], CoveredStmts: 4, TotalStmts: 4},
		{TestSymbolID: ids["billing.TestRefund"], SymbolID: ids["billing.Refund"], CoveredStmts: 3, TotalStmts: 3},
	}); err != nil {
		t.Fatalf("TestCoverage.Insert: %v", err)
	}

	// The state a scan leaves behind: the file on disk, and the hash of it the
	// spans above were measured from.
	for _, rel := range indexedFiles {
		hash := f.writeFile(t, rel, "package billing\n\n// "+rel+"\n")
		if err := s.FileHashes().Upsert(ctx, store.FileHashRow{FilePath: rel, ContentHash: hash}); err != nil {
			t.Fatalf("FileHashes.Upsert(%s): %v", rel, err)
		}
	}
}

// stubGit stands in for the `git` binary so the test controls the diff exactly.
type stubGit struct {
	files []string
	lines map[string][]affected.LineRange
}

func (g *stubGit) ChangedFiles(context.Context, string) ([]string, error) { return g.files, nil }

func (g *stubGit) ChangedLines(context.Context, string) (map[string][]affected.LineRange, error) {
	return g.lines, nil
}

// runAffectedCmd drives the real command tree with the git seam stubbed and
// the process-exit seam captured.
func runAffectedCmd(t *testing.T, fix *affectedFixture, git affected.GitDiff, args ...string) (string, int, error) {
	t.Helper()
	// From the fixture's own directory: config resolution recomputes repoRoot
	// per invocation, and the freshness check hashes the changed files
	// relative to it. Pointed at atlas's own tree, every fixture path would
	// classify as deleted and the command would never exercise the fresh path.
	t.Chdir(fix.root)
	root := NewRootCmd()
	flags = globalFlags{DBPath: fix.dbPath}

	prevGit, prevExit := newAffectedGit, affectedExit
	t.Cleanup(func() { newAffectedGit, affectedExit = prevGit, prevExit })

	newAffectedGit = func(string) affected.GitDiff { return git }
	exitCode := 0
	affectedExit = func(code int) { exitCode = code }

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"affected", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), exitCode, err
}

func changedCheckout() *stubGit {
	return &stubGit{
		files: []string{"billing/checkout.go"},
		lines: map[string][]affected.LineRange{"billing/checkout.go": {{Start: 12, End: 14}}},
	}
}

func TestAffected_FlagsWired(t *testing.T) {
	cmd := newAffectedCmd()
	for _, name := range []string{"since", "kind", "fallback-exit-code"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("atlas affected is missing --%s", name)
		}
	}
}

func TestAffected_RequiresSince(t *testing.T) {
	fix := newAffectedFixture(t)
	if _, _, err := runAffectedCmd(t, fix, changedCheckout()); err == nil {
		t.Fatal("atlas affected with no --since must fail rather than diff against an implied default")
	}
}

// The headline: a diff inside Checkout runs only the test that executed it,
// and the output carries the pattern a runner can paste.
func TestAffected_HumanOutputNamesTheSubsetAndTheEvidence(t *testing.T) {
	fix := newAffectedFixture(t)
	out, code, err := runAffectedCmd(t, fix, changedCheckout(), "--since", "origin/main")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 for a narrowed selection", code)
	}
	for _, want := range []string{
		"TestCheckout",
		"^(TestCheckout)$",
		"1 of 2",
		"billing.Checkout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "TestRefund") {
		t.Errorf("output selected a test that never executed the changed symbol:\n%s", out)
	}
	// The evidence and the diff are from different commits, and the output has
	// to say so -- naming the runs it read and how old they are.
	if !strings.Contains(out, "run 1") && !strings.Contains(out, "runs 1") {
		t.Errorf("output does not name the frontier run ids:\n%s", out)
	}
	if !strings.Contains(out, "measured at an earlier commit") {
		t.Errorf("output does not warn that the evidence predates the diff:\n%s", out)
	}
}

func TestAffected_JSONCarriesTheOutcomeAndTheSubset(t *testing.T) {
	fix := newAffectedFixture(t)
	out, _, err := runAffectedCmd(t, fix, changedCheckout(), "--since", "origin/main", "--json")
	if err != nil {
		t.Fatalf("affected --json: %v\n%s", err, out)
	}
	var env struct {
		Command string `json:"command"`
		Result  struct {
			Outcome       string  `json:"outcome"`
			RunPattern    string  `json:"run_pattern"`
			Reduction     float64 `json:"reduction"`
			TotalTests    int     `json:"total_tests"`
			SelectedTests []struct {
				RunName string `json:"run_name"`
			} `json:"selected_tests"`
			Evidence struct {
				RunIDs []int64 `json:"run_ids"`
			} `json:"evidence"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if env.Command != "affected" {
		t.Errorf("command = %q, want %q", env.Command, "affected")
	}
	if env.Result.Outcome != "selected" {
		t.Fatalf("outcome = %q, want selected", env.Result.Outcome)
	}
	if len(env.Result.SelectedTests) != 1 || env.Result.SelectedTests[0].RunName != "TestCheckout" {
		t.Fatalf("selected_tests = %+v, want [TestCheckout]", env.Result.SelectedTests)
	}
	if env.Result.RunPattern != "^(TestCheckout)$" {
		t.Errorf("run_pattern = %q", env.Result.RunPattern)
	}
	if env.Result.TotalTests != 2 || env.Result.Reduction != 0.5 {
		t.Errorf("reduction = %v of %d tests, want 0.5 of 2", env.Result.Reduction, env.Result.TotalTests)
	}
	if len(env.Result.Evidence.RunIDs) != 1 {
		t.Errorf("evidence.run_ids = %v, want one run", env.Result.Evidence.RunIDs)
	}
}

// The bail-out path is the one CI depends on: a distinguishable exit code, no
// -run pattern to accidentally consume, and the reason stated.
func TestAffected_FallbackExitCodeSignalsRunEverything(t *testing.T) {
	fix := newAffectedFixture(t)
	git := &stubGit{
		files: []string{"go.mod"},
		lines: map[string][]affected.LineRange{"go.mod": {{Start: 3, End: 3}}},
	}
	out, code, err := runAffectedCmd(t, fix, git, "--since", "origin/main", "--fallback-exit-code", "7")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7 so CI can branch without parsing text", code)
	}
	if !strings.Contains(out, "RUN EVERYTHING") {
		t.Errorf("output does not say the selection bailed:\n%s", out)
	}
	if !strings.Contains(out, "build-config") {
		t.Errorf("output does not name the rule that fired:\n%s", out)
	}
	if strings.Contains(out, "-run ") {
		t.Errorf("a bail-out must not emit a -run pattern a runner could consume:\n%s", out)
	}
}

// Without the flag a bail-out is still exit 0: `atlas affected` succeeded at
// answering the question, and a team that has not opted in must not have their
// pipeline start failing.
func TestAffected_FallbackIsExitZeroByDefault(t *testing.T) {
	fix := newAffectedFixture(t)
	git := &stubGit{files: []string{"go.mod"}, lines: map[string][]affected.LineRange{"go.mod": {{Start: 3, End: 3}}}}
	out, code, err := runAffectedCmd(t, fix, git, "--since", "origin/main")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 without --fallback-exit-code", code)
	}
}

func TestAffected_KindPackageEmitsGoTestArguments(t *testing.T) {
	fix := newAffectedFixture(t)
	out, _, err := runAffectedCmd(t, fix, changedCheckout(), "--since", "origin/main", "--kind", "package")
	if err != nil {
		t.Fatalf("affected --kind package: %v\n%s", err, out)
	}
	if !strings.Contains(out, "./billing/...") {
		t.Errorf("output does not carry a go test package argument:\n%s", out)
	}
}

func TestAffected_RejectsUnknownKind(t *testing.T) {
	fix := newAffectedFixture(t)
	if _, _, err := runAffectedCmd(t, fix, changedCheckout(), "--since", "origin/main", "--kind", "nonsense"); err == nil {
		t.Fatal("an unknown --kind must be rejected, not silently defaulted")
	}
}

// addUncoveredSymbol seeds a symbol with no coverage rows, on disk and
// hashed, so a diff touching it produces a genuine empty selection rather than
// a widening.
func (f *affectedFixture) addUncoveredSymbol(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	end := 40
	if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "billing.Void", Kind: shared.KindFunc,
		FilePath: "billing/void.go", Line: 3, EndLine: &end,
	}); err != nil {
		t.Fatalf("Symbols.Insert: %v", err)
	}
	hash := f.writeFile(t, "billing/void.go", "package billing\n\n// void\n")
	if err := s.FileHashes().Upsert(ctx, store.FileHashRow{
		FilePath: "billing/void.go", ContentHash: hash,
	}); err != nil {
		t.Fatalf("FileHashes.Upsert: %v", err)
	}
}

func changedVoid() *stubGit {
	return &stubGit{
		files: []string{"billing/void.go"},
		lines: map[string][]affected.LineRange{"billing/void.go": {{Start: 10, End: 10}}},
	}
}

// A changed symbol nothing covers is the most actionable thing the command can
// tell you, and it must not be buried.
func TestAffected_ReportsChangedSymbolsNoTestCovers(t *testing.T) {
	fix := newAffectedFixture(t)
	fix.addUncoveredSymbol(t)

	out, _, err := runAffectedCmd(t, fix, changedVoid(), "--since", "origin/main")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no test covers") || !strings.Contains(out, "billing.Void") {
		t.Errorf("output does not surface the uncovered changed symbol:\n%s", out)
	}
}

// The quiet catastrophe (#90 finding 5): the diff changed code, no rule fired,
// and the selection is EMPTY. A recipe that branches only on run-all runs
// `go test -run ”` -- nothing -- and exits 0. The output must make that
// impossible to mistake for a pass.
func TestAffected_EmptySelectionIsUnmistakableInTheHumanOutput(t *testing.T) {
	fix := newAffectedFixture(t)
	fix.addUncoveredSymbol(t)

	out, _, err := runAffectedCmd(t, fix, changedVoid(), "--since", "origin/main")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if !strings.Contains(out, "NOTHING SELECTED") {
		t.Errorf("an empty selection is not called out in the human output:\n%s", out)
	}
	if !strings.Contains(out, "Run the full suite") {
		t.Errorf("the output does not tell the reader what to do about it:\n%s", out)
	}
	if strings.Contains(out, "go test -run ''") {
		t.Errorf("an empty -run pattern must never be offered as a command to paste:\n%s", out)
	}
}

func TestAffected_EmptySelectionIsWarnedAboutInJSON(t *testing.T) {
	fix := newAffectedFixture(t)
	fix.addUncoveredSymbol(t)

	out, _, err := runAffectedCmd(t, fix, changedVoid(), "--since", "origin/main", "--json")
	if err != nil {
		t.Fatalf("affected --json: %v\n%s", err, out)
	}
	var env struct {
		Warnings []string `json:"warnings"`
		Result   struct {
			Outcome       string   `json:"outcome"`
			RunPattern    string   `json:"run_pattern"`
			SelectedTests []any    `json:"selected_tests"`
			ChangedFiles  []string `json:"changed_files"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	// The shape a CI recipe has to branch on: a non-empty diff, outcome
	// "selected", and nothing to run.
	if env.Result.Outcome != "selected" || len(env.Result.SelectedTests) != 0 || env.Result.RunPattern != "" {
		t.Fatalf("fixture no longer produces an empty selection: %+v", env.Result)
	}
	if len(env.Result.ChangedFiles) == 0 {
		t.Fatal("fixture produced an empty diff; the empty-selection case needs a real change")
	}
	found := false
	for _, w := range env.Warnings {
		if strings.Contains(w, "NO test was selected") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one saying an empty selection is not a passing build", env.Warnings)
	}
}

// End to end for #90 finding 1: a file edited since the last `atlas scan` has
// spans that describe a version of it that no longer exists. Lines 12-14 land
// inside billing.Checkout's STORED span, so an unchecked join answers
// "TestCheckout" alone. The command must widen instead, and say why.
func TestAffected_StaleIndexWidensAndNamesTheFile(t *testing.T) {
	fix := newAffectedFixture(t)
	fix.touch(t, "billing/checkout.go")

	out, _, err := runAffectedCmd(t, fix, changedCheckout(), "--since", "origin/main")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if !strings.Contains(out, "stale-index") {
		t.Errorf("output does not name the reason the selection widened:\n%s", out)
	}
	if !strings.Contains(out, "billing/checkout.go") {
		t.Errorf("output does not name the file whose index is out of date:\n%s", out)
	}
	if !strings.Contains(out, "atlas scan") {
		t.Errorf("output does not tell the reader how to recover the reduction:\n%s", out)
	}
	// And the safety property itself: the tests of the package's other symbol
	// are no longer omitted on the strength of a line number nothing backs.
	if !strings.Contains(out, "TestRefund") {
		t.Errorf("stale spans still narrowed to one symbol's tests:\n%s", out)
	}
}

func TestAffected_StaleIndexIsWarnedAboutInJSON(t *testing.T) {
	fix := newAffectedFixture(t)
	fix.touch(t, "billing/checkout.go")

	out, _, err := runAffectedCmd(t, fix, changedCheckout(), "--since", "origin/main", "--json")
	if err != nil {
		t.Fatalf("affected --json: %v\n%s", err, out)
	}
	var env struct {
		Warnings []string `json:"warnings"`
		Result   struct {
			Widenings []struct {
				Path   string `json:"path"`
				Scope  string `json:"scope"`
				Reason string `json:"reason"`
			} `json:"widenings"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if len(env.Result.Widenings) != 1 {
		t.Fatalf("widenings = %+v, want exactly one", env.Result.Widenings)
	}
	w := env.Result.Widenings[0]
	if w.Reason != "stale-index" || w.Path != "billing/checkout.go" || w.Scope != "package" {
		t.Errorf("widening = %+v, want the stale-index package widening for checkout.go", w)
	}
	found := false
	for _, msg := range env.Warnings {
		if strings.Contains(msg, "index is out of date") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the stale index", env.Warnings)
	}
}
