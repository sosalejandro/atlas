package report

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// RenderGitHub writes one GitHub Actions workflow command per finding.
//
// This is the cheap path: no upload step, no `security-events: write`
// permission, no code-scanning setup. The runner parses these lines out of the
// step's stdout as it streams and hangs the annotation on the PR diff. The cost
// is that annotations are per-run and not deduped — which is exactly the
// trade-off SARIF exists to make the other way.
//
// The format is `::<verb> file=,line=,endLine=,title=::<message>`; see
// escapeData / escapeProperty for the escaping the runner requires.
func RenderGitHub(w io.Writer, findings []Finding) error {
	bw := bufio.NewWriter(w)
	for _, f := range Sort(findings) {
		if _, err := bw.WriteString(annotationLine(f)); err != nil {
			return fmt.Errorf("render github annotations: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("render github annotations: %w", err)
	}
	return nil
}

// annotationLine formats one workflow command, newline included.
func annotationLine(f Finding) string {
	start, end := f.span()

	props := []string{
		"file=" + escapeProperty(f.Path),
		"line=" + strconv.Itoa(start),
	}
	// Only emit endLine when it actually widens the span. GitHub highlights
	// line..endLine inclusive, so endLine == line is redundant, and an
	// endLine below line makes the runner drop the annotation entirely.
	if end > 0 {
		props = append(props, "endLine="+strconv.Itoa(end))
	}
	props = append(props, "title="+escapeProperty(f.RuleID))

	return fmt.Sprintf("::%s %s::%s\n",
		workflowVerb(f.Severity), strings.Join(props, ","), escapeData(f.Message))
}

// workflowVerb maps a severity onto the three commands the runner understands.
// SARIF calls the mildest level "note" and GitHub calls it "notice"; the
// translation lives here so the rest of the package speaks one vocabulary.
func workflowVerb(s Severity) string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityNote:
		return "notice"
	case SeverityWarning:
		return "warning"
	default:
		return "warning"
	}
}

// escapeData escapes a workflow command's message payload.
//
// The runner reads one command per line, so an unescaped newline in a message
// does not produce a two-line annotation — it terminates the command and feeds
// the remainder back as ordinary log output. A literal '%' has to go first or
// it would corrupt the escapes introduced after it.
func escapeData(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
	)
	return r.Replace(s)
}

// escapeProperty escapes a property VALUE, which needs more than a message
// does: ',' separates properties and ':' ends the property list, so a path
// containing either would silently truncate the annotation's location and hang
// the finding on the wrong file — or on no file at all.
func escapeProperty(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	)
	return r.Replace(s)
}
