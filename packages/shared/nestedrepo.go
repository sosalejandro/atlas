package shared

import (
	"os"
	"path/filepath"
)

// IsNestedRepoRoot reports whether dir is the root of a git repository other
// than the one being scanned.
//
// It lives in shared because atlas has four independent directory walks --
// the annotation walk, the Go scanner, and the Python and TypeScript
// sub-scanners, the last two in their own languages. The rule has to be
// identical in all of them: a boundary honoured by three walkers and missed
// by the fourth leaks exactly the symbols the fourth produces, which is the
// bug this exists to fix, wearing a different hat.
//
// Two details decide whether this is correct rather than merely effective:
//
//   - `.git` may be a FILE, not a directory. That is exactly what a git
//     worktree has (it holds `gitdir: ...`), and it is the case that produced
//     the original report. os.Lstat accepts both; an IsDir() check would have
//     missed the very instance this exists for.
//   - The scan root is exempt. Atlas is nearly always run at the top of a
//     repository, which by definition contains .git, so treating the root as
//     a boundary would make every scan return nothing -- the same bug in the
//     other direction and far louder.
func IsNestedRepoRoot(dir, scanRoot string) bool {
	if dir == scanRoot {
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
