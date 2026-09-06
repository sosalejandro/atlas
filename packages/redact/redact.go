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

// redactableColumns indexes the registry by "table.column", for callers that
// have a value in hand and need to know whether they may rewrite it.
//
// Built once from the same slice schema_test.go compares against a migrated
// store, so a column that stops being redactable stops being redacted at
// ingest in the same commit.
var redactableColumns = func() map[string]bool {
	m := make(map[string]bool, len(columns))
	for _, c := range columns {
		if c.Redactable {
			m[c.Table+"."+c.Name] = true
		}
	}
	return m
}()

// Redactable reports whether the registry allows table.column to be
// rewritten in place. An unregistered column is not redactable: this package
// does not rewrite a column it cannot describe.
func Redactable(table, column string) bool {
	return redactableColumns[table+"."+column]
}

// Field redacts a value on its way INTO one registered column.
//
// It is the ingest-time half of Sweep. Sweep cleans a store that already
// holds a credential; Field is what a write path calls so the credential
// never lands there in the first place -- the concrete case being a
// hardcoded connection string inside a query, stored verbatim as
// sql_operations.sql_text.
//
// A column the registry does not mark redactable comes back unchanged and
// with no findings. That is not an oversight: rewriting an identifier or a
// path would change what the index MEANS rather than what it discloses, and
// a Sweep still reports the disclosure to the operator either way. The
// asymmetry is the same one `atlas security redact` applies, and it is
// stated in one place -- the registry -- so the two cannot drift.
func Field(table, column, value string) Result {
	if value == "" || !Redactable(table, column) {
		return Result{Text: value}
	}
	return Text(value)
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
