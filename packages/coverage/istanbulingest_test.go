package coverage

import (
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage/istanbul"
	"github.com/sosalejandro/atlas/packages/store"
)

// TestAttributeIstanbulStatements_LineWeightedAndPathSuffix mirrors the Go
// ingester's attribution test: istanbul reports use ABSOLUTE file paths, atlas
// FE symbols use repo-relative paths (apps/web-*/src/...). Each statement is
// attributed to the symbol whose [start,end] span contains its start line;
// covered counts only executed (Count>0) statements. FE symbols carry NO
// end_line in atlas (all NULL), so indexSymbolsByFile's next-symbol-1 / EOF
// fallback supplies the span — exercised here by leaving EndLine nil on sym2.
func TestAttributeIstanbulStatements_LineWeightedAndPathSuffix(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "apps/web-patient/src/svc.tsx", Line: 10, EndLine: ip(20)},
		{ID: 2, FilePath: "apps/web-patient/src/svc.tsx", Line: 30}, // no end_line → next-1/EOF
		{ID: 3, FilePath: "apps/web-patient/src/other.tsx", Line: 5, EndLine: ip(9)},
	})
	// sym1 [10,20]: stmt@11 (ran) + stmt@15 (NOT run)        → 1/2
	// sym2 [30,EOF]: stmt@31 (NOT run) + stmt@35 (ran)       → 1/2
	// sym3 [5,9]:   stmt@6 (ran)                             → 1/1
	byFileStmts := map[string][]istanbul.Statement{
		"/abs/repo/apps/web-patient/src/svc.tsx": {
			{StartLine: 11, EndLine: 12, Count: 2},
			{StartLine: 15, EndLine: 16, Count: 0},
			{StartLine: 31, EndLine: 31, Count: 0},
			{StartLine: 35, EndLine: 36, Count: 4},
		},
		"/abs/repo/apps/web-patient/src/other.tsx": {
			{StartLine: 6, EndLine: 6, Count: 1},
		},
	}
	rep := attributeIstanbulStatements(byFileStmts, byFile)
	counts, matched, unmatched := rep.counts, rep.filesMatched, rep.filesUnmatched
	if matched != 2 {
		t.Errorf("filesMatched = %d, want 2", matched)
	}
	if unmatched != 0 {
		t.Errorf("filesUnmatched = %d, want 0", unmatched)
	}
	if c := counts[1]; c.covered != 1 || c.total != 2 {
		t.Errorf("sym1 = %+v, want covered=1 total=2", c)
	}
	if c := counts[2]; c.covered != 1 || c.total != 2 {
		t.Errorf("sym2 = %+v, want covered=1 total=2 (open-ended span)", c)
	}
	if c := counts[3]; c.covered != 1 || c.total != 1 {
		t.Errorf("sym3 = %+v, want covered=1 total=1", c)
	}
}

// TestAttributeIstanbulStatements_UnmatchedFileCounted verifies a report file
// that reconciles to no atlas symbol increments FilesUnmatched (the FE
// analogue of issue #85) without polluting the per-symbol counts.
func TestAttributeIstanbulStatements_UnmatchedFileCounted(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "apps/web-patient/src/svc.tsx", Line: 10, EndLine: ip(20)},
	})
	byFileStmts := map[string][]istanbul.Statement{
		"/abs/repo/apps/web-patient/src/svc.tsx": {
			{StartLine: 11, EndLine: 12, Count: 1},
		},
		"/abs/repo/apps/web-patient/src/ghost.tsx": {
			{StartLine: 3, EndLine: 4, Count: 1},
		},
	}
	rep := attributeIstanbulStatements(byFileStmts, byFile)
	counts, matched, unmatched := rep.counts, rep.filesMatched, rep.filesUnmatched
	if matched != 1 || unmatched != 1 {
		t.Errorf("matched=%d unmatched=%d, want 1/1", matched, unmatched)
	}
	if c, ok := counts[1]; !ok || c.covered != 1 || c.total != 1 {
		t.Errorf("sym1 = %+v ok=%v, want covered=1 total=1", c, ok)
	}
}

// TestAttributeIstanbulStatements_NoDoubleCountAdjacentSymbols verifies a
// statement on a line owned by exactly one symbol is charged once (tightest
// span wins), so adjacent symbols never double-count.
func TestAttributeIstanbulStatements_NoDoubleCountAdjacentSymbols(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "apps/web-patient/src/a.tsx", Line: 1, EndLine: ip(100)}, // broad
		{ID: 2, FilePath: "apps/web-patient/src/a.tsx", Line: 40, EndLine: ip(60)}, // tighter, nested
	})
	byFileStmts := map[string][]istanbul.Statement{
		"/x/apps/web-patient/src/a.tsx": {
			{StartLine: 50, EndLine: 50, Count: 1}, // inside both → tightest (sym2) wins
		},
	}
	counts := attributeIstanbulStatements(byFileStmts, byFile).counts
	if c := counts[2]; c.total != 1 || c.covered != 1 {
		t.Errorf("sym2 = %+v, want 1/1 (tightest span owns line 50)", c)
	}
	if _, ok := counts[1]; ok {
		t.Errorf("sym1 should NOT be charged (no double-count)")
	}
}
