package annotations

import (
	"bytes"
	"strings"
)

// CommentStyle is the closed enum of per-language comment dialects the
// parser understands. styleUnsupported short-circuits Parse to return nil.
type CommentStyle int

const (
	styleUnsupported CommentStyle = iota
	styleGoTS                     // // ...   /* ... */
	stylePython                   // # ...
	styleMarkdown                 // <!-- ... -->
)

// CommentStyleFor returns the dialect for the given file extension.
// Exported so callers that already know the language can bypass
// extension-sniffing.
func CommentStyleFor(ext string) CommentStyle { return commentStyleFor(ext) }

func commentStyleFor(ext string) CommentStyle {
	switch strings.ToLower(ext) {
	case ".go", ".ts", ".tsx", ".js", ".jsx":
		return styleGoTS
	case ".py":
		return stylePython
	case ".md", ".markdown":
		return styleMarkdown
	default:
		return styleUnsupported
	}
}

// logicalLine is one line-of-comment after block-comment unwrapping. lineNum
// is the 1-based line number of the comment opener (`/*` for block
// comments, the line itself for `//` and `#`). Multi-line block comments
// produce ONE logicalLine per inner content line so the parser can pin
// position accurately.
type logicalLine struct {
	text    string
	lineNum int
}

// unwrapBlockComments walks content and returns one logicalLine per
// comment-bearing line. For block comments `/* … */` and `<!-- … -->`,
// each inner line is emitted as a separate logicalLine with the
// content stripped of its delimiters and leading `*`/whitespace.
//
// Non-comment lines are NOT emitted — the parser only inspects comments.
// This is intentional: it makes the @atlas/@testreg matchers O(comment
// lines) instead of O(all lines).
func unwrapBlockComments(content []byte, style CommentStyle) []logicalLine {
	switch style {
	case styleGoTS:
		return unwrapGoTS(content)
	case stylePython:
		return unwrapPython(content)
	case styleMarkdown:
		return unwrapMarkdown(content)
	default:
		return nil
	}
}

func unwrapGoTS(content []byte) []logicalLine {
	var (
		out     []logicalLine
		inBlock bool
	)
	lines := bytes.Split(content, []byte("\n"))
	for i, raw := range lines {
		lineNum := i + 1
		line := string(raw)

		// Inside an open /* … */ block.
		if inBlock {
			// Strip leading * if present (JSDoc style).
			inner := strings.TrimSpace(line)
			inner = strings.TrimPrefix(inner, "*")
			inner = strings.TrimSpace(inner)
			if idx := strings.Index(inner, "*/"); idx >= 0 {
				inner = strings.TrimSpace(inner[:idx])
				inBlock = false
			}
			// Each inner line carries its own lineNum so per-annotation
			// position is accurate (block-comment annotations like
			//
			//   /*
			//    * @atlas:feature x.y      <- line N
			//    * @atlas:owner alice       <- line N+1
			//    */
			//
			// resolve to two records with distinct positions).
			if inner != "" {
				out = append(out, logicalLine{text: inner, lineNum: lineNum})
			}
			continue
		}

		// Line comment `// ...`. Walk all occurrences so we don't trip on
		// `var s = "http://example.com" // @atlas:feature s.x` — the first
		// `//` is inside a string; the second is the real comment.
		if idx := findCommentOpen(line, "//"); idx >= 0 {
			text := strings.TrimSpace(line[idx+2:])
			if text != "" {
				out = append(out, logicalLine{text: text, lineNum: lineNum})
			}
			// Continue to check for block-open in the same line — uncommon but possible.
		}

		// Block comment open `/*` (possibly closes on same line).
		if openIdx := findCommentOpen(line, "/*"); openIdx >= 0 {
			rest := line[openIdx+2:]
			if closeIdx := strings.Index(rest, "*/"); closeIdx >= 0 {
				inner := strings.TrimSpace(rest[:closeIdx])
				if inner != "" {
					out = append(out, logicalLine{text: inner, lineNum: lineNum})
				}
				// Block opened and closed on the same line — keep inBlock=false.
			} else {
				// Block opens on this line but doesn't close — switch to
				// in-block mode for subsequent lines.
				inBlock = true
				inner := strings.TrimSpace(rest)
				if inner != "" {
					out = append(out, logicalLine{text: inner, lineNum: lineNum})
				}
			}
		}
	}
	return out
}

// findCommentOpen returns the index of the first occurrence of `marker` in
// `line` that is NOT inside a single- or double-quoted string literal or a
// backtick raw string. Returns -1 if no real comment opener is found.
//
// The string-tracking is intentionally crude — we only honour `"`, `'`,
// and ` ` and treat `\` as an escape only inside double-quoted strings.
// Good enough for the 95% case where annotations live in normal source;
// edge cases (multiline raw strings, template interpolation) are rare in
// annotation-bearing files.
func findCommentOpen(line, marker string) int {
	var (
		inDouble bool
		inSingle bool
		inTick   bool
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		// Skip escape sequences inside double-quoted strings.
		if inDouble && c == '\\' && i+1 < len(line) {
			i++
			continue
		}
		switch c {
		case '"':
			if !inSingle && !inTick {
				inDouble = !inDouble
			}
			continue
		case '\'':
			if !inDouble && !inTick {
				inSingle = !inSingle
			}
			continue
		case '`':
			if !inDouble && !inSingle {
				inTick = !inTick
			}
			continue
		}
		if inDouble || inSingle || inTick {
			continue
		}
		if i+len(marker) <= len(line) && line[i:i+len(marker)] == marker {
			return i
		}
	}
	return -1
}

func unwrapPython(content []byte) []logicalLine {
	var out []logicalLine
	lines := bytes.Split(content, []byte("\n"))
	for i, raw := range lines {
		line := string(raw)
		if idx := strings.Index(line, "#"); idx >= 0 {
			text := strings.TrimSpace(line[idx+1:])
			if text != "" {
				out = append(out, logicalLine{text: text, lineNum: i + 1})
			}
		}
	}
	return out
}

func unwrapMarkdown(content []byte) []logicalLine {
	var (
		out     []logicalLine
		inBlock bool
	)
	lines := bytes.Split(content, []byte("\n"))
	for i, raw := range lines {
		lineNum := i + 1
		line := string(raw)

		if inBlock {
			text := line
			if idx := strings.Index(text, "-->"); idx >= 0 {
				text = strings.TrimSpace(text[:idx])
				inBlock = false
			}
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, logicalLine{text: text, lineNum: lineNum})
			}
			continue
		}

		if openIdx := strings.Index(line, "<!--"); openIdx >= 0 {
			rest := line[openIdx+4:]
			if closeIdx := strings.Index(rest, "-->"); closeIdx >= 0 {
				inner := strings.TrimSpace(rest[:closeIdx])
				if inner != "" {
					out = append(out, logicalLine{text: inner, lineNum: lineNum})
				}
			} else {
				inBlock = true
				inner := strings.TrimSpace(rest)
				if inner != "" {
					out = append(out, logicalLine{text: inner, lineNum: lineNum})
				}
			}
		}
	}
	return out
}
