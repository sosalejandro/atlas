package annotations

import (
	"bytes"
	"iter"
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

// splitLines yields each line of content with its 1-based number, as a
// SUBSLICE of content rather than a copy, and with one trailing "\r"
// removed.
//
// Both halves of that sentence are the point.
//
// The subslice is why this is not bytes.Split: Split allocates a header per
// line for the whole file, and the callers then did string(line) on every
// one of them — a full second copy of the file, on top of the copy
// ParseRelative used to make rebuilding it (issue #152: unwrapGoTS, 13.50 MB
// flat). Only the comment text is kept, so only the comment text is copied,
// in appendLogical.
//
// The "\r" is inherited behaviour, made explicit. ParseRelative used to read
// files through a bufio.Scanner, and bufio.ScanLines drops a trailing "\r"
// as well as the "\n" — including on a final line with no "\n" at all — so
// a CRLF file arrived at the matchers already normalised to LF. Nobody chose
// that; it fell out of the reader. Now that ParseRelative hands over the
// file's real bytes, the normalisation has to happen here or not at all, and
// "not at all" would change what atlas reports for CRLF sources. Note the
// asymmetry, which is also ScanLines': a "\r" that does NOT precede a "\n"
// is ordinary content, so a classic-Mac CR-only file stays one long line.
// lineendings_test.go pins all of it.
func splitLines(content []byte) iter.Seq2[int, []byte] {
	return func(yield func(int, []byte) bool) {
		for lineNum := 1; ; lineNum++ {
			i := bytes.IndexByte(content, '\n')
			if i < 0 {
				yield(lineNum, dropCR(content))
				return
			}
			if !yield(lineNum, dropCR(content[:i])) {
				return
			}
			content = content[i+1:]
		}
	}
}

func dropCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

// appendLogical appends text as a logicalLine unless it is empty (a blank
// comment carries nothing to match against).
//
// This is the ONLY place a line's bytes become a string. Everything above it
// works on subslices of the file the caller already holds, so the copy is
// paid for the handful of comment lines a source file has rather than for
// every line in it.
func appendLogical(out []logicalLine, text []byte, lineNum int) []logicalLine {
	if len(text) == 0 {
		return out
	}
	return append(out, logicalLine{text: string(text), lineNum: lineNum})
}

var (
	lineCommentOpen = []byte("//")
	blockOpen       = []byte("/*")
	blockClose      = []byte("*/")
	jsdocStar       = []byte("*")
	mdOpen          = []byte("<!--")
	mdClose         = []byte("-->")
)

func unwrapGoTS(content []byte) []logicalLine {
	var (
		out     []logicalLine
		inBlock bool
	)
	for lineNum, line := range splitLines(content) {
		// Inside an open /* … */ block.
		if inBlock {
			// Strip leading * if present (JSDoc style).
			inner := bytes.TrimSpace(line)
			inner = bytes.TrimPrefix(inner, jsdocStar)
			inner = bytes.TrimSpace(inner)
			if idx := bytes.Index(inner, blockClose); idx >= 0 {
				inner = bytes.TrimSpace(inner[:idx])
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
			out = appendLogical(out, inner, lineNum)
			continue
		}

		// Line comment `// ...`. Walk all occurrences so we don't trip on
		// `var s = "http://example.com" // @atlas:feature s.x` — the first
		// `//` is inside a string; the second is the real comment.
		if idx := findCommentOpen(line, lineCommentOpen); idx >= 0 {
			out = appendLogical(out, bytes.TrimSpace(line[idx+2:]), lineNum)
			// Continue to check for block-open in the same line — uncommon but possible.
		}

		// Block comment open `/*` (possibly closes on same line).
		if openIdx := findCommentOpen(line, blockOpen); openIdx >= 0 {
			rest := line[openIdx+2:]
			if closeIdx := bytes.Index(rest, blockClose); closeIdx >= 0 {
				out = appendLogical(out, bytes.TrimSpace(rest[:closeIdx]), lineNum)
				// Block opened and closed on the same line — keep inBlock=false.
			} else {
				// Block opens on this line but doesn't close — switch to
				// in-block mode for subsequent lines.
				inBlock = true
				out = appendLogical(out, bytes.TrimSpace(rest), lineNum)
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
func findCommentOpen(line, marker []byte) int {
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
		if bytes.HasPrefix(line[i:], marker) {
			return i
		}
	}
	return -1
}

func unwrapPython(content []byte) []logicalLine {
	var out []logicalLine
	for lineNum, line := range splitLines(content) {
		if idx := bytes.IndexByte(line, '#'); idx >= 0 {
			out = appendLogical(out, bytes.TrimSpace(line[idx+1:]), lineNum)
		}
	}
	return out
}

func unwrapMarkdown(content []byte) []logicalLine {
	var (
		out     []logicalLine
		inBlock bool
	)
	for lineNum, line := range splitLines(content) {
		if inBlock {
			text := line
			if idx := bytes.Index(text, mdClose); idx >= 0 {
				text = text[:idx]
				inBlock = false
			}
			out = appendLogical(out, bytes.TrimSpace(text), lineNum)
			continue
		}

		if openIdx := bytes.Index(line, mdOpen); openIdx >= 0 {
			rest := line[openIdx+4:]
			if closeIdx := bytes.Index(rest, mdClose); closeIdx >= 0 {
				out = appendLogical(out, bytes.TrimSpace(rest[:closeIdx]), lineNum)
			} else {
				inBlock = true
				out = appendLogical(out, bytes.TrimSpace(rest), lineNum)
			}
		}
	}
	return out
}
