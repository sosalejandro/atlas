package pyscan

import (
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// tierOf returns the tier of the first edge from->to in res.
func tierOf(t *testing.T, res *Result, from, to shared.SymbolID) graph.ResolutionTier {
	t.Helper()
	for _, e := range res.Edges {
		if e.From == from && e.To == to {
			return e.Tier
		}
	}
	t.Fatalf("no edge %s -> %s in %+v", from, to, res.Edges)
	return graph.TierUnset
}

func pyFixture() *rawScannerOutput {
	return &rawScannerOutput{
		Nodes: []rawNode{
			{ID: "sample", Kind: "module", File: "sample.py", Line: 1},
			{ID: "sample.helper", Kind: "function", File: "sample.py", Line: 5},
			{ID: "sample.compute", Kind: "function", File: "sample.py", Line: 9},
			{ID: "sample.Base", Kind: "class", File: "sample.py", Line: 15},
			{ID: "sample.Child", Kind: "class", File: "sample.py", Line: 19},
		},
		Edges: []rawEdge{
			// Import of a first-party module the resolver can bind.
			{From: "sample", To: "sample.helper", Kind: "import", Line: 2, Scope: "module"},
			// Import of a stdlib module nothing in this project declares.
			{From: "sample", To: "os.path", Kind: "import", Line: 3, Scope: "module"},
			// A base class that resolves to an indexed definition.
			{From: "sample.Child", To: "Base", Kind: "inheritance", Line: 19},
			// A call. Python dispatches these at runtime; scanner.py's
			// callee rendering is best-effort text either way.
			{From: "sample.compute", To: "helper", Kind: "call", Line: 10},
		},
	}
}

// Every Python edge must state a tier, and the two the scanner can
// reach must both actually appear -- a scanner that stamped one
// constant on everything would satisfy "has a tier" and tell a reader
// nothing.
func TestMapToResult_EveryEdgeCarriesATier(t *testing.T) {
	res := (&Scanner{}).mapToResult(pyFixture())
	seen := map[graph.ResolutionTier]int{}
	for _, e := range res.Edges {
		if !graph.IsValidTier(e.Tier) {
			t.Errorf("edge %s -> %s has tier %q", e.From, e.To, e.Tier)
		}
		seen[e.Tier]++
	}
	if seen[graph.TierNameResolved] == 0 {
		t.Error("no name_resolved edges: a resolved import or base class must reach tier B")
	}
	if seen[graph.TierSyntactic] == 0 {
		t.Error("no syntactic edges: unresolved targets and calls must stay at tier C")
	}
}

// An import or base-class reference whose target the resolver bound to
// a symbol this scan indexed IS a name resolution: an explicit module
// path or class name, matched against the project's own index. Tier B.
func TestMapToResult_BoundImportsAndBasesAreNameResolved(t *testing.T) {
	res := (&Scanner{}).mapToResult(pyFixture())
	if got := tierOf(t, res, "sample", "sample.helper"); got != graph.TierNameResolved {
		t.Errorf("bound import tier = %q, want %q", got, graph.TierNameResolved)
	}
	if got := tierOf(t, res, "sample.Child", "sample.Base"); got != graph.TierNameResolved {
		t.Errorf("bound inheritance tier = %q, want %q", got, graph.TierNameResolved)
	}
}

// A target the resolver could not bind gets a synthetic external stub.
// The edge then points at a rendering of the source text rather than at
// anything atlas has seen, which is tier C however explicit the import
// statement looked.
func TestMapToResult_UnboundTargetsAreSyntactic(t *testing.T) {
	res := (&Scanner{}).mapToResult(pyFixture())
	if got := tierOf(t, res, "sample", "os.path"); got != graph.TierSyntactic {
		t.Errorf("unbound import tier = %q, want %q", got, graph.TierSyntactic)
	}
}

// Call edges stay at tier C even when the target resolves. Python
// dispatches attribute access at runtime, and scanner.py renders the
// callee from the AST text; that a same-named symbol exists in the
// module is a coincidence the resolver cannot distinguish from a real
// binding.
func TestMapToResult_CallEdgesStaySyntactic(t *testing.T) {
	res := (&Scanner{}).mapToResult(pyFixture())
	if got := tierOf(t, res, "sample.compute", "sample.helper"); got != graph.TierSyntactic {
		t.Errorf("call edge tier = %q, want %q -- a resolved NAME is not a resolved CALL "+
			"in a language that dispatches at runtime", got, graph.TierSyntactic)
	}
}
