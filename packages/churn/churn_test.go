package churn

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeGit is the GitRunner seam under test. Keyed by git subcommand
// (args[0]) because every call Mine makes uses a distinct one.
type fakeGit struct {
	out   map[string]string
	err   map[string]error
	calls [][]string
}

func (f *fakeGit) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if e, ok := f.err[args[0]]; ok {
		return "", e
	}
	return f.out[args[0]], nil
}

func (f *fakeGit) argsFor(sub string) []string {
	for _, c := range f.calls {
		if c[0] == sub {
			return c
		}
	}
	return nil
}

const (
	rs = "\x1e" // record separator: starts a commit header line
	fs = "\x1f" // field separator inside a commit header line
)

// commit renders one commit in the exact shape Mine's `git log` format
// produces, so the parser is exercised against real output layout.
func commit(sha, email string, at time.Time, subject string, entries ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s%s%s%s%d%s%s\n", rs, sha, fs, email, fs, at.Unix(), fs, subject)
	for _, e := range entries {
		b.WriteString(e + "\n")
	}
	return b.String()
}

func tracked(paths ...string) string { return strings.Join(paths, "\x00") + "\x00" }

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return testNow }

func daysAgo(d int) time.Time { return testNow.AddDate(0, 0, -d) }

// mine is the common harness: wire the fake, run Mine, fail on error.
func mine(t *testing.T, g *fakeGit, opts Options) *Report {
	t.Helper()
	opts.Repo = "/repo"
	opts.Git = g
	if opts.Now == nil {
		opts.Now = fixedNow
	}
	r, err := Mine(context.Background(), opts)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return r
}

// -----------------------------------------------------------------------------
// Basic mining
// -----------------------------------------------------------------------------

func TestMine_CountsCommitsAndDistinctAuthors(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("a.go", "b.go"),
		"log": commit("c1", "ann@x", daysAgo(1), "feat: a", "M\ta.go", "M\tb.go") +
			commit("c2", "bob@x", daysAgo(2), "fix: a", "M\ta.go") +
			commit("c3", "ann@x", daysAgo(3), "feat: a again", "M\ta.go"),
	}}
	r := mine(t, g, Options{})

	a, ok := r.File("a.go")
	if !ok {
		t.Fatalf("a.go missing from report")
	}
	if a.Commits != 3 {
		t.Errorf("a.go commits = %d, want 3", a.Commits)
	}
	if a.Authors != 2 {
		t.Errorf("a.go authors = %d, want 2", a.Authors)
	}
	if !a.LastCommit.Equal(daysAgo(1)) {
		t.Errorf("a.go last commit = %v, want %v", a.LastCommit, daysAgo(1))
	}
	b, _ := r.File("b.go")
	if b.Commits != 1 {
		t.Errorf("b.go commits = %d, want 1", b.Commits)
	}
	if a.Score <= b.Score {
		t.Errorf("a.go score %.2f should exceed b.go %.2f", a.Score, b.Score)
	}
}

func TestMine_PassesWindowAndRenameDetectionToGit(t *testing.T) {
	g := &fakeGit{out: map[string]string{"rev-parse": "false\n", "ls-files": "", "log": ""}}
	mine(t, g, Options{Window: 30 * 24 * time.Hour})

	args := strings.Join(g.argsFor("log"), " ")
	for _, want := range []string{"--name-status", "-M", "--no-merges", "--since=" + daysAgo(30).Format(time.RFC3339)} {
		if !strings.Contains(args, want) {
			t.Errorf("git log args %q missing %q", args, want)
		}
	}
	if strings.Contains(args, "--follow") {
		t.Errorf("git log must not use --follow (O(files) subprocesses): %q", args)
	}
}

// -----------------------------------------------------------------------------
// Trap 1: moves, sweeps and chores are not churn
// -----------------------------------------------------------------------------

func TestMine_PureRenameIsNotChurnButCarriesHistoryForward(t *testing.T) {
	// Newest first, as git log emits: the rename happened yesterday, the
	// two real edits happened before it under the old path.
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("new.go"),
		"log": commit("c1", "ann@x", daysAgo(1), "refactor: move", "R100\told.go\tnew.go") +
			commit("c2", "ann@x", daysAgo(2), "fix: bug", "M\told.go") +
			commit("c3", "bob@x", daysAgo(3), "feat: thing", "M\told.go"),
	}}
	r := mine(t, g, Options{})

	if _, ok := r.File("old.go"); ok {
		t.Errorf("old.go should have been folded into new.go, not reported separately")
	}
	n, ok := r.File("new.go")
	if !ok {
		t.Fatalf("new.go missing from report")
	}
	if n.Commits != 2 {
		t.Errorf("new.go commits = %d, want 2 (the move itself is not churn)", n.Commits)
	}
	if n.Authors != 2 {
		t.Errorf("new.go authors = %d, want 2 (carried across the rename)", n.Authors)
	}
}

func TestMine_RenameWithEditCountsAsChurn(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("new.go"),
		"log":       commit("c1", "ann@x", daysAgo(1), "refactor: move and rework", "R085\told.go\tnew.go"),
	}}
	r := mine(t, g, Options{})
	n, ok := r.File("new.go")
	if !ok || n.Commits != 1 {
		t.Fatalf("rename-with-edit should count: got %+v ok=%v", n, ok)
	}
}

func TestMine_BulkCommitExcluded(t *testing.T) {
	entries := make([]string, 0, 60)
	paths := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		p := fmt.Sprintf("f%02d.go", i)
		entries = append(entries, "M\t"+p)
		paths = append(paths, p)
	}
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked(paths...),
		"log": commit("c1", "ann@x", daysAgo(1), "reindent everything", entries...) +
			commit("c2", "ann@x", daysAgo(2), "fix: real change", "M\tf00.go"),
	}}
	r := mine(t, g, Options{})

	if r.CommitsSkippedBulk != 1 {
		t.Errorf("CommitsSkippedBulk = %d, want 1", r.CommitsSkippedBulk)
	}
	f0, _ := r.File("f00.go")
	if f0.Commits != 1 {
		t.Errorf("f00.go commits = %d, want 1 (bulk commit excluded)", f0.Commits)
	}
	if _, ok := r.File("f42.go"); ok {
		t.Errorf("f42.go was only in the bulk commit; it must not appear")
	}
}

func TestMine_BulkThresholdDisabledByZero(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("a.go"),
		"log":       commit("c1", "ann@x", daysAgo(1), "sweep", "M\ta.go"),
	}}
	r := mine(t, g, Options{MaxFilesPerCommit: -1})
	if r.CommitsSkippedBulk != 0 {
		t.Errorf("negative MaxFilesPerCommit must disable the rule, got %d", r.CommitsSkippedBulk)
	}
}

func TestMine_DefaultMessageExclusionsDropChores(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("a.go"),
		"log": commit("c1", "ann@x", daysAgo(1), "chore(deps): bump modernc.org/sqlite", "M\ta.go") +
			commit("c2", "ann@x", daysAgo(2), "style: gofmt the tree", "M\ta.go") +
			commit("c3", "ann@x", daysAgo(3), "feat: real work", "M\ta.go"),
	}}
	r := mine(t, g, Options{})
	a, _ := r.File("a.go")
	if a.Commits != 1 {
		t.Errorf("a.go commits = %d, want 1 (chore + style excluded)", a.Commits)
	}
	if r.CommitsSkippedMessage != 2 {
		t.Errorf("CommitsSkippedMessage = %d, want 2", r.CommitsSkippedMessage)
	}
}

func TestMine_EmptyExcludeMessagesDisablesTheRule(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("a.go"),
		"log":       commit("c1", "ann@x", daysAgo(1), "chore(deps): bump", "M\ta.go"),
	}}
	r := mine(t, g, Options{ExcludeMessages: []string{}})
	a, _ := r.File("a.go")
	if a.Commits != 1 {
		t.Errorf("explicit empty ExcludeMessages must disable the rule, got %d commits", a.Commits)
	}
}

func TestMine_InvalidExcludePatternIsAnError(t *testing.T) {
	g := &fakeGit{out: map[string]string{"rev-parse": "false\n", "ls-files": "", "log": ""}}
	_, err := Mine(context.Background(), Options{
		Repo: "/repo", Git: g, Now: fixedNow, ExcludeMessages: []string{"("},
	})
	if err == nil {
		t.Fatalf("want an error for an unparseable exclude pattern")
	}
}

// -----------------------------------------------------------------------------
// Trap 2: recency outweighs volume
// -----------------------------------------------------------------------------

func TestMine_RecentBurstBeatsOlderLargerBurst(t *testing.T) {
	var log strings.Builder
	// hot.go: 5 commits in the last week.
	for i := 0; i < 5; i++ {
		log.WriteString(commit(fmt.Sprintf("h%d", i), "ann@x", daysAgo(i+1), "feat: hot", "M\thot.go"))
	}
	// cold.go: 20 commits, all roughly two years back.
	for i := 0; i < 20; i++ {
		log.WriteString(commit(fmt.Sprintf("c%d", i), "ann@x", daysAgo(700+i), "feat: cold", "M\tcold.go"))
	}
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("hot.go", "cold.go"),
		"log":       log.String(),
	}}
	// Widen the window past the cold burst so the decay, not the window,
	// is what separates them.
	r := mine(t, g, Options{Window: 1000 * 24 * time.Hour})

	hot, _ := r.File("hot.go")
	cold, _ := r.File("cold.go")
	if cold.Commits <= hot.Commits {
		t.Fatalf("fixture broken: cold should have more raw commits")
	}
	if hot.Score <= cold.Score {
		t.Errorf("recency must outweigh volume: hot=%.2f cold=%.2f", hot.Score, cold.Score)
	}
}

func TestMine_HalfLifeHalvesTheWeight(t *testing.T) {
	half := 90 * 24 * time.Hour
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("now.go", "old.go"),
		"log": commit("c1", "ann@x", testNow, "feat: now", "M\tnow.go") +
			commit("c2", "ann@x", testNow.Add(-half), "feat: old", "M\told.go"),
	}}
	r := mine(t, g, Options{Window: 365 * 24 * time.Hour, HalfLife: half})
	n, _ := r.File("now.go")
	o, _ := r.File("old.go")
	if got := o.WeightedCommits / n.WeightedCommits; got < 0.49 || got > 0.51 {
		t.Errorf("one half-life should halve the weight, ratio = %.4f", got)
	}
}

// -----------------------------------------------------------------------------
// Trap 3: author diversity
// -----------------------------------------------------------------------------

func TestMine_AuthorDiversityRaisesScoreAndCanBeDisabled(t *testing.T) {
	logFor := func(file string, emails ...string) string {
		var b strings.Builder
		for i, e := range emails {
			b.WriteString(commit(fmt.Sprintf("%s%d", file, i), e, daysAgo(i+1), "feat: x", "M\t"+file))
		}
		return b.String()
	}
	out := map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("solo.go", "crowd.go"),
		"log": logFor("solo.go", "ann@x", "ann@x", "ann@x") +
			logFor("crowd.go", "ann@x", "bob@x", "cid@x"),
	}
	r := mine(t, &fakeGit{out: out}, Options{})
	solo, _ := r.File("solo.go")
	crowd, _ := r.File("crowd.go")
	if crowd.Score <= solo.Score {
		t.Errorf("three authors should out-rank one: crowd=%.2f solo=%.2f", crowd.Score, solo.Score)
	}

	off := mine(t, &fakeGit{out: out}, Options{AuthorBonus: -1})
	soloOff, _ := off.File("solo.go")
	crowdOff, _ := off.File("crowd.go")
	if crowdOff.Score != soloOff.Score {
		t.Errorf("author diversity off: scores should match, got %.4f vs %.4f",
			crowdOff.Score, soloOff.Score)
	}
}

// -----------------------------------------------------------------------------
// Trap 4: unknown is not zero
// -----------------------------------------------------------------------------

func TestMine_ShallowCloneIsReportedAndMakesEverythingUnknown(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "true\n",
		"ls-files":  tracked("a.go"),
		"log":       commit("c1", "ann@x", daysAgo(1), "feat: a", "M\ta.go"),
	}}
	r := mine(t, g, Options{})
	if !r.Shallow {
		t.Fatalf("shallow repository not detected")
	}
	if len(r.Warnings) == 0 {
		t.Errorf("a shallow clone must produce a warning; rankings taken in CI depend on it")
	}
	fc := r.ForFiles([]string{"a.go"})
	if fc.Status != StatusUnknown {
		t.Errorf("shallow clone churn status = %q, want %q", fc.Status, StatusUnknown)
	}
	if fc.Score != DefaultUnknownScore {
		t.Errorf("unknown churn scored %.2f, want the neutral %.2f", fc.Score, DefaultUnknownScore)
	}
}

func TestForFiles_UntrackedFileIsUnknownNotZero(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("tracked.go"),
		"log":       "",
	}}
	r := mine(t, g, Options{})

	brandNew := r.ForFiles([]string{"brand_new.go"})
	if brandNew.Status != StatusUnknown {
		t.Errorf("untracked file status = %q, want unknown", brandNew.Status)
	}
	if brandNew.Score == 0 {
		t.Errorf("untracked file must not score 0 — that is 'dead', not 'unknown'")
	}

	quiet := r.ForFiles([]string{"tracked.go"})
	if quiet.Status != StatusKnown {
		t.Errorf("tracked-but-quiet file status = %q, want known", quiet.Status)
	}
	if quiet.Score != 0 {
		t.Errorf("tracked file with no commits in the window must score 0, got %.2f", quiet.Score)
	}
}

func TestForFiles_NoPathsIsUnknown(t *testing.T) {
	g := &fakeGit{out: map[string]string{"rev-parse": "false\n", "ls-files": "", "log": ""}}
	r := mine(t, g, Options{})
	if got := r.ForFiles(nil); got.Status != StatusUnknown {
		t.Errorf("no paths → status %q, want unknown", got.Status)
	}
}

// -----------------------------------------------------------------------------
// Roll-up
// -----------------------------------------------------------------------------

func TestForFiles_TakesTheHottestFileAndNamesIt(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 4; i++ {
		log.WriteString(commit(fmt.Sprintf("h%d", i), "ann@x", daysAgo(i+1), "feat: hot", "M\thot.go"))
	}
	log.WriteString(commit("q0", "ann@x", daysAgo(200), "feat: quiet", "M\tquiet.go"))
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("hot.go", "quiet.go"),
		"log":       log.String(),
	}}
	r := mine(t, g, Options{})

	fc := r.ForFiles([]string{"quiet.go", "hot.go"})
	if fc.HotFile != "hot.go" {
		t.Errorf("HotFile = %q, want hot.go", fc.HotFile)
	}
	hot, _ := r.File("hot.go")
	if fc.Score != hot.Score {
		t.Errorf("roll-up score %.2f should equal the hottest file's %.2f", fc.Score, hot.Score)
	}
	if fc.Commits != 4 {
		t.Errorf("roll-up commits = %d, want the hot file's 4", fc.Commits)
	}
	if fc.FilesKnown != 2 || fc.FilesUnknown != 0 {
		t.Errorf("known/unknown = %d/%d, want 2/0", fc.FilesKnown, fc.FilesUnknown)
	}
}

func TestForFiles_MixedKnownAndUnknownPrefersEvidence(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("known.go"),
		"log":       commit("c1", "ann@x", daysAgo(1), "feat: x", "M\tknown.go"),
	}}
	r := mine(t, g, Options{})
	fc := r.ForFiles([]string{"known.go", "ghost.go"})
	if fc.Status != StatusKnown {
		t.Errorf("status = %q, want known — one file with history is evidence", fc.Status)
	}
	if fc.FilesUnknown != 1 {
		t.Errorf("FilesUnknown = %d, want 1", fc.FilesUnknown)
	}
}

// -----------------------------------------------------------------------------
// Failure modes
// -----------------------------------------------------------------------------

func TestMine_NotAGitRepositoryIsAnError(t *testing.T) {
	g := &fakeGit{
		out: map[string]string{},
		err: map[string]error{"rev-parse": errBoom},
	}
	if _, err := Mine(context.Background(), Options{Repo: "/repo", Git: g, Now: fixedNow}); err == nil {
		t.Fatalf("want an error when git is unusable")
	}
}

func TestMine_RepoRequired(t *testing.T) {
	if _, err := Mine(context.Background(), Options{}); err == nil {
		t.Fatalf("want an error when Repo is empty")
	}
}

var errBoom = fmt.Errorf("git exploded")
