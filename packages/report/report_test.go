package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/report"
)

// fixtureTool is the driver identity every test renders with. Pinned so the
// golden files don't churn when the CLI's build version moves.
var fixtureTool = report.Tool{
	Name:            "atlas",
	InformationURI:  "https://github.com/sosalejandro/atlas",
	SemanticVersion: "v0.13.0",
}

// fixtureFindings is one finding per rule family, deliberately supplied in a
// non-sorted order so every renderer's ordering guarantee is exercised.
//
// The severities are NOT all their rule's DefaultLevel. That is the point:
// GitHub gates on the per-finding `level`, and a fixture where every finding
// happens to sit at its rule default cannot tell "the renderer read the
// finding" from "the renderer read the catalog". fixtureOverrides names the
// ones that diverge so the assertions can say which half they are proving.
func fixtureFindings() []report.Finding {
	return []report.Finding{
		{
			RuleID:   report.RuleCoverageUnattributed,
			Severity: report.SeverityNote,
			Path:     "internal/worker/queue.go",
			Line:     1,
			Identity: "gap:internal/worker/queue.go",
			Message:  "118 statements executed but charged to no indexed symbol (reason: no-indexed-symbol)",
		},
		{
			// atlas/feature-uncovered defaults to warning; FromAudit
			// raises a feature under --error-below to an error. This is
			// the real override, and it is what a `fail-on: error` gate
			// reads.
			RuleID:   report.RuleFeatureUncovered,
			Severity: report.SeverityError,
			Path:     "internal/billing/invoice.go",
			Line:     42,
			EndLine:  87,
			Identity: "feature:billing.invoice",
			Message:  "feature billing.invoice scores 12.0/100 (coverage 4.0)",
		},
		{
			// atlas/dead-code defaults to note; this one is pinned to
			// warning so the divergence is exercised in both directions
			// (a rule default above AND below the finding's level).
			RuleID:   report.RuleDeadCode,
			Severity: report.SeverityWarning,
			Path:     "internal/legacy/shim.go",
			Line:     7,
			Identity: "symbol:internal/legacy.Shim",
			Message:  "Shim has 0 incoming import edges (dead-code candidate)",
		},
	}
}

// fixtureOverrides is the subset of fixtureFindings whose Severity differs from
// its rule's DefaultLevel, keyed by rule id. Derived rather than hard-coded, so
// it cannot drift out of sync with the fixture or the catalog.
func fixtureOverrides(t *testing.T) map[string]report.Severity {
	t.Helper()
	out := map[string]report.Severity{}
	for _, f := range fixtureFindings() {
		r, ok := report.LookupRule(f.RuleID)
		if !ok {
			t.Fatalf("fixture finding references rule %q, which is not in the catalog", f.RuleID)
		}
		if f.Severity != r.DefaultLevel {
			out[f.RuleID] = f.Severity
		}
	}
	if len(out) == 0 {
		t.Fatal("no fixture finding overrides its rule default; the severity assertions below " +
			"would pass with the renderers reading the catalog instead of the finding")
	}
	return out
}

// decodeSARIF renders the fixture and unmarshals it into a generic map so the
// assertions below can inspect the wire shape rather than the Go structs — the
// wire shape is what GitHub reads, and it is what silently breaks.
func decodeSARIF(t *testing.T, findings []report.Finding) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := report.RenderSARIF(&buf, fixtureTool, findings); err != nil {
		t.Fatalf("RenderSARIF: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal sarif: %v\n%s", err, buf.String())
	}
	return out
}

func sarifRun(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	runs, ok := doc["runs"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("want exactly one run, got %#v", doc["runs"])
	}
	run, ok := runs[0].(map[string]any)
	if !ok {
		t.Fatalf("run is not an object: %#v", runs[0])
	}
	return run
}

// TestSARIF_EveryResultRuleIsDeclared is the load-bearing SARIF invariant:
// GitHub drops a result whose ruleId is absent from tool.driver.rules without
// any error, so the finding just never appears on the PR. Assert the two sides
// agree instead of trusting them to.
func TestSARIF_EveryResultRuleIsDeclared(t *testing.T) {
	doc := decodeSARIF(t, fixtureFindings())
	run := sarifRun(t, doc)

	driver := run["tool"].(map[string]any)["driver"].(map[string]any)
	rules, _ := driver["rules"].([]any)
	if len(rules) == 0 {
		t.Fatal("tool.driver.rules is empty; every result would be dropped by GitHub")
	}
	declared := map[string]int{}
	for i, r := range rules {
		declared[r.(map[string]any)["id"].(string)] = i
	}

	results, _ := run["results"].([]any)
	if len(results) != len(fixtureFindings()) {
		t.Fatalf("results = %d, want %d", len(results), len(fixtureFindings()))
	}
	for _, raw := range results {
		res := raw.(map[string]any)
		id, _ := res["ruleId"].(string)
		idx, ok := declared[id]
		if !ok {
			t.Errorf("result ruleId %q is not in tool.driver.rules", id)
			continue
		}
		// ruleIndex must point at the same rule, or GitHub renders the
		// wrong rule metadata against the finding.
		if got := int(res["ruleIndex"].(float64)); got != idx {
			t.Errorf("ruleId %q: ruleIndex = %d, want %d", id, got, idx)
		}
	}
}

// TestSARIF_UnknownRuleIsRejected proves the writer refuses to emit a result it
// cannot declare. Failing loudly here is the whole point: the alternative is a
// valid-looking SARIF upload where the finding is invisible.
func TestSARIF_UnknownRuleIsRejected(t *testing.T) {
	findings := []report.Finding{{
		RuleID:   "atlas/not-a-real-rule",
		Severity: report.SeverityWarning,
		Path:     "a.go",
		Line:     1,
		Message:  "nope",
	}}
	err := report.RenderSARIF(&bytes.Buffer{}, fixtureTool, findings)
	if err == nil {
		t.Fatal("want an error for an undeclared rule id, got nil")
	}
	if !strings.Contains(err.Error(), "atlas/not-a-real-rule") {
		t.Errorf("error should name the offending rule; got %v", err)
	}
}

// TestSARIF_FingerprintIgnoresLineNumbers is the dedupe contract. A finding
// that moved because someone added an import above it must keep its
// fingerprint, otherwise GitHub re-reports every finding in the file as new.
func TestSARIF_FingerprintIgnoresLineNumbers(t *testing.T) {
	base := report.Finding{
		RuleID:   report.RuleDeadCode,
		Severity: report.SeverityNote,
		Path:     "internal/legacy/shim.go",
		Line:     7,
		Identity: "symbol:internal/legacy.Shim",
		Message:  "Shim has 0 incoming import edges (dead-code candidate)",
	}
	moved := base
	moved.Line = 91
	moved.EndLine = 140

	if base.Fingerprint() != moved.Fingerprint() {
		t.Errorf("fingerprint changed when only the line moved: %s != %s",
			base.Fingerprint(), moved.Fingerprint())
	}

	// Different subject in the same file must NOT collide, or two findings
	// dedupe into one and the second disappears.
	other := base
	other.Identity = "symbol:internal/legacy.OtherShim"
	if base.Fingerprint() == other.Fingerprint() {
		t.Error("two different subjects share a fingerprint; one would be swallowed")
	}

	// Same subject, different rule: also distinct.
	reruled := base
	reruled.RuleID = report.RuleFeatureUncovered
	if base.Fingerprint() == reruled.Fingerprint() {
		t.Error("rule id is not part of the fingerprint")
	}
}

func TestSARIF_ResultsCarryPartialFingerprints(t *testing.T) {
	run := sarifRun(t, decodeSARIF(t, fixtureFindings()))
	for _, raw := range run["results"].([]any) {
		res := raw.(map[string]any)
		fp, ok := res["partialFingerprints"].(map[string]any)
		if !ok || len(fp) == 0 {
			t.Fatalf("result %v has no partialFingerprints", res["ruleId"])
		}
		if v, _ := fp[report.FingerprintKey].(string); v == "" {
			t.Errorf("result %v: %s fingerprint is empty", res["ruleId"], report.FingerprintKey)
		}
	}
}

// TestSARIF_HeaderIsWellFormed guards the fields GitHub's ingest validates
// before it will accept the upload at all.
func TestSARIF_HeaderIsWellFormed(t *testing.T) {
	doc := decodeSARIF(t, fixtureFindings())
	if doc["$schema"] == "" || doc["$schema"] == nil {
		t.Error("$schema is missing; some ingests reject the document outright")
	}
	if got := doc["version"]; got != "2.1.0" {
		t.Errorf("version = %v, want 2.1.0", got)
	}
	driver := sarifRun(t, doc)["tool"].(map[string]any)["driver"].(map[string]any)
	for _, k := range []string{"name", "informationUri", "semanticVersion"} {
		if v, _ := driver[k].(string); v == "" {
			t.Errorf("tool.driver.%s is empty", k)
		}
	}
	// SARIF semanticVersion is a semver string, and a leading "v" is not
	// semver. atlas's own Version constant carries one, so the writer has
	// to strip it rather than pass the CLI's string through.
	if got := driver["semanticVersion"].(string); strings.HasPrefix(got, "v") {
		t.Errorf("semanticVersion = %q; the v-prefix is not valid semver", got)
	}
}

// TestSARIF_SeverityMapping checks that a result's `level` is the FINDING's
// severity, not its rule's DefaultLevel.
//
// The distinction is the whole contract: `level` is what GitHub colours the
// annotation with and what a `fail-on: error` gate reads, and the rule default
// is only the fallback for a producer with no opinion. A fixture where every
// finding sits at its rule default proves neither, which is why this asserts
// against fixtureOverrides — the findings that deliberately diverge.
func TestSARIF_SeverityMapping(t *testing.T) {
	run := sarifRun(t, decodeSARIF(t, fixtureFindings()))
	levels := map[string]string{}
	for _, raw := range run["results"].([]any) {
		res := raw.(map[string]any)
		levels[res["ruleId"].(string)] = res["level"].(string)
	}

	// Every finding's level is its own severity, override or not.
	for _, f := range fixtureFindings() {
		if got := levels[f.RuleID]; got != string(f.Severity) {
			t.Errorf("%s: level = %q, want the finding's severity %q",
				f.RuleID, got, f.Severity)
		}
	}

	// And say so loudly for the overrides: this is the assertion that fails
	// if the renderer ever falls back to the catalog's DefaultLevel.
	for id, sev := range fixtureOverrides(t) {
		r, _ := report.LookupRule(id)
		if levels[id] == string(r.DefaultLevel) {
			t.Errorf("%s: level = %q, which is the rule DEFAULT; the finding's severity is %q "+
				"and GitHub gates on that", id, levels[id], sev)
		}
	}
}

// TestSARIF_DefaultConfigurationStillCarriesTheRuleDefault: the rule default
// does not disappear when a finding overrides it — it moves to where SARIF puts
// it, tool.driver.rules[].defaultConfiguration.level, which is what a consumer
// falls back to for a result that carries no level of its own.
func TestSARIF_DefaultConfigurationStillCarriesTheRuleDefault(t *testing.T) {
	run := sarifRun(t, decodeSARIF(t, fixtureFindings()))
	driver := run["tool"].(map[string]any)["driver"].(map[string]any)
	for _, raw := range driver["rules"].([]any) {
		rule := raw.(map[string]any)
		id := rule["id"].(string)
		want, ok := report.LookupRule(id)
		if !ok {
			t.Errorf("declared rule %q is not in the catalog", id)
			continue
		}
		cfg, _ := rule["defaultConfiguration"].(map[string]any)
		if got, _ := cfg["level"].(string); got != string(want.DefaultLevel) {
			t.Errorf("%s: defaultConfiguration.level = %q, want %q", id, got, want.DefaultLevel)
		}
	}
}

// TestSARIF_EmptySeverityFallsBackToTheRuleDefault is the other half of the
// same contract: a producer that expresses no opinion gets the catalog's level
// rather than an empty `level`, which GitHub rejects the result over.
func TestSARIF_EmptySeverityFallsBackToTheRuleDefault(t *testing.T) {
	run := sarifRun(t, decodeSARIF(t, []report.Finding{{
		RuleID:   report.RuleDeadCode,
		Path:     "internal/legacy/shim.go",
		Line:     7,
		Identity: "symbol:internal/legacy.Shim",
		Message:  "no severity supplied",
	}}))
	res := run["results"].([]any)[0].(map[string]any)
	rule, _ := report.LookupRule(report.RuleDeadCode)
	if got := res["level"].(string); got != string(rule.DefaultLevel) {
		t.Errorf("level = %q, want the rule default %q", got, rule.DefaultLevel)
	}
}

// TestNormalizePaths_RejectsWhatGitHubWouldSilentlyDrop covers the second
// SARIF footgun: an absolute uri makes the finding vanish from the Files view.
func TestNormalizePaths_RejectsWhatGitHubWouldSilentlyDrop(t *testing.T) {
	root := "/home/dev/repo"
	in := []report.Finding{
		{RuleID: report.RuleDeadCode, Path: "/home/dev/repo/pkg/a.go", Line: 1},
		{RuleID: report.RuleDeadCode, Path: "./pkg/b.go", Line: 1},
		{RuleID: report.RuleDeadCode, Path: `pkg\windows\c.go`, Line: 1},
		{RuleID: report.RuleDeadCode, Path: "/etc/passwd", Line: 1},
		{RuleID: report.RuleDeadCode, Path: "github.com/sosalejandro/atlas/pkg/d.go", Line: 1},
		{RuleID: report.RuleDeadCode, Path: "", Line: 1},
	}
	kept, dropped := report.NormalizePaths(root, in)

	wantKept := []string{"pkg/a.go", "pkg/b.go", "pkg/windows/c.go", "github.com/sosalejandro/atlas/pkg/d.go"}
	if len(kept) != len(wantKept) {
		t.Fatalf("kept %d findings, want %d: %+v", len(kept), len(wantKept), kept)
	}
	for i, w := range wantKept {
		if kept[i].Path != w {
			t.Errorf("kept[%d].Path = %q, want %q", i, kept[i].Path, w)
		}
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped %d findings, want 2 (/etc/passwd and the empty path): %+v", len(dropped), dropped)
	}
}

// TestNormalizePaths_WindowsDriveLettersAreAbsolute covers the absolute form a
// POSIX prefix check does not see.
//
// A Windows runner's scan produces `C:\src\repo\pkg\a.go`. Once the backslashes
// are rewritten it is "C:/src/repo/pkg/a.go", which has no leading "/" — so a
// HasPrefix(p, "/") test reads it as repo-relative and ships it as a SARIF uri
// verbatim. GitHub then resolves it against the checkout, finds nothing, and
// drops the finding from the Files view without a word.
func TestNormalizePaths_WindowsDriveLettersAreAbsolute(t *testing.T) {
	for _, tc := range []struct {
		name string
		root string
		path string
		want string // "" means the finding must be dropped
	}{
		{
			// The root reaches NormalizePaths already forward-slashed
			// (the CLI stores it that way); the finding's path does not.
			name: "drive-letter path with a matching windows root relativises",
			root: "C:/src/repo",
			path: `C:\src\repo\pkg\a.go`,
			want: "pkg/a.go",
		},
		{
			name: "drive-letter path with no root is unresolvable",
			path: `C:\src\repo\pkg\a.go`,
		},
		{
			name: "already-slashed drive-letter path is still absolute",
			path: "C:/src/repo/pkg/a.go",
		},
		{
			name: "lowercase drive letter too",
			path: "d:/src/repo/pkg/a.go",
		},
		{
			name: "a different drive escapes the root",
			root: "C:/src/repo",
			path: `D:\elsewhere\a.go`,
		},
		{
			name: "a drive-letter path under a posix root cannot be relativised",
			root: "/home/dev/repo",
			path: `C:\src\repo\pkg\a.go`,
		},
		{
			name: "a colon that is not a drive letter is an ordinary path",
			root: "/home/dev/repo",
			path: "pkg/weird:name.go",
			want: "pkg/weird:name.go",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := []report.Finding{{RuleID: report.RuleDeadCode, Path: tc.path, Line: 1}}
			kept, dropped := report.NormalizePaths(tc.root, in)
			if tc.want == "" {
				if len(dropped) != 1 {
					t.Fatalf("path %q with root %q was kept as %+v; an unresolvable "+
						"absolute uri vanishes from the Files view instead of being reported",
						tc.path, tc.root, kept)
				}
				return
			}
			if len(kept) != 1 {
				t.Fatalf("path %q with root %q was dropped, want %q", tc.path, tc.root, tc.want)
			}
			if kept[0].Path != tc.want {
				t.Errorf("path = %q, want %q", kept[0].Path, tc.want)
			}
		})
	}
}

// TestRenderGitHub_EscapesWorkflowCommandMetacharacters is the annotation
// footgun: an unescaped comma in a property value ends the property, and an
// unescaped newline ends the whole command. Either one corrupts the run's log
// stream rather than producing a visibly wrong annotation.
func TestRenderGitHub_EscapesWorkflowCommandMetacharacters(t *testing.T) {
	findings := []report.Finding{{
		RuleID:   report.RuleDeadCode,
		Severity: report.SeverityWarning,
		// The colon is the untested half of escapeProperty and the more
		// dangerous one: ':' ends the property list, so an unescaped one
		// in a path makes the runner read the rest of the path as the
		// message and hang the annotation on a truncated file name.
		Path:    "pkg/a,b:c.go",
		Line:    3,
		Message: "100% of\nthe: thing, went wrong",
	}}
	var buf bytes.Buffer
	if err := report.RenderGitHub(&buf, findings); err != nil {
		t.Fatalf("RenderGitHub: %v", err)
	}
	got := strings.TrimRight(buf.String(), "\n")

	// Note the asymmetry, which is the runner's and not a typo here: ':' and
	// ',' are escaped in the property VALUE and left alone in the message,
	// where they are ordinary text.
	want := "::warning file=pkg/a%2Cb%3Ac.go,line=3,title=atlas/dead-code::100%25 of%0Athe: thing, went wrong"
	if got != want {
		t.Errorf("annotation mismatch\n got: %s\nwant: %s", got, want)
	}
	// The location ends at the first unescaped ':' the runner sees, so a
	// leaked colon truncates the file name rather than failing visibly.
	head, _, _ := strings.Cut(strings.TrimPrefix(got, "::warning "), "::")
	if !strings.Contains(head, "file=pkg/a%2Cb%3Ac.go") {
		t.Errorf("property list = %q; the path's ':' leaked and truncated the location", head)
	}
	if strings.Count(got, "\n") != 0 {
		t.Error("annotation spans more than one line; the workflow command is truncated")
	}
}

func TestRenderGitHub_EndLineOnlyWhenItAddsInformation(t *testing.T) {
	findings := []report.Finding{
		{RuleID: report.RuleDeadCode, Severity: report.SeverityNote, Path: "a.go", Line: 5, EndLine: 5, Message: "m"},
		{RuleID: report.RuleDeadCode, Severity: report.SeverityNote, Path: "b.go", Line: 5, EndLine: 9, Message: "m"},
	}
	var buf bytes.Buffer
	if err := report.RenderGitHub(&buf, findings); err != nil {
		t.Fatalf("RenderGitHub: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 annotations, got %d: %q", len(lines), lines)
	}
	if strings.Contains(lines[0], "endLine") {
		t.Errorf("endLine == line should be omitted: %s", lines[0])
	}
	if !strings.Contains(lines[1], "endLine=9") {
		t.Errorf("endLine 9 should be emitted: %s", lines[1])
	}
}

// TestRenderComment_MarkerLeadsTheBody is what makes the comment sticky: the
// workflow finds its previous comment by grepping for the marker, so it has to
// be present, unique, and stable across runs.
func TestRenderComment_MarkerLeadsTheBody(t *testing.T) {
	var buf bytes.Buffer
	in := report.CommentInput{Findings: fixtureFindings()}
	if err := report.RenderComment(&buf, in); err != nil {
		t.Fatalf("RenderComment: %v", err)
	}
	body := buf.String()
	if !strings.HasPrefix(body, report.StickyMarker+"\n") {
		t.Fatalf("body must start with the sticky marker, got:\n%s", body[:min(len(body), 120)])
	}
	if strings.Count(body, report.StickyMarker) != 1 {
		t.Error("marker must appear exactly once or a naive matcher finds the wrong comment")
	}
}

func TestRenderComment_TruncatesPerRuleAndSaysSo(t *testing.T) {
	findings := make([]report.Finding, 0, 12)
	for i := range 12 {
		findings = append(findings, report.Finding{
			RuleID:   report.RuleDeadCode,
			Severity: report.SeverityNote,
			Path:     "pkg/a.go",
			Line:     i + 1,
			Identity: string(rune('a' + i)),
			Message:  "dead",
		})
	}
	var buf bytes.Buffer
	err := report.RenderComment(&buf, report.CommentInput{Findings: findings, MaxRowsPerRule: 3})
	if err != nil {
		t.Fatalf("RenderComment: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "9 more") {
		t.Errorf("truncation notice missing; a silently short list reads as a clean run:\n%s", body)
	}
}
