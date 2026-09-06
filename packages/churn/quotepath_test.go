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

// Mine joins two git outputs by string equality: `git log --name-status`
// names the files, `git ls-files -z` says which are tracked. The two do not
// agree on how to spell a path unless we make them. With core.quotePath at
// its default (true) the log renders any non-ASCII byte as an octal escape
// inside literal quotes — `"caf\303\251.go"` — while ls-files -z emits the
// raw bytes. Every accented, CJK or emoji path in the repository then fails
// to join and silently scores no churn.

func TestParseStatus_UnquotesGitCQuotedPaths(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantOld string
		want    string
	}{
		{
			name: "non-ascii edit",
			line: "M\t\"caf\\303\\251.go\"",
			want: "café.go",
		},
		{
			name:    "non-ascii rename names both sides",
			line:    "R100\t\"caf\\303\\251.go\"\t\"th\\303\\251.go\"",
			wantOld: "café.go",
			want:    "thé.go",
		},
		{
			name: "a path git quotes whatever core.quotePath says",
			line: "M\t\"say \\\"hi\\\".go\"",
			want: `say "hi".go`,
		},
		{
			name: "an unquoted path is left exactly as it is",
			line: "M\tplain.go",
			want: "plain.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := parseStatus(tc.line)
			if !ok {
				t.Fatalf("parseStatus(%q) rejected the line", tc.line)
			}
			if e.Path != tc.want {
				t.Errorf("path = %q, want %q", e.Path, tc.want)
			}
			if e.Old != tc.wantOld {
				t.Errorf("old = %q, want %q", e.Old, tc.wantOld)
			}
		})
	}
}

// The end-to-end shape of the bug: a quoted log path and a raw tracked path
// must reach the same key, or ForFiles reports the file as unknown.
func TestMine_QuotedLogPathJoinsRawTrackedPath(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"rev-parse": "false\n",
		"ls-files":  tracked("café.go"),
		"log": commit("a1", "ann@example.com", daysAgo(1), "feat: work",
			"M\t\"caf\\303\\251.go\""),
	}}
	rep := mine(t, g, Options{})

	if _, ok := rep.File("café.go"); !ok {
		t.Fatalf("café.go absent from the report; files = %v", rep.Files)
	}
	fc := rep.ForFiles([]string{"café.go"})
	if fc.Status != StatusKnown {
		t.Errorf("roll-up status = %q, want %q: the log and ls-files spellings did not join",
			fc.Status, StatusKnown)
	}
	if fc.Commits != 1 {
		t.Errorf("commits = %d, want 1", fc.Commits)
	}
}

// And the runner half: whatever the repository's own core.quotePath says,
// the log we parse must carry raw paths. The fixture sets core.quotePath
// true explicitly so this cannot pass by accident on a machine whose git
// config already disabled quoting.
func TestGitRunner_OverridesRepositoryQuotePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
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
	run("init", "-q", "-b", "main")
	run("config", "user.email", "ann@example.com")
	run("config", "user.name", "Ann")
	run("config", "core.quotePath", "true")

	const name = "café.go"
	if err := os.WriteFile(filepath.Join(repo, name), []byte("package p\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "feat: add the accented file")

	out, err := NewGitRunner(repo).Run(context.Background(), "log", "--name-status", "--format=")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, name) {
		t.Errorf("git log did not name %q rawly (core.quotePath was not overridden):\n%q", name, out)
	}

	// The whole pass, against the same repository: the file must be known.
	rep, err := Mine(context.Background(), Options{Repo: repo, Window: time.Hour})
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	if fc := rep.ForFiles([]string{name}); fc.Status != StatusKnown || fc.Commits != 1 {
		t.Errorf("roll-up for %q = %+v, want known with 1 commit", name, fc)
	}
}
