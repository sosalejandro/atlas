package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// cyclesFixture is the per-test scaffolding for the cycles CLI
// integration tests. Mirrors traceFixture in trace_test.go — owns a
// tempdir that doubles as repo root + SQLite state location, and
// resets the package-level cobra globals so each test starts clean.
type cyclesFixture struct {
	root   string
	dbPath string
}

func newCyclesFixture(t *testing.T) *cyclesFixture {
	t.Helper()
	dir := t.TempDir()
	atlasDir := filepath.Join(dir, ".atlas")
	if err := os.MkdirAll(atlasDir, 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(atlasDir, "atlas.db")
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return &cyclesFixture{root: dir, dbPath: dbPath}
}

func (f *cyclesFixture) openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}

// seedImportEdge inserts a (from_symbol, to_symbol, kind='import')
// edge triple plus the two backing symbols.
func (f *cyclesFixture) seedImportEdge(t *testing.T, fromQN, fromFile, toQN, toFile, scope string) {
	t.Helper()
	ctx := context.Background()
	s := f.openStore(t)
	defer s.Close()

	from, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(fromQN), Kind: shared.KindFunc,
		FilePath: fromFile, Line: 1,
	})
	if err != nil {
		t.Fatalf("insert from symbol: %v", err)
	}
	to, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(toQN), Kind: shared.KindFunc,
		FilePath: toFile, Line: 1,
	})
	if err != nil {
		t.Fatalf("insert to symbol: %v", err)
	}
	if _, err := s.Edges().Insert(ctx, store.EdgeRow{
		FromID: from, ToID: to, Kind: store.EdgeKindImport,
		FilePath: fromFile, Line: 1, Meta: scope,
	}); err != nil {
		t.Fatalf("insert import edge: %v", err)
	}
}

// runCyclesCmd drives `atlas codebase cycles` end-to-end through the
// cobra dispatch tree.
func runCyclesCmd(t *testing.T, fix *cyclesFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	full := append([]string{"codebase", "cycles", "--db-path", fix.dbPath}, args...)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(full)
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestCodebaseCycles_TwoNodeCycle is the canonical happy path: two
// files mutually import each other, both at module scope, the verb
// must report a single 2-node cycle.
func TestCodebaseCycles_TwoNodeCycle(t *testing.T) {
	fix := newCyclesFixture(t)
	fix.seedImportEdge(t, "pkg.a", "a.py", "pkg.b", "b.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "pkg.b", "b.py", "pkg.a", "a.py", store.EdgeMetaImportScopeModule)

	stdout, stderr, err := runCyclesCmd(t, fix)
	if err != nil {
		t.Fatalf("cycles returned error: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "2-node cycles: 1") {
		t.Fatalf("expected '2-node cycles: 1' in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "a.py") || !strings.Contains(stdout, "b.py") {
		t.Fatalf("expected both endpoints in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "<->") {
		t.Fatalf("expected bidirectional arrow for 2-node cycle:\n%s", stdout)
	}
}

// TestCodebaseCycles_ThreeNodeCycle exercises the triangle case the
// issue spec calls out as the second example.
func TestCodebaseCycles_ThreeNodeCycle(t *testing.T) {
	fix := newCyclesFixture(t)
	fix.seedImportEdge(t, "pkg.main", "main.py", "pkg.routes", "routes.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "pkg.routes", "routes.py", "pkg.deps", "deps.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "pkg.deps", "deps.py", "pkg.main", "main.py", store.EdgeMetaImportScopeModule)

	stdout, _, err := runCyclesCmd(t, fix)
	if err != nil {
		t.Fatalf("cycles returned error: %v", err)
	}
	if !strings.Contains(stdout, "3-node cycles: 1") {
		t.Fatalf("expected '3-node cycles: 1' in output:\n%s", stdout)
	}
	for _, file := range []string{"main.py", "routes.py", "deps.py"} {
		if !strings.Contains(stdout, file) {
			t.Fatalf("expected %s in cycle output:\n%s", file, stdout)
		}
	}
}

// TestCodebaseCycles_NoCycles asserts the no-cycle case prints the
// reassuring "no cycles found" line with the scan stats so the user
// can confirm the verb actually walked something (not a silent zero
// from an empty DB).
func TestCodebaseCycles_NoCycles(t *testing.T) {
	fix := newCyclesFixture(t)
	fix.seedImportEdge(t, "pkg.a", "a.py", "pkg.b", "b.py", store.EdgeMetaImportScopeModule)

	stdout, _, err := runCyclesCmd(t, fix)
	if err != nil {
		t.Fatalf("cycles returned error: %v", err)
	}
	if !strings.Contains(stdout, "no cycles found") {
		t.Fatalf("expected 'no cycles found' in output:\n%s", stdout)
	}
}

// TestCodebaseCycles_ScopeFilterModuleByDefault asserts the default
// --scope-filter hides function-scoped imports — the deferred-import
// workaround case the issue spec wants suppressed unless explicitly
// requested.
func TestCodebaseCycles_ScopeFilterModuleByDefault(t *testing.T) {
	fix := newCyclesFixture(t)
	// Cycle exists only because of a function-scoped import on the
	// reverse leg.
	fix.seedImportEdge(t, "pkg.a", "a.py", "pkg.b", "b.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "pkg.b", "b.py", "pkg.a", "a.py", store.EdgeMetaImportScopeFunction)

	stdout, _, err := runCyclesCmd(t, fix)
	if err != nil {
		t.Fatalf("cycles returned error: %v", err)
	}
	if !strings.Contains(stdout, "no cycles found") {
		t.Fatalf("expected 'no cycles found' under default module-only filter:\n%s", stdout)
	}
}

// TestCodebaseCycles_ScopeFilterAllSurfacesDeferred confirms
// --scope-filter=all brings the deferred-import cycle back into
// view, with the scope annotation flagging it as non-module.
func TestCodebaseCycles_ScopeFilterAllSurfacesDeferred(t *testing.T) {
	fix := newCyclesFixture(t)
	fix.seedImportEdge(t, "pkg.a", "a.py", "pkg.b", "b.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "pkg.b", "b.py", "pkg.a", "a.py", store.EdgeMetaImportScopeFunction)

	stdout, _, err := runCyclesCmd(t, fix, "--scope-filter", "all")
	if err != nil {
		t.Fatalf("cycles returned error: %v", err)
	}
	if !strings.Contains(stdout, "2-node cycles: 1") {
		t.Fatalf("expected the deferred-import cycle to surface under --scope-filter=all:\n%s", stdout)
	}
	if !strings.Contains(stdout, "function-import edge") {
		t.Fatalf("expected function-scope annotation in output:\n%s", stdout)
	}
}

// TestCodebaseCycles_InvalidScopeFilter is the negative-path control:
// an unknown --scope-filter value fails fast with a usage-style
// error rather than silently returning every cycle.
func TestCodebaseCycles_InvalidScopeFilter(t *testing.T) {
	fix := newCyclesFixture(t)
	_, _, err := runCyclesCmd(t, fix, "--scope-filter", "totally_made_up")
	if err == nil {
		t.Fatalf("expected error for invalid scope-filter, got nil")
	}
	if !strings.Contains(err.Error(), "--scope-filter must be one of") {
		t.Fatalf("expected vocabulary hint in error; got: %v", err)
	}
}

// TestCodebaseCycles_JSONEnvelope locks in the JSON contract — the
// envelope must carry schema_version + command + the result with a
// cycles array and the total_edges count. Atlas's stable JSON-output
// promise (architecture.md §6) means breaking this is a public API
// regression.
func TestCodebaseCycles_JSONEnvelope(t *testing.T) {
	fix := newCyclesFixture(t)
	fix.seedImportEdge(t, "pkg.a", "a.py", "pkg.b", "b.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "pkg.b", "b.py", "pkg.a", "a.py", store.EdgeMetaImportScopeModule)

	stdout, _, err := runCyclesCmd(t, fix, "--json")
	if err != nil {
		t.Fatalf("cycles returned error: %v", err)
	}

	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Cycles     []graph.Cycle `json:"cycles"`
			TotalEdges int           `json:"total_edges"`
			Filter     string        `json:"scope_filter"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode JSON envelope: %v\nstdout:\n%s", err, stdout)
	}
	if env.SchemaVersion != "v1" {
		t.Fatalf("schema_version mismatch: got %q want v1", env.SchemaVersion)
	}
	if env.Command != "codebase.cycles" {
		t.Fatalf("command tag mismatch: got %q", env.Command)
	}
	if len(env.Result.Cycles) != 1 {
		t.Fatalf("expected 1 cycle in JSON result, got %d", len(env.Result.Cycles))
	}
	if env.Result.Cycles[0].Length != 2 {
		t.Fatalf("expected length=2 cycle, got %d", env.Result.Cycles[0].Length)
	}
	if env.Result.Filter != store.EdgeMetaImportScopeModule {
		t.Fatalf("filter echo mismatch: got %q", env.Result.Filter)
	}
}

// TestCodebaseCycles_ScopePrefix exercises the `--scope <prefix>`
// flag (qualified-name filter). When supplied, only edges whose
// from-OR-to symbol matches the prefix participate in the SCC
// analysis — useful for cycle hunting inside one BC of a monorepo.
func TestCodebaseCycles_ScopePrefix(t *testing.T) {
	fix := newCyclesFixture(t)
	// Cycle inside services.preprocessor
	fix.seedImportEdge(t, "services.preprocessor.a", "services/preprocessor/a.py", "services.preprocessor.b", "services/preprocessor/b.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "services.preprocessor.b", "services/preprocessor/b.py", "services.preprocessor.a", "services/preprocessor/a.py", store.EdgeMetaImportScopeModule)
	// Unrelated cycle outside the prefix
	fix.seedImportEdge(t, "services.api.x", "services/api/x.py", "services.api.y", "services/api/y.py", store.EdgeMetaImportScopeModule)
	fix.seedImportEdge(t, "services.api.y", "services/api/y.py", "services.api.x", "services/api/x.py", store.EdgeMetaImportScopeModule)

	stdout, _, err := runCyclesCmd(t, fix, "--scope", "services.preprocessor")
	if err != nil {
		t.Fatalf("cycles returned error: %v", err)
	}
	if !strings.Contains(stdout, "services/preprocessor/a.py") {
		t.Fatalf("expected preprocessor cycle in output:\n%s", stdout)
	}
	if strings.Contains(stdout, "services/api/") {
		t.Fatalf("api files should be filtered out by --scope:\n%s", stdout)
	}
}
