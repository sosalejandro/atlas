package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/report"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// reportFixture is a tempdir repo root + .atlas/atlas.db, with the package
// singletons pointed at it. Mirrors deadFixture in dead_test.go; the
// singletons make t.Parallel() unsafe here (see NewRootCmd's note).
type reportFixture struct {
	root   string
	dbPath string
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(dir, ".atlas", "atlas.db")
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return &reportFixture{root: dir, dbPath: dbPath}
}

// seed writes the smallest state that produces one finding of each kind the
// report command collects by default: an annotated-but-unverified feature
// (audit scores it at the presence floor) and a coverage run with an
// unattributed file.
func (f *reportFixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	end := 87
	symID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "internal/billing.Invoice",
		Kind:          shared.KindFunc,
		// Repo-relative, as the scanner writes them. NormalizePaths'
		// absolute-path handling is exercised in packages/report; here
		// the point is that the CLI feeds it the repo root at all.
		FilePath: "internal/billing/invoice.go",
		Line:     42,
		EndLine:  &end,
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
	if err := s.Features().Upsert(ctx, store.Feature{
		ID:    "billing.invoice",
		Title: "Invoice generation",
		Kind:  store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: "billing.invoice",
		SymbolID:  symID,
		Role:      store.RoleImpl,
		Source:    store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("link feature symbol: %v", err)
	}

	runID, err := s.Coverage().InsertRun(ctx, store.CoverageRun{
		Framework:         store.FrameworkGoTest,
		StartedAt:         time.Unix(1_700_000_000, 0).UTC(),
		FinishedAt:        time.Unix(1_700_000_060, 0).UTC(),
		SummaryJSON:       "{}",
		StmtsAttributed:   900,
		StmtsUnattributed: 118,
	})
	if err != nil {
		t.Fatalf("insert coverage run: %v", err)
	}
	if _, err := s.CoverageGaps().Insert(ctx, runID, []store.CoverageGap{
		{Path: "internal/worker/queue.go", Stmts: 118, Reason: "no-indexed-symbol"},
	}); err != nil {
		t.Fatalf("insert gaps: %v", err)
	}
}

func runReportCmd(t *testing.T, fix *reportFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"report", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestReport_Registered guards the one line in root.go. A command that is not
// registered is not delivered, whatever the package behind it does.
func TestReport_Registered(t *testing.T) {
	root := NewRootCmd()
	var found bool
	for _, c := range root.Commands() {
		if c.Name() == "report" {
			found = true
			for _, want := range []string{"sarif", "github", "pr"} {
				var sub bool
				for _, s := range c.Commands() {
					if s.Name() == want {
						sub = true
					}
				}
				if !sub {
					t.Errorf("atlas report is missing the %q subcommand", want)
				}
			}
		}
	}
	if !found {
		t.Fatal("atlas report is not registered on the root command")
	}
}

// TestReport_SARIF_IsUploadable is the end-to-end assertion that matters: what
// lands on stdout has to be something `github/codeql-action/upload-sarif` will
// accept AND display. Both halves are checked — a document that uploads but
// shows nothing is the failure mode this command exists to avoid.
func TestReport_SARIF_IsUploadable(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	stdout, stderr, err := runReportCmd(t, fix, "sarif")
	if err != nil {
		t.Fatalf("report sarif: %v\nstderr:\n%s", err, stderr)
	}

	var doc struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name            string `json:"name"`
					SemanticVersion string `json:"semanticVersion"`
					Rules           []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				Level     string `json:"level"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid SARIF json: %v\n%s", err, stdout)
	}
	if doc.Version != "2.1.0" || doc.Schema == "" {
		t.Errorf("header = version %q schema %q", doc.Version, doc.Schema)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(doc.Runs))
	}
	run := doc.Runs[0]
	if run.Tool.Driver.Name != "atlas" {
		t.Errorf("driver name = %q", run.Tool.Driver.Name)
	}
	if strings.HasPrefix(run.Tool.Driver.SemanticVersion, "v") {
		t.Errorf("semanticVersion %q keeps the v-prefix", run.Tool.Driver.SemanticVersion)
	}

	var sawFeature bool
	for _, r := range run.Results {
		if r.RuleID != report.RuleFeatureUncovered {
			continue
		}
		sawFeature = true
		uri := r.Locations[0].PhysicalLocation.ArtifactLocation.URI
		if uri != "internal/billing/invoice.go" {
			t.Errorf("uri = %q; an absolute or unrelativised path vanishes from the Files view", uri)
		}
		if r.PartialFingerprints[report.FingerprintKey] == "" {
			t.Error("result carries no partialFingerprints; GitHub will re-report it every push")
		}
	}
	if !sawFeature {
		t.Fatalf("no %s result; the seeded feature scores at the presence floor and should be reported:\n%s",
			report.RuleFeatureUncovered, stdout)
	}
}

func TestReport_GitHubAnnotationsGoToStdout(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	stdout, stderr, err := runReportCmd(t, fix, "github")
	if err != nil {
		t.Fatalf("report github: %v\nstderr:\n%s", err, stderr)
	}
	// The seeded feature has a coverage signal of 0, which lands under the
	// default --error-below of 30 rather than in the warning band.
	if !strings.Contains(stdout, "::error file=internal/billing/invoice.go,line=42,endLine=87") {
		t.Errorf("missing the feature annotation:\n%s", stdout)
	}
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if !strings.HasPrefix(line, "::") {
			t.Errorf("non-command line on stdout would be logged verbatim by the runner: %q", line)
		}
	}
}

// TestReport_PRCommentIsSticky checks the property that makes the comment
// updatable in place rather than duplicated on every push.
func TestReport_PRCommentIsSticky(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	stdout, stderr, err := runReportCmd(t, fix, "pr")
	if err != nil {
		t.Fatalf("report pr: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.HasPrefix(stdout, report.StickyMarker) {
		t.Fatalf("body does not lead with the sticky marker:\n%s", stdout)
	}
	if !strings.Contains(stdout, "billing.invoice") {
		t.Errorf("comment omits the finding:\n%s", stdout)
	}
	// The coverage gap has no repo-relative path in this fixture either
	// way, so the comment is where it has to survive.
	if !strings.Contains(stdout, "internal/worker/queue.go") {
		t.Errorf("comment omits the coverage gap:\n%s", stdout)
	}
}

// TestReport_MissingBaseSnapshotDegrades: a first run on a new branch has no
// base to diff against. That is the normal case, not an error — failing here
// would break the workflow on exactly the push that introduces it.
func TestReport_MissingBaseSnapshotDegrades(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	stdout, _, err := runReportCmd(t, fix, "pr", "--base", "refs/heads/nope", "--json")
	if err != nil {
		t.Fatalf("report pr --base with no snapshot should not fail: %v", err)
	}
	var env struct {
		Command  string   `json:"command"`
		Warnings []string `json:"warnings"`
		Result   struct {
			Body     string `json:"body"`
			Marker   string `json:"marker"`
			Findings int    `json:"finding_count"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	if env.Command != "report.pr" {
		t.Errorf("command = %q, want report.pr", env.Command)
	}
	if env.Result.Marker != report.StickyMarker {
		t.Errorf("marker = %q; the workflow needs it to find the comment", env.Result.Marker)
	}
	if env.Result.Body == "" {
		t.Error("result.body is empty")
	}
	if len(env.Warnings) == 0 {
		t.Error("a missing base snapshot must be reported as a warning, not swallowed")
	}
}

// TestReport_JSONEnvelopeCarriesTheRendering locks the envelope shape for
// `report sarif --json`: consumers read result.body, not stdout.
func TestReport_JSONEnvelopeCarriesTheRendering(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	stdout, _, err := runReportCmd(t, fix, "sarif", "--json")
	if err != nil {
		t.Fatalf("report sarif --json: %v", err)
	}
	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Format       string `json:"format"`
			Body         string `json:"body"`
			FindingCount int    `json:"finding_count"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	if env.SchemaVersion != "v1" || env.Command != "report.sarif" {
		t.Errorf("envelope = %q / %q", env.SchemaVersion, env.Command)
	}
	if env.Result.Format != "sarif" {
		t.Errorf("result.format = %q", env.Result.Format)
	}
	if !strings.Contains(env.Result.Body, `"version": "2.1.0"`) {
		t.Errorf("result.body is not the SARIF document:\n%s", env.Result.Body)
	}
	if env.Result.FindingCount < 1 {
		t.Errorf("finding_count = %d, want at least 1", env.Result.FindingCount)
	}
}

// TestReport_OutWritesTheFile covers the flag CI actually uses: upload-sarif
// takes a path, not a pipe.
func TestReport_OutWritesTheFile(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	out := filepath.Join(fix.root, "atlas.sarif")
	if _, stderr, err := runReportCmd(t, fix, "sarif", "--out", out); err != nil {
		t.Fatalf("report sarif --out: %v\nstderr:\n%s", err, stderr)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s: %v", out, err)
	}
	if !bytes.Contains(raw, []byte(`"$schema"`)) {
		t.Errorf("--out file is not SARIF:\n%s", raw)
	}
}

// TestReport_DeadCodeIsOptIn: dead-code candidates are a triage list with known
// false positives, so they must not appear on a PR unless asked for.
func TestReport_DeadCodeIsOptIn(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	stdout, _, err := runReportCmd(t, fix, "github")
	if err != nil {
		t.Fatalf("report github: %v", err)
	}
	if strings.Contains(stdout, report.RuleDeadCode) {
		t.Errorf("dead-code findings must be opt-in:\n%s", stdout)
	}

	stdout, _, err = runReportCmd(t, fix, "github", "--include", "dead")
	if err != nil {
		t.Fatalf("report github --include dead: %v", err)
	}
	if !strings.Contains(stdout, report.RuleDeadCode) {
		t.Errorf("--include dead produced no dead-code findings:\n%s", stdout)
	}
}

func TestReport_UnknownIncludeIsRejected(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	if _, _, err := runReportCmd(t, fix, "github", "--include", "audit,typo"); err == nil {
		t.Fatal("a typo'd --include value must fail loudly, not silently report less")
	}
}
