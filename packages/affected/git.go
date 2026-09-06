package affected

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// GitDiff is the seam over the two `git diff` invocations the selector needs.
// Two methods rather than one because they answer genuinely different
// questions: --name-only is the authoritative list of touched paths (it
// includes a mode change or a pure rename, which produce no hunks at all),
// while --unified=0 is what narrows a file to the symbols inside it.
//
// Both are keyed on the POST-image path, because that is the revision atlas
// indexed.
type GitDiff interface {
	// ChangedFiles lists every path touched between `since` and HEAD.
	ChangedFiles(ctx context.Context, since string) ([]string, error)

	// ChangedLines maps a post-image path to the line spans the diff touched.
	// A path present in ChangedFiles but absent here has no line-level detail
	// (a rename, a mode change) and must be treated as "the whole file".
	ChangedLines(ctx context.Context, since string) (map[string][]LineRange, error)
}

// NewGit returns a GitDiff backed by the `git` binary in repoRoot.
//
// The range form is the three-dot `<since>...HEAD`, which diffs against the
// MERGE BASE rather than the tip of `since`. On a branch that is behind main,
// two-dot would report every commit that landed on main meanwhile as part of
// "your change" — turning a three-line PR into a repo-wide diff and destroying
// the reduction this command exists to produce.
func NewGit(repoRoot string) GitDiff { return &gitCLI{root: repoRoot} }

type gitCLI struct{ root string }

func (g *gitCLI) ChangedFiles(ctx context.Context, since string) ([]string, error) {
	out, err := g.run(ctx, "diff", "--name-only", "--no-renames", since+"...HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			files = append(files, filepath.ToSlash(p))
		}
	}
	return files, nil
}

func (g *gitCLI) ChangedLines(ctx context.Context, since string) (map[string][]LineRange, error) {
	// --no-renames on BOTH invocations, not just this one: with rename
	// detection on, a moved file appears here only under its new path while
	// --name-only lists both, and the old path would silently arrive with no
	// hunks. Off, a rename is a delete plus an add in both views, and the two
	// agree about which paths exist.
	out, err := g.run(ctx, "diff", "--unified=0", "--no-color", "--no-renames", since+"...HEAD")
	if err != nil {
		return nil, err
	}
	ranges, err := parseUnifiedDiff(out)
	if err != nil {
		return nil, fmt.Errorf("affected: parse diff %s...HEAD: %w", since, err)
	}
	return ranges, nil
}

func (g *gitCLI) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // args are fixed verbs plus the caller's ref.
	cmd.Dir = g.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The overwhelmingly common failure is an unknown ref ("origin/main"
		// on a fresh clone with no remote), and git's own stderr says so far
		// better than a wrapped exit status would.
		return "", fmt.Errorf("affected: git %s: %w (%s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// parseUnifiedDiff extracts post-image line ranges per file from
// `git diff --unified=0` output.
//
// Only two line shapes matter:
//
//	+++ b/<path>                 sets the current file (or "+++ /dev/null")
//	@@ -<old> +<start>[,<count>] @@   one hunk
//
// The count is optional and means 1 when omitted. A count of ZERO is the
// interesting case: it marks a pure deletion, whose post-image range is empty.
// Read literally that yields [start, start-1] and matches no symbol, so a
// deleted function body would attribute to nothing and quietly narrow the run.
// Instead the range is anchored across the cut — [start, start+1], clamped to
// line 1 — so the symbol the code was removed from is still attributed.
func parseUnifiedDiff(out string) (map[string][]LineRange, error) {
	ranges := make(map[string][]LineRange)
	var current, preImage string
	// A removed line whose own content begins with "--" arrives as "--- ...",
	// indistinguishable from a file header by prefix alone. git only emits the
	// ---/+++ pair between "diff --git" and the first hunk, so header parsing
	// is gated on not being inside a hunk body.
	inHunk := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk, current, preImage = false, "", ""
		case !inHunk && strings.HasPrefix(line, "--- "):
			preImage = diffHeaderPath(strings.TrimPrefix(line, "--- "), "a/")
		case !inHunk && strings.HasPrefix(line, "+++ "):
			current = diffHeaderPath(strings.TrimPrefix(line, "+++ "), "b/")
			if current == "" {
				// "+++ /dev/null": the file was deleted, so it has no
				// post-image path. Key its hunks on the pre-image path so the
				// deletion still appears in the result — a vanished file that
				// silently dropped out of the map would narrow the run.
				current = preImage
			}
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
			if current == "" {
				return nil, fmt.Errorf("hunk header %q with no preceding file header", line)
			}
			r, ok := parseHunkHeader(line)
			if !ok {
				return nil, fmt.Errorf("unparseable hunk header %q", line)
			}
			ranges[current] = append(ranges[current], r)
		}
	}
	return ranges, nil
}

// diffHeaderPath strips the a/ or b/ prefix git prepends in a file header.
// "/dev/null" (one side of an add or a delete) yields the empty string.
func diffHeaderPath(raw, prefix string) string {
	p := strings.TrimSpace(raw)
	// git appends a tab plus metadata when the path contains spaces.
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, prefix)
}

// parseHunkHeader reads the "+start[,count]" half of a `@@ -a,b +c,d @@`
// header and returns the post-image range it covers.
func parseHunkHeader(line string) (LineRange, bool) {
	plus := strings.Index(line, "+")
	if plus < 0 {
		return LineRange{}, false
	}
	spec := line[plus+1:]
	if end := strings.IndexAny(spec, " \t"); end >= 0 {
		spec = spec[:end]
	}
	startStr, countStr, hasCount := strings.Cut(spec, ",")
	start, err := strconv.Atoi(startStr)
	if err != nil {
		return LineRange{}, false
	}
	count := 1
	if hasCount {
		if count, err = strconv.Atoi(countStr); err != nil {
			return LineRange{}, false
		}
	}
	if count <= 0 {
		// Pure deletion: anchor across the cut. See parseUnifiedDiff.
		if start < 1 {
			return LineRange{Start: 1, End: 1}, true
		}
		return LineRange{Start: start, End: start + 1}, true
	}
	if start < 1 {
		start = 1
	}
	return LineRange{Start: start, End: start + count - 1}, true
}
