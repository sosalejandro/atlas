package codeindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkRepo writes a minimal Go tree at dir and makes it look like a git
// repository. `.git` is written as a DIRECTORY here and as a FILE in the
// worktree test below, because those are the two shapes on disk and only one
// of them is what a worktree has.
func mkRepo(t *testing.T, dir string, gitAsFile bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	src := "package pkg\n\n// @atlas:feature nested.one\nfunc One() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	git := filepath.Join(dir, ".git")
	if gitAsFile {
		// Exactly what `git worktree add` leaves behind.
		if err := os.WriteFile(git, []byte("gitdir: /elsewhere/.git/worktrees/w\n"), 0o644); err != nil {
			t.Fatalf("write .git file: %v", err)
		}
		return
	}
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
}

func indexNested(t *testing.T, root string, opts Options) *Index {
	t.Helper()
	opts.HashFiles = true
	idx, err := IndexProject(context.Background(), root, opts)
	if err != nil {
		t.Fatalf("IndexProject(%s): %v", root, err)
	}
	return idx
}

// The defect, as a test: a repository nested in the tree was indexed as part
// of it. Found because a dogfood run reported 584 SQL operations over 102
// tables where a clean checkout reports 145 over 27 -- three git worktrees
// were sitting under .claude/worktrees.
func TestNestedRepo_IsNotIndexedAsPartOfThisOne(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root, false)                               // the repo being scanned
	mkRepo(t, filepath.Join(root, "vendorclone"), false) // a separate repository

	idx := indexNested(t, root, Options{})

	for path := range idx.FileHashes {
		if strings.HasPrefix(filepath.ToSlash(path), "vendorclone/") {
			t.Errorf("indexed %s, which belongs to a different repository", path)
		}
	}
	if got := len(idx.FileHashes); got != 1 {
		t.Errorf("hashed %d files, want 1 (only this repository's): %v", got, idx.FileHashes)
	}
	// Its annotations must not materialise features here either -- that is
	// how a nested repo's capabilities end up attributed to this product.
	if got := len(idx.Annotations); got != 1 {
		t.Errorf("collected %d annotations, want 1: %+v", got, idx.Annotations)
	}
}

// A git WORKTREE has `.git` as a file holding "gitdir: ...", not a
// directory. This is the shape that produced the original report, so an
// IsDir() check would pass every other test here and still miss the bug.
func TestNestedRepo_WorktreeGitFileCountsAsABoundary(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root, false)
	// NOT under a dot-directory. An earlier version of this test used
	// .worktrees/, which the walk already skips as hidden, so it passed
	// whatever isNestedRepoRoot did -- it never reached the boundary check.
	mkRepo(t, filepath.Join(root, "worktrees", "wt1"), true)

	idx := indexNested(t, root, Options{})
	for path := range idx.FileHashes {
		if strings.Contains(filepath.ToSlash(path), "wt1/") {
			t.Errorf("indexed %s from a git worktree", path)
		}
	}
}

// The bug in the other direction, and the more spectacular one: atlas is
// nearly always run at the top of a repository, which by definition contains
// .git. If the root were treated as a boundary every scan would return
// nothing.
func TestNestedRepo_ScanRootIsNeverSkipped(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root, false)

	idx := indexNested(t, root, Options{})
	if len(idx.FileHashes) == 0 {
		t.Fatal("the scan root was treated as a nested repository; every scan would return nothing")
	}
	if isNestedRepoRoot(root, root) {
		t.Error("isNestedRepoRoot reports the scan root as nested")
	}
}

// Skipping silently is its own failure: in every number that follows, a
// repository that was skipped is indistinguishable from one that was never
// there.
func TestNestedRepo_SkippingIsReported(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root, false)
	mkRepo(t, filepath.Join(root, "vendorclone"), false)

	idx := indexNested(t, root, Options{})
	var found string
	for _, w := range idx.Warnings {
		if strings.Contains(w, "nested git repositor") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("no warning names the skipped repository: %v", idx.Warnings)
	}
	if !strings.Contains(found, "vendorclone") {
		t.Errorf("the warning does not name which repository: %q", found)
	}
	if !strings.Contains(found, "include_nested_repos") {
		t.Errorf("the warning does not say how to opt in: %q", found)
	}
}

// Teams who consider a submodule part of their product can say so.
func TestNestedRepo_OptInRestoresTheOldBehaviour(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, root, false)
	mkRepo(t, filepath.Join(root, "vendorclone"), false)

	idx := indexNested(t, root, Options{IncludeNestedRepos: true})
	var sawNested bool
	for path := range idx.FileHashes {
		if strings.HasPrefix(filepath.ToSlash(path), "vendorclone/") {
			sawNested = true
		}
	}
	if !sawNested {
		t.Errorf("IncludeNestedRepos did not include the nested repository: %v", idx.FileHashes)
	}
}
