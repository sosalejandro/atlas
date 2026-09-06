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

	"github.com/sosalejandro/atlas/packages/diff"
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

// seedAbsolutePaths writes the state a scanner run outside the repo-relative
// happy path produces: a symbol recorded by its ABSOLUTE path, and a coverage
// gap for a file outside the checkout entirely.
//
// Both are what NormalizePaths exists for, and both fail silently without it —
// GitHub accepts an absolute uri, shows a green check, and displays nothing.
func (f *reportFixture) seedAbsolutePaths(t *testing.T) {
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
		// Absolute, as a scanner invoked with an absolute package root
		// records them.
		FilePath: filepath.Join(f.root, "internal", "billing", "invoice.go"),
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
	// Absolute and outside the repo root: there is no such file in the
	// checkout, so this one must be dropped and counted, not shipped.
	if _, err := s.CoverageGaps().Insert(ctx, runID, []store.CoverageGap{
		{Path: "/opt/vendor/queue.go", Stmts: 118, Reason: "no-indexed-symbol"},
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

// TestReport_CollectionRelativisesPathsAndReportsWhatItDropped is the boundary
// test for NormalizePaths.
//
// packages/report proves normalisation works; nothing proved the CLI actually
// calls it. That gap matters more than it sounds: the absolute-uri defence
// could be deleted from the collection pass and every test would still pass,
// while every finding silently disappeared from the PR's Files view behind a
// green check. So this asserts both halves at the real boundary — the surviving
// finding is relativised, and the unrelativisable one is counted in a warning
// rather than dropped in silence.
func TestReport_CollectionRelativisesPathsAndReportsWhatItDropped(t *testing.T) {
	fix := newReportFixture(t)
	// The root command's PersistentPreRunE recomputes the config, and with
	// it repoRoot, from the process working directory — so a repoRoot
	// assigned by the fixture is discarded before the collection pass runs.
	// Chdir into the fixture so the root NormalizePaths is handed is the
	// one these findings were seeded against. (t.Chdir restores it and
	// forbids t.Parallel, which these tests already cannot use.)
	t.Chdir(fix.root)
	fix.seedAbsolutePaths(t)

	stdout, _, err := runReportCmd(t, fix, "sarif", "--json")
	if err != nil {
		t.Fatalf("report sarif --json: %v", err)
	}
	var env struct {
		Warnings []string `json:"warnings"`
		Result   struct {
			Body string `json:"body"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}

	var doc struct {
		Runs []struct {
			Results []struct {
				RuleID    string `json:"ruleId"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(env.Result.Body), &doc); err != nil {
		t.Fatalf("result.body is not SARIF: %v\n%s", err, env.Result.Body)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(doc.Runs))
	}

	var sawFeature bool
	for _, r := range doc.Runs[0].Results {
		uri := r.Locations[0].PhysicalLocation.ArtifactLocation.URI
		if strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, fix.root) {
			t.Errorf("%s: uri = %q is absolute; GitHub accepts it and shows nothing", r.RuleID, uri)
		}
		if r.RuleID == report.RuleFeatureUncovered {
			sawFeature = true
			if uri != "internal/billing/invoice.go" {
				t.Errorf("uri = %q, want the repo-relative %q",
					uri, "internal/billing/invoice.go")
			}
		}
		if uri == "/opt/vendor/queue.go" {
			t.Errorf("the out-of-checkout gap was emitted as %q instead of being dropped", uri)
		}
	}
	if !sawFeature {
		t.Fatalf("no %s result; the absolutely-pathed symbol should still anchor one:\n%s",
			report.RuleFeatureUncovered, env.Result.Body)
	}

	// The drop has to be announced. A report that silently contains less
	// than it measured is the failure this whole path is guarding against.
	var sawWarning bool
	for _, w := range env.Warnings {
		if strings.Contains(w, "could not be made repo-relative") {
			sawWarning = true
			if !strings.Contains(w, "/opt/vendor/queue.go") {
				t.Errorf("the warning should name an example path; got %q", w)
			}
		}
	}
	if !sawWarning {
		t.Errorf("no warning about the dropped finding; warnings = %q", env.Warnings)
	}
}

// TestReport_BelowTheFloorCountsFeaturesNotAnnotations: the summary row says
// "Features below the floor", and it has to be that number.
//
// FromAudit drops every below-floor feature it cannot anchor, so the finding
// count is smaller than the label promises. A compliance number in a PR comment
// that quietly under-reports is worse than no number at all — the reviewer
// reads "1 below the floor" and closes the tab with 2 features failing.
func TestReport_BelowTheFloorCountsFeaturesNotAnnotations(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	// A second below-floor feature, deliberately with no linked symbol: it
	// counts toward the floor and can never be annotated.
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.Features().Upsert(ctx, store.Feature{
		ID:    "billing.ghost",
		Title: "Unlinked feature",
		Kind:  store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	stdout, _, err := runReportCmd(t, fix, "pr", "--json")
	if err != nil {
		t.Fatalf("report pr --json: %v", err)
	}
	var env struct {
		Warnings []string `json:"warnings"`
		Result   struct {
			Body     string `json:"body"`
			Findings int    `json:"finding_count"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}

	if !strings.Contains(env.Result.Body, "| Features below the floor | 2 |") {
		t.Errorf("summary does not report both below-floor features as 2:\n%s", env.Result.Body)
	}
	// And the shortfall is named rather than left for the reader to notice
	// that two numbers on the same page disagree.
	if !strings.Contains(env.Result.Body, "of those, not annotated") {
		t.Errorf("summary hides that one below-floor feature could not be annotated:\n%s",
			env.Result.Body)
	}
	var sawUnanchored bool
	for _, w := range env.Warnings {
		if strings.Contains(w, "no linked symbol to anchor") {
			sawUnanchored = true
		}
	}
	if !sawUnanchored {
		t.Errorf("no warning for the unanchored below-floor feature; warnings = %q", env.Warnings)
	}
}

// TestAuditDeltaToReport_SurfacesWhatItCouldNotCompare: packages/diff reports
// what it could not compare through MissingOnA / MissingOnB. Reading only
// Changed/Added/Removed renders a delta section that looks complete and is not
// — "no regressions" and "no base-side scores to find regressions in" produce
// the same empty table.
func TestAuditDeltaToReport_SurfacesWhatItCouldNotCompare(t *testing.T) {
	d := diff.AuditDelta{
		Changed: []diff.AuditScoreChange{
			{FeatureID: "billing.invoice", Before: 58, After: 31, Delta: -27},
		},
		MissingOnA: []shared.FeatureID{"auth.login", "search.index"},
		MissingOnB: []shared.FeatureID{"billing.legacy"},
	}
	got, warns := auditDeltaToReport("origin/main", "", d)

	if len(got.Regressed) != 1 {
		t.Fatalf("the comparable change was lost: %+v", got)
	}
	if len(warns) != 2 {
		t.Fatalf("want a warning for each side that could not be compared, got %d: %q",
			len(warns), warns)
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"auth.login", "billing.legacy", "origin/main"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not mention %q:\n%s", want, joined)
		}
	}
	// The counts matter as much as the names: "2 features" is the size of
	// the blind spot, and an example alone reads like the whole of it.
	if !strings.Contains(joined, "2 features") || !strings.Contains(joined, "1 feature") {
		t.Errorf("warnings do not report how many features each side is missing:\n%s", joined)
	}
}

// A complete comparison must stay quiet. A warning on every clean run is a
// warning nobody reads on the run that matters.
func TestAuditDeltaToReport_SaysNothingWhenNothingIsMissing(t *testing.T) {
	_, warns := auditDeltaToReport("origin/main", "HEAD", diff.AuditDelta{
		Changed: []diff.AuditScoreChange{
			{FeatureID: "billing.invoice", Before: 58, After: 31, Delta: -27},
		},
	})
	if len(warns) != 0 {
		t.Errorf("a complete delta should warn about nothing, got %q", warns)
	}
}

func TestReport_UnknownIncludeIsRejected(t *testing.T) {
	fix := newReportFixture(t)
	fix.seed(t)

	if _, _, err := runReportCmd(t, fix, "github", "--include", "audit,typo"); err == nil {
		t.Fatal("a typo'd --include value must fail loudly, not silently report less")
	}
}
