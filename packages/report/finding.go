package report

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
)

// Severity is the finding's importance, named with the SARIF `level` vocabulary
// so the mapping to SARIF is the identity and the mapping to a workflow command
// is a one-word lookup. Three levels is deliberate: GitHub renders exactly
// three (error / warning / notice) and inventing a fourth would collapse into
// one of them at the boundary anyway.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityNote    Severity = "note"
)

// rank orders severities worst-first for display. Unknown values sort last so
// a future level added by a producer degrades to "least important" rather than
// jumping to the top of the PR comment.
func (s Severity) rank() int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	case SeverityNote:
		return 2
	default:
		return 3
	}
}

// Finding is the single line-anchored fact every renderer consumes.
//
// Path is repo-relative with forward slashes — see NormalizePaths, which is
// the only supported way to get one. Line is 1-based; EndLine is optional and
// zero means "the finding is a point, not a span".
type Finding struct {
	// RuleID is one of the Rule* constants. A finding whose rule is not in
	// the catalog is refused by RenderSARIF rather than emitted, because
	// GitHub drops such a result without reporting anything.
	RuleID string

	// Severity drives the SARIF level and the workflow command verb.
	Severity Severity

	// Path is repo-relative, forward-slashed, with no leading "./".
	Path string

	// Line is the 1-based start line. Zero is tolerated by the renderers
	// (clamped to 1) because several producers describe a whole file.
	Line int

	// EndLine is the last line of the span, or 0 for a point finding. It is
	// also ignored when it is not greater than Line.
	EndLine int

	// Message is the human sentence shown on the annotation. It may contain
	// any character; the renderers escape for their own format.
	Message string

	// Identity names the *subject* of the finding — the feature id, the
	// symbol's qualified name, the file whose coverage is unattributed. It
	// is the fingerprint input, so it must be stable across pushes: no line
	// numbers, no scores, no counts. Empty falls back to Message, which is
	// correct only when the message itself carries no volatile numbers.
	Identity string
}

// FingerprintKey is the partialFingerprints key SARIF results carry. GitHub
// treats the key as opaque but compares per-key, so changing this string
// re-reports every open finding once; treat it as a wire constant.
const FingerprintKey = "atlasFindingV1"

// Fingerprint is the stable identity GitHub dedupes on across pushes.
//
// It is derived from rule id + path + subject identity and deliberately NOT
// from the line number: a finding that moved because someone added an import
// above it is the same finding, and fingerprinting the line would mark every
// finding in the file as new after any edit — which is how a code-scanning
// integration turns into noise nobody looks at.
//
// It is a method rather than a stored field so it cannot go stale: a
// fingerprint field set at construction and then edited alongside the message
// would silently describe a finding that no longer exists.
func (f Finding) Fingerprint() string {
	subject := f.Identity
	if subject == "" {
		subject = f.Message
	}
	// NUL separators, because the parts are user-derived strings and a
	// plain concatenation lets ("a/b", "c") collide with ("a", "b/c").
	h := sha256.Sum256([]byte(f.RuleID + "\x00" + f.Path + "\x00" + subject))
	return hex.EncodeToString(h[:8])
}

// span returns the start and end line to render, clamped into the range every
// consumer accepts: SARIF rejects startLine < 1, and an endLine that is not
// greater than startLine is noise the renderers omit.
func (f Finding) span() (start, end int) {
	start = f.Line
	if start < 1 {
		start = 1
	}
	if f.EndLine > start {
		end = f.EndLine
	}
	return start, end
}

// Sort orders findings worst-first, then by location, so two runs over the same
// state produce byte-identical output. Determinism is not cosmetic here: a
// sticky comment that reshuffles on every push shows a diff where nothing
// changed, and a SARIF file with unstable ordering defeats caching.
//
// It sorts a copy; producers routinely pass slices they still own.
func Sort(in []Finding) []Finding {
	out := make([]Finding, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Severity.rank() != b.Severity.rank() {
			return a.Severity.rank() < b.Severity.rank()
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.RuleID != b.RuleID {
			return a.RuleID < b.RuleID
		}
		return a.Message < b.Message
	})
	return out
}

// NormalizePaths rewrites each finding's Path into the repo-relative,
// forward-slashed form GitHub requires and returns the findings that could not
// be expressed that way.
//
// This is the second SARIF footgun and it fails silently: an absolute uri makes
// every finding vanish from the Files view, with a green "analysis uploaded"
// check to reassure you. Anything that survives normalisation is renderable;
// anything in `dropped` would have disappeared, so the caller reports the count
// rather than shipping a report that quietly says nothing.
//
// `root` is the repo root used to relativise absolute paths. Pass "" when paths
// are already relative.
func NormalizePaths(root string, in []Finding) (kept, dropped []Finding) {
	kept = make([]Finding, 0, len(in))
	for _, f := range in {
		p, ok := normalizePath(root, f.Path)
		if !ok {
			dropped = append(dropped, f)
			continue
		}
		f.Path = p
		kept = append(kept, f)
	}
	return kept, dropped
}

// normalizePath does the per-path work. Returns ok=false for anything that
// would not resolve against the repo checkout: an empty path, a path outside
// the root, or an absolute path with no root to relativise it against.
func normalizePath(root, p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	// Windows-produced paths reach us with backslashes; SARIF uris and
	// workflow commands are forward-slashed regardless of the runner OS.
	p = strings.ReplaceAll(p, `\`, "/")

	if isAbsolutePath(p) {
		if root == "" {
			return "", false
		}
		rel, err := filepath.Rel(filepath.ToSlash(root), p)
		if err != nil {
			return "", false
		}
		p = filepath.ToSlash(rel)
	}

	p = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "./")
	// A path that still escapes the root cannot be annotated on the PR —
	// there is no such file in the checkout.
	if p == "." || p == ".." || strings.HasPrefix(p, "../") || isAbsolutePath(p) {
		return "", false
	}
	return p, true
}

// isAbsolutePath reports whether an already-forward-slashed path is absolute in
// either of the two forms that reach us.
//
// This is deliberately NOT filepath.IsAbs: that answers for the OS running
// atlas, and the path in a finding was produced wherever the scan ran. A
// Windows runner's "C:/src/repo/pkg/a.go" is repo-relative to a Linux
// filepath.IsAbs, so it would sail through unrelativised and then vanish from
// the Files view — the exact silent failure NormalizePaths exists to catch.
func isAbsolutePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	// Drive-letter form: a single ASCII letter, a colon, and either the end
	// of the path or a separator ("C:", "C:/src/a.go"). A bare "C:foo" is a
	// drive-relative path, which is no more resolvable against the checkout.
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
