package affected

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The parser tests above feed canned output. This one drives the real `git`
// binary end to end, because the shape of that output is a contract with a
// tool we do not control: a change in git's default rename detection or hunk
// header format would break selection silently, and the parser tests would
// keep passing.
func TestGitCLI_AgainstARealRepository(t *testing.T) {
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...) //nolint:gosec // fixed verbs, temp dir.
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=atlas", "GIT_AUTHOR_EMAIL=atlas@example.com",
			"GIT_COMMITTER_NAME=atlas", "GIT_COMMITTER_EMAIL=atlas@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	run("init", "-q", "-b", "main")
	write("checkout.go", "package billing\n\nfunc Checkout() error {\n\treturn nil\n}\n")
	write("README.md", "hello\n")
	run("add", ".")
	run("commit", "-qm", "base")

	// Edit exactly one line, deep enough in the file that a wrong hunk offset
	// would be visible rather than coincidentally right.
	write("checkout.go", "package billing\n\nfunc Checkout() error {\n\treturn errBoom\n}\n")
	write("refund.go", "package billing\n\nfunc Refund() {}\n")
	run("add", ".")
	run("commit", "-qm", "change")

	g := NewGit(dir)
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
