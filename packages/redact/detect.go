package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Kind names the rule that fired. It is part of the placeholder text left
// behind by a redaction, so it is a stable identifier, not a description.
type Kind string

const (
	// KindPrivateKey is a PEM private key block. The armour markers are
	// unambiguous: no legitimate query, identifier or doc comment contains
	// them by accident, so this rule needs no entropy gate at all.
	KindPrivateKey Kind = "private-key"

	// KindAWSAccessKey is an AWS access key id. The four-character prefix
	// plus the fixed 16-character body is a checkable shape rather than a
	// guess, which is why it is worth a rule of its own.
	KindAWSAccessKey Kind = "aws-access-key"

	// KindConnectionString is the password half of a scheme://user:pass@host
	// URL. Only the password is covered: leaving the scheme, user and host
	// in place keeps the row useful to `atlas sql` while removing the part
	// that grants access.
	KindConnectionString Kind = "connection-string"

	// KindAssignedSecret is a high-entropy literal assigned to an
	// identifier whose NAME says it is a credential. Neither half is
	// sufficient alone: the name without the entropy matches
	// `password_hash = $1`, and the entropy without the name matches every
	// base64 blob and UUID in the repository.
	KindAssignedSecret Kind = "assigned-secret"
)

// digestLen is how many hex characters of the SHA-256 of a secret are kept.
// Twelve is short enough to read in a terminal and long enough that two
// distinct secrets in one repository will not collide, and the point of
// keeping any of it is correlation: the same credential redacted in nine
// places should be visibly ONE leak to fix, not nine.
const digestLen = 12

// Finding is one detected secret, described without reproducing it.
//
// Everything here is safe to print, log, and put in a JSON envelope. That is
// deliberate and load-bearing: a report about secrets that quotes the
// secrets is a second copy of the leak, and this struct is what `atlas
// security` renders.
type Finding struct {
	Kind Kind `json:"kind"`

	// Start and End are byte offsets into the scanned text; the half-open
	// span [Start, End) is what a redaction replaces.
	Start int `json:"start"`
	End   int `json:"end"`

	// Line is the 1-based line of the scanned text that Start falls on. For
	// a column swept out of the store this is a line within that column's
	// value, not a line in any source file.
	Line int `json:"line"`

	// Length is the byte length of the covered secret. Reported because the
	// span offsets are meaningless once the text has been rewritten.
	Length int `json:"length"`

	// Digest is the first digestLen hex characters of SHA-256 over the
	// covered bytes. It identifies the secret across occurrences without
	// disclosing it.
	Digest string `json:"digest"`

	// Entropy is the Shannon entropy of the covered bytes in bits per byte.
	// Only the rules that gate on it set it; zero means "not gated on
	// entropy", which is not the same as "no entropy".
	Entropy float64 `json:"entropy,omitempty"`

	// Context is the surrounding evidence that made this a finding: the
	// identifier that was assigned, the PEM label, or the scheme/user/host
	// of a connection string. It NEVER contains any part of the secret --
	// its whole job is to let a reader locate the leak in the source and
	// judge a false positive without being shown the credential again.
	Context string `json:"context,omitempty"`
}

// Scan returns every secret found in text, ordered by position, with
// overlapping matches collapsed.
//
// Scan is the only entry point the detectors are reachable through, so the
// overlap collapse cannot be skipped: two rules firing on nested spans would
// otherwise produce a nested replacement (a placeholder inside a
// placeholder) and count one leak twice.
func Scan(text string) []Finding {
	var out []Finding
	for _, d := range detectors {
		out = append(out, d.findAll(text)...)
	}
	return collapse(out)
}

// detector is one rule: a pattern plus the gate that decides whether a
// structural match is really a secret. Splitting the gate out of the regexp
// is what keeps the false-positive policy readable -- the expensive
// judgement calls (entropy, placeholder vocabulary) are Go, not regex.
type detector struct {
	kind Kind
	re   *regexp.Regexp
	// build maps one FindAllStringSubmatchIndex row onto a Finding, or
	// returns false to reject the match as a false positive.
	build func(text string, m []int) (Finding, bool)
}

func (d detector) findAll(text string) []Finding {
	var out []Finding
	for _, m := range d.re.FindAllStringSubmatchIndex(text, -1) {
		f, ok := d.build(text, m)
		if !ok {
			continue
		}
		// A span that already holds a placeholder is a previous
		// redaction, not a secret. Without this guard every sweep of an
		// already-redacted store rewrites the last sweep's output --
		// `apiKey = "[redacted:...]"` still satisfies the assignment
		// rule, and the digest, which is the only thing tying one leaked
		// credential to the nine rows it sits in, would change on every
		// run. The cost is a real secret written immediately adjacent to
		// a placeholder going unredacted; that is the rarer failure and
		// the one a reader can still see in the report.
		if placeholderRe.MatchString(text[f.Start:f.End]) {
			continue
		}
		f.Kind = d.kind
		f.Length = f.End - f.Start
		f.Digest = digest(text[f.Start:f.End])
		f.Line = lineOf(text, f.Start)
		out = append(out, f)
	}
	return out
}

// privateKeyRe matches a PEM private key block, and matches an unterminated
// one to the end of the text. A truncated key is still a key: refusing to
// cover it because the END marker was cut off by a column width or a JSON
// escape would leave the bytes that matter in the store.
var privateKeyRe = regexp.MustCompile(
	`(?s)-----BEGIN ([A-Z0-9 ]*)PRIVATE KEY-----(?:.*?-----END [A-Z0-9 ]*PRIVATE KEY-----|.*)`)

// awsAccessKeyRe matches the documented AWS access-key-id prefixes followed
// by exactly sixteen more base32-ish characters. The `\b` anchors are what
// stop a longer identifier that merely starts with AKIA from matching.
var awsAccessKeyRe = regexp.MustCompile(
	`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ABIA|ACCA)[A-Z0-9]{16}\b`)

// connectionStringRe matches scheme://user:password@host, capturing the four
// parts separately so the redaction can cover the password ALONE. The
// character classes are exclusions rather than allow-lists because passwords
// are percent-encoded, punctuated and generally hostile to an allow-list;
// what they cannot contain is the delimiters that end the field.
var connectionStringRe = regexp.MustCompile(
	`(?i)\b([a-z][a-z0-9+.\-]{1,20})://([^\s:/?#@\[\]]{1,128}):([^\s/?#@]{1,256})@([^\s:/?#]{0,255})`)

// secretAssignmentRe matches `<name-ending-in-a-credential-word> = "<value>"`
// in the assignment spellings Go, YAML, JSON, JS and TS all use. The value
// must be quoted: an unquoted right-hand side is an expression
// (`os.Getenv("TOKEN")`, `$1`, `:token`) and reading a secret out of one is
// how a detector starts rewriting bind parameters.
//
// The optional backslashes are what let this reach into a JSON blob. Doc
// comments live inside snapshots.index_json as JSON strings, so a credential
// quoted in one arrives as `apiKey = \"sk_live_...\"` -- and index_json is
// the largest disclosure atlas creates, so a detector that could not read it
// would be blind exactly where it matters most. The optional quote after the
// name covers the other JSON shape, `"client_secret": "..."`.
var secretAssignmentRe = regexp.MustCompile(
	`(?i)\b([A-Za-z0-9_.\-]{0,48}?` +
		`(?:passwd|password|passphrase|secret|token|api[_-]?key|access[_-]?key|` +
		`auth[_-]?key|private[_-]?key|credential)s?)\b` +
		`\\?["']?\s*(?::=|=>|[:=])\s*` +
		`(?:` + quotedValueRe(`"`) + `|` + quotedValueRe(`'`) + `|` + quotedValueRe("`") + `)`)

// quotedValueRe builds the sub-pattern matching a value in one quote style,
// tolerating a backslash-escaped quote on either side.
//
// The value itself may not contain a backslash. That is the price of reading
// escaped text with a regexp rather than a JSON parser: a credential
// containing a literal backslash goes undetected, which is rare, whereas
// admitting backslashes would let the value swallow the escape of its own
// closing quote and produce a replacement that corrupts the JSON around it.
func quotedValueRe(quote string) string {
	return `\\?` + quote + `([^` + quote + `\\\n]{1,512})\\?` + quote
}

// minAssignedLength and minAssignedEntropy are the gate on
// KindAssignedSecret, and they are set to under-report on purpose.
//
// At 16 bytes and 3.5 bits/byte, "supersecretvalue" (3.13) and
// "Authorization" (too short) are both let through unredacted, while a
// 32-hex-character API key (3.9) and a base64 token (>4.5) are caught. The
// cost of the two errors is not symmetric: a missed weak password is a
// credential the operator could have found by reading the file, whereas a
// false positive silently rewrites a query that `atlas sql` then analyses
// and reports on as if it were the code.
const (
	minAssignedLength  = 16
	minAssignedEntropy = 3.5
	// minPasswordLength is the far weaker gate on a connection-string
	// password. No entropy gate applies: the scheme://user:pass@host shape
	// is itself the evidence, and a short password inside one is a weak
	// credential rather than a coincidence.
	minPasswordLength = 6
)

var detectors = []detector{
	{
		kind: KindPrivateKey,
		re:   privateKeyRe,
		build: func(text string, m []int) (Finding, bool) {
			return Finding{
				Start:   m[0],
				End:     m[1],
				Context: strings.TrimSpace(group(text, m, 1) + "PRIVATE KEY"),
			}, true
		},
	},
	{
		kind:  KindAWSAccessKey,
		re:    awsAccessKeyRe,
		build: func(_ string, m []int) (Finding, bool) { return Finding{Start: m[0], End: m[1]}, true },
	},
	{
		kind:  KindConnectionString,
		re:    connectionStringRe,
		build: buildConnectionString,
	},
	{
		kind:  KindAssignedSecret,
		re:    secretAssignmentRe,
		build: buildAssignedSecret,
	},
}

// buildConnectionString covers the password only, and reports the rest of
// the URL as context so a reviewer can tell a production DSN from the
// example in a doc comment without being shown the password twice.
func buildConnectionString(text string, m []int) (Finding, bool) {
	pass := group(text, m, 3)
	if len(pass) < minPasswordLength || isPlaceholder(pass) {
		return Finding{}, false
	}
	return Finding{
		Start: m[6],
		End:   m[7],
		Context: group(text, m, 1) + "://" + group(text, m, 2) +
			":<redacted>@" + group(text, m, 4),
	}, true
}

// buildAssignedSecret applies the entropy gate, and picks whichever of the
// three quote styles actually matched.
func buildAssignedSecret(text string, m []int) (Finding, bool) {
	start, end := firstMatchedGroup(m, 2, 3, 4)
	if start < 0 {
		return Finding{}, false
	}
	value := text[start:end]
	entropy := shannonEntropy(value)
	if len(value) < minAssignedLength || entropy < minAssignedEntropy || isPlaceholder(value) {
		return Finding{}, false
	}
	return Finding{Start: start, End: end, Entropy: entropy, Context: group(text, m, 1)}, true
}

// placeholderValues are values that ARE the placeholder. Compared whole and
// case-insensitively rather than as substrings, so a real credential that
// merely contains "test" is not waved through.
var placeholderValues = map[string]bool{
	"password": true, "passwd": true, "pass": true, "secret": true,
	"token": true, "user": true, "username": true, "admin": true,
	"root": true, "test": true, "example": true, "foo": true, "bar": true,
	"none": true, "null": true, "nil": true, "empty": true, "redacted": true,
}

// placeholderMarkers appear inside values that are templates or invitations
// to substitute, not credentials. Substring matched, case-insensitively.
var placeholderMarkers = []string{
	"${", "{{", "}}", "$(", "<%", "%s", "%d", "%v", "%q",
	"changeme", "change-me", "change_me", "placeholder", "replaceme",
	"replace-me", "replace_me", "your-", "your_", "yourpassword",
	"fixme", "todo", "xxxxxxxx", "notasecret",
}

// isPlaceholder reports whether value is documentation rather than a
// credential. Whitespace is disqualifying on its own: no credential
// delivered as a single quoted literal contains a space, and prose that
// happens to sit after an `=` routinely does.
func isPlaceholder(value string) bool {
	lower := strings.ToLower(value)
	if placeholderValues[lower] {
		return true
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return true
	}
	for _, marker := range placeholderMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// collapse sorts findings by position and drops any that overlap one already
// kept, preferring the widest span at each start offset.
//
// Preferring the WIDER span is the safe direction when two rules disagree:
// `dbPassword = "postgres://svc:pw@host"` is matched both as an assignment
// (the whole DSN) and as a connection string (the password). Redacting the
// whole DSN removes a host name that might have been worth keeping;
// redacting only the password would leave a value the assignment rule has
// already judged to be a credential sitting in the store.
func collapse(in []Finding) []Finding {
	if len(in) == 0 {
		// nil rather than an empty slice, so `Scan(x) != nil` reads as
		// "found something" at every call site.
		return nil
	}
	if len(in) == 1 {
		return in
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Start != in[j].Start {
			return in[i].Start < in[j].Start
		}
		return in[i].End > in[j].End
	})
	out := make([]Finding, 0, len(in))
	end := -1
	for _, f := range in {
		if f.Start < end {
			continue
		}
		out = append(out, f)
		end = f.End
	}
	return out
}

// group returns submatch n, or "" when the group did not participate.
func group(text string, m []int, n int) string {
	if 2*n+1 >= len(m) || m[2*n] < 0 {
		return ""
	}
	return text[m[2*n]:m[2*n+1]]
}

// firstMatchedGroup returns the span of the first of the candidate groups
// that participated in the match, or (-1, -1).
func firstMatchedGroup(m []int, candidates ...int) (int, int) {
	for _, n := range candidates {
		if 2*n+1 < len(m) && m[2*n] >= 0 {
			return m[2*n], m[2*n+1]
		}
	}
	return -1, -1
}

// lineOf returns the 1-based line number that byte offset falls on.
func lineOf(text string, offset int) int {
	if offset > len(text) {
		offset = len(text)
	}
	return 1 + strings.Count(text[:offset], "\n")
}

// digest is the short, non-reversible identity of a secret.
func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:digestLen]
}

// shannonEntropy returns the entropy of s in bits per byte.
//
// Bytes, not runes: the values this gates on are ASCII in practice, and
// counting runes would let a UTF-8 blob dilute its own score.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	total := float64(len(s))
	var e float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / total
		e -= p * math.Log2(p)
	}
	return e
}
