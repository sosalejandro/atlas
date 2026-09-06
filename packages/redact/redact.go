package redact

import (
	"regexp"
	"strconv"
	"strings"
)

// Result is the outcome of redacting one text: the rewritten value plus the
// evidence of what was changed and why.
//
// The findings travel WITH the text rather than being logged and dropped.
// That is the whole compromise this package rests on: redaction is allowed
// to be wrong, because every rewrite is accounted for and a reader can see
// what was taken out, from where, and on which rule's say-so.
type Result struct {
	Text     string    `json:"text"`
	Findings []Finding `json:"findings,omitempty"`
}

// Redacted reports whether anything was replaced.
func (r Result) Redacted() bool { return len(r.Findings) > 0 }

// Text detects secrets in s and returns s with each one replaced by a
// placeholder naming the rule and the secret's digest.
//
// This is the function an ingest path calls on any value it is about to
// persist verbatim -- SQL text, a doc comment, a branch condition. It is
// safe to call on already-redacted text: placeholders are inert against
// every detector (see TestText_IsIdempotent), so a re-scan of the store
// neither doubles up nor churns the digests that correlate one leaked
// credential across the places it appears.
func Text(s string) Result {
	findings := Scan(s)
	if len(findings) == 0 {
		return Result{Text: s}
	}
	var b strings.Builder
	// The replacements are longer than most secrets but not all of them, so
	// the length hint is only a hint.
	b.Grow(len(s))
	prev := 0
	for _, f := range findings {
		b.WriteString(s[prev:f.Start])
		b.WriteString(Placeholder(f.Kind, f.Digest))
		prev = f.End
	}
	b.WriteString(s[prev:])
	return Result{Text: b.String(), Findings: findings}
}

// placeholderPrefix and placeholderSuffix bracket a redaction.
//
// Square brackets rather than a comment or a quoted string: the redacted
// value has to survive being stored in a column, re-read, diffed and printed
// without any consumer mistaking it for content, and it must not accidentally
// close a quote it is sitting inside.
const (
	placeholderPrefix = "[redacted:"
	placeholderSuffix = "]"
)

// Placeholder is the exact text a redaction leaves behind.
//
// It names the rule and the digest, and nothing else. The digest is what
// makes the placeholder useful rather than merely safe: the same credential
// hardcoded in nine queries redacts to nine identical placeholders, so the
// operator sees one secret to rotate instead of nine unrelated holes.
func Placeholder(kind Kind, digest string) string {
	return placeholderPrefix + string(kind) + ":" + digest + placeholderSuffix
}

// placeholderRe recognises text this package has already redacted. The
// detectors consult it so a second pass cannot redact a redaction; see
// detector.findAll for why that matters more than it sounds.
var placeholderRe = regexp.MustCompile(
	`\[redacted:[a-z][a-z\-]*:[0-9a-f]{` + strconv.Itoa(digestLen) + `}\]`)

// IsPlaceholder reports whether s is exactly one placeholder this package
// wrote. Consumers rendering stored values use it to show "redacted" rather
// than a literal that looks like content.
func IsPlaceholder(s string) bool {
	// The empty string is excluded explicitly: FindString returns "" both
	// for "no match" and for "matched the empty string", so comparing the
	// result to s would call an empty column value a redaction.
	return s != "" && placeholderRe.FindString(s) == s
}
