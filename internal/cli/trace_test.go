package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// traceFixture is the per-test scaffolding for the cached-store trace tests.
// It owns a tempdir that simultaneously plays repo root + holds the SQLite
// state file, plus a seeded *store.Store. The fixture resets the
// package-level `loaded`/`flags` singletons so each test starts from a
// known baseline.
type traceFixture struct {
	root   string
	dbPath string
}

func newTraceFixture(t *testing.T) *traceFixture {
	t.Helper()
	dir := t.TempDir()
	atlasDir := filepath.Join(dir, ".atlas")
	if err := os.MkdirAll(atlasDir, 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(atlasDir, "atlas.db")

	// Reset package-level globals so tests don't bleed state. trace.go reads
	// `loaded` and `flags` directly; the rest of the cli package does the
	// same in production via the cobra PersistentPreRunE.
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}

	return &traceFixture{root: dir, dbPath: dbPath}
}

// openStore opens a Store rooted at the fixture's dbPath. Caller must Close.
func (f *traceFixture) openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}

// seedChain inserts a linear symbol chain A -> B -> C with the given root
// qualified name and returns each symbol's surrogate id. Used by the
// symbol-trace tests as the minimum viable index.
func (f *traceFixture) seedChain(t *testing.T, rootQN, midQN, leafQN string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	s := f.openStore(t)
	defer s.Close()

	a, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(rootQN), Kind: shared.KindFunc,
		FilePath: "src/a.go", Line: 10,
	})
	if err != nil {
		t.Fatalf("insert root: %v", err)
	}
	b, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(midQN), Kind: shared.KindFunc,
		FilePath: "src/b.go", Line: 20,
	})
	if err != nil {
		t.Fatalf("insert mid: %v", err)
	}
	c, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(leafQN), Kind: shared.KindFunc,
		FilePath: "src/c.go", Line: 30,
	})
	if err != nil {
		t.Fatalf("insert leaf: %v", err)
	}
	for _, e := range []store.EdgeRow{
		{FromID: a, ToID: b, Kind: store.EdgeKindCall, FilePath: "src/a.go", Line: 11},
		{FromID: b, ToID: c, Kind: store.EdgeKindCall, FilePath: "src/b.go", Line: 21},
	} {
		if _, err := s.Edges().Insert(ctx, e); err != nil {
			t.Fatalf("insert edge: %v", err)
		}
	}
	return a, b, c
}

// seedFeature creates a feature, links it to the supplied symbol surrogate
// ids (role=impl, source=annotation), and returns the FeatureID.
func (f *traceFixture) seedFeature(t *testing.T, fid string, symbolIDs ...int64) shared.FeatureID {
	t.Helper()
	ctx := context.Background()
	s := f.openStore(t)
	defer s.Close()

	feat := store.Feature{
		ID:    shared.FeatureID(fid),
		Title: "test feature " + fid,
		Kind:  store.FeatureKindFeature,
	}
	if err := s.Features().Upsert(ctx, feat); err != nil {
		t.Fatalf("upsert feature: %v", err)
	}
	for _, sid := range symbolIDs {
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: feat.ID, SymbolID: sid,
			Role: store.RoleImpl, Source: store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("link feature_symbol: %v", err)
		}
	}
	return feat.ID
}

// runTraceCmd drives the trace command end-to-end via the cobra tree the
// production binary uses. Returns stdout + stderr + the cobra RunE error.
func runTraceCmd(t *testing.T, fix *traceFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	// NewRootCmd resets the globals; re-pin them to the fixture so the
	// PersistentPreRunE -> loadConfig doesn't blow them away.
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"trace", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestTrace_UsesCachedDBByDefault confirms the default path opens the
// cached store and resolves a trace WITHOUT re-walking the codebase. The
// proxy for "no walk happened" is wall-clock — a real walk costs seconds
// even on a tiny corpus because codeindex.IndexProject spins up the TS
// scanner subprocess. A cached read is <100ms.
func TestTrace_UsesCachedDBByDefault(t *testing.T) {
	fix := newTraceFixture(t)
	_, _, _ = fix.seedChain(t, "pkg.Root", "pkg.Mid", "pkg.Leaf")

	start := time.Now()
	stdout, stderr, err := runTraceCmd(t, fix, "pkg.Root")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("trace returned error: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "pkg.Root") || !strings.Contains(stdout, "pkg.Leaf") {
		t.Fatalf("expected chain output to include both endpoints; got:\n%s", stdout)
	}
	// The cached path must NOT spawn the TS scanner subprocess; allow a
	// generous ceiling so the test isn't flaky on shared CI runners.
	if elapsed > 2*time.Second {
		t.Fatalf("cached trace took %v — likely re-walked the codebase", elapsed)
	}
}

// TestTrace_ErrorsWhenNoDB confirms the explicit error message when the
// state DB doesn't exist. Silent re-walks here would mask the missing-init
// case — exactly what atlas#29 set out to fix.
func TestTrace_ErrorsWhenNoDB(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, ".atlas", "missing.db")

	loaded = Config{repoRoot: dir}
	flags = globalFlags{DBPath: bogus}

	root := NewRootCmd()
	loaded = Config{repoRoot: dir}
	flags = globalFlags{DBPath: bogus}
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"trace", "--db-path", bogus, "SomeSymbol"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected error when DB missing; stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(err.Error(), "no atlas state found") {
		t.Fatalf("expected 'no atlas state found' in error; got: %v", err)
	}
	if !strings.Contains(err.Error(), "Run 'atlas init' first") {
		t.Fatalf("expected onboarding hint in error; got: %v", err)
	}
}

// TestTrace_FreshFlagReWalks confirms --fresh re-walks the codebase from
// disk and is allowed to take materially longer than the cached path. We
// assert behavioural equivalence by feeding it a tiny fixture (one Go file)
// and confirming the resulting chain still surfaces the seeded symbol.
func TestTrace_FreshFlagReWalks(t *testing.T) {
	fix := newTraceFixture(t)
	// Seed a chain in the DB so --fresh ignoring the cache is observable.
	_, _, _ = fix.seedChain(t, "pkg.OnlyInDB", "pkg.Mid", "pkg.Leaf")

	// Write a tiny Go file the fresh scan can find — its symbol qualified
	// name will be different from what's in the cache, proving the fresh
	// scan didn't read the DB.
	srcDir := filepath.Join(fix.root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	goFile := filepath.Join(srcDir, "freshonly.go")
	if err := os.WriteFile(goFile, []byte("package src\n\nfunc FreshOnly() {}\n"), 0o644); err != nil {
		t.Fatalf("write go file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fix.root, "go.mod"),
		[]byte("module fixture\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	stdout, stderr, err := runTraceCmd(t, fix, "--fresh", "--root", fix.root, "FreshOnly")
	if err != nil {
		t.Fatalf("trace --fresh returned error: %v\nstderr:\n%s\nstdout:\n%s", err, stderr, stdout)
	}
	if !strings.Contains(stdout, "FreshOnly") {
		t.Fatalf("expected --fresh to resolve symbol from disk; stdout:\n%s", stdout)
	}
}

// TestTrace_AcceptsFeatureID covers atlas#28: an unprefixed feature id
// resolves via the feature_symbols link table to a merged chain over every
// linked symbol.
func TestTrace_AcceptsFeatureID(t *testing.T) {
	fix := newTraceFixture(t)
	rootA, _, _ := fix.seedChain(t, "pkg.RootA", "pkg.MidA", "pkg.LeafA")
	// Seed a second chain so the feature has 2 linked symbols.
	ctx := context.Background()
	s := fix.openStore(t)
	rootB, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.RootB", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 100,
	})
	if err != nil {
		t.Fatalf("insert rootB: %v", err)
	}
	leafB, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.LeafB", Kind: shared.KindFunc, FilePath: "src/b.go", Line: 110,
	})
	if err != nil {
		t.Fatalf("insert leafB: %v", err)
	}
	if _, err := s.Edges().Insert(ctx, store.EdgeRow{
		FromID: rootB, ToID: leafB, Kind: store.EdgeKindCall, FilePath: "src/b.go", Line: 101,
	}); err != nil {
		t.Fatalf("insert edge B: %v", err)
	}
	s.Close()

	fid := fix.seedFeature(t, "plans-patient.export-pdf", rootA, rootB)

	stdout, stderr, err := runTraceCmd(t, fix, string(fid))
	if err != nil {
		t.Fatalf("trace feature-id returned error: %v\nstderr:\n%s", err, stderr)
	}
	// Both chains must show up in the merged output.
	for _, want := range []string{"pkg.RootA", "pkg.LeafA", "pkg.RootB", "pkg.LeafB"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected merged feature chain to include %q; stdout:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "feature "+string(fid)) {
		t.Errorf("expected feature header line; stdout:\n%s", stdout)
	}
}

// TestTrace_FeatureIDPrefix exercises the explicit `feature:` prefix. The
// prefix path MUST NOT consult symbol matches even when a same-named symbol
// exists.
func TestTrace_FeatureIDPrefix(t *testing.T) {
	fix := newTraceFixture(t)
	rootID, _, _ := fix.seedChain(t, "shared-name", "pkg.Mid", "pkg.Leaf")
	fid := fix.seedFeature(t, "shared-name", rootID)

	stdout, _, err := runTraceCmd(t, fix, "feature:"+string(fid))
	if err != nil {
		t.Fatalf("trace feature: returned error: %v", err)
	}
	if !strings.Contains(stdout, "feature "+string(fid)) {
		t.Fatalf("expected feature dispatch; stdout:\n%s", stdout)
	}
}

// TestTrace_SymbolIDPrefix exercises the explicit `symbol:` prefix when the
// same id also resolves to a feature. The symbol path takes precedence
// under the prefix.
func TestTrace_SymbolIDPrefix(t *testing.T) {
	fix := newTraceFixture(t)
	rootID, _, _ := fix.seedChain(t, "shared-name", "pkg.Mid", "pkg.Leaf")
	_ = fix.seedFeature(t, "shared-name", rootID)

	stdout, _, err := runTraceCmd(t, fix, "symbol:shared-name")
	if err != nil {
		t.Fatalf("trace symbol: returned error: %v", err)
	}
	// The "call" header — symbol trace, not feature — is what we expect.
	if !strings.Contains(stdout, "trace shared-name") {
		t.Fatalf("expected symbol-mode header; stdout:\n%s", stdout)
	}
	if strings.Contains(stdout, "trace feature shared-name") {
		t.Fatalf("symbol: prefix must NOT dispatch to feature; stdout:\n%s", stdout)
	}
}

// TestTrace_SagaPrefix is the regression guard: the saga: branch must
// continue to dispatch into store.EDA.WalkSaga. We assert the saga-shaped
// human output ("saga <id> (...)") to prove the saga path fired and we did
// NOT silently fall through to the symbol resolver (which would have
// errored on the unknown id with a different message).
func TestTrace_SagaPrefix(t *testing.T) {
	fix := newTraceFixture(t)
	// Open + close so the DB file exists; WalkSaga over an empty
	// annotations table returns 0 steps with no error.
	s := fix.openStore(t)
	s.Close()

	stdout, _, err := runTraceCmd(t, fix, "saga:meal-prep-flow")
	if err != nil {
		t.Fatalf("trace saga returned error: %v", err)
	}
	if !strings.Contains(stdout, "saga meal-prep-flow") {
		t.Fatalf("expected saga-shaped output; got:\n%s", stdout)
	}
}

// TestTrace_AmbiguousErrors checks the dual-match disambiguation. We seed
// a feature whose ID is "foo.bar" AND a symbol whose qualified name ends
// in "foo.bar" — the unprefixed input must error with a hint to use a
// prefix.
func TestTrace_AmbiguousErrors(t *testing.T) {
	fix := newTraceFixture(t)
	ctx := context.Background()
	s := fix.openStore(t)
	sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "pkg.foo.bar", Kind: shared.KindFunc,
		FilePath: "src/x.go", Line: 1,
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
	s.Close()
	fid := fix.seedFeature(t, "foo.bar", sid)

	_, _, err = runTraceCmd(t, fix, string(fid))
	if err == nil {
		t.Fatal("expected disambiguation error")
	}
	for _, want := range []string{
		"matches both feature", "matches both feature \"foo.bar\"",
		"feature:foo.bar", "symbol:foo.bar",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to contain %q; got: %v", want, err)
		}
	}
}

// TestTrace_FeatureWithNoLinkedSymbols verifies the empty-chain branch:
// a feature that exists with zero links emits a clean warning to stderr,
// no error, and an empty chain.
func TestTrace_FeatureWithNoLinkedSymbols(t *testing.T) {
	fix := newTraceFixture(t)
	fid := fix.seedFeature(t, "orphan-feature")

	stdout, stderr, err := runTraceCmd(t, fix, "feature:"+string(fid))
	if err != nil {
		t.Fatalf("expected nil error; got: %v", err)
	}
	if !strings.Contains(stderr, "no linked symbols") {
		t.Errorf("expected stderr warning about orphan feature; got:\n%s", stderr)
	}
	if !strings.Contains(stdout, "feature "+string(fid)) {
		t.Errorf("expected feature header on stdout; got:\n%s", stdout)
	}
}

// TestTrace_StaleStateWarning seeds a chain, mutates the on-disk content of
// the file referenced by file_hashes, and confirms the warning surfaces
// (but the trace still succeeds — the cached data is still usable).
func TestTrace_StaleStateWarning(t *testing.T) {
	fix := newTraceFixture(t)
	_, _, _ = fix.seedChain(t, "pkg.Root", "pkg.Mid", "pkg.Leaf")

	// Write a file at the path referenced by file_hashes with content that
	// matches the seeded hash, then mutate it.
	ctx := context.Background()
	s := fix.openStore(t)
	defer s.Close()

	srcDir := filepath.Join(fix.root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	original := []byte("// generated for stale-check test\n")
	if err := os.WriteFile(filepath.Join(srcDir, "a.go"), original, 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	sum := sha256.Sum256(original)
	if err := s.FileHashes().Upsert(ctx, store.FileHashRow{
		FilePath:    "src/a.go",
		ContentHash: hex.EncodeToString(sum[:]),
		ModTime:     time.Now().UTC(),
		LastScanned: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert file_hash: %v", err)
	}
	// Mutate the file — its hash no longer matches what's in the DB.
	if err := os.WriteFile(filepath.Join(srcDir, "a.go"),
		[]byte("// MUTATED — stale-check test\n"), 0o644); err != nil {
		t.Fatalf("mutate a.go: %v", err)
	}

	_, stderr, err := runTraceCmd(t, fix, "pkg.Root")
	if err != nil {
		t.Fatalf("trace returned error: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "stale") {
		t.Errorf("expected stale warning on stderr; got:\n%s", stderr)
	}
}

// TestTrace_JSONEnvelope_Cache exercises the --json output for the cached
// path so consumers can rely on the source=cache field.
func TestTrace_JSONEnvelope_Cache(t *testing.T) {
	fix := newTraceFixture(t)
	_, _, _ = fix.seedChain(t, "pkg.Root", "pkg.Mid", "pkg.Leaf")

	stdout, _, err := runTraceCmd(t, fix, "--json", "pkg.Root")
	if err != nil {
		t.Fatalf("trace --json: %v", err)
	}
	var env struct {
		Result struct {
			Kind   string `json:"kind"`
			Source string `json:"source"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\nstdout:\n%s", err, stdout)
	}
	if env.Result.Source != "cache" {
		t.Errorf("expected source=cache; got %q", env.Result.Source)
	}
	if env.Result.Kind != "call" {
		t.Errorf("expected kind=call; got %q", env.Result.Kind)
	}
}

// TestTrace_FreshFlagWired is the cheap flag-surface regression guard —
// future refactors that drop --fresh would silently re-introduce the
// minutes-per-trace cost.
func TestTrace_FreshFlagWired(t *testing.T) {
	c := newTraceCmd()
	if c.Flags().Lookup("fresh") == nil {
		t.Fatal("atlas trace is missing --fresh flag")
	}
}

// TestTrace_DepthFlagWired is the same surface guard for issue #61's
// --depth flag.
func TestTrace_DepthFlagWired(t *testing.T) {
	c := newTraceCmd()
	if c.Flags().Lookup("depth") == nil {
		t.Fatal("atlas trace is missing --depth flag")
	}
}

// seedFanChain seeds a non-linear chain that branches at depth 1 so the
// tree shape is observably different from the legacy flat output.
//
//	root → mid1 → leaf1
//	     → mid2 → leaf2
//
// Returns the surrogate id of root.
func (f *traceFixture) seedFanChain(t *testing.T, rootQN string) int64 {
	t.Helper()
	ctx := context.Background()
	s := f.openStore(t)
	defer s.Close()

	insert := func(qn, file string, line int) int64 {
		t.Helper()
		id, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(qn), Kind: shared.KindFunc,
			FilePath: file, Line: line,
		})
		if err != nil {
			t.Fatalf("insert %q: %v", qn, err)
		}
		return id
	}
	root := insert(rootQN, "src/root.go", 10)
	mid1 := insert("pkg.Mid1", "src/mid1.go", 20)
	mid2 := insert("pkg.Mid2", "src/mid2.go", 30)
	leaf1 := insert("pkg.Leaf1", "src/leaf1.go", 40)
	leaf2 := insert("pkg.Leaf2", "src/leaf2.go", 50)
	for _, e := range []store.EdgeRow{
		{FromID: root, ToID: mid1, Kind: store.EdgeKindCall, FilePath: "src/root.go", Line: 11},
		{FromID: root, ToID: mid2, Kind: store.EdgeKindCall, FilePath: "src/root.go", Line: 12},
		{FromID: mid1, ToID: leaf1, Kind: store.EdgeKindCall, FilePath: "src/mid1.go", Line: 21},
		{FromID: mid2, ToID: leaf2, Kind: store.EdgeKindCall, FilePath: "src/mid2.go", Line: 31},
	} {
		if _, err := s.Edges().Insert(ctx, e); err != nil {
			t.Fatalf("insert edge: %v", err)
		}
	}
	return root
}

// seedCycleChain seeds a 2-node cycle so the cycle-detection branch is
// exercised: a → b → a. Returns the surrogate id of a.
func (f *traceFixture) seedCycleChain(t *testing.T, aQN, bQN string) int64 {
	t.Helper()
	ctx := context.Background()
	s := f.openStore(t)
	defer s.Close()

	a, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(aQN), Kind: shared.KindFunc,
		FilePath: "src/a.go", Line: 10,
	})
	if err != nil {
		t.Fatalf("insert %q: %v", aQN, err)
	}
	b, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(bQN), Kind: shared.KindFunc,
		FilePath: "src/b.go", Line: 20,
	})
	if err != nil {
		t.Fatalf("insert %q: %v", bQN, err)
	}
	for _, e := range []store.EdgeRow{
		{FromID: a, ToID: b, Kind: store.EdgeKindCall, FilePath: "src/a.go", Line: 11},
		{FromID: b, ToID: a, Kind: store.EdgeKindCall, FilePath: "src/b.go", Line: 21},
	} {
		if _, err := s.Edges().Insert(ctx, e); err != nil {
			t.Fatalf("insert edge: %v", err)
		}
	}
	return a
}

// TestTrace_DepthDefault confirms the default --depth of 3 is what the
// command actually uses. We seed a linear chain longer than 3 hops and
// expect the output to clip at 3.
func TestTrace_DepthDefault(t *testing.T) {
	fix := newTraceFixture(t)
	// Build a 5-deep chain: pkg.L0 → pkg.L1 → pkg.L2 → pkg.L3 → pkg.L4
	ctx := context.Background()
	s := fix.openStore(t)
	var ids [5]int64
	for i := 0; i < 5; i++ {
		id, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID("pkg.L" + string(rune('0'+i))),
			Kind:          shared.KindFunc,
			FilePath:      "src/x.go", Line: 10 + i,
		})
		if err != nil {
			t.Fatalf("insert L%d: %v", i, err)
		}
		ids[i] = id
	}
	for i := 0; i < 4; i++ {
		if _, err := s.Edges().Insert(ctx, store.EdgeRow{
			FromID: ids[i], ToID: ids[i+1], Kind: store.EdgeKindCall,
			FilePath: "src/x.go", Line: 100 + i,
		}); err != nil {
			t.Fatalf("insert edge: %v", err)
		}
	}
	s.Close()

	stdout, _, err := runTraceCmd(t, fix, "pkg.L0")
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	// Default depth=3 means we see L0..L3, NOT L4.
	for _, want := range []string{"pkg.L0", "pkg.L1", "pkg.L2", "pkg.L3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in default-depth output; stdout:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "pkg.L4") {
		t.Errorf("default depth=3 should clip pkg.L4; stdout:\n%s", stdout)
	}
}

// TestTrace_DepthOneMatchesLegacy confirms --depth 1 reproduces the
// pre-issue-#61 depth-1 walk (root + direct callees only). The fan
// fixture has 2 mids at depth 1 and 2 leaves at depth 2; --depth 1
// must show mids, not leaves.
func TestTrace_DepthOneMatchesLegacy(t *testing.T) {
	fix := newTraceFixture(t)
	_ = fix.seedFanChain(t, "pkg.Root")

	stdout, _, err := runTraceCmd(t, fix, "--depth", "1", "pkg.Root")
	if err != nil {
		t.Fatalf("trace --depth 1: %v", err)
	}
	for _, want := range []string{"pkg.Root", "pkg.Mid1", "pkg.Mid2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in --depth 1 output; stdout:\n%s", want, stdout)
		}
	}
	for _, bad := range []string{"pkg.Leaf1", "pkg.Leaf2"} {
		if strings.Contains(stdout, bad) {
			t.Errorf("--depth 1 should not include depth-2 %q; stdout:\n%s", bad, stdout)
		}
	}
}

// TestTrace_DepthUnlimitedCycle confirms --depth -1 walks unlimited
// and the cycle-detection guard prevents infinite recursion. The
// fixture is a 2-node cycle; the trace must terminate, mark the back
// edge as [cycle], and emit the cycle node in cycle_nodes.
func TestTrace_DepthUnlimitedCycle(t *testing.T) {
	fix := newTraceFixture(t)
	_ = fix.seedCycleChain(t, "pkg.A", "pkg.B")

	done := make(chan struct {
		stdout string
		err    error
	}, 1)
	go func() {
		stdout, _, err := runTraceCmd(t, fix, "--depth", "-1", "pkg.A")
		done <- struct {
			stdout string
			err    error
		}{stdout, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("trace --depth -1: %v", res.err)
		}
		if !strings.Contains(res.stdout, "[cycle]") {
			t.Errorf("expected [cycle] marker; stdout:\n%s", res.stdout)
		}
		for _, want := range []string{"pkg.A", "pkg.B"} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("expected %q in unlimited-cycle output; stdout:\n%s", want, res.stdout)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trace --depth -1 hung — cycle detection is broken")
	}
}

// TestTrace_JSONTreeShape exercises the nested-tree JSON contract that
// issue #61 introduces. Downstream tooling (e.g. atlas's own
// integration tests, future LSP plugins) will read this structure.
func TestTrace_JSONTreeShape(t *testing.T) {
	fix := newTraceFixture(t)
	_ = fix.seedFanChain(t, "pkg.Root")

	stdout, _, err := runTraceCmd(t, fix, "--json", "--depth", "2", "pkg.Root")
	if err != nil {
		t.Fatalf("trace --json --depth 2: %v", err)
	}
	var env struct {
		Result struct {
			Tree struct {
				Symbol   string `json:"symbol"`
				Depth    int    `json:"depth"`
				Children []struct {
					Symbol   string `json:"symbol"`
					Depth    int    `json:"depth"`
					Children []struct {
						Symbol string `json:"symbol"`
						Depth  int    `json:"depth"`
					} `json:"children"`
				} `json:"children"`
			} `json:"tree"`
			DepthReached int `json:"depth_reached"`
			MaxDepth     int `json:"max_depth"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\nstdout:\n%s", err, stdout)
	}
	if env.Result.Tree.Symbol != "pkg.Root" {
		t.Errorf("tree.symbol = %q; want pkg.Root", env.Result.Tree.Symbol)
	}
	if env.Result.Tree.Depth != 0 {
		t.Errorf("root depth = %d; want 0", env.Result.Tree.Depth)
	}
	if len(env.Result.Tree.Children) != 2 {
		t.Fatalf("expected 2 mid children; got %d: %+v", len(env.Result.Tree.Children), env.Result.Tree.Children)
	}
	if env.Result.DepthReached != 2 {
		t.Errorf("depth_reached = %d; want 2", env.Result.DepthReached)
	}
	if env.Result.MaxDepth != 2 {
		t.Errorf("max_depth = %d; want 2", env.Result.MaxDepth)
	}
	for _, mid := range env.Result.Tree.Children {
		if len(mid.Children) != 1 {
			t.Errorf("mid %s expected 1 leaf child; got %d", mid.Symbol, len(mid.Children))
		}
		for _, leaf := range mid.Children {
			if leaf.Depth != 2 {
				t.Errorf("leaf %s depth = %d; want 2", leaf.Symbol, leaf.Depth)
			}
		}
	}
}
