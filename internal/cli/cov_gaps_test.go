package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/store"
)

// covFixture is the per-test scaffolding for `atlas cov status`: a tempdir
// with a .atlas/atlas.db, and the package-level singletons pinned at it so
// successive runs don't bleed configuration. Mirrors deadFixture.
type covFixture struct {
	root   string
	dbPath string
}

func newCovFixture(t *testing.T) *covFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(dir, ".atlas", "atlas.db")
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return &covFixture{root: dir, dbPath: dbPath}
}

// seedAttributedRun writes the run + gap rows a go-cover ingest would have
// left behind, so the command under test reads them back the way any consumer
// that did not run the ingest itself must.
func (f *covFixture) seedAttributedRun(t *testing.T, gaps []store.CoverageGap) int64 {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	runID, err := s.Coverage().InsertRun(ctx, store.CoverageRun{
		Framework:         store.FrameworkGoTest,
		FilesInReport:     3,
		FilesMatched:      1,
		FilesUnmatched:    2,
		StmtsAttributed:   40,
		StmtsUnattributed: 60,
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if _, err := s.CoverageGaps().Insert(ctx, runID, gaps); err != nil {
		t.Fatalf("CoverageGaps.Insert: %v", err)
	}
	return runID
}

func runCovStatusCmd(t *testing.T, fix *covFixture, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"cov", "status", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), err
}

func TestCovStatus_GapsFlagWired(t *testing.T) {
	if newCovStatusCmd().Flags().Lookup("gaps") == nil {
		t.Fatal("atlas cov status is missing --gaps")
	}
}

// The whole point of issue #100: the attribution gap must be inspectable
// without re-ingesting the profile.
func TestCovStatus_GapsReadsPersistedAttribution(t *testing.T) {
	fix := newCovFixture(t)
	fix.seedAttributedRun(t, []store.CoverageGap{
		{Path: "github.com/org/repo/generated/queries.sql.go", Stmts: 50, Reason: "no-indexed-symbol"},
		{Path: "github.com/org/repo/billing/svc.go", Stmts: 10, Reason: "outside-symbol-spans"},
	})

	out, err := runCovStatusCmd(t, fix, "--gaps")
	if err != nil {
		t.Fatalf("cov status --gaps: %v", err)
	}
	for _, want := range []string{
		"60/100",                   // unattributed / total statements
		"1/3",                      // files matched / files in report
		"generated/queries.sql.go", // the biggest loss, by name
		"no-indexed-symbol",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Biggest loss first, so a truncated read still sees the worst offender.
	if strings.Index(out, "queries.sql.go") > strings.Index(out, "billing/svc.go") {
		t.Errorf("gaps are not ordered biggest-loss-first:\n%s", out)
	}
}

// Without --gaps the command's output is unchanged — the flag is additive.
func TestCovStatus_WithoutGapsFlagStaysQuiet(t *testing.T) {
	fix := newCovFixture(t)
	fix.seedAttributedRun(t, []store.CoverageGap{
		{Path: "github.com/org/repo/generated/queries.sql.go", Stmts: 50, Reason: "no-indexed-symbol"},
	})

	out, err := runCovStatusCmd(t, fix)
	if err != nil {
		t.Fatalf("cov status: %v", err)
	}
	if strings.Contains(out, "queries.sql.go") {
		t.Errorf("gap list leaked into the default view:\n%s", out)
	}
}

// A run ingested before schema 0011 (or by a framework with no statement
// coverage) carries no accounting. Saying so is honest; printing 0/0 as
// though everything was attributed is not.
func TestCovStatus_GapsOnRunWithoutAttribution(t *testing.T) {
	fix := newCovFixture(t)
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := s.Coverage().InsertRun(ctx, store.CoverageRun{Framework: store.FrameworkPlaywright}); err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	_ = s.Close()

	out, err := runCovStatusCmd(t, fix, "--gaps")
	if err != nil {
		t.Fatalf("cov status --gaps: %v", err)
	}
	if !strings.Contains(out, "no attribution metadata") {
		t.Errorf("output does not flag the missing accounting:\n%s", out)
	}
}

// --json carries the full accounting so CI can gate on it ("fail if
// unattributed > 10%") without scraping the terminal view.
func TestCovStatus_GapsJSON(t *testing.T) {
	fix := newCovFixture(t)
	fix.seedAttributedRun(t, []store.CoverageGap{
		{Path: "a.go", Stmts: 50, Reason: "no-indexed-symbol"},
		{Path: "b.go", Stmts: 10, Reason: "outside-symbol-spans"},
	})

	out, err := runCovStatusCmd(t, fix, "--gaps", "--json")
	if err != nil {
		t.Fatalf("cov status --gaps --json: %v", err)
	}
	var env struct {
		Result struct {
			Attribution *struct {
				FilesInReport     int `json:"files_in_report"`
				StmtsAttributed   int `json:"stmts_attributed"`
				StmtsUnattributed int `json:"stmts_unattributed"`
				GapsTruncated     int `json:"gaps_truncated"`
				Gaps              []struct {
					Path   string `json:"path"`
					Stmts  int    `json:"stmts"`
					Reason string `json:"reason"`
				} `json:"gaps"`
			} `json:"attribution"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err, out)
	}
	a := env.Result.Attribution
	if a == nil {
		t.Fatal("result.attribution absent")
	}
	if a.StmtsUnattributed != 60 || a.StmtsAttributed != 40 || a.FilesInReport != 3 {
		t.Errorf("attribution = %+v, want 40/60 over 3 files", *a)
	}
	if len(a.Gaps) != 2 || a.Gaps[0].Path != "a.go" || a.Gaps[0].Stmts != 50 {
		t.Errorf("gaps = %+v, want a.go first", a.Gaps)
	}
}
