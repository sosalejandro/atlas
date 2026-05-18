package diagnose

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// symptomMatcher carries the precompiled forms a single symptom is matched
// with. Constructed once per Diagnose call, reused across every candidate.
//
// Two flavours are kept:
//
//   - whole   — the full symptom escaped as a regex literal; matching this
//     is the strongest signal ("this exact string appears in the body").
//   - tokens  — the symptom's significant tokens (whitespace-split, alnum
//     chunks ≥ minTokenLen, no stop-words), each escaped and joined with |.
//     Used to score symbols that *partially* echo the symptom — e.g. the
//     symptom contains "panic: nil pointer deref at handler.go:42" and a
//     candidate's body emits the panic format string `"nil pointer deref
//     at %s:%d"`.
//
// Both regexes are case-insensitive (?i) — symptom text comes from logs,
// browser DevTools, etc. with inconsistent casing, and the source code
// strings being matched against are equally inconsistent.
type symptomMatcher struct {
	whole  *regexp.Regexp
	tokens *regexp.Regexp
	raw    string
}

// minTokenLen is the floor for "significant" symptom tokens. Anything
// shorter (e.g. "at", "in", "of") tends to match every Go file and adds
// noise to the score without adding signal.
const minTokenLen = 4

// stopTokens are common-but-unhelpful symptom tokens we drop before
// building the tokens regex. Kept short on purpose — over-filtering hurts
// recall on terse symptoms more than the false-positive rate hurts
// precision.
var stopTokens = map[string]struct{}{
	"error":   {},
	"failed":  {},
	"failure": {},
	"server":  {},
	"client":  {},
	"request": {},
	"http":    {},
	"https":   {},
	"with":    {},
	"from":    {},
	"this":    {},
	"that":    {},
	"true":    {},
	"false":   {},
	"null":    {},
	"value":   {},
}

// newSymptomMatcher constructs the symptom-side regexes. Returns a non-nil
// matcher even for an empty symptom — the caller has already validated
// non-empty before reaching the scorer, so this function never sees ""
// in practice, but defensive construction beats a nil-pointer panic if a
// future refactor changes that.
//
// Regex metachars in the raw symptom are escaped via regexp.QuoteMeta —
// the user is expected to paste a stack trace, not author a regex.
func newSymptomMatcher(symptom string) *symptomMatcher {
	trimmed := strings.TrimSpace(symptom)
	if trimmed == "" {
		// Pathological branch: the public API rejects empty symptoms
		// upstream, but constructing the matcher must still be safe.
		return &symptomMatcher{raw: trimmed}
	}

	wholeRe := regexp.MustCompile("(?i)" + regexp.QuoteMeta(trimmed))

	var tokens []string
	seen := map[string]struct{}{}
	for _, tok := range splitTokens(trimmed) {
		lower := strings.ToLower(tok)
		if len(lower) < minTokenLen {
			continue
		}
		if _, drop := stopTokens[lower]; drop {
			continue
		}
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		tokens = append(tokens, regexp.QuoteMeta(tok))
	}

	var tokensRe *regexp.Regexp
	if len(tokens) > 0 {
		tokensRe = regexp.MustCompile("(?i)(" + strings.Join(tokens, "|") + ")")
	}

	return &symptomMatcher{
		whole:  wholeRe,
		tokens: tokensRe,
		raw:    trimmed,
	}
}

// splitTokens chunks a symptom on non-alnum runs. Underscores and dots are
// preserved as token-internal because Go identifiers and qualified names
// frequently include them (e.g. "auth.Login", "context_deadline").
//
// Splitting on ASCII boundaries only is intentional: error messages we
// match against are overwhelmingly ASCII (HTTP status text, panic
// formatters, SQL driver messages). A Unicode-aware split would slow the
// hot path without changing real-world results.
func splitTokens(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_' || r == '.':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// bodyCache is a per-Diagnose cache of file contents keyed by repo-relative
// path. It exists so a project with many symbols in the same file doesn't
// re-read the file once per candidate.
//
// The cache is scoped to a single Diagnose call (the scorer constructs one
// and discards it on return); a long-lived in-memory cache across calls
// would race with editors writing to source files and is explicitly NOT
// the design.
type bodyCache struct {
	root  string
	files map[string][]byte
}

// newBodyCache returns a cache anchored at root. root SHOULD be the
// absolute project directory the Store was opened against, but the
// implementation only joins it onto the repo-relative file_path values
// from the symbols table — passing "" works for tests where symbols carry
// absolute Position.Path.
func newBodyCache(root string) *bodyCache {
	return &bodyCache{root: root, files: map[string][]byte{}}
}

// readBody returns the body excerpt of a symbol from line `startLine` to
// `endLine` (1-based, inclusive). When endLine is 0 or negative, a
// fallback window of fallbackSpan lines is read. Files that cannot be
// opened return ("", nil) — a missing source file is a recoverable
// condition (e.g. generated code excluded from the worktree), not an
// error the scorer should propagate.
//
// startLine ≤ 0 means "from line 1". An out-of-range endLine clamps to
// the file's last line.
func (c *bodyCache) readBody(relPath string, startLine, endLine int) (string, error) {
	if relPath == "" {
		return "", nil
	}

	content, ok := c.files[relPath]
	if !ok {
		// Resolve the on-disk path. If relPath is absolute, use it
		// verbatim (tests do this); otherwise join with the cache root.
		path := relPath
		if !filepath.IsAbs(relPath) && c.root != "" {
			path = filepath.Join(c.root, relPath)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			// Miss is sticky — record the failure as an empty body so
			// the next candidate in the same file doesn't re-attempt.
			c.files[relPath] = nil
			if os.IsNotExist(err) {
				return "", nil
			}
			return "", err //nolint:wrapcheck // fs error surfaced verbatim by design
		}
		content = b
		c.files[relPath] = content
	}
	if len(content) == 0 {
		return "", nil
	}

	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine < startLine {
		endLine = startLine + fallbackSpan
	}

	return sliceLines(content, startLine, endLine), nil
}

// fallbackSpan is the number of lines to read past startLine when EndLine
// is missing. Most legacy Go symbols (testreg-fork era) only carry Line;
// EndLine became populated later. 80 lines is generous for typical
// functions but caps the read so a top-of-file declaration doesn't pull
// in the entire source.
const fallbackSpan = 80

// sliceLines returns the 1-based [startLine, endLine] slice of content
// as a string. Out-of-range endLine clamps; an entirely empty result is
// returned as "".
//
// The implementation walks bytes once — no allocation per line — because
// the scorer can call this hundreds of times per Diagnose call on large
// codebases.
func sliceLines(content []byte, startLine, endLine int) string {
	if len(content) == 0 || startLine > endLine {
		return ""
	}

	line := 1
	start := -1
	end := len(content)

	for i := 0; i < len(content); i++ {
		if line == startLine && start == -1 {
			start = i
		}
		if content[i] == '\n' {
			if line == endLine {
				end = i + 1
				break
			}
			line++
		}
	}

	if start == -1 {
		// startLine is past EOF.
		return ""
	}
	if end > len(content) {
		end = len(content)
	}
	return string(content[start:end])
}
