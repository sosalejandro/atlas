package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// StickyMarker is the HTML comment that makes the PR comment sticky.
//
// A workflow updates the comment in place by listing the PR's comments, finding
// the one whose body contains this marker, and editing it — instead of posting
// a new one on every push and burying the conversation under a column of stale
// bots. The marker is invisible in rendered markdown, must appear exactly once,
// and must never change: change it and every existing comment is orphaned, so
// the next push posts a second one that then sticks alongside the first.
//
// atlas does not post the comment. It emits the body; `gh pr comment` posts it.
const StickyMarker = "<!-- atlas-report:sticky -->"

// DefaultCommentRows is how many findings a rule's section lists before it
// collapses into a "N more" line. A PR comment that scrolls for a screen is a
// PR comment nobody reads; the full set is in the SARIF upload.
const DefaultCommentRows = 10

// SummaryRow is one label/value pair in the comment's summary table.
type SummaryRow struct {
	Label string
	Value string
}

// ScoreChange is one feature's audit-score movement between two snapshots.
type ScoreChange struct {
	FeatureID string
	Before    int
	After     int

	// Delta is After - Before, signed. Carried rather than recomputed so
	// the renderer reports the same number the differ decided on, including
	// when a caller's noise floor suppressed a movement.
	Delta int
}

// Delta is the base-to-head comparison the comment leads with.
//
// It is the part reviewers actually act on: "this PR moved billing.invoice from
// 58 to 31" is a review comment, where "billing.invoice scores 31" is a fact
// about the repository that was already true before the PR. Nil when no base
// snapshot was available — a first run on a new branch has nothing to compare
// against, and inventing a baseline of zero would report every feature as a
// regression.
type Delta struct {
	BaseRef         string
	HeadRef         string
	Regressed       []ScoreChange
	Improved        []ScoreChange
	NewFeatures     []string
	RemovedFeatures []string
}

func (d *Delta) empty() bool {
	return d == nil || (len(d.Regressed) == 0 && len(d.Improved) == 0 &&
		len(d.NewFeatures) == 0 && len(d.RemovedFeatures) == 0)
}

// CommentInput is everything the sticky comment renders.
type CommentInput struct {
	// Title is the comment's H2. Defaults to "Atlas report".
	Title string

	// Summary is the headline table. Rendered in the given order.
	Summary []SummaryRow

	// Delta is the base-to-head movement, or nil when there is no base.
	Delta *Delta

	// Findings are the line-anchored findings, grouped by rule.
	Findings []Finding

	// MaxRowsPerRule caps each rule's listed findings. 0 uses
	// DefaultCommentRows.
	MaxRowsPerRule int

	// Footer is an optional trailing line (the atlas version, a link to the
	// workflow run). Rendered small.
	Footer string
}

// RenderComment writes the sticky PR comment body to w.
//
// The body always starts with StickyMarker, and is always non-empty even with
// no findings: a clean run has to overwrite the previous comment with "no
// findings", or the last failing run's list stays pinned to the PR after the
// developer has already fixed it.
func RenderComment(w io.Writer, in CommentInput) error {
	var b strings.Builder

	title := in.Title
	if title == "" {
		title = "Atlas report"
	}
	fmt.Fprintf(&b, "%s\n## %s\n", StickyMarker, title)

	writeSummaryTable(&b, in.Summary)
	writeDeltaSection(&b, in.Delta)
	writeFindingsSections(&b, in.Findings, in.MaxRowsPerRule)

	if in.Footer != "" {
		fmt.Fprintf(&b, "\n<sub>%s</sub>\n", in.Footer)
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render pr comment: %w", err)
	}
	return nil
}

func writeSummaryTable(b *strings.Builder, rows []SummaryRow) {
	if len(rows) == 0 {
		return
	}
	b.WriteString("\n| Metric | Value |\n| --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(b, "| %s | %s |\n", mdCell(r.Label), mdCell(r.Value))
	}
}

func writeDeltaSection(b *strings.Builder, d *Delta) {
	if d.empty() {
		return
	}
	fmt.Fprintf(b, "\n### Change since `%s`\n\n", d.BaseRef)
	if len(d.Regressed) > 0 || len(d.Improved) > 0 {
		b.WriteString("| Feature | Before | After | Delta |\n| --- | ---: | ---: | ---: |\n")
		// Regressions first: they are the reason the comment exists.
		for _, c := range append(append([]ScoreChange{}, d.Regressed...), d.Improved...) {
			fmt.Fprintf(b, "| `%s` | %d | %d | %+d |\n", c.FeatureID, c.Before, c.After, c.Delta)
		}
	}
	if len(d.NewFeatures) > 0 {
		fmt.Fprintf(b, "\nNew features: %s\n", codeList(d.NewFeatures))
	}
	if len(d.RemovedFeatures) > 0 {
		fmt.Fprintf(b, "\nRemoved features: %s\n", codeList(d.RemovedFeatures))
	}
}

func writeFindingsSections(b *strings.Builder, findings []Finding, maxRows int) {
	b.WriteString("\n### Findings\n\n")
	if len(findings) == 0 {
		b.WriteString("No findings.\n")
		return
	}
	if maxRows <= 0 {
		maxRows = DefaultCommentRows
	}

	sorted := Sort(findings)
	fmt.Fprintf(b, "**%s** — %s.\n", plural(len(sorted), "finding"), severityTally(sorted))

	for _, id := range groupOrder(sorted) {
		writeRuleSection(b, id, filterByRule(sorted, id), maxRows)
	}
}

func writeRuleSection(b *strings.Builder, ruleID string, rows []Finding, maxRows int) {
	label := ruleID
	if r, ok := LookupRule(ruleID); ok {
		label = fmt.Sprintf("%s — %s", ruleID, r.ShortDescription)
	}
	fmt.Fprintf(b, "\n#### %s (%d)\n\n", label, len(rows))

	shown := rows
	if len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	for _, f := range shown {
		start, _ := f.span()
		fmt.Fprintf(b, "- `%s:%d` — %s\n", f.Path, start, oneLine(f.Message))
	}
	if n := len(rows) - len(shown); n > 0 {
		// Say what was hidden. A section silently cut to its first ten
		// rows reads as a ten-finding rule, which is how a gate gets
		// tuned against a number that is not the real one.
		fmt.Fprintf(b, "- ...and %d more (see the code scanning results for the full list)\n", n)
	}
}

// groupOrder returns the rule ids in the order their sections are rendered:
// worst severity present in the group first, then alphabetically. `findings`
// must already be Sort-ed, so the first occurrence of a rule carries its worst
// severity.
func groupOrder(findings []Finding) []string {
	worst := map[string]int{}
	ids := make([]string, 0, 4)
	for _, f := range findings {
		if _, seen := worst[f.RuleID]; !seen {
			worst[f.RuleID] = f.Severity.rank()
			ids = append(ids, f.RuleID)
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if worst[ids[i]] != worst[ids[j]] {
			return worst[ids[i]] < worst[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}

func filterByRule(findings []Finding, ruleID string) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.RuleID == ruleID {
			out = append(out, f)
		}
	}
	return out
}

// severityTally renders "1 error, 2 warnings, 3 notices", omitting the levels
// with no findings.
func severityTally(findings []Finding) string {
	counts := map[Severity]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	parts := make([]string, 0, 3)
	for _, s := range []struct {
		sev  Severity
		noun string
	}{
		{SeverityError, "error"},
		{SeverityWarning, "warning"},
		{SeverityNote, "notice"},
	} {
		if counts[s.sev] > 0 {
			parts = append(parts, plural(counts[s.sev], s.noun))
		}
	}
	if len(parts) == 0 {
		return "no severities recorded"
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func codeList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, "`"+s+"`")
	}
	return strings.Join(quoted, ", ")
}

// oneLine collapses a message onto a single line. A finding message with an
// embedded newline would otherwise break out of its list item and reflow the
// rest of the section.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mdCell escapes the one character that can break a markdown table cell.
func mdCell(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", `\|`)
}
