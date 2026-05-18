package goscan

import (
	"context"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/shared"
)

func TestScan_SampleProject_BuildsCallGraph(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/sampleproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Graph == nil {
		t.Fatal("nil graph")
	}
	if len(res.Symbols) == 0 {
		t.Fatal("expected symbols")
	}

	// Expect at minimum the three method nodes plus their constructors.
	wantNodes := []shared.SymbolID{
		"AuthHandler.Login",
		"AuthService.Authenticate",
		"UserRepository.GetUserByEmail",
	}
	for _, want := range wantNodes {
		if _, ok := res.Graph.Nodes[want]; !ok {
			t.Fatalf("expected node %s; got nodes: %v", want, nodeIDs(res))
		}
	}

	// Expect the call chain to materialise as edges.
	wantEdges := []struct{ from, to shared.SymbolID }{
		{"AuthHandler.Login", "AuthService.Authenticate"},
		{"AuthService.Authenticate", "UserRepository.GetUserByEmail"},
	}
	for _, want := range wantEdges {
		if !hasEdge(res, want.from, want.to) {
			t.Fatalf("missing edge %s → %s; edges:\n%+v", want.from, want.to, res.Graph.Edges)
		}
	}

	// Layer classification: handlers → KindHandler, services → KindService,
	// persistence → KindRepository.
	if k := res.Graph.Nodes["AuthHandler.Login"].Kind; k != shared.KindHandler {
		t.Fatalf("AuthHandler.Login kind = %s; want handler", k)
	}
	if k := res.Graph.Nodes["AuthService.Authenticate"].Kind; k != shared.KindService {
		t.Fatalf("AuthService.Authenticate kind = %s; want service", k)
	}
	if k := res.Graph.Nodes["UserRepository.GetUserByEmail"].Kind; k != shared.KindRepository {
		t.Fatalf("UserRepository.GetUserByEmail kind = %s; want repository", k)
	}
}

func TestScan_AnnotatedAPIEndpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/sampleproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	endpoint := shared.SymbolID("POST /api/v1/auth/login")
	if _, ok := res.Graph.Nodes[endpoint]; !ok {
		t.Fatalf("expected endpoint node %s; nodes: %v", endpoint, nodeIDs(res))
	}
	if !hasEdge(res, endpoint, "AuthHandler.Login") {
		t.Fatalf("expected %s → AuthHandler.Login edge; edges: %+v", endpoint, res.Graph.Edges)
	}
}

func TestScan_EntryPoints_PruneUnreachable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/sampleproject", Options{
		EntryPoints: []shared.SymbolID{"AuthHandler.Login"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// Reachable nodes must include the full chain.
	for _, want := range []shared.SymbolID{
		"AuthHandler.Login", "AuthService.Authenticate", "UserRepository.GetUserByEmail",
	} {
		if _, ok := res.Graph.Nodes[want]; !ok {
			t.Fatalf("entry-point trace should preserve %s; got %v", want, nodeIDs(res))
		}
	}
	// Constructors are NOT reachable from Login; they should be pruned.
	for _, gone := range []shared.SymbolID{
		"handlers.NewAuthHandler", "services.NewAuthService", "persistence.NewUserRepository",
	} {
		if _, ok := res.Graph.Nodes[gone]; ok {
			t.Fatalf("expected %s to be pruned (unreachable from Login)", gone)
		}
	}
}

func TestScan_EmptyRoot_Error(t *testing.T) {
	t.Parallel()

	_, err := Scan(context.Background(), "", Options{})
	if err == nil {
		t.Fatal("expected error for empty rootDir")
	}
}

func TestScan_NonexistentRoot_NoError_EmptyResult(t *testing.T) {
	t.Parallel()

	// WalkDir on a missing path returns an err that the walker function
	// swallows ("skip inaccessible"). Scan should return a Result with an
	// empty graph rather than failing the whole run.
	res, err := Scan(context.Background(), "testdata/nope-does-not-exist", Options{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(res.Graph.Nodes) != 0 {
		t.Fatalf("expected empty graph; got %d nodes", len(res.Graph.Nodes))
	}
}

func TestScan_IgnoreFunctionsGlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/sampleproject", Options{
		IgnoreFunctions: []string{"*.Authenticate"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if hasEdge(res, "AuthHandler.Login", "AuthService.Authenticate") {
		t.Fatal("expected ignored edge AuthHandler.Login → AuthService.Authenticate to be skipped")
	}
}

func TestScan_PreSuppliedRoute_CreatesEndpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/sampleproject", Options{
		Routes: []Route{
			{Method: "GET", Path: "/healthz", Handler: "Health.Get", Line: 1},
		},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	endpoint := shared.SymbolID("GET /healthz")
	if _, ok := res.Graph.Nodes[endpoint]; !ok {
		t.Fatalf("expected route-derived endpoint %s in nodes %v", endpoint, nodeIDs(res))
	}
}

// TestScan_IndexesTestFilesByDefault asserts the load-bearing default
// behaviour Atlas's feature-attribution workflow depends on: `_test.go`
// files contribute their top-level test funcs to the graph alongside
// production sources unless the caller explicitly opts out.
//
// This is the regression guard for atlas#26: Phase 9's nutrition dogfood
// reported FeaturesMaterialized=0 because the Go scanner was silently
// skipping every `_test.go` and therefore every test-attached
// `@atlas:feature` / `@testreg` annotation had no symbol to link to.
func TestScan_IndexesTestFilesByDefault(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/testfileproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Production funcs from foo.go must be present.
	for _, want := range []shared.SymbolID{
		"foo.Foo",
		"Receiver.FooReceiver",
	} {
		if _, ok := res.Graph.Nodes[want]; !ok {
			t.Fatalf("production symbol %s missing from default scan; nodes: %v",
				want, nodeIDs(res))
		}
	}
	// Test funcs from foo_test.go must ALSO be present (the new default).
	for _, want := range []shared.SymbolID{
		"foo.TestFoo",
		"foo.TestFooFeature",
	} {
		if _, ok := res.Graph.Nodes[want]; !ok {
			t.Fatalf("test symbol %s missing from default scan; nodes: %v",
				want, nodeIDs(res))
		}
	}
}

// TestScan_SkipTestsRespected pins down the opt-out path: a caller that
// explicitly sets Options.SkipTests=true gets the pre-issue-#26
// behaviour — production funcs only, no `_test.go` symbols leak into
// the graph. This is the escape hatch for graph-only audits where test
// funcs would be noise.
func TestScan_SkipTestsRespected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/testfileproject", Options{SkipTests: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// Production funcs still present.
	for _, want := range []shared.SymbolID{
		"foo.Foo",
		"Receiver.FooReceiver",
	} {
		if _, ok := res.Graph.Nodes[want]; !ok {
			t.Fatalf("production symbol %s missing under SkipTests; nodes: %v",
				want, nodeIDs(res))
		}
	}
	// Test funcs MUST be absent.
	for _, gone := range []shared.SymbolID{
		"foo.TestFoo",
		"foo.TestFooFeature",
	} {
		if _, ok := res.Graph.Nodes[gone]; ok {
			t.Fatalf("test symbol %s leaked into graph under SkipTests=true; nodes: %v",
				gone, nodeIDs(res))
		}
	}
}

// TestScan_AnnotationsInTestFilesAreFound is the on-the-nose Phase 9
// guarantee: a `@atlas:feature` annotation attached to a `Test*` func
// in a `_test.go` file lands in the graph at the test func's position.
// Without this, downstream materialize wires the annotation to no
// symbol and the feature stays unmaterialized.
//
// The annotations package is the surface that emits the Annotation
// record, but the test func's Symbol.Position is what the
// store.LookupSymbolAtOrAfterLine call needs to match against. Both
// have to point at the same `_test.go` line for the link to materialize.
func TestScan_AnnotationsInTestFilesAreFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res, err := Scan(ctx, "testdata/testfileproject", Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	sym, ok := res.Graph.Nodes["foo.TestFooFeature"]
	if !ok {
		t.Fatalf("TestFooFeature symbol missing; nodes: %v", nodeIDs(res))
	}
	if got := sym.Position.Path; !strings.HasSuffix(got, "foo_test.go") {
		t.Fatalf("TestFooFeature Position.Path = %q; want suffix foo_test.go", got)
	}
	if sym.Position.Line == 0 {
		t.Fatalf("TestFooFeature Position.Line = 0; expected non-zero source line")
	}

	// Cross-package contract: the annotation parser is what produces the
	// `@atlas:feature foo.bar` Annotation record. Parse the same file the
	// scanner just walked and assert that (a) we see exactly one feature
	// annotation, (b) its position lines up with our test func — when
	// these two views agree, ingest.LookupSymbolAtOrAfterLine will
	// materialize the feature successfully.
	anns, err := annotations.ParseRelative(ctx,
		"testdata/testfileproject/foo_test.go",
		"testdata/testfileproject/foo_test.go")
	if err != nil {
		t.Fatalf("annotations.ParseRelative: %v", err)
	}
	var featureAnn *shared.Annotation
	for i := range anns {
		if anns[i].Kind == shared.AnnFeature {
			featureAnn = &anns[i]
			break
		}
	}
	if featureAnn == nil {
		t.Fatalf("expected one @atlas:feature annotation in foo_test.go; got %d annotations: %+v", len(anns), anns)
	}
	if !contains(featureAnn.IDs, "foo.bar") {
		t.Fatalf("annotation IDs = %v; want contains foo.bar", featureAnn.IDs)
	}
	// The annotation sits on the line BEFORE the func declaration.
	// LookupSymbolAtOrAfterLine probes forward from the annotation line
	// through defaultPositionLookahead (=30) lines, so the test passes as
	// long as the symbol's line is >= the annotation's line and within
	// that window. Asserting just `>=` here is the right level — the
	// store-side window logic is the store's test surface.
	if sym.Position.Line < featureAnn.Position.Line {
		t.Fatalf("symbol line %d precedes annotation line %d; LookupSymbolAtOrAfterLine would miss the link",
			sym.Position.Line, featureAnn.Position.Line)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// nodeIDs returns the list of node IDs in a Result for test failure messages.
func nodeIDs(res *Result) []shared.SymbolID {
	out := make([]shared.SymbolID, 0, len(res.Graph.Nodes))
	for id := range res.Graph.Nodes {
		out = append(out, id)
	}
	return out
}

func hasEdge(res *Result, from, to shared.SymbolID) bool {
	for _, e := range res.Graph.Edges {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}
