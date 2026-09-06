package sqlops

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
)

// DirectivePrefix is the comment marker that silences an advisory.
//
//	// atlas:sql-ignore sql.unbounded-list,sql.select-star
//
// Every advisory in this package must be suppressible, because every one of
// them has a legitimate exception -- the deliberately unbounded read over a
// table that holds nine rows, the offset pagination on an admin screen nobody
// pages past the second page of. A check with no way to say "yes, I know" gets
// turned off wholesale, and then it protects nothing.
const DirectivePrefix = "atlas:sql-ignore"

// DirectiveAll silences every code at a site.
const DirectiveAll = "all"

// commentMarks indexes a file's suppression directives by line, plus the set
// of lines that hold any comment at all -- needed to walk up from a call
// through a contiguous comment block.
type commentMarks struct {
	directives map[int][]string
	commented  map[int]bool
}

// at returns the codes suppressed for a statement on the given line: those on
// the line itself, plus those in the contiguous comment block immediately
// above it. The block walk is what makes the natural spelling work --
// developers put the directive on its own line above the call, not trailing
// it.
func (m commentMarks) at(line int) []string {
	var out []string
	out = append(out, m.directives[line]...)
	for l := line - 1; m.commented[l]; l-- {
		out = append(out, m.directives[l]...)
	}
	return out
}

// indexComments builds the per-file directive index.
func indexComments(fset *token.FileSet, f *ast.File) commentMarks {
	m := commentMarks{directives: map[int][]string{}, commented: map[int]bool{}}
	for _, group := range f.Comments {
		for _, c := range group.List {
			line := fset.Position(c.Slash).Line
			// A block comment spanning lines marks each of them, so the
			// walk-up does not stop at its closing line.
			end := fset.Position(c.End()).Line
			for l := line; l <= end; l++ {
				m.commented[l] = true
			}
			if codes := parseDirective(c.Text); len(codes) > 0 {
				m.directives[line] = append(m.directives[line], codes...)
			}
		}
	}
	return m
}

// directivesIn pulls suppression codes out of a doc comment group, so a
// directive on a function applies to every query inside it.
func directivesIn(doc *ast.CommentGroup) []string {
	if doc == nil {
		return nil
	}
	var out []string
	for _, c := range doc.List {
		out = append(out, parseDirective(c.Text)...)
	}
	return out
}

// parseDirective extracts the codes from one comment. Text after the codes is
// ignored, so `// atlas:sql-ignore sql.select-star -- payload is versioned`
// works and reads well.
func parseDirective(text string) []string {
	idx := strings.Index(text, DirectivePrefix)
	if idx < 0 {
		return nil
	}
	rest := strings.TrimSpace(text[idx+len(DirectivePrefix):])
	if rest == "" {
		return nil
	}
	field := strings.Fields(rest)[0]
	var out []string
	for _, code := range strings.Split(field, ",") {
		if code = strings.TrimSpace(code); code != "" {
			out = append(out, code)
		}
	}
	return out
}

// mergeSuppressions unions the code lists and returns them sorted and unique
// so stored rows are stable across runs.
func mergeSuppressions(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, c := range l {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}
