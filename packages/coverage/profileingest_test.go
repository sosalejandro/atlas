package coverage

import (
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/store"
)

func ip(i int) *int { return &i }

func TestIndexSymbolsByFile_EffectiveRanges(t *testing.T) {
	syms := []store.SymbolRow{
		{ID: 1, FilePath: "src/svc.go", Line: 10, EndLine: ip(20)}, // explicit span
		{ID: 2, FilePath: "src/svc.go", Line: 30},                  // no end → next-1
		{ID: 3, FilePath: "src/svc.go", Line: 50},                  // last → EOF
	}
	got := indexSymbolsByFile(syms)["src/svc.go"]
	if len(got) != 3 {
		t.Fatalf("want 3 spans, got %d", len(got))
	}
	if got[0].start != 10 || got[0].end != 20 {
		t.Errorf("sym1 = %+v, want [10,20]", got[0])
	}
	if got[1].start != 30 || got[1].end != 49 {
		t.Errorf("sym2 = %+v, want [30,49] (next-1)", got[1])
	}
	if got[2].start != 50 || got[2].end < 1000 {
		t.Errorf("sym3 = %+v, want open-ended", got[2])
	}
}

func TestAttributeStatements_LineWeightedAndPathSuffix(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "src/contexts/billing/svc.go", Line: 10, EndLine: ip(20)},
		{ID: 2, FilePath: "src/contexts/billing/svc.go", Line: 30, EndLine: ip(40)},
		{ID: 3, FilePath: "src/contexts/billing/other.go", Line: 5, EndLine: ip(9)},
	})
	// Profile paths are module-qualified. Each block is attributed to the
	// symbol whose [start,end] span contains its start line; covered counts
	// only executed (Count>0) statements.
	//   sym1: two blocks @11 (3 stmts, ran) + @15 (2 stmts, NOT run) → 3/5
	//   sym2: one block @31 (4 stmts, NOT run)                       → 0/4
	//   sym3: one block @6 (1 stmt, ran)                             → 1/1
	blocksByFile := map[string][]gocover.Block{
		"github.com/org/repo/src/contexts/billing/svc.go": {
			{File: "svc.go", StartLine: 11, EndLine: 12, NumStmts: 3, Count: 2},
			{File: "svc.go", StartLine: 15, EndLine: 16, NumStmts: 2, Count: 0},
			{File: "svc.go", StartLine: 31, EndLine: 32, NumStmts: 4, Count: 0},
		},
		"github.com/org/repo/src/contexts/billing/other.go": {
			{File: "other.go", StartLine: 6, EndLine: 6, NumStmts: 1, Count: 1},
		},
	}
	counts, matched, unmatched := attributeStatements(blocksByFile, byFile)
	if matched != 2 {
		t.Errorf("filesMatched = %d, want 2", matched)
	}
	if unmatched != 0 {
		t.Errorf("filesUnmatched = %d, want 0", unmatched)
	}
	if c := counts[1]; c.covered != 3 || c.total != 5 {
		t.Errorf("sym1 = %+v, want covered=3 total=5", c)
	}
	if c := counts[2]; c.covered != 0 || c.total != 4 {
		t.Errorf("sym2 = %+v, want covered=0 total=4 (did not run)", c)
	}
	if c := counts[3]; c.covered != 1 || c.total != 1 {
		t.Errorf("sym3 = %+v, want covered=1 total=1", c)
	}
}

func TestAttributeStatements_UnmatchedFileCounted(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "src/contexts/billing/svc.go", Line: 10, EndLine: ip(20)},
	})
	blocksByFile := map[string][]gocover.Block{
		"github.com/org/repo/src/contexts/billing/svc.go": {
			{File: "svc.go", StartLine: 11, EndLine: 12, NumStmts: 2, Count: 1},
		},
		"github.com/org/repo/src/contexts/unknown/ghost.go": {
			{File: "ghost.go", StartLine: 3, EndLine: 4, NumStmts: 5, Count: 1},
		},
	}
	counts, matched, unmatched := attributeStatements(blocksByFile, byFile)
	if matched != 1 || unmatched != 1 {
		t.Errorf("matched=%d unmatched=%d, want 1/1", matched, unmatched)
	}
	if _, ok := counts[1]; !ok {
		t.Errorf("sym1 should have counts")
	}
}

func TestReconcilePath_BoundaryAlignment(t *testing.T) {
	byBase := map[string][]string{"svc.go": {"contexts/billing/svc.go", "billing/svc.go"}}
	// Prefers the most specific (longest) suffix.
	if got := reconcilePath("github.com/x/contexts/billing/svc.go", byBase); got != "contexts/billing/svc.go" {
		t.Errorf("got %q, want contexts/billing/svc.go", got)
	}
	// Non-boundary suffix must NOT match (xsvc.go vs svc.go).
	byBase2 := map[string][]string{"svc.go": {"svc.go"}}
	if got := reconcilePath("github.com/x/xsvc.go", byBase2); got != "" {
		t.Errorf("got %q, want '' (xsvc.go must not match suffix svc.go)", got)
	}
}
