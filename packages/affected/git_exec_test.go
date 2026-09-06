package affected

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo is a throwaway repository driven through the real `git` binary.
type gitRepo struct {
	t   *testing.T
	dir string
	bin string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	r := &gitRepo{t: t, dir: t.TempDir(), bin: bin}
	r.run("init", "-q", "-b", "main")
	return r
}

func (r *gitRepo) run(args ...string) {
	r.t.Helper()
	cmd := exec.Command(r.bin, args...) //nolint:gosec // fixed verbs, temp dir.
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=atlas", "GIT_AUTHOR_EMAIL=atlas@example.com",
		"GIT_COMMITTER_NAME=atlas", "GIT_COMMITTER_EMAIL=atlas@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (r *gitRepo) write(name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o600); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

func (r *gitRepo) commit(msg string) {
	r.t.Helper()
	r.run("add", "-A")
	r.run("commit", "-qm", msg)
}

// The parser tests feed canned output. This one drives the real `git` binary
// end to end, because the shape of that output is a contract with a tool we do
// not control: a change in git's default rename detection or hunk header
// format would break selection silently, and the parser tests would keep
// passing.
func TestGitCLI_AgainstARealRepository(t *testing.T) {
	r := newGitRepo(t)

	r.write("checkout.go", "package billing\n\nfunc Checkout() error {\n\treturn nil\n}\n")
	r.write("README.md", "hello\n")
	r.commit("base")

	// Edit exactly one line, deep enough in the file that a wrong hunk offset
	// would be visible rather than coincidentally right.
	r.write("checkout.go", "package billing\n\nfunc Checkout() error {\n\treturn errBoom\n}\n")
	r.write("refund.go", "package billing\n\nfunc Refund() {}\n")
	r.commit("change")

	g := NewGit(r.dir)
	ctx := context.Background()

	files, err := g.ChangedFiles(ctx, "HEAD~1")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	want := map[string]bool{"checkout.go": true, "refund.go": true}
	if len(files) != 2 {
		t.Fatalf("ChangedFiles = %v, want checkout.go and refund.go", files)
	}
	for _, f := range files {
		if !want[f] {
			t.Errorf("ChangedFiles returned unexpected %q", f)
		}
	}

	lines, err := g.ChangedLines(ctx, "HEAD~1")
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}
	got := lines["checkout.go"]
	if len(got) != 1 || got[0].Start != 4 || got[0].End != 4 {
		t.Fatalf("checkout.go ranges = %+v, want a single line-4 span", got)
	}
	if _, ok := lines["refund.go"]; !ok {
		t.Errorf("ChangedLines = %+v, want the added file present", lines)
	}
}

// The range form is the whole reason this command is usable on a real branch,
// and a linear fixture cannot test it: with no divergence `since..HEAD` and
// `since...HEAD` return the same thing, so a two-dot regression would pass
// unnoticed. This fixture branches:
//
//	base ── feature-work        <- HEAD (the branch under review)
//	  └──── main-moved-on       <- main (which gained a commit meanwhile)
//
// Two-dot `main..HEAD` reports everything that differs between the two TIPS,
// which includes main-moved-on's file appearing as a DELETION — i.e. a
// three-line PR reported as touching files the author never opened, and,
// worse, symbols attributed to it that belong to someone else's commit.
// Three-dot diffs against the merge base and reports the branch's own work.
func TestGitCLI_ThreeDotDiffsAgainstTheMergeBase(t *testing.T) {
	r := newGitRepo(t)

	r.write("checkout.go", "package billing\n\nfunc Checkout() error {\n\treturn nil\n}\n")
	r.commit("base")

	r.run("checkout", "-q", "-b", "feature")
	r.write("checkout.go", "package billing\n\nfunc Checkout() error {\n\treturn errBoom\n}\n")
	r.commit("the change under review")

	// main moves on independently, touching a file the branch never saw.
	r.run("checkout", "-q", "main")
	r.write("ledger.go", "package billing\n\nfunc Post() {}\n")
	r.commit("someone else's commit, landed on main meanwhile")
	r.run("checkout", "-q", "feature")

	g := NewGit(r.dir)
	ctx := context.Background()

	files, err := g.ChangedFiles(ctx, "main")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "checkout.go" {
		t.Fatalf("ChangedFiles = %v, want just [checkout.go]; ledger.go landed on main, "+
			"not on this branch — reporting it means the range is two-dot, not three-dot", files)
	}

	lines, err := g.ChangedLines(ctx, "main")
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}
	if _, ok := lines["ledger.go"]; ok {
		t.Errorf("ChangedLines = %+v, want no hunks for a file only main touched", lines)
	}
	got := lines["checkout.go"]
	if len(got) != 1 || got[0].Start != 4 || got[0].End != 4 {
		t.Fatalf("checkout.go ranges = %+v, want the branch's own single line-4 edit", got)
	}
}
