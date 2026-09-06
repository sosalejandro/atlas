package churn

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitRunner executes one git invocation inside the repository and returns
// its stdout.
//
// It exists so the mining logic can be tested against recorded `git log`
// output instead of against a fixture repository: the parser is where the
// interesting mistakes live (rename statuses, bulk commits, separator
// collisions), and building a repo per case would hide them behind minutes
// of subprocess time.
type GitRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// NewGitRunner returns a GitRunner that shells out to the git binary with
// the given directory as its working tree.
func NewGitRunner(repo string) GitRunner { return &execRunner{repo: repo} }

type execRunner struct{ repo string }

// quotePathOff disables git's path quoting for every invocation.
//
// This is not a nicety: `git log --name-status` renders a path with any
// non-ASCII byte as `"caf\303\251.go"` — literal quotes, octal escapes —
// while `git ls-files -z` renders it raw. Mine joins those two outputs by
// string equality, so with quoting left on (the DEFAULT, core.quotePath is
// true) every non-ASCII path in the repository silently fails to join and
// scores no churn at all. Setting it here rather than at each call site
// means no future invocation can forget it.
var quotePathOff = []string{"-c", "core.quotePath=false"}

func (e *execRunner) Run(ctx context.Context, args ...string) (string, error) {
	full := append(append([]string{}, quotePathOff...), args...)
	cmd := exec.CommandContext(ctx, "git", full...) //nolint:gosec // args are built here, never from user input.
	cmd.Dir = e.repo
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)",
			strings.Join(full, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// Field and record separators for the `git log` format below.
//
// ASCII 0x1e/0x1f are the unit and record separators; git will not emit
// them on its own and a commit subject containing one is not a thing that
// happens. Using them rather than, say, "|" is what keeps a commit message
// from being able to forge a header line and rewrite the ranking.
const (
	recSep = "\x1e"
	fldSep = "\x1f"
)

// logFormat emits one header line per commit: sha, author email, author
// time as unix seconds, subject. Author *email* rather than name because
// names get retyped ("Ann B" / "ann b") and would inflate author diversity.
const logFormat = "--format=" + recSep + "%H" + fldSep + "%ae" + fldSep + "%at" + fldSep + "%s"

// isShallow asks git whether history is truncated.
//
// This is the CI case, not an exotic one: actions/checkout clones with
// fetch-depth 1 by default, so an unguarded implementation reports every
// file as barely-changed and every hotspot ranking taken in CI is silently
// wrong. Better to say "unknown" loudly.
func isShallow(ctx context.Context, g GitRunner) (bool, error) {
	out, err := g.Run(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, fmt.Errorf("churn: probe repository: %w", err)
	}
	return strings.TrimSpace(out) == "true", nil
}

// trackedFiles returns the set git currently tracks.
//
// This set is what makes "no history" distinguishable from "no changes": a
// tracked file missing from the log really has been quiet, while an
// untracked one is a file git has nothing to say about at all. NUL
// separation because a path may legally contain a newline.
func trackedFiles(ctx context.Context, g GitRunner) (map[string]bool, error) {
	out, err := g.Run(ctx, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("churn: list tracked files: %w", err)
	}
	set := map[string]bool{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			set[p] = true
		}
	}
	return set, nil
}

// runLog fetches the history window in a single subprocess.
//
// -M asks for rename detection, which is the whole reason the parser can
// tell a move from an edit. --no-merges because a merge commit's diff
// re-lists work already counted on the branch it merges.
func runLog(ctx context.Context, g GitRunner, since time.Time) (string, error) {
	out, err := g.Run(ctx,
		"log", "--no-merges", "-M", "--name-status", logFormat,
		"--since="+since.Format(time.RFC3339),
	)
	if err != nil {
		return "", fmt.Errorf("churn: read history: %w", err)
	}
	return out, nil
}

// logCommit is one parsed commit: its header plus its name-status entries.
type logCommit struct {
	Author  string
	At      time.Time
	Subject string
	Entries []statusEntry
}

// statusEntry is one `git log --name-status` line. Old is set only for
// renames and copies; Path is always the name the file has after the
// commit.
type statusEntry struct {
	Status string
	Old    string
	Path   string
}

// forEachCommit parses the log stream and calls fn once per commit, newest
// first (git's own order, which the rename resolution below depends on).
//
// The stream is a header line per commit followed by that commit's
// name-status lines; a header is recognised by the record separator, which
// nothing else in the stream can contain.
func forEachCommit(raw string, fn func(logCommit)) {
	var cur *logCommit
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, recSep) {
			if cur != nil {
				fn(*cur)
			}
			c, ok := parseHeader(strings.TrimPrefix(line, recSep))
			if !ok {
				cur = nil
				continue
			}
			cur = &c
			continue
		}
		if cur == nil || strings.TrimSpace(line) == "" {
			continue
		}
		if e, ok := parseStatus(line); ok {
			cur.Entries = append(cur.Entries, e)
		}
	}
	if cur != nil {
		fn(*cur)
	}
}

// parseHeader splits "sha<FS>email<FS>unixtime<FS>subject". A header we
// cannot parse drops the whole commit rather than attributing it to a zero
// timestamp, which the decay would score as maximally recent.
func parseHeader(s string) (logCommit, bool) {
	parts := strings.SplitN(s, fldSep, 4)
	if len(parts) < 4 {
		return logCommit{}, false
	}
	secs, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return logCommit{}, false
	}
	return logCommit{
		Author:  strings.ToLower(strings.TrimSpace(parts[1])),
		At:      time.Unix(secs, 0).UTC(),
		Subject: parts[3],
	}, true
}

// parseStatus reads one name-status line: "M\tpath", or for renames and
// copies "R100\told\tnew".
func parseStatus(line string) (statusEntry, bool) {
	fields := strings.Split(line, "\t")
	if len(fields) < 2 || fields[0] == "" {
		return statusEntry{}, false
	}
	e := statusEntry{Status: fields[0]}
	if len(fields) >= 3 && (fields[0][0] == 'R' || fields[0][0] == 'C') {
		e.Old, e.Path = unquotePath(fields[1]), unquotePath(fields[2])
	} else {
		e.Path = unquotePath(fields[1])
	}
	if e.Path == "" {
		return statusEntry{}, false
	}
	return e, true
}

// unquotePath undoes git's C-style path quoting.
//
// quotePathOff above turns the common case (any non-ASCII byte) off, but
// git still quotes a path containing a double quote, a backslash or a
// control character whatever that setting says. Those paths must come out
// in the same namespace as `git ls-files -z`, which never quotes, or they
// silently fail to join and score no churn. Git's quoting is C string
// syntax, which is also Go's, so strconv does the decoding; a value we
// cannot decode is returned untouched rather than dropped, because a path
// that joins nothing is a better outcome than a path that joins the wrong
// file.
func unquotePath(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	out, err := strconv.Unquote(s)
	if err != nil {
		return s
	}
	return out
}

// isPureMove reports whether an entry is a rename or copy that changed
// nothing. Git appends a similarity percentage to R and C; 100 means the
// content is byte-identical, so the commit relocated the file and did no
// work on it. A rename at 85% edited the file on the way and is real churn.
func isPureMove(e statusEntry) bool {
	if e.Old == "" {
		return false
	}
	return strings.TrimLeft(e.Status, "RC") == "100"
}
