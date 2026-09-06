package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// indexFilesAtHEAD records the content hash of each file the way `atlas scan`
// running at HEAD would.
//
// Every test that expects a changed file to be SCORED has to call this after
// its last write: cov diff refuses to join stored spans against a file that
// no longer hashes to what the scanner saw, so a fixture that skipped this
// would be measuring the staleness guard rather than the scorer.
func (f *covDiffFixture) indexFilesAtHEAD(t *testing.T, rels ...string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(f.root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		sum := sha256.Sum256(body)
		if err := s.FileHashes().Upsert(ctx, store.FileHashRow{
			FilePath: rel, ContentHash: hex.EncodeToString(sum[:]), ModTime: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("FileHashes.Upsert %s: %v", rel, err)
		}
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
	f.indexFilesAtHEAD(t, "pkg/a.go")
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
	f.indexFilesAtHEAD(t, "pkg/a.go")

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

// gitOut runs a git command in the fixture repo and returns its stdout, for
// the tests that need to check what git itself produced before asserting on
// what atlas made of it.
func (f *covDiffFixture) gitOut(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// covDiffJSON is the result object of `cov diff --json`, in the shape the
// documented PR-bot contract describes.
type covDiffJSON struct {
	ChangedFiles    int      `json:"changed_files"`
	ChangedLines    int      `json:"changed_lines"`
	KnownLines      int      `json:"known_lines"`
	UnknownLines    int      `json:"unknown_lines"`
	Measurable      bool     `json:"measurable"`
	Percent         *float64 `json:"percent"`
	StaleIndexLines int      `json:"stale_index_lines"`
	StaleIndexFiles []struct {
		Path  string `json:"path"`
		State string `json:"state"`
		Lines int    `json:"lines"`
	} `json:"stale_index_files"`
	Unknown []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
		Lines  int    `json:"lines"`
	} `json:"unknown"`
	Uncovered []struct {
		Symbol string `json:"symbol"`
		Lines  int    `json:"lines"`
	} `json:"uncovered"`
	Covered []struct {
		Symbol string `json:"symbol"`
	} `json:"covered"`
}

func decodeCovDiff(t *testing.T, out string) covDiffJSON {
	t.Helper()
	var env struct {
		Result covDiffJSON `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	return env.Result
}

// A patch percentage computed against spans that no longer describe the file
// is a confident wrong number: the changed lines land inside whichever symbol
// has since drifted into their range and are scored as ITS coverage. The
// index is hashed at the base content here and the file then rewritten, so
// every changed line must be refused rather than mis-attributed.
func TestCovDiff_StaleIndexIsRefusedRatherThanMisattributed(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	// The scan happened BEFORE the branch's commit -- the ordinary CI mistake.
	f.indexFilesAtHEAD(t, "pkg/a.go")

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

	out, err := execCovDiff(t, f, "--json", "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	r := decodeCovDiff(t, out)
	if r.KnownLines != 0 {
		t.Errorf("known_lines = %d, want 0: spans from a stale index must not be joined", r.KnownLines)
	}
	if r.Measurable || r.Percent != nil {
		t.Errorf("a diff scored only against a stale index must not be measurable: %+v", r)
	}
	if len(r.StaleIndexFiles) != 1 {
		t.Fatalf("stale_index_files = %+v, want exactly pkg/a.go", r.StaleIndexFiles)
	}
	if got := r.StaleIndexFiles[0]; got.Path != "pkg/a.go" || got.State != "stale" || got.Lines != 15 {
		t.Errorf("stale_index_files[0] = %+v, want {pkg/a.go stale 15}", got)
	}
	if r.StaleIndexLines != 15 {
		t.Errorf("stale_index_lines = %d, want 15", r.StaleIndexLines)
	}
	var staleLines int
	for _, u := range r.Unknown {
		if u.Reason == "index-stale" {
			staleLines += u.Lines
		}
	}
	if staleLines != 15 {
		t.Errorf("index-stale unknown lines = %d, want 15 (reasons: %+v)", staleLines, r.Unknown)
	}

	// And the text report has to say so where a reader will see it, with the
	// remedy attached -- an unexplained shrunken denominator is the failure.
	text, err := execCovDiff(t, f, "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff: %v\n%s", err, text)
	}
	for _, want := range []string{"STALE INDEX", "pkg/a.go", "atlas scan"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}

// A file that atlas has no symbols for is reported as file-not-indexed, not
// as a stale index: the remedies are different, and the freshness guard must
// not swallow the sharper answer.
func TestCovDiff_UnindexedFileIsNotReportedAsStale(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.write(t, "docs/readme.md", "# atlas\n\nnow with prose\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "docs only")

	out, err := execCovDiff(t, f, "--json", "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	r := decodeCovDiff(t, out)
	if len(r.StaleIndexFiles) != 0 {
		t.Errorf("an unindexed doc is not a stale index: %+v", r.StaleIndexFiles)
	}
	if len(r.Unknown) != 1 || r.Unknown[0].Reason != "file-not-indexed" {
		t.Errorf("unknown = %+v, want one file-not-indexed span", r.Unknown)
	}
}

// git rewrites the "+++" operand when the repo configures diff.srcPrefix /
// diff.dstPrefix. Every parsed path then matches no indexed file, the whole
// diff becomes file-not-indexed, and --fail-under clears unconditionally: a
// silently green gate, which is the one outcome this command must never
// produce.
func TestCovDiff_CustomGitPrefixesDoNotBreakPathMatching(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.git(t, "config", "diff.srcPrefix", "src/")
	f.git(t, "config", "diff.dstPrefix", "dst/")
	f.touchBothSymbols(t)

	if raw := f.gitOut(t, "diff", "--unified=0", "main~1...HEAD"); !strings.Contains(raw, "dst/pkg/a.go") {
		t.Skipf("this git does not honour diff.dstPrefix; nothing to harden against:\n%s", raw)
	}

	out, err := execCovDiff(t, f, "--json", "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	r := decodeCovDiff(t, out)
	if r.KnownLines != 15 || !r.Measurable {
		t.Fatalf("a configured diff prefix must not hide the changed lines: %+v", r)
	}
	if got := *r.Percent; got < 66.6 || got > 66.8 {
		t.Errorf("percent = %v, want ~66.7", got)
	}
	// The gate must still be able to fail.
	if _, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "80"); err == nil {
		t.Error("66.7% under a target of 80 must fail even with custom git diff prefixes")
	}
}

// forkDivergentBranch builds the history the three-dot form exists for: the
// base branch gains a commit AFTER this branch forked. Two-dot would charge
// that commit's lines to this branch; three-dot diffs from the merge base.
//
//	main:    C0 ---- C_main (rewrites lines 5-14, inside pkg.Covered)
//	feature:   \---- C_feat (rewrites lines 25-29, inside pkg.Uncovered)
func (f *covDiffFixture) forkDivergentBranch(t *testing.T) {
	t.Helper()
	f.git(t, "checkout", "-q", "-b", "feature")
	featChanged := map[int]bool{}
	for i := 25; i <= 29; i++ {
		featChanged[i] = true
	}
	f.write(t, "pkg/a.go", numberedGo(40, "feature", featChanged))
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "feature: touch the uncovered symbol")

	f.git(t, "checkout", "-q", "main")
	mainChanged := map[int]bool{}
	for i := 5; i <= 14; i++ {
		mainChanged[i] = true
	}
	f.write(t, "pkg/a.go", numberedGo(40, "mainline", mainChanged))
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "main: someone else touched the covered symbol")

	f.git(t, "checkout", "-q", "feature")
	f.indexFilesAtHEAD(t, "pkg/a.go")
}

// <base>...HEAD is the merge-base form: it reports what THIS branch did, not
// how it differs from the current tip of base. Two-dot would also report
// lines 5-14 -- work someone else landed on main after the fork -- and score
// them into this branch's patch coverage.
func TestCovDiff_ThreeDotExcludesCommitsLandedOnBaseAfterTheFork(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.forkDivergentBranch(t)

	// Establish that the two forms really disagree here, so the assertions
	// below cannot pass for a repo where the distinction is invisible.
	twoDot := f.gitOut(t, "diff", "--unified=0", "--numstat", "main", "HEAD")
	threeDot := f.gitOut(t, "diff", "--unified=0", "--numstat", "main...HEAD")
	if twoDot == threeDot {
		t.Fatalf("fixture is not divergent: two-dot and three-dot agree\n%s", twoDot)
	}

	out, err := execCovDiff(t, f, "--json", "--base", "main")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	r := decodeCovDiff(t, out)
	if r.ChangedLines != 5 {
		t.Errorf("changed_lines = %d, want 5 (only this branch's own commit; "+
			"15 means main's post-fork commit was charged to the branch)", r.ChangedLines)
	}
	if r.KnownLines != 5 {
		t.Errorf("known_lines = %d, want 5", r.KnownLines)
	}
	if r.Percent == nil || *r.Percent != 0 {
		t.Errorf("percent = %v, want 0: the branch only touched the uncovered symbol", r.Percent)
	}
	if len(r.Covered) != 0 {
		t.Errorf("covered = %+v, want none: pkg.Covered was changed on main, not here", r.Covered)
	}
	if len(r.Uncovered) != 1 || r.Uncovered[0].Symbol != "pkg.Uncovered" || r.Uncovered[0].Lines != 5 {
		t.Errorf("uncovered = %+v, want pkg.Uncovered with 5 lines", r.Uncovered)
	}
}

// commitTouch rewrites [from,to] of pkg/a.go, commits it, and re-indexes --
// the whole setup for a run whose patch percentage is an exact round number.
func (f *covDiffFixture) commitTouch(t *testing.T, from, to int, marker string) {
	t.Helper()
	changed := map[int]bool{}
	for i := from; i <= to; i++ {
		changed[i] = true
	}
	f.write(t, "pkg/a.go", numberedGo(40, marker, changed))
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", marker)
	f.indexFilesAtHEAD(t, "pkg/a.go")
}

// The documented deviation from a strict reading of "fail under N": a run
// that exactly MEETS the target passes, which is Codecov's convention. Pinned
// because flipping the comparison to > would fail a run that hit its target
// precisely, and nothing else in the suite would notice.
func TestCovDiff_FailUnderPassesOnExactEquality(t *testing.T) {
	t.Run("100 meets 100", func(t *testing.T) {
		f := newCovDiffFixture(t)
		f.seedSymbolsAndCoverage(t)
		f.commitTouch(t, 5, 14, "covered-only") // inside pkg.Covered: 10/10 stmts

		out, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "100")
		if err != nil {
			t.Fatalf("100%% patch coverage must MEET a target of 100: %v\n%s", err, out)
		}
		if !strings.Contains(out, "PASS  patch coverage 100.0% meets --fail-under 100") {
			t.Errorf("the verdict line must say it passed:\n%s", out)
		}
	})

	t.Run("0 meets 0", func(t *testing.T) {
		f := newCovDiffFixture(t)
		f.seedSymbolsAndCoverage(t)
		f.commitTouch(t, 25, 29, "uncovered-only") // inside pkg.Uncovered: 0/8 stmts

		out, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "0")
		if err != nil {
			t.Fatalf("0%% patch coverage must MEET a target of 0: %v\n%s", err, out)
		}
		// ...and the same run is genuinely below anything higher, so the pass
		// above is equality and not an unconditional green.
		if _, err := execCovDiff(t, f, "--base", "main~1", "--fail-under", "0.1"); err == nil {
			t.Error("0% patch coverage must fail a target of 0.1")
		}
	})
}

// The documented JSON contract says these are arrays. A nil slice marshals as
// null, which is a runtime error in the PR-comment bot the payload exists for.
func TestCovDiff_JSONEmptyBucketsAreArraysNotNull(t *testing.T) {
	f := newCovDiffFixture(t)
	f.seedSymbolsAndCoverage(t)
	f.write(t, "docs/readme.md", "# atlas\n\nnow with prose\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "docs only")

	out, err := execCovDiff(t, f, "--json", "--base", "main~1")
	if err != nil {
		t.Fatalf("cov diff --json: %v\n%s", err, out)
	}
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	for _, key := range []string{
		"uncovered", "partial", "covered", "unknown", "files", "features", "stale_index_files",
	} {
		raw, ok := env.Result[key]
		if !ok {
			t.Errorf("result is missing the documented %q key", key)
			continue
		}
		if string(raw) == "null" {
			t.Errorf("result[%q] marshalled as null; the contract is an array", key)
		}
	}
}
