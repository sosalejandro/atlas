package tsscan

import (
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
)

// The TypeScript scanner reaches tier C and no further, and the reason
// is visible in scanner.ts itself: it parses each file with
// ts.createSourceFile and never builds a Program, so there is no type
// checker and no module resolution. Hook-to-API and route-to-component
// edges are matched by directory convention and identifier text.
//
// The test runs on the mapper rather than through a scan because the
// mapper is where the tier is stamped, and because the end-to-end
// scanner tests skip on a machine with no `node`. A provenance
// guarantee that evaporates when a runtime is missing is not a
// guarantee.
func TestMapToResult_EveryEdgeIsSyntactic(t *testing.T) {
	raw := &rawScannerOutput{
		Nodes: []rawNode{
			{ID: "web/src/hooks/useAuth.ts::useAuth", Kind: "hook", File: "web/src/hooks/useAuth.ts", Line: 19},
			{ID: "web/src/services/api/auth.ts::login", Kind: "api", File: "web/src/services/api/auth.ts", Line: 4},
		},
		Edges: []rawEdge{
			{From: "web/src/hooks/useAuth.ts::useAuth", To: "web/src/services/api/auth.ts::login"},
		},
	}

	res := (&Scanner{}).mapToResult(raw)
	if len(res.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(res.Edges))
	}
	if got := res.Edges[0].Tier; got != graph.TierSyntactic {
		t.Errorf("edge tier = %q, want %q -- scanner.ts has no type checker and no "+
			"cross-file name binding, so anything stronger is a claim it cannot support",
			got, graph.TierSyntactic)
	}
}
