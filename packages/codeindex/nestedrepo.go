package codeindex

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A git repository nested inside the scanned tree is not part of it.
//
// Atlas's walk skipped by NAME -- vendor, node_modules, anything starting
// with a dot, plus whatever .atlas.yaml added. Nothing detected a repository
// BOUNDARY, so a clone, submodule or worktree sitting under the scan root
// was indexed as though its files belonged to this codebase.
//
// The inflated file count is not the damage. The damage is that every number
// downstream is then computed over a codebase that is not this one: symbol
// and edge counts, the coverage denominator (diluted by files no test here
// could execute), feature linkage, sql capabilities. And it is silent in the
// worst direction -- doctor reports the index is fresh, which it faithfully
// is, for a tree that includes somebody else's repository.
//
// This was not hypothetical. A dogfood run on this repo reported 584 SQL
// operations over 102 tables where a clean checkout reports 145 over 27,
// because three git worktrees were sitting under .claude/worktrees.

// isNestedRepoRoot reports whether dir is the root of a git repository other
// than the one being scanned.
//
// Two details decide whether this is correct rather than merely effective:
//
//   - `.git` may be a FILE, not a directory. That is exactly what a git
//     worktree has (it holds `gitdir: ...`), and it is the case that
//     produced the report above. os.Lstat accepts both; an IsDir() check
//     would have missed the very instance this exists for.
//   - The scan root is exempt. Atlas is nearly always run at the top of a
//     repository, which by definition contains .git, so skipping the root
//     would make every scan return nothing -- the same bug, spectacularly,
//     in the other direction. TestNestedRepo_ScanRootIsNeverSkipped pins it.
func isNestedRepoRoot(dir, scanRoot string) bool {
	if dir == scanRoot {
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// nestedRepos collects the repository roots a walk declined to descend into,
// so they can be reported rather than silently dropped. A user who wanted
// them included has to be able to find out why they were not, without
// reading the issue that produced this file.
type nestedRepos struct {
	seen map[string]bool
}

func (n *nestedRepos) add(rel string) {
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	n.seen[rel] = true
}

// paths returns the skipped repository roots, repo-relative and sorted.
func (n *nestedRepos) paths() []string {
	if len(n.seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(n.seen))
	for p := range n.seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (n *nestedRepos) len() int { return len(n.seen) }

// nestedRepoWarning renders what the scan declined to index, or "" if it
// declined nothing.
func nestedRepoWarning(n *nestedRepos) string {
	count := n.len()
	if count == 0 {
		return ""
	}
	paths := n.paths()
	const maxListed = 5
	listed := paths
	if len(listed) > maxListed {
		listed = listed[:maxListed]
	}
	noun, verb, pronoun := "repositories", "were", "their"
	if count == 1 {
		noun, verb, pronoun = "repository", "was", "its"
	}
	msg := fmt.Sprintf("%d nested git %s %s not indexed (%s",
		count, noun, verb, strings.Join(listed, ", "))
	if len(paths) > maxListed {
		msg += fmt.Sprintf(", +%d more", len(paths)-maxListed)
	}
	return msg + fmt.Sprintf("); %s files belong to another repository, so "+
		"counting them would dilute every coverage denominator here. "+
		"Set include_nested_repos to index them anyway.", pronoun)
}
