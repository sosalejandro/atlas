package goscan

import (
	"context"
	"testing"

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
