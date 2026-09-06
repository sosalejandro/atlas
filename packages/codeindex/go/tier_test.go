package goscan

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// scanCorpusEdges scans the golden corpus and returns its edges.
func scanCorpusEdges(t *testing.T, opts Options) []graph.Edge {
	t.Helper()
	res, err := Scan(context.Background(), goldenCorpusDir, opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return res.Graph.Edges
}

// Every edge the Go scanner emits must state a tier. An edge with none
// cannot be persisted at all (packages/store refuses it), so this is
// the test that stops a new emit site from being written without one.
func TestGoScanner_EveryEdgeCarriesATier(t *testing.T) {
	for _, opts := range []Options{
		{},
		{Routes: corpusRoutes()},
		{EntryPoints: []shared.SymbolID{"Router.Handle"}},
	} {
		for _, e := range scanCorpusEdges(t, opts) {
			if !graph.IsValidTier(e.Tier) {
				t.Errorf("edge %s -> %s has tier %q; every emit site must state one",
					e.From, e.To, e.Tier)
			}
		}
	}
}

// #87 landed, and this is the assertion it broke.
//
// #146 shipped TestGoScanner_ClaimsNoTypedEdgesYet -- "this scanner has
// no type information, so it must never claim tier A" -- precisely so
// that the resolver migration would fail a test instead of passing one.
// It did. The test is replaced rather than deleted, because the claim
// worth pinning did not disappear, it inverted: the Go scanner now DOES
// type-check, and what needs guarding is that it type-checks the corpus
// it is pointed at rather than quietly degrading to name matching and
// still passing every count-based test in this file.
//
// The histogram for testdata/goldencorpus, identical with and without
// the route table:
//
//	                 #146 (AST)   #87 (typed)
//	typed                     0            48
//	name_resolved            26             0
//	syntactic                17             4
//	total                    43            52
//
// The four remaining syntactic edges are the @api comment -> handler
// edges (the route table's three merge onto the same endpoints). Neither
// is call resolution and neither gets any better with types. Every CALL
// edge in the corpus is typed. docs/languages/go.md carries the same
// table for this repository, where the fallback tiers do not reach zero.
func TestGoScanner_TypeChecksTheCorpus(t *testing.T) {
	seen := map[graph.ResolutionTier]int{}
	for _, e := range scanCorpusEdges(t, Options{Routes: corpusRoutes()}) {
		seen[e.Tier]++
		if e.Tier == graph.TierImported {
			t.Errorf("edge %s -> %s claims tier %q, which only SCIP ingest may produce",
				e.From, e.To, graph.TierImported)
		}
	}
	if seen[graph.TierTyped] == 0 {
		t.Error("no typed edges: the corpus is a module that compiles, so every call in it " +
			"must resolve through go/packages -- a zero here means the scan degraded silently")
	}
	if seen[graph.TierNameResolved] != 0 {
		t.Errorf("%d name_resolved edges: every package in the corpus type-checks, so no call "+
			"should have reached the name ladder", seen[graph.TierNameResolved])
	}
}

// The name ladder is still there, still reachable, and still honest about
// itself. SkipTypedResolution is the escape hatch for a machine with no
// toolchain or a scan that cannot afford the load, and if it ever stopped
// producing tier B and C the fallback would have rotted without anything
// noticing -- the typed path passes every other test in this file on its
// own.
func TestGoScanner_FallbackStillReachesBothTiersItCan(t *testing.T) {
	seen := map[graph.ResolutionTier]int{}
	for _, e := range scanCorpusEdges(t, Options{Routes: corpusRoutes(), SkipTypedResolution: true}) {
		seen[e.Tier]++
		if e.Tier == graph.TierTyped {
			t.Errorf("edge %s -> %s is typed with SkipTypedResolution set", e.From, e.To)
		}
	}
	if seen[graph.TierNameResolved] == 0 {
		t.Error("no name_resolved edges: resolveInScope hits must be recorded as tier B")
	}
	if seen[graph.TierSyntactic] == 0 {
		t.Error("no syntactic edges: fuzzy and unresolved-guess paths must be recorded as tier C")
	}
}

// The route table feeds the scanner handler references as STRINGS
// ("h.orderHandler.Create"), matched later against symbols by name
// suffix. Nothing about that is resolution, so both the route->handler
// and the @api->handler edges are syntactic. Recording them as
// name_resolved would put the weakest edges atlas produces in the same
// bucket as its scope-resolved calls.
func TestGoScanner_RouteAndAPIEdgesAreSyntactic(t *testing.T) {
	for _, e := range scanCorpusEdges(t, Options{Routes: corpusRoutes()}) {
		if !isEndpointID(e.From) {
			continue
		}
		if e.Tier != graph.TierSyntactic {
			t.Errorf("endpoint edge %s -> %s has tier %q, want %q",
				e.From, e.To, e.Tier, graph.TierSyntactic)
		}
	}
}

// isEndpointID reports whether id looks like the "METHOD /path" node the
// route and @api passes synthesise.
func isEndpointID(id shared.SymbolID) bool {
	for _, m := range []string{"GET ", "POST ", "PUT ", "PATCH ", "DELETE "} {
		if len(id) > len(m) && string(id[:len(m)]) == m {
			return true
		}
	}
	return false
}
