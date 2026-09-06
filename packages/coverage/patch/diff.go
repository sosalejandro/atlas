package patch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// LineRange is an inclusive run of line numbers in a file's POST-image --
// the file as it exists at HEAD. Pre-image (deleted) lines never appear:
// there is nothing left to test in them.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len is the number of lines the range covers.
func (r LineRange) Len() int {
	if r.End < r.Start {
		return 0
	}
	return r.End - r.Start + 1
}

// String renders the range the way a reviewer pastes it into an editor:
// "88-131", or just "88" for a single line.
func (r LineRange) String() string {
	if r.Start == r.End {
		return strconv.Itoa(r.Start)
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

// FileChange is one file's added-or-modified lines, at the path they live at
// in the post-image (so a rename is reported under its NEW name -- that is
// the path the symbol index and the coverage frontier both know it by).
//
// Ranges is sorted ascending and non-overlapping.
type FileChange struct {
	Path   string      `json:"path"`
	Ranges []LineRange `json:"ranges"`
}

// Lines is the total number of changed lines in the file.
func (c FileChange) Lines() int {
	n := 0
	for _, r := range c.Ranges {
		n += r.Len()
	}
	return n
}

// Diff runs `git diff --unified=0 <base>...HEAD` inside repoRoot and returns
// the changed line ranges it reports.
//
// Shelling out rather than linking a git library is deliberate: the diff is
// exactly the one CI would compute, it costs no dependency, and a user who
// can run `atlas` in a checkout can already run `git`.
//
// The three-dot form is the merge-base diff -- "what this branch did", not
// "how this branch differs from the current tip of base". Without it, commits
// that landed on base after the branch forked are attributed to the branch,
// and a long-lived PR is gated on other people's code.
//
// Note this reads COMMITTED work only. Uncommitted edits in the working tree
// are invisible here, which is what a CI gate wants and what a local caller
// has to know.
func Diff(ctx context.Context, repoRoot, base string) ([]FileChange, error) {
	if strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("patch diff: a base ref is required")
	}
	// A ref that begins with '-' would be parsed by git as an option. Reject
	// rather than sanitise: there is no legitimate ref of that shape, and a
	// silent rewrite would diff something other than what was asked for.
	if strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("patch diff: invalid base ref %q", base)
	}

	//nolint:gosec // base is validated above; repoRoot is the caller's own checkout.
	cmd := exec.CommandContext(ctx, "git", "diff",
		"--unified=0",   // no context lines: every reported line really changed
		"--no-color",    // a configured color.ui=always would poison the parse
		"--no-ext-diff", // an external difftool emits something else entirely
		"--find-renames",
		base+"...HEAD",
	)
	cmd.Dir = repoRoot
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("patch diff: git diff %s...HEAD: %w (stderr: %s)",
			base, err, strings.TrimSpace(errBuf.String()))
	}
	changes, err := ParseDiff(&out)
	if err != nil {
		return nil, fmt.Errorf("patch diff %s...HEAD: %w", base, err)
	}
	return changes, nil
}

// ParseDiff reads a unified diff produced with `--unified=0` and returns the
// post-image line ranges per file.
//
// Exported separately from Diff so the parse is testable against fixtures
// without a repository, and so a caller holding a diff from elsewhere (a CI
// artifact, a review API) can score it.
func ParseDiff(r io.Reader) ([]FileChange, error) {
	p := newDiffParser()
	sc := bufio.NewScanner(r)
	// Diff lines are source lines, and a minified bundle or a generated file
	// can blow well past bufio's 64KiB default. Overflowing would truncate
	// the stream mid-hunk and silently under-report the diff.
	sc.Buffer(make([]byte, 0, 64*1024), maxDiffLineBytes)
	for sc.Scan() {
		p.consume(sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read diff: %w", err)
	}
	return p.changes(), nil
}

// maxDiffLineBytes caps a single diff line. 4MiB is far past any hand-written
// source line and still bounded, so a pathological input cannot exhaust memory.
const maxDiffLineBytes = 4 << 20

// diffParser walks the diff as a state machine rather than by line prefix.
//
// The reason is a real ambiguity in the format: an ADDED line whose content
// begins with "++ " renders as "+++ ...", byte-identical to a file header.
// Matching on the prefix alone lets file content re-point the following hunks
// at another path. Because `--unified=0` hunks declare exactly how many
// pre- and post-image lines their body holds, the parser instead counts the
// body out and only interprets headers when it is between hunks.
type diffParser struct {
	// path is the current section's post-image path; "" when the section has
	// none yet, or names /dev/null (a deletion, which contributes nothing).
	path string
	// remain is how many body lines the current hunk still owes.
	remain int

	byPath map[string][]LineRange
}

func newDiffParser() *diffParser {
	return &diffParser{byPath: map[string][]LineRange{}}
}

// consume advances the state machine by one line of the diff.
func (p *diffParser) consume(line string) {
	if p.remain > 0 && p.consumeBody(line) {
		return
	}
	switch {
	case strings.HasPrefix(line, "diff --git "):
		// A new file section: drop the previous path so a section without a
		// readable "+++" header (a binary file, a pure mode change) cannot
		// inherit the last one's.
		p.path = ""
	case strings.HasPrefix(line, "+++ "):
		p.path = postImagePath(strings.TrimPrefix(line, "+++ "))
	case strings.HasPrefix(line, "@@ "):
		p.startHunk(line)
	}
}

// consumeBody accounts for one line of a hunk body, reporting whether the
// line belonged to it. A body line that is neither an addition nor a deletion
// means the hunk header lied (a truncated or hand-edited diff); the parser
// abandons the count and re-reads the line as a header rather than
// swallowing the rest of the file.
func (p *diffParser) consumeBody(line string) bool {
	switch {
	case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
		p.remain--
		return true
	case strings.HasPrefix(line, `\`):
		// "\ No newline at end of file" annotates the preceding line and is
		// not itself part of the body count.
		return true
	default:
		p.remain = 0
		return false
	}
}

// startHunk parses an `@@ -a,b +c,d @@` header, records the post-image range
// it introduces, and arms the body counter.
func (p *diffParser) startHunk(line string) {
	pre, post, ok := hunkCounts(line)
	if !ok {
		return
	}
	// The body is b pre-image lines followed by d post-image lines. Arm the
	// counter even for a section we are ignoring, so its content can never be
	// mistaken for the next file's header.
	p.remain = pre.count + post.count

	// A hunk whose post-image is empty is a pure deletion. Deleted lines are
	// not patch coverage -- there is nothing left to cover.
	if post.count == 0 || p.path == "" {
		return
	}
	p.byPath[p.path] = append(p.byPath[p.path], LineRange{
		Start: post.start,
		End:   post.start + post.count - 1,
	})
}

// hunkSide is one side of a hunk header: `-a,b` or `+c,d`.
type hunkSide struct{ start, count int }

// hunkCounts extracts both sides of an `@@ -a,b +c,d @@` header. The trailing
// section heading git appends (the enclosing function's signature) is ignored
// -- it is a display convenience and is not always present.
func hunkCounts(line string) (pre, post hunkSide, ok bool) {
	body := strings.TrimPrefix(line, "@@ ")
	end := strings.Index(body, "@@")
	if end < 0 {
		return pre, post, false
	}
	fields := strings.Fields(body[:end])
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "-") || !strings.HasPrefix(fields[1], "+") {
		return pre, post, false
	}
	if pre, ok = parseHunkSide(fields[0][1:]); !ok {
		return pre, post, false
	}
	post, ok = parseHunkSide(fields[1][1:])
	return pre, post, ok
}

// parseHunkSide reads "start,count" or the bare "start" shorthand git uses
// when the count is exactly 1.
func parseHunkSide(s string) (hunkSide, bool) {
	startStr, countStr, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(startStr)
	if err != nil {
		return hunkSide{}, false
	}
	count := 1
	if hasCount {
		if count, err = strconv.Atoi(countStr); err != nil {
			return hunkSide{}, false
		}
	}
	if count < 0 {
		return hunkSide{}, false
	}
	return hunkSide{start: start, count: count}, true
}

// postImagePath normalises the operand of a "+++" header into a repo-relative
// path, or "" when the header names /dev/null (the file was deleted).
//
// git C-quotes any path with a byte outside the printable ASCII range or a
// space. Leaving the quotes and escapes on would make the path match no
// indexed file, quietly turning a real changed file into an unknown one.
func postImagePath(operand string) string {
	s := strings.TrimSpace(operand)
	if s == "" || s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if unquoted, err := strconv.Unquote(s); err == nil {
			s = unquoted
		}
	}
	// Strip git's destination prefix. Both are checked because a caller may
	// hand us a diff produced with a non-default --dst-prefix.
	for _, prefix := range []string{"b/", "a/"} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimPrefix(s, prefix)
		}
	}
	return s
}

// changes renders the accumulated ranges as a stable, sorted result: files by
// path, ranges ascending and merged, so two runs over the same diff produce
// byte-identical output.
func (p *diffParser) changes() []FileChange {
	out := make([]FileChange, 0, len(p.byPath))
	for path, ranges := range p.byPath {
		merged := mergeRanges(ranges)
		if len(merged) == 0 {
			continue
		}
		out = append(out, FileChange{Path: path, Ranges: merged})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// mergeRanges sorts and coalesces touching or overlapping ranges so a line is
// never counted twice in the denominator.
func mergeRanges(in []LineRange) []LineRange {
	if len(in) == 0 {
		return nil
	}
	cp := append([]LineRange(nil), in...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].Start != cp[j].Start {
			return cp[i].Start < cp[j].Start
		}
		return cp[i].End < cp[j].End
	})
	out := []LineRange{cp[0]}
	for _, r := range cp[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}
