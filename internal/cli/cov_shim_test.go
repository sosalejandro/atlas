package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// fixtureModule is the miniature module the shim package owns. Driving the
// CLI against it is the only way to prove the end-to-end claim in issue
// #128: a suite goes in, per-test evidence comes out of the store.
const fixtureModule = "../../packages/coverage/shim/testdata/fixture"

// covShimFixture points the CLI globals at a repo root and a fresh store,
// the same shape the other CLI integration tests use.
func newCovShimFixture(t *testing.T, root string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "atlas.db")
	loaded = Config{repoRoot: root, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return dbPath
}

// `atlas cov run` end to end: index the fixture module, run its suite under
// the shim, and expect the store to know which production symbol each test
// executed — including that TestTotal did NOT run Refund.
func TestCovRun_WritesPerTestEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs the fixture module")
	}
	ctx := context.Background()
	root, err := filepath.Abs(fixtureModule)
	if err != nil {
		t.Fatalf("abs fixture: %v", err)
	}
	dbPath := newCovShimFixture(t, root)

	idx, err := codeindex.IndexProject(ctx, root, codeindex.Options{SkipTS: true, SkipPY: true})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	_ = s.Close()

	var out bytes.Buffer
	cmd := newCovRunCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{}) // the fixture suite's own output
	cmd.SetArgs([]string{"--", "go", "test", "./..."})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("cov run: %v\n%s", err, out.String())
	}

	s, err = store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = s.Close() }()

	runs, err := s.Coverage().ListRuns(ctx, "")
	if err != nil || len(runs) == 0 {
		t.Fatalf("no coverage run recorded (%v)\n%s", err, out.String())
	}
	runID := runs[0].ID

	executed := symbolsRunBy(t, s, runID, "billing.TestTotal")
	if !executed["billing.Total"] {
		t.Errorf("TestTotal executed %v, want it to include billing.Total", executed)
	}
	if executed["billing.Refund"] {
		t.Errorf("TestTotal is credited with billing.Refund, which it never called")
	}
	if executed["billing.Unused"] {
		t.Errorf("TestTotal is credited with billing.Unused, which nothing calls")
	}

	// shipping degrades — its tests are parallel — and the run says so out loud.
	if !strings.Contains(out.String(), "degraded") || !strings.Contains(out.String(), "t.Parallel()") {
		t.Errorf("output does not report the degradation:\n%s", out.String())
	}
	if strings.Contains(out.String(), "shipping.TestRate") {
		t.Errorf("a degraded package's tests were ingested anyway:\n%s", out.String())
	}
}

// symbolsRunBy resolves a test by qualified name and returns the qualified
// names of the symbols the store says it executed.
func symbolsRunBy(t *testing.T, s *store.Store, runID int64, test string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	row, err := s.Symbols().FindByQualifiedName(ctx, shared.SymbolID(test))
	if err != nil {
		t.Fatalf("find %s: %v", test, err)
	}
	rows, err := s.TestCoverage().SymbolsExecutedBy(ctx, runID, row.ID)
	if err != nil {
		t.Fatalf("SymbolsExecutedBy: %v", err)
	}
	all, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		t.Fatalf("list symbols: %v", err)
	}
	names := make(map[int64]shared.SymbolID, len(all))
	for _, sym := range all {
		names[sym.ID] = sym.QualifiedName
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[string(names[r.SymbolID])] = true
	}
	return out
}

// --out is the bridge to `cov sync --per-test`, which reads a flat directory
// of <test symbol>.out files. It has to work without a store to write to.
func TestCovRun_OutWritesTheSyncLayoutWithoutIngesting(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs the fixture module")
	}
	root, err := filepath.Abs(fixtureModule)
	if err != nil {
		t.Fatalf("abs fixture: %v", err)
	}
	newCovShimFixture(t, root)
	outDir := filepath.Join(t.TempDir(), "profiles")

	cmd := newCovRunCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--no-ingest", "--out", outDir, "--", "go", "test", "./billing"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("cov run: %v", err)
	}

	for _, want := range []string{"billing.TestTotal.out", "billing.TestRefund.out"} {
		if _, err := os.Stat(filepath.Join(outDir, want)); err != nil {
			entries, _ := os.ReadDir(outDir)
			t.Fatalf("no %s in --out dir; got %v", want, entries)
		}
	}
}

// `atlas cov shim init` on a module it has never touched, then again.
func TestCovShimInit_IsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("shells out to go list")
	}
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module example.com/initfixture\n\ngo 1.25\n")
	write("svc/svc.go", "package svc\n\nfunc Hello() string { return \"hi\" }\n")
	write("svc/svc_test.go", "package svc\n\nimport \"testing\"\n\nfunc TestHello(t *testing.T) {\n\tif Hello() == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n")
	newCovShimFixture(t, root)

	statuses := func() map[string]string {
		t.Helper()
		flags.JSON = true
		var out bytes.Buffer
		cmd := newCovShimInitCmd()
		cmd.SetOut(&out)
		cmd.SetArgs(nil)
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("cov shim init: %v", err)
		}
		var env struct {
			Result struct {
				Packages []struct {
					Dir    string `json:"dir"`
					Status string `json:"status"`
				} `json:"packages"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("decode envelope: %v\n%s", err, out.String())
		}
		got := map[string]string{}
		for _, p := range env.Result.Packages {
			got[p.Dir] = p.Status
		}
		return got
	}

	first := statuses()
	if first["example.com/initfixture/svc"] != "created" {
		t.Fatalf("first init = %v, want svc created", first)
	}
	second := statuses()
	if second["example.com/initfixture/svc"] != "unchanged" {
		t.Fatalf("second init = %v, want svc unchanged", second)
	}
}
