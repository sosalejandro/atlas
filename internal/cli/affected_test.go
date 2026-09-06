package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/affected"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// affectedFixture is a repo with a real store behind it: two production
// symbols, two tests, and the per-test coverage rows a `cov sync --per-test`
// would have left. The git side is faked, because the point under test is the
// wiring from store rows to a -run pattern, not git's own diff machinery.
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
	fix.seed(t)
	return fix
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
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
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

// A changed symbol nothing covers is the most actionable thing the command can
// tell you, and it must not be buried.
func TestAffected_ReportsChangedSymbolsNoTestCovers(t *testing.T) {
	fix := newAffectedFixture(t)
	// Refund IS covered in the fixture, so add an uncovered symbol instead.
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	end := 40
	if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "billing.Void", Kind: shared.KindFunc,
		FilePath: "billing/void.go", Line: 3, EndLine: &end,
	}); err != nil {
		t.Fatalf("Symbols.Insert: %v", err)
	}
	_ = s.Close()

	git := &stubGit{
		files: []string{"billing/void.go"},
		lines: map[string][]affected.LineRange{"billing/void.go": {{Start: 10, End: 10}}},
	}
	out, _, err := runAffectedCmd(t, fix, git, "--since", "origin/main")
	if err != nil {
		t.Fatalf("affected: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no test covers") || !strings.Contains(out, "billing.Void") {
		t.Errorf("output does not surface the uncovered changed symbol:\n%s", out)
	}
}
