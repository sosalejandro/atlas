package audit

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GitBlameSource returns the latest git author-date for a (filePath, line)
// pair. Implementations decide whether to invoke `git`, read a cached
// blame index, or feed fixture data — the audit package only needs the
// timestamp.
//
// Returning a zero time.Time (with nil error) means "unknown" — the audit
// package treats that as "skip this site" rather than "this site is stale."
type GitBlameSource interface {
	// AuthorDate returns the latest commit author-date for the given line
	// in the file. Path is repo-relative (filepath.ToSlash form expected).
	AuthorDate(ctx context.Context, filePath string, line int) (time.Time, error)
}

// NewGitBlame returns a GitBlameSource that shells out to `git blame` from
// the given repository root. The implementation caches per-file blame so a
// burst of (file, line) queries for the same file pays only one subprocess.
//
// A zero / empty root makes every AuthorDate call return a zero time —
// useful in tests where git isn't wired but Options.GitBlame still has to
// be non-nil to enable the signal.
func NewGitBlame(repoRoot string) GitBlameSource {
	return &gitBlame{
		root:  repoRoot,
		cache: make(map[string][]time.Time),
	}
}

type gitBlame struct {
	root  string
	cache map[string][]time.Time // file → (line-1)→author_time
}

// AuthorDate runs `git blame -t --line-porcelain <file>` and parses the
// per-line author-time entries. Result is cached per file — subsequent
// per-line queries are O(1).
func (g *gitBlame) AuthorDate(ctx context.Context, filePath string, line int) (time.Time, error) {
	if g.root == "" || filePath == "" || line <= 0 {
		return time.Time{}, nil
	}
	rel := filepath.ToSlash(filePath)
	if cached, ok := g.cache[rel]; ok {
		if line-1 < len(cached) {
			return cached[line-1], nil
		}
		return time.Time{}, nil
	}
	times, err := runGitBlame(ctx, g.root, rel)
	if err != nil {
		// Cache the empty slice so we don't reissue the subprocess for the
		// same file. Callers see zero time → "skip this site."
		g.cache[rel] = nil
		return time.Time{}, err
	}
	g.cache[rel] = times
	if line-1 < len(times) {
		return times[line-1], nil
	}
	return time.Time{}, nil
}

// runGitBlame executes `git blame --porcelain` and returns one author-time
// per line of the input file.
//
// The porcelain format emits per-line metadata blocks; the first line in
// each block carries the commit hash, and subsequent header lines include
// `author-time <unix-seconds>`. We parse only the author-time line per
// block; everything else is ignored.
func runGitBlame(ctx context.Context, root, relPath string) ([]time.Time, error) {
	cmd := exec.CommandContext(ctx, "git", "blame", "--porcelain", relPath) //nolint:gosec
	cmd.Dir = root
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git blame %q: %w (stderr: %s)", relPath, err, strings.TrimSpace(errBuf.String()))
	}
	return parseBlamePorcelain(out.String()), nil
}

// parseBlamePorcelain extracts one author-time per source line from
// `git blame --porcelain` output.
//
// Format reminder: the porcelain stream is a sequence of "blocks" — each
// block starts with `<sha> <orig-line> <final-line> [<count>]`. Header
// lines that follow include `author-time N` (unix seconds, UTC). The
// `\t<source-line>` line at the end of each block separates blocks. The
// parser is forgiving: a missing author-time is treated as zero time for
// that line.
func parseBlamePorcelain(s string) []time.Time {
	var out []time.Time
	var curTime time.Time
	for _, rawLine := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(rawLine, "author-time "):
			ts, err := strconv.ParseInt(strings.TrimPrefix(rawLine, "author-time "), 10, 64)
			if err == nil {
				curTime = time.Unix(ts, 0).UTC()
			}
		case strings.HasPrefix(rawLine, "\t"):
			// End-of-block marker. Emit the current time, reset.
			out = append(out, curTime)
			curTime = time.Time{}
		}
	}
	return out
}

// fixedBlameSource is a deterministic GitBlameSource used by audit tests.
// It returns a single configured time for every line regardless of file.
//
// Internal type — exported only inside the package because the tests in
// the same package wire it directly.
type fixedBlameSource struct{ ts time.Time }

func (f *fixedBlameSource) AuthorDate(context.Context, string, int) (time.Time, error) {
	return f.ts, nil
}
