package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRewriteLine_Cases exercises the per-line rewrite engine across the
// shapes Atlas needs to support: Go double-slash, Python hash, /* */ open,
// tag-stripping, idempotency for already-migrated lines, suppressor bypass.
func TestRewriteLine_Cases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		in        string
		wantOut   string
		rewritten bool
	}{
		{
			name:      "go-double-slash-no-tags",
			in:        "// @testreg auth.login",
			wantOut:   "// @atlas:feature auth.login",
			rewritten: true,
		},
		{
			name:      "go-double-slash-with-tags",
			in:        "// @testreg auth.login #real #happy-path",
			wantOut:   "// @atlas:feature auth.login real happy-path",
			rewritten: true,
		},
		{
			name:      "leading-whitespace",
			in:        "    // @testreg pantry.add-item",
			wantOut:   "    // @atlas:feature pantry.add-item",
			rewritten: true,
		},
		{
			name:      "python-hash",
			in:        "# @testreg auth.login",
			wantOut:   "# @atlas:feature auth.login",
			rewritten: true,
		},
		{
			name:      "block-open",
			in:        "/* @testreg auth.login */",
			wantOut:   "/* @atlas:feature auth.login */",
			rewritten: true,
		},
		{
			name:      "already-migrated-no-touch",
			in:        "// @atlas:feature auth.login",
			wantOut:   "// @atlas:feature auth.login",
			rewritten: false,
		},
		{
			name:      "non-comment-line-no-touch",
			in:        "var x = \"@testreg auth.login\"",
			wantOut:   "var x = \"@testreg auth.login\"",
			rewritten: false,
		},
		{
			name:      "dashed-id",
			in:        "// @testreg meal-prep.batch-session",
			wantOut:   "// @atlas:feature meal-prep.batch-session",
			rewritten: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rew := rewriteLine(tc.in)
			if rew != tc.rewritten {
				t.Fatalf("rewritten = %v want %v", rew, tc.rewritten)
			}
			if got != tc.wantOut {
				t.Fatalf("got %q want %q", got, tc.wantOut)
			}
		})
	}
}

// TestRewriteMigrateAnnotations_RoundTrip writes a fixture file, runs the
// rewrite, asserts the count, then runs the rewrite again and asserts it
// is a no-op. Idempotency is load-bearing — a CI re-run mustn't churn the
// tree once the migration has been applied.
func TestRewriteMigrateAnnotations_RoundTrip(t *testing.T) {
	t.Parallel()
	src := []byte(`package x
// @testreg auth.login #real
// not annotated
// @testreg pantry.add-item
`)
	got, rewrites := rewriteMigrateAnnotations(src, "x.go")
	if len(rewrites) != 2 {
		t.Fatalf("expected 2 rewrites, got %d: %+v", len(rewrites), rewrites)
	}
	if !strings.Contains(string(got), "// @atlas:feature auth.login real") {
		t.Fatalf("missing rewritten line:\n%s", string(got))
	}

	// Second pass must be idempotent.
	out2, rewrites2 := rewriteMigrateAnnotations(got, "x.go")
	if len(rewrites2) != 0 {
		t.Fatalf("expected 0 rewrites on second pass, got %d", len(rewrites2))
	}
	if string(out2) != string(got) {
		t.Fatalf("output changed on second pass")
	}
}

// TestProcessMigrateFile_SuppressorSkip verifies the
// `// nolint:atlas-migrate` opt-out keeps a file untouched.
func TestProcessMigrateFile_SuppressorSkip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "x.go")
	body := []byte(`package x
// nolint:atlas-migrate
// @testreg auth.login
`)
	if err := os.WriteFile(file, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	touched, rewrites, err := processMigrateFile(file, dir, true)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if touched {
		t.Fatal("file with suppressor must NOT be touched")
	}
	if len(rewrites) != 0 {
		t.Fatalf("rewrites = %d, want 0", len(rewrites))
	}
	// Original content preserved on disk.
	got, _ := os.ReadFile(file)
	if string(got) != string(body) {
		t.Fatalf("file body modified despite suppressor")
	}
}

// TestProcessMigrateFile_DryRun confirms --dry-run reports candidates
// but never touches disk.
func TestProcessMigrateFile_DryRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "x.go")
	body := []byte("package x\n// @testreg auth.login\n")
	if err := os.WriteFile(file, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	touched, rewrites, err := processMigrateFile(file, dir, false)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if touched {
		t.Fatal("dry-run must NOT touch disk")
	}
	if len(rewrites) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(rewrites))
	}
	got, _ := os.ReadFile(file)
	if string(got) != string(body) {
		t.Fatalf("file body modified during dry-run")
	}
}

// TestSniffFramework_Filename verifies the heuristic that picks a
// framework from the filename. The CLI falls back to --framework when
// this returns "".
func TestSniffFramework_Filename(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"playwright-results.json": "playwright",
		"vitest-output.json":      "vitest",
		"jest.json":               "jest",
		"maestro.json":            "maestro",
		"gotest.json":             "go-test",
		"results.json":            "",
	}
	for in, want := range cases {
		if got := sniffFramework(in); got != want {
			t.Errorf("sniff(%q) = %q want %q", in, got, want)
		}
	}
}
