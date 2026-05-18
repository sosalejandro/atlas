package coverage_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/coverage/gotest"
	"github.com/sosalejandro/atlas/packages/coverage/maestro"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// openStore opens a fresh tempfile-backed store; closes on cleanup.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "atlas-state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// upsertFeature creates the feature row so the FK on coverage_results
// accepts FeatureID references.
func upsertFeature(t *testing.T, s *store.Store, id shared.FeatureID, title string) {
	t.Helper()
	if err := s.Features().Upsert(context.Background(), store.Feature{ID: id, Title: title}); err != nil {
		t.Fatalf("Features.Upsert(%s): %v", id, err)
	}
}

const goldenGoTestStream = `{"Action":"run","Package":"github.com/atlas/example/auth","Test":"TestLogin"}
{"Action":"output","Package":"github.com/atlas/example/auth","Test":"TestLogin","Output":"@atlas:feature auth.login\n"}
{"Action":"pass","Package":"github.com/atlas/example/auth","Test":"TestLogin","Elapsed":0.5}
{"Action":"run","Package":"github.com/atlas/example/auth","Test":"TestRegister"}
{"Action":"fail","Package":"github.com/atlas/example/auth","Test":"TestRegister","Elapsed":0.2}
{"Action":"run","Package":"github.com/atlas/example/auth","Test":"TestLogout"}
{"Action":"skip","Package":"github.com/atlas/example/auth","Test":"TestLogout","Elapsed":0}
`

func TestIngest_GoTest_GoldenStream(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "auth.login", "Login")

	id, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(gotest.Parse),
		strings.NewReader(goldenGoTestStream),
		coverage.IngestOptions{Framework: coverage.FrameworkGoTest},
	)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if id == 0 {
		t.Fatal("got run id 0")
	}

	results, err := s.Coverage().ListResults(context.Background(), id)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}
	var pass, fail, skip int
	for _, r := range results {
		switch r.Status {
		case store.StatusPass:
			pass++
		case store.StatusFail:
			fail++
		case store.StatusSkip:
			skip++
		}
	}
	if pass != 1 || fail != 1 || skip != 1 {
		t.Errorf("counts %d/%d/%d, want 1/1/1", pass, fail, skip)
	}

	// auth.login feature must be attributed to at least one row.
	foundFeature := false
	for _, r := range results {
		if r.FeatureID != nil && *r.FeatureID == "auth.login" {
			foundFeature = true
		}
	}
	if !foundFeature {
		t.Error("expected one result with FeatureID=auth.login")
	}
}

// fakeResolver implements coverage.SymbolResolver in-memory.
type fakeResolver struct {
	rows map[shared.SymbolID]int64
}

func (f *fakeResolver) FindByQualifiedName(_ context.Context, qn shared.SymbolID) (store.SymbolRow, error) {
	if id, ok := f.rows[qn]; ok {
		return store.SymbolRow{ID: id, QualifiedName: qn}, nil
	}
	return store.SymbolRow{}, shared.ErrSymbolNotFound
}

func TestIngest_ResolverPopulatesSymbolID(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "auth.login", "Login")

	// Insert a symbol so the resolver returns a real surrogate id.
	gotSymID, err := s.Symbols().Insert(context.Background(), store.SymbolRow{
		QualifiedName: "auth.TestLogin",
		Kind:          shared.KindFunc,
		FilePath:      "src/auth/login_test.go",
		Line:          10,
	})
	if err != nil {
		t.Fatalf("Symbols.Insert: %v", err)
	}

	id, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(gotest.Parse),
		strings.NewReader(goldenGoTestStream),
		coverage.IngestOptions{
			Framework: coverage.FrameworkGoTest,
			Resolver:  s.Symbols(),
		},
	)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	results, _ := s.Coverage().ListResults(context.Background(), id)
	var found bool
	for _, r := range results {
		if r.SymbolID != nil && *r.SymbolID == gotSymID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected symbol_id=%d on at least one row; got results=%+v", gotSymID, results)
	}
}

func TestIngest_ResolverMissSymbolStillWrites(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "auth.login", "Login")

	// No symbol inserted — resolver should miss and leave symbol_id NULL.
	id, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(gotest.Parse),
		strings.NewReader(goldenGoTestStream),
		coverage.IngestOptions{
			Framework: coverage.FrameworkGoTest,
			Resolver:  &fakeResolver{rows: map[shared.SymbolID]int64{}},
		},
	)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	results, _ := s.Coverage().ListResults(context.Background(), id)
	for _, r := range results {
		if r.SymbolID != nil {
			t.Errorf("row %+v: symbol_id should be nil when resolver misses", r)
		}
	}
}

func TestIngest_ResolverHardErrorPropagates(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "auth.login", "Login")

	stubErr := errors.New("DB on fire")
	_, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(gotest.Parse),
		strings.NewReader(goldenGoTestStream),
		coverage.IngestOptions{
			Framework: coverage.FrameworkGoTest,
			Resolver:  &erroringResolver{err: stubErr},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "DB on fire") {
		t.Errorf("expected propagated error, got %v", err)
	}
}

type erroringResolver struct{ err error }

func (e *erroringResolver) FindByQualifiedName(_ context.Context, _ shared.SymbolID) (store.SymbolRow, error) {
	return store.SymbolRow{}, e.err
}

func TestIngest_NilStoreRejected(t *testing.T) {
	if _, err := coverage.Ingest(context.Background(), nil, coverage.ParseFunc(gotest.Parse), strings.NewReader(""), coverage.IngestOptions{}); err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestIngest_NilParserRejected(t *testing.T) {
	s := openStore(t)
	if _, err := coverage.Ingest(context.Background(), s, nil, strings.NewReader(""), coverage.IngestOptions{}); err == nil {
		t.Fatal("expected error for nil parser")
	}
}

func TestIngest_BackfillsTimes(t *testing.T) {
	s := openStore(t)
	// Empty input → parser returns zero StartedAt/FinishedAt. Orchestrator
	// must backfill via opts.Now or wall-clock.
	fixed := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	id, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(gotest.Parse),
		strings.NewReader(""),
		coverage.IngestOptions{
			Framework: coverage.FrameworkGoTest,
			Now:       func() time.Time { return fixed },
		},
	)
	if err != nil {
		t.Fatalf("Ingest empty: %v", err)
	}
	run, err := s.Coverage().GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !run.StartedAt.Equal(fixed) {
		t.Errorf("StartedAt = %v, want %v", run.StartedAt, fixed)
	}
	if !run.FinishedAt.Equal(fixed) {
		t.Errorf("FinishedAt = %v, want %v", run.FinishedAt, fixed)
	}
}

// TestIngest_RealGoTestJSON pipes a real `go test -json` of the gotest
// sub-package itself through Ingest. The whole pipeline (real bytes from
// the Go toolchain → parser → store → SELECT back) is exercised.
//
// Skipped in `-short` mode because spawning `go test` is heavy and the
// pure parser unit tests already cover the per-row mapping.
func TestIngest_RealGoTestJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real go-test-json round-trip in short mode")
	}

	cmd := exec.Command("go", "test", "-json", "-count=1", "-run", "TestParse_GoldenPassRow",
		"github.com/sosalejandro/atlas/packages/coverage/gotest")
	out, _ := cmd.CombinedOutput()
	if len(out) == 0 {
		t.Fatalf("go test -json produced no output; combined err: %s", string(out))
	}

	s := openStore(t)
	id, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(gotest.Parse),
		strings.NewReader(string(out)),
		coverage.IngestOptions{Framework: coverage.FrameworkGoTest},
	)
	if err != nil {
		t.Fatalf("Ingest real go-test-json: %v", err)
	}
	results, err := s.Coverage().ListResults(context.Background(), id)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("no results parsed from real go-test-json stream (out len=%d)", len(out))
	}
	// At least one pass row expected (TestParse_MixedStatuses itself passes).
	var sawPass bool
	for _, r := range results {
		if r.Status == store.StatusPass {
			sawPass = true
			break
		}
	}
	if !sawPass {
		t.Errorf("expected at least one pass row; got %+v", results)
	}
}

func TestIngest_MaestroXML(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "auth.login", "Login")

	xmlReport := `<?xml version="1.0"?>
<testsuite name="flows" tests="2" failures="1" timestamp="2026-05-18T10:00:00Z">
  <testcase name="auth-login-valid" classname="login" time="1.0" file="apps/mobile/e2e/flows/auth-login-valid.yaml"/>
  <testcase name="auth-login-invalid" classname="login" time="0.5" file="apps/mobile/e2e/flows/auth-login-invalid.yaml">
    <failure message="not found"/>
  </testcase>
</testsuite>`
	id, err := coverage.Ingest(
		context.Background(),
		s,
		coverage.ParseFunc(maestro.Parse),
		strings.NewReader(xmlReport),
		coverage.IngestOptions{Framework: coverage.FrameworkMaestro},
	)
	if err != nil {
		t.Fatalf("Ingest maestro: %v", err)
	}
	results, err := s.Coverage().ListResults(context.Background(), id)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len = %d, want 2", len(results))
	}
}
