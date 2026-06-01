package coverage

import (
	"testing"

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

func TestAttributeExecution_SpanOverlapAndPathSuffix(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "src/contexts/billing/svc.go", Line: 10, EndLine: ip(20)},
		{ID: 2, FilePath: "src/contexts/billing/svc.go", Line: 30, EndLine: ip(40)},
		{ID: 3, FilePath: "src/contexts/billing/other.go", Line: 5, EndLine: ip(9)},
	})
	// Profile paths are module-qualified; only sym1 (11-12) and sym3 (6) ran.
	spans := map[string][][2]int{
		"github.com/org/repo/src/contexts/billing/svc.go":   {{11, 12}},
		"github.com/org/repo/src/contexts/billing/other.go": {{6, 6}},
	}
	executed, matched := attributeExecution(spans, byFile)
	if matched != 2 {
		t.Errorf("filesMatched = %d, want 2", matched)
	}
	if !executed[1] || executed[2] || !executed[3] {
		t.Errorf("executed = %v, want {1,3} (sym2 did not run)", executed)
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
