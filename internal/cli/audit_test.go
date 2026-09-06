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

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// auditFixture is a store seeded with one feature whose implementation surface
// has been measured both ways: `atlas cov` statement results and `atlas flow`
// branch verdicts.
type auditFixture struct {
	root   string
	dbPath string
}

// newAuditFixture seeds two impl symbols. Statement coverage sees one of the
// two executed (50%); decision coverage sees 3 of 4 DECIDABLE outcomes taken
// (75%) with 2 more outcomes nothing could judge.
//
// The two numbers are deliberately different, and neither is derivable from
// the other, so a JSON reader that blended them would land on a value this
// test can name.
func newAuditFixture(t *testing.T) *auditFixture { return newAuditFixtureWithFlow(t, true) }

// newAuditFixtureWithFlow builds the same store with or without the `atlas
// flow` half, so a test can compare the two directly.
func newAuditFixtureWithFlow(t *testing.T, withFlow bool) *auditFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	fix := &auditFixture{root: dir, dbPath: filepath.Join(dir, ".atlas", "atlas.db")}

	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Features().Upsert(ctx, store.Feature{
		ID: "auth.login", Title: "Login", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("Upsert feature: %v", err)
	}

	ids := make([]int64, 0, 2)
	for i, name := range []string{"auth.Login", "auth.Verify"} {
		id, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(name),
			Kind:          shared.KindFunc,
			FilePath:      "auth/login.go",
			Line:          i + 1,
		})
		if err != nil {
			t.Fatalf("insert symbol %s: %v", name, err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: "auth.login", SymbolID: id,
			Role: store.RoleImpl, Source: store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
		ids = append(ids, id)
	}

	now := time.Now().UTC()
	if _, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, []store.CoverageResult{
		{SymbolID: &ids[0], Status: store.StatusPass},
		{SymbolID: &ids[1], Status: store.StatusFail},
	}); err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}

	if !withFlow {
		return fix
	}
	for _, dc := range []store.DecisionCoverage{
		{SymbolID: ids[0], OutcomesTotal: 4, OutcomesDecidable: 2, OutcomesTaken: 2, Source: "cover.out"},
		{SymbolID: ids[1], OutcomesTotal: 2, OutcomesDecidable: 2, OutcomesTaken: 1, Source: "cover.out"},
	} {
		if err := s.ControlFlow().SetDecisionCoverage(ctx, dc); err != nil {
			t.Fatalf("SetDecisionCoverage: %v", err)
		}
	}
	return fix
}

func runAuditCmd(t *testing.T, fix *auditFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"audit", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// auditJSONFeature mirrors the slice of the envelope this test reads.
type auditJSONFeature struct {
	FeatureID  string             `json:"feature_id"`
	Score      float64            `json:"score"`
	Components map[string]float64 `json:"components"`
	Decision   *struct {
		Available            bool    `json:"available"`
		Percent              float64 `json:"percent"`
		OutcomesTaken        int     `json:"outcomes_taken"`
		OutcomesDecidable    int     `json:"outcomes_decidable"`
		OutcomesUndetermined int     `json:"outcomes_undetermined"`
		OutcomesTotal        int     `json:"outcomes_total"`
		SymbolsMeasured      int     `json:"symbols_measured"`
		SymbolsUnmeasured    int     `json:"symbols_unmeasured"`
	} `json:"decision_coverage"`
}

func decodeAuditJSON(t *testing.T, out string) []auditJSONFeature {
	t.Helper()
	var env struct {
		Result struct {
			Features []auditJSONFeature `json:"features"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode audit envelope: %v\n%s", err, out)
	}
	return env.Result.Features
}

// TestAuditJSON_StatementAndDecisionCoverageStaySeparate is issue #140's
// acceptance criterion at the surface an integrator actually reads.
//
// A single composite number would satisfy "the audit reports both" on paper
// and destroy the reason for reporting both: statement coverage says a line
// ran, decision coverage says a branch went both ways, and a caller deciding
// where to send a test-writing effort needs to know WHICH one is low.
func TestAuditJSON_StatementAndDecisionCoverageStaySeparate(t *testing.T) {
	fix := newAuditFixture(t)
	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })

	stdout, stderr, err := runAuditCmd(t, fix, "--feature", "auth.login", "--json")
	if err != nil {
		t.Fatalf("audit --json: %v\nstderr:\n%s", err, stderr)
	}
	feats := decodeAuditJSON(t, stdout)
	if len(feats) != 1 {
		t.Fatalf("features = %d, want 1\n%s", len(feats), stdout)
	}
	f := feats[0]

	stmt, hasStmt := f.Components["coverage"]
	dec, hasDec := f.Components["decision_coverage"]
	if !hasStmt || !hasDec {
		t.Fatalf("components = %v; want both `coverage` and `decision_coverage`", f.Components)
	}
	if stmt != 50 {
		t.Errorf("components.coverage = %.2f, want 50 (1 of 2 impl symbols executed)", stmt)
	}
	if dec != 75 {
		t.Errorf("components.decision_coverage = %.2f, want 75 (3 of 4 decidable outcomes taken)", dec)
	}
	if stmt == dec {
		t.Error("the two coverage components are equal; the fixture was built so they cannot be")
	}

	d := f.Decision
	if d == nil {
		t.Fatal("result.features[0].decision_coverage missing; the availability flag and the counters behind the score have to travel with it")
	}
	if !d.Available {
		t.Error("decision_coverage.available = false, want true")
	}
	if d.OutcomesTaken != 3 || d.OutcomesDecidable != 4 || d.OutcomesTotal != 6 {
		t.Errorf("taken/decidable/total = %d/%d/%d, want 3/4/6",
			d.OutcomesTaken, d.OutcomesDecidable, d.OutcomesTotal)
	}
	if d.OutcomesUndetermined != 2 {
		t.Errorf("outcomes_undetermined = %d, want 2 — the outcomes no profile could judge must be reported, not folded into either side of the ratio",
			d.OutcomesUndetermined)
	}
	if d.SymbolsMeasured != 2 || d.SymbolsUnmeasured != 0 {
		t.Errorf("symbols measured/unmeasured = %d/%d, want 2/0", d.SymbolsMeasured, d.SymbolsUnmeasured)
	}
}

// TestAuditJSON_OmitsDecisionCoverageWhenNothingMeasured pins the other half:
// a store that has never run `atlas flow` must emit the JSON it always did.
// An integrator's schema does not gain a field because a feature they do not
// use exists.
func TestAuditJSON_OmitsDecisionCoverageWhenNothingMeasured(t *testing.T) {
	// The same store, minus the `atlas flow` half.
	fix := newAuditFixtureWithFlow(t, false)

	flags.JSON = true
	t.Cleanup(func() { flags.JSON = false })
	stdout, stderr, err := runAuditCmd(t, fix, "--feature", "auth.login", "--json")
	if err != nil {
		t.Fatalf("audit --json: %v\nstderr:\n%s", err, stderr)
	}
	if strings.Contains(stdout, "decision_coverage") {
		t.Errorf("`decision_coverage` present for a store with no cfg rows:\n%s", stdout)
	}
	feats := decodeAuditJSON(t, stdout)
	if len(feats) != 1 || feats[0].Score != 50 {
		t.Fatalf("score = %v, want a single feature at 50 (statement coverage alone)\n%s", feats, stdout)
	}
}

// TestAuditText_PrintsTheDecisionReading checks the human view names both
// numbers and the undetermined count. The text view is where the "90%
// statement coverage, every error path unexercised" story gets told or missed.
func TestAuditText_PrintsTheDecisionReading(t *testing.T) {
	fix := newAuditFixture(t)
	stdout, stderr, err := runAuditCmd(t, fix, "--feature", "auth.login")
	if err != nil {
		t.Fatalf("audit: %v\nstderr:\n%s", err, stderr)
	}
	for _, want := range []string{
		"coverage                50.00",
		"decision_coverage       75.00",
		"3/4 decidable outcomes taken, 2 undetermined",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}
