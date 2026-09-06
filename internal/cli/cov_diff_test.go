package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// covDiffFixture is a throwaway git repo plus an atlas store describing it.
// `cov diff` joins the two, so a test that stubbed either half would not be
// testing the join.
type covDiffFixture struct {
	root   string
	dbPath string
}

// git runs a git command in the fixture repo with a hermetic environment:
// the developer's global config (a commit.gpgsign, a default branch name, an
// alias) must not decide whether this test passes.
func (f *covDiffFixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=atlas", "GIT_AUTHOR_EMAIL=atlas@example.test",
		"GIT_COMMITTER_NAME=atlas", "GIT_COMMITTER_EMAIL=atlas@example.test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (f *covDiffFixture) write(t *testing.T, rel, body string) {
	t.Helper()
	path := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// numberedGo renders a Go-ish file of n lines whose content is a function of
// (marker, line), so a test can rewrite an exact line range and get a diff
// with exactly the hunks it intended.
func numberedGo(n int, marker string, changed map[int]bool) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if changed[i] {
			fmt.Fprintf(&b, "\t// %s line %d\n", marker, i)
			continue
		}
		fmt.Fprintf(&b, "\t// line %d\n", i)
	}
	return b.String()
}

// newCovDiffFixture builds a repo whose base commit holds a 40-line source
// file and a doc, then leaves the caller to commit HEAD.
func newCovDiffFixture(t *testing.T) *covDiffFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	f := &covDiffFixture{root: root, dbPath: filepath.Join(root, ".atlas", "atlas.db")}

	f.git(t, "init", "-q", "-b", "main")
	f.write(t, "pkg/a.go", numberedGo(40, "", nil))
	f.write(t, "docs/readme.md", "# atlas\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "base")
	return f
}

// seedSymbolsAndCoverage indexes pkg/a.go as two symbols and records a
// coverage frontier: the first fully covered, the second measured and
// completely uncovered.
func (f *covDiffFixture) seedSymbolsAndCoverage(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	end20, end40 := 20, 40
	coveredID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.Covered", Kind: shared.KindFunc,
		FilePath: "pkg/a.go", Line: 1, EndLine: &end20,
	})
	if err != nil {
		t.Fatalf("insert covered symbol: %v", err)
	}
	uncoveredID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.Uncovered", Kind: shared.KindFunc,
		FilePath: "pkg/a.go", Line: 21, EndLine: &end40,
	})
	if err != nil {
		t.Fatalf("insert uncovered symbol: %v", err)
	}

	feature := shared.FeatureID("billing.checkout")
	if err := s.Features().Upsert(ctx, store.Feature{ID: feature, Title: "checkout"}); err != nil {
		t.Fatalf("Features.Upsert: %v", err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: feature, SymbolID: uncoveredID, Role: store.RoleImpl,
	}); err != nil {
		t.Fatalf("FeatureSymbols.Link: %v", err)
	}

	if _, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest,
	}, []store.CoverageResult{
		{SymbolID: &coveredID, Status: store.StatusPass, CoveredStmts: 10, TotalStmts: 10},
		{SymbolID: &uncoveredID, Status: store.StatusFail, CoveredStmts: 0, TotalStmts: 8},
	}); err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}
}

// linkAgainAsContract adds a SECOND link between the same feature and the
// same symbol, under a different role -- the shape that exposes a rollup
// which counts links instead of symbols.
func (f *covDiffFixture) linkAgainAsContract(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	sym, err := s.Symbols().FindByQualifiedName(ctx, "pkg.Uncovered")
	if err != nil {
		t.Fatalf("FindByQualifiedName: %v", err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: "billing.checkout", SymbolID: sym.ID, Role: store.RoleContract,
	}); err != nil {
		t.Fatalf("FeatureSymbols.Link: %v", err)
	}
}

// touchBothSymbols rewrites 10 lines inside the covered symbol and 5 inside
// the uncovered one: 10 of 15 known changed lines are covered, so the patch
// fraction is 66.7%.
func (f *covDiffFixture) touchBothSymbols(t *testing.T) {
	t.Helper()
	changed := map[int]bool{}
	for i := 5; i <= 14; i++ {
		changed[i] = true
	}
	for i := 25; i <= 29; i++ {
		changed[i] = true
	}
	f.write(t, "pkg/a.go", numberedGo(40, "touched", changed))
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "touch both symbols")
}

// execCovDiff drives the real command tree, from the fixture's working
// directory so config resolution finds the fixture repo rather than atlas's.
func execCovDiff(t *testing.T, f *covDiffFixture, args ...string) (string, error) {
	t.Helper()
	t.Chdir(f.root)
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"cov", "diff", "--db-path", f.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), err
}

func TestCovDiff_FlagsWired(t *testing.T) {
	cmd := newCovDiffCmd()
	for _, name := range []string{"base", "fail-under"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("atlas cov diff is missing --%s", name)
		}
	}
	var found bool
	for _, sub := range newCovCmd().Commands() {
		if sub.Name() == "diff" {
			found = true
		}
	}
	if !found {
		t.Error("cov diff is not registered on the cov group -- the feature is unreachable")
	}
}

// The gate: below the target the command must exit non-zero, because that
// exit code is the entire point of shipping this in CI.
func TestCovDiff_FailUnderDecidesTheExitCode(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.touchBothSymbols(t)

	if _, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "80"); err == nil {
		t.Error("66.7% patch coverage under a target of 80 must fail the command")
	}
	out, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "60")
	if err != nil {
		t.Errorf("66.7%% patch coverage under a target of 60 must pass: %v\n%s", err, out)
	}
	// Without a target the command only reports.
	if _, err := execCovDiff(t, f, "--base", "main~1"); err != nil {
		t.Errorf("cov diff without --fail-under must not fail: %v", err)
	}
}

// No denominator is not zero percent: a docs-only change must clear even a
// 100% target, or the gate fires on work no test could ever cover.
func TestCovDiff_NoMeasurableChangeExitsZero(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.write(t, "docs/readme.md", "# atlas\n\nnow with prose\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "docs only")

	out, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "100")
	if err != nil {
		t.Fatalf("a docs-only diff must exit 0 under any target: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no measurable change") {
		t.Errorf("output must say the diff was unmeasurable, not report 0%%:\n%s", out)
	}
	if strings.Contains(out, "0.0%") {
		t.Errorf("an unmeasurable diff must never be rendered as 0%%:\n%s", out)
	}
}

// A number without the lines is not actionable.
func TestCovDiff_TextNamesTheUncoveredChangedLines(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.touchBothSymbols(t)

	out, err := execCovDiff(t, f, "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff: %v\n%s", err, out)
	}
	for _, want := range []string{
		"pkg/a.go:25-29",   // the uncovered changed lines, addressable
		"pkg.Uncovered",    // and the symbol that owns them
		"66.7%",            // the known patch fraction
		"billing.checkout", // the feature the diff moved
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// The unknown bucket is reported as its own state, and it never enters the
// denominator that --fail-under reads.
func TestCovDiff_JSONCarriesEveryBucket(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	changed := map[int]bool{}
	for i := 5; i <= 14; i++ {
		changed[i] = true
	}
	for i := 25; i <= 29; i++ {
		changed[i] = true
	}
	f.write(t, "pkg/a.go", numberedGo(40, "touched", changed))
	f.write(t, "docs/readme.md", "# atlas\nline\nline\nline\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "code and docs")

	out, err := execCovDiff(t, f, "--json", "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	var env struct {
		Command string `json:"command"`
		Result  struct {
			ChangedFiles int      `json:"changed_files"`
			ChangedLines int      `json:"changed_lines"`
			KnownLines   int      `json:"known_lines"`
			CoveredLines float64  `json:"covered_lines"`
			UnknownLines int      `json:"unknown_lines"`
			Measurable   bool     `json:"measurable"`
			Percent      *float64 `json:"percent"`
			Unknown      []struct {
				Path   string `json:"path"`
				Reason string `json:"reason"`
			} `json:"unknown"`
			Uncovered []struct {
				Symbol string `json:"symbol"`
			} `json:"uncovered"`
			Features []struct {
				FeatureID string `json:"feature_id"`
			} `json:"features"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if env.Command != "cov.diff" {
		t.Errorf("command = %q, want cov.diff", env.Command)
	}
	r := env.Result
	if r.ChangedFiles != 2 {
		t.Errorf("changed_files = %d, want 2", r.ChangedFiles)
	}
	if r.KnownLines != 15 {
		t.Errorf("known_lines = %d, want 15", r.KnownLines)
	}
	if r.UnknownLines == 0 {
		t.Error("the doc edit must land in the unknown bucket, not be dropped")
	}
	if r.ChangedLines != r.KnownLines+r.UnknownLines {
		t.Errorf("buckets do not account for every changed line: %d != %d + %d",
			r.ChangedLines, r.KnownLines, r.UnknownLines)
	}
	if !r.Measurable || r.Percent == nil {
		t.Fatalf("result should be measurable: %+v", r)
	}
	if got := *r.Percent; got < 66.6 || got > 66.8 {
		t.Errorf("percent = %v, want ~66.7 (unknown lines must not dilute it)", got)
	}
	if len(r.Unknown) == 0 || r.Unknown[0].Path != "docs/readme.md" {
		t.Errorf("unknown spans = %+v", r.Unknown)
	}
	if len(r.Uncovered) != 1 || r.Uncovered[0].Symbol != "pkg.Uncovered" {
		t.Errorf("uncovered = %+v", r.Uncovered)
	}
	if len(r.Features) != 1 || r.Features[0].FeatureID != "billing.checkout" {
		t.Errorf("features = %+v", r.Features)
	}
}

// A symbol linked to one feature under two roles is still one symbol. Counting
// its changed lines once per link would inflate every feature that carries
// both an impl and a contract link for the same declaration.
func TestCovDiff_FeatureRollupDoesNotDoubleCountRoles(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.linkAgainAsContract(t)
	f.touchBothSymbols(t)

	out, err := execCovDiff(t, f, "--json", "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	var env struct {
		Result struct {
			Features []struct {
				FeatureID string `json:"feature_id"`
				Lines     int    `json:"lines"`
			} `json:"features"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	if len(env.Result.Features) != 1 {
		t.Fatalf("features = %+v, want exactly one row", env.Result.Features)
	}
	if got := env.Result.Features[0].Lines; got != 5 {
		t.Errorf("feature lines = %d, want 5 (the symbol's changed lines, counted once)", got)
	}
}

// Without a coverage frontier every line would be unknown and the gate would
// pass silently. That is a misconfiguration, not a clean run.
func TestCovDiff_WithoutACoverageFrontierIsAnError(t *testing.T) {
	f := newCovDiffFixture(t)
	f.touchBothSymbols(t)

	_, err := execCovDiff(t, f, "--base", "main~1")
	if err == nil {
		t.Fatal("cov diff must refuse to score against an empty store")
	}
	if !strings.Contains(err.Error(), "cov sync") {
		t.Errorf("the error should point at the fix: %v", err)
	}
}

func TestCovDiff_BaseIsRequired(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	if _, err := execCovDiff(t, f); err == nil {
		t.Fatal("cov diff without --base must fail rather than guess a base")
	}
}
