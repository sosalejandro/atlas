package churn

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fake-runner tests above pin the parser against recorded output. This
// one pins the recording itself: that the exact flags and --format string
// Mine sends really produce the layout the parser expects, on a real git.
// Without it a git-side format change would leave every unit test green and
// every real ranking empty.
func TestMine_AgainstARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com",
			"GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "ann@example.com")
	run("config", "user.name", "Ann")

	write("old.go", "package p\n\nfunc A() {}\n")
	run("add", "-A")
	run("commit", "-q", "-m", "feat: add A")

	write("old.go", "package p\n\nfunc A() { println(1) }\n")
	run("add", "-A")
	run("commit", "-q", "-m", "fix: A prints")

	// A pure move, byte-for-byte, in its own commit so git reports R100.
	run("mv", "old.go", "new.go")
	run("commit", "-q", "-m", "refactor: relocate A")

	// A sweep that must be excluded by the default message patterns.
	write("swept.go", "package p\n")
	run("add", "-A")
	run("commit", "-q", "-m", "chore(deps): bump everything")

	rep, err := Mine(context.Background(), Options{Repo: repo, Window: time.Hour})
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	if rep.Shallow {
		t.Errorf("a freshly-initialised repo is not shallow")
	}
	got, ok := rep.File("new.go")
	if !ok {
		t.Fatalf("new.go absent; files = %v", rep.Files)
	}
	if got.Commits != 2 {
		t.Errorf("new.go commits = %d, want 2 (two edits under old.go, move not counted)", got.Commits)
	}
	if _, ok := rep.File("old.go"); ok {
		t.Errorf("old.go should have been folded into new.go")
	}
	if rep.CommitsSkippedMessage != 1 {
		t.Errorf("CommitsSkippedMessage = %d, want 1 (the chore commit)", rep.CommitsSkippedMessage)
	}
	// swept.go is tracked but every commit touching it was excluded: quiet,
	// not unknown.
	if fc := rep.ForFiles([]string{"swept.go"}); fc.Status != StatusKnown || fc.Score != 0 {
		t.Errorf("swept.go roll-up = %+v, want known/0", fc)
	}
	if fc := rep.ForFiles([]string{"never_existed.go"}); fc.Status != StatusUnknown {
		t.Errorf("untracked path roll-up = %+v, want unknown", fc)
	}
}
