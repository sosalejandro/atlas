package report_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/report"
)

// updateGolden regenerates the checked-in renderings instead of asserting
// against them:
//
//	go test ./packages/report -update
//
// The goldens exist because nothing in this repo can render SARIF, workflow
// commands, or a PR comment — the only place a reviewer can see what atlas
// actually emits into CI is the checked-in file. Regenerate deliberately and
// read the diff; an unreviewed -update turns the test into a tautology.
var updateGolden = flag.Bool("update", false,
	"regenerate packages/report/testdata/golden/* from the current renderers")

const goldenDir = "testdata/golden"

// goldenComment is the fixture PR comment: the finding set plus the summary
// and delta a real `atlas report pr --base main` would carry.
func goldenComment() report.CommentInput {
	return report.CommentInput{
		Summary: []report.SummaryRow{
			{Label: "Features scored", Value: "18"},
			{Label: "Worst score", Value: "31.0  (billing.invoice)"},
			{Label: "Statements attributed", Value: "4812 / 4930 (97.6%)"},
		},
		Delta: &report.Delta{
			BaseRef: "main",
			HeadRef: "feat/invoice-pdf",
			Regressed: []report.ScoreChange{
				{FeatureID: "billing.invoice", Before: 58, After: 31, Delta: -27},
			},
			Improved: []report.ScoreChange{
				{FeatureID: "auth.login", Before: 71, After: 80, Delta: 9},
			},
			NewFeatures:     []string{"billing.pdf"},
			RemovedFeatures: []string{"billing.legacy-export"},
		},
		Findings: fixtureFindings(),
	}
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(goldenDir, name)
	if *updateGolden {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", goldenDir, err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (regenerate with `go test ./packages/report -update`): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s is stale.\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

func TestGolden_SARIF(t *testing.T) {
	var buf bytes.Buffer
	if err := report.RenderSARIF(&buf, fixtureTool, fixtureFindings()); err != nil {
		t.Fatalf("RenderSARIF: %v", err)
	}
	assertGolden(t, "findings.sarif.json", buf.Bytes())
}

func TestGolden_GitHubAnnotations(t *testing.T) {
	var buf bytes.Buffer
	if err := report.RenderGitHub(&buf, fixtureFindings()); err != nil {
		t.Fatalf("RenderGitHub: %v", err)
	}
	assertGolden(t, "annotations.txt", buf.Bytes())
}

func TestGolden_StickyComment(t *testing.T) {
	var buf bytes.Buffer
	if err := report.RenderComment(&buf, goldenComment()); err != nil {
		t.Fatalf("RenderComment: %v", err)
	}
	assertGolden(t, "comment.md", buf.Bytes())
}

// TestGolden_EmptyRunIsStillARun covers the case CI hits most often once a
// repo is healthy: no findings at all. Every renderer must still produce a
// valid artifact — an empty SARIF file fails `upload-sarif`, and an absent
// comment body leaves the last run's stale findings on the PR forever.
func TestGolden_EmptyRunIsStillARun(t *testing.T) {
	var sarif, anns, comment bytes.Buffer
	if err := report.RenderSARIF(&sarif, fixtureTool, nil); err != nil {
		t.Fatalf("RenderSARIF(nil): %v", err)
	}
	if err := report.RenderGitHub(&anns, nil); err != nil {
		t.Fatalf("RenderGitHub(nil): %v", err)
	}
	if err := report.RenderComment(&comment, report.CommentInput{}); err != nil {
		t.Fatalf("RenderComment(zero): %v", err)
	}
	assertGolden(t, "empty.sarif.json", sarif.Bytes())
	if anns.Len() != 0 {
		t.Errorf("no findings should produce no annotations, got %q", anns.String())
	}
	assertGolden(t, "empty.comment.md", comment.Bytes())
}
