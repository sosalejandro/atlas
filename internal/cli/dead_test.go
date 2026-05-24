package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// deadFixture is the per-test scaffolding for `atlas codebase dead`.
// Owns a tempdir + .atlas/atlas.db state file, and resets the
// package-level singletons (`flags`, `loaded`) so successive runs
// don't bleed configuration. Mirrors trace_test.go's traceFixture
// pattern.
type deadFixture struct {
	root   string
	dbPath string
}

func newDeadFixture(t *testing.T) *deadFixture {
	t.Helper()
	dir := t.TempDir()
	atlasDir := filepath.Join(dir, ".atlas")
	if err := os.MkdirAll(atlasDir, 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(atlasDir, "atlas.db")
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return &deadFixture{root: dir, dbPath: dbPath}
}

func (f *deadFixture) openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}

// seedTinyOrphanGraph inserts the smallest meaningful dead-code
// fixture: main imports used; orphan has no importers. Mirrors the
// shape the user-supplied /tmp/dead-test verification fixture
// produces.
func (f *deadFixture) seedTinyOrphanGraph(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s := f.openStore(t)
	defer s.Close()

	mainID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.main",
		Kind:          shared.KindFunc,
		FilePath:      "pkg/main.py",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("seed main: %v", err)
	}
	usedID, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.used.my_func",
		Kind:          shared.KindFunc,
		FilePath:      "pkg/used.py",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("seed used: %v", err)
	}
	if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.orphan.never_imported",
		Kind:          shared.KindFunc,
		FilePath:      "pkg/orphan.py",
		Line:          1,
	}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	if _, err := s.Edges().Insert(ctx, store.EdgeRow{
		FromID:   mainID,
		ToID:     usedID,
		Kind:     store.EdgeKindImport,
		FilePath: "pkg/main.py",
		Line:     1,
		Meta:     store.EdgeMetaImportScopeModule,
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
}

// runDeadCmd drives the dead command through the full root tree, the
// same way the production binary executes it. Pins the fixture's db
// path into the package singletons before the command fires so the
// PersistentPreRunE's loadConfig doesn't clobber it.
func runDeadCmd(t *testing.T, fix *deadFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"codebase", "dead", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestCodebaseDead_FlagsWired is the surface-level guard: every flag
// from the issue spec must be present. Drift in the cobra command
// definition would silently break the contract — this catches
// rename / drop / typo regressions.
func TestCodebaseDead_FlagsWired(t *testing.T) {
	c := newCodebaseDeadCmd()
	for _, name := range []string{"kind", "filter", "include-tests", "include-scopes"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("atlas codebase dead is missing --%s", name)
		}
	}
}

// TestCodebaseDead_OrphanSurfaced is the end-to-end happy path: the
// orphan symbol appears in human output, the live symbol does NOT,
// and the WARN banner reflects the count.
func TestCodebaseDead_OrphanSurfaced(t *testing.T) {
	fix := newDeadFixture(t)
	fix.seedTinyOrphanGraph(t)

	stdout, stderr, err := runDeadCmd(t, fix)
	if err != nil {
		t.Fatalf("codebase dead: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "pkg/orphan.py") {
		t.Errorf("expected orphan.py in output; got:\n%s", stdout)
	}
	if strings.Contains(stdout, "pkg/used.py") {
		t.Errorf("used.py must not appear; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "WARN") {
		t.Errorf("expected WARN banner; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Caveats:") {
		t.Errorf("expected Caveats block; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Python dynamic dispatch") {
		t.Errorf("expected dynamic-dispatch caveat surfaced; got:\n%s", stdout)
	}
}

// TestCodebaseDead_JSONEnvelopeShape locks in the v1 envelope
// contract — downstream consumers will read these field names. Any
// rename / removal would silently break their parsers.
func TestCodebaseDead_JSONEnvelopeShape(t *testing.T) {
	fix := newDeadFixture(t)
	fix.seedTinyOrphanGraph(t)

	stdout, _, err := runDeadCmd(t, fix, "--json")
	if err != nil {
		t.Fatalf("codebase dead --json: %v", err)
	}

	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Kind             string `json:"kind"`
			IncludeTests     bool   `json:"include_tests"`
			TotalCandidates  int    `json:"total_candidates"`
			ExternalExcluded bool   `json:"external_excluded"`
			Caveats          []string
			IncludeScopes    []string `json:"include_scopes"`
			DeadCandidates   []struct {
				Path          string `json:"path"`
				QualifiedName string `json:"qualified_name"`
				SymbolKind    string `json:"symbol_kind"`
				IncomingCount int    `json:"incoming_count"`
			} `json:"dead_candidates"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\nstdout:\n%s", err, stdout)
	}
	if env.SchemaVersion != "v1" {
		t.Errorf("schema_version = %q; want v1", env.SchemaVersion)
	}
	if env.Command != "codebase.dead" {
		t.Errorf("command = %q; want codebase.dead", env.Command)
	}
	if env.Result.Kind != "import" {
		t.Errorf("result.kind = %q; want import", env.Result.Kind)
	}
	if !env.Result.ExternalExcluded {
		t.Error("result.external_excluded must be true (external:py stubs are always filtered)")
	}
	if len(env.Result.Caveats) < 3 {
		t.Errorf("result.caveats must list at least 3 caveats; got %d", len(env.Result.Caveats))
	}
	if len(env.Result.IncludeScopes) == 0 {
		t.Error("result.include_scopes must echo the active filter; got empty")
	}
	var sawOrphan bool
	for _, c := range env.Result.DeadCandidates {
		if c.QualifiedName == "pkg.orphan.never_imported" {
			sawOrphan = true
			if c.IncomingCount != 0 {
				t.Errorf("orphan.incoming_count = %d; want 0", c.IncomingCount)
			}
			if c.Path != "pkg/orphan.py" {
				t.Errorf("orphan.path = %q; want pkg/orphan.py", c.Path)
			}
		}
		if c.QualifiedName == "pkg.used.my_func" {
			t.Errorf("used symbol must not appear: %+v", c)
		}
	}
	if !sawOrphan {
		t.Errorf("orphan missing from dead_candidates; got: %+v", env.Result.DeadCandidates)
	}
}

// TestCodebaseDead_UnknownKindErrors guards the failure mode: a typo'd
// --kind value must return an actionable error rather than silently
// defaulting to import.
func TestCodebaseDead_UnknownKindErrors(t *testing.T) {
	fix := newDeadFixture(t)
	fix.seedTinyOrphanGraph(t)

	_, _, err := runDeadCmd(t, fix, "--kind", "imports")
	if err == nil {
		t.Fatal("expected error on unknown --kind")
	}
	if !strings.Contains(err.Error(), "unknown --kind") {
		t.Errorf("expected 'unknown --kind' in error; got: %v", err)
	}
}

// TestCodebaseDead_FilterNarrows confirms --filter restricts the
// candidate set without affecting live-symbol detection from edges
// originating outside the filter.
func TestCodebaseDead_FilterNarrows(t *testing.T) {
	fix := newDeadFixture(t)
	fix.seedTinyOrphanGraph(t)

	stdout, _, err := runDeadCmd(t, fix, "--filter", "pkg/orphan")
	if err != nil {
		t.Fatalf("codebase dead --filter: %v", err)
	}
	if !strings.Contains(stdout, "pkg/orphan.py") {
		t.Errorf("orphan must remain under --filter pkg/orphan; got:\n%s", stdout)
	}
	if strings.Contains(stdout, "pkg/main.py") {
		t.Errorf("main.py is outside --filter and must not appear; got:\n%s", stdout)
	}
}

// TestParseDeadScopes_KnownAndAll exercises the two non-trivial
// branches: a hand-typed CSV expands element-wise, while the "all"
// shortcut expands to every scope.
func TestParseDeadScopes_KnownAndAll(t *testing.T) {
	t.Parallel()

	got, err := parseDeadScopes("module,conditional")
	if err != nil {
		t.Fatalf("parse module,conditional: %v", err)
	}
	if len(got) != 2 || got[0] != "module" || got[1] != "conditional" {
		t.Errorf("got %v; want [module conditional]", got)
	}

	all, err := parseDeadScopes("all")
	if err != nil {
		t.Fatalf("parse all: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("all expansion got %d scopes; want 5", len(all))
	}

	if _, err := parseDeadScopes("module,bogus"); err == nil {
		t.Error("expected error on bogus scope")
	}
}
