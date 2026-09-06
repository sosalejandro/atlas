package onboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PromoteResult records what a promotion did, or would do.
type PromoteResult struct {
	ID   string `json:"id"`
	File string `json:"file"`
	// Line is where the annotation was, or would be, inserted (1-based, in
	// the file as it stands before the edit).
	Line int    `json:"line"`
	Text string `json:"text"`
	// Anchor is the declaration the annotation attaches to, echoed so the
	// user can check the target before agreeing to the edit.
	Anchor  string `json:"anchor,omitempty"`
	Applied bool   `json:"applied"`
	// Skipped carries the reason nothing was written. An empty Skipped with
	// Applied=false means this was a dry run.
	Skipped string `json:"skipped,omitempty"`
}

// annotationMarkers are the annotation forms that already make a
// declaration a declared member of something. Finding any of them above the
// anchor means a human has spoken and promotion must not write over them.
var annotationMarkers = []string{"@atlas:feature", "@atlas:contract", "@testreg"}

// Promote writes the @atlas:feature annotation for one provisional
// capability into the source file, above its anchor declaration.
//
// This is deliberately the long way round. Promotion could write the
// features table directly and be finished in one statement -- and then the
// registry would contain rows no human wrote, which is the one thing the
// registry is for. Writing the annotation instead means the promotion lands
// in the user's source control, is reviewed like any other change, and
// reaches the features table through exactly the ingest path a hand-written
// annotation takes. There is no second way in.
//
// apply=false is a dry run: the result carries the exact line that would be
// written and the file is not touched.
func Promote(root string, c Capability, apply bool) (PromoteResult, error) {
	if c.Anchor == nil {
		return PromoteResult{}, fmt.Errorf(
			"onboard promote: %s has no anchor declaration to annotate", c.Ref())
	}
	res := PromoteResult{
		ID: c.ID, File: c.Anchor.FilePath, Line: c.Anchor.Line,
		Anchor: c.Anchor.Qualified,
	}

	abs, err := resolveInsideRoot(root, c.Anchor.FilePath)
	if err != nil {
		return res, err
	}
	raw, err := os.ReadFile(abs) //nolint:gosec // path is confined to root by resolveInsideRoot.
	if err != nil {
		return res, fmt.Errorf("onboard promote: read %s: %w", c.Anchor.FilePath, err)
	}

	lines, trailingNewline := splitLines(string(raw))
	idx := c.Anchor.Line - 1
	if idx < 0 || idx >= len(lines) {
		// A stale index is the dangerous case: the line number is real but
		// points somewhere else now, and writing to it would annotate the
		// wrong declaration.
		return res, fmt.Errorf(
			"onboard promote: %s:%d is past the end of the file (%d lines) -- re-run `atlas onboard` to refresh the map",
			c.Anchor.FilePath, c.Anchor.Line, len(lines))
	}

	comment := commentPrefix(c.Anchor.FilePath)
	res.Text = leadingWhitespace(lines[idx]) + comment + " @atlas:feature " + c.ID

	if marker, found := existingAnnotation(lines, idx, comment); found {
		res.Skipped = fmt.Sprintf("%s already carries %s -- existing annotations are adopted, never overwritten",
			shortName(c.Anchor.Qualified), marker)
		return res, nil
	}
	if !apply {
		return res, nil
	}

	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:idx]...)
	out = append(out, res.Text)
	out = append(out, lines[idx:]...)
	body := strings.Join(out, "\n")
	if trailingNewline {
		body += "\n"
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil { //nolint:gosec // preserving the repo's own file mode is the caller's business.
		return res, fmt.Errorf("onboard promote: write %s: %w", c.Anchor.FilePath, err)
	}
	res.Applied = true
	return res, nil
}

// existingAnnotation walks the contiguous comment block immediately above
// the declaration looking for an annotation somebody already wrote.
//
// Only the contiguous block counts. A match further up the file belongs to a
// different declaration, and treating it as this one's would silently skip a
// promotion the user asked for.
func existingAnnotation(lines []string, declIdx int, comment string) (string, bool) {
	for i := declIdx - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || !strings.HasPrefix(t, comment) {
			break
		}
		for _, m := range annotationMarkers {
			if strings.Contains(t, m) {
				return m, true
			}
		}
	}
	// A one-line declaration can also carry the annotation as a trailing
	// comment, which the parser accepts and this must not duplicate.
	for _, m := range annotationMarkers {
		if strings.Contains(lines[declIdx], m) {
			return m, true
		}
	}
	return "", false
}

// resolveInsideRoot turns a repo-relative index path into an absolute path
// and refuses anything that escapes the root.
//
// The index never produces such a path, which is the reason the check lives
// here: promotion is the one code path in atlas that writes to arbitrary
// source files, and a guard that depends on every caller getting it right is
// not a guard.
func resolveInsideRoot(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("onboard promote: resolve root %q: %w", root, err)
	}
	abs := filepath.Clean(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("onboard promote: %q resolves outside the project root", rel)
	}
	return abs, nil
}

// commentPrefix picks the line-comment token for a file's language. The
// default is "//" because it covers Go, TypeScript, JavaScript, Java, C and
// Rust; the map names the exceptions atlas can index.
func commentPrefix(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".rb", ".sh", ".yaml", ".yml":
		return "#"
	default:
		return "//"
	}
}

func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// splitLines splits a file into lines and reports whether it ended with a
// newline, so the rewrite can put it back. Dropping a trailing newline turns
// a one-line annotation into a two-line diff in every review that follows.
func splitLines(s string) ([]string, bool) {
	trailing := strings.HasSuffix(s, "\n")
	if trailing {
		s = strings.TrimSuffix(s, "\n")
	}
	if s == "" {
		return nil, trailing
	}
	return strings.Split(s, "\n"), trailing
}
