package coverage

import (
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/store"
)

// Issue #85: the ingest used to report only "files=641/852", so the ~25% of
// the profile that reconciled to no symbol was invisible. Attribution gaps
// must be enumerable — both files atlas has never indexed AND statements
// inside indexed files that fall outside every symbol's span.
func TestAttributeStatements_ReportsGaps(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "src/billing/svc.go", Line: 10, EndLine: ip(20)},
	})
	blocksByFile := map[string][]gocover.Block{
		"github.com/org/repo/src/billing/svc.go": {
			{File: "svc.go", StartLine: 11, NumStmts: 2, Count: 1}, // attributed
			{File: "svc.go", StartLine: 80, NumStmts: 7, Count: 1}, // outside every span
		},
		"github.com/org/repo/src/billing/generated.go": {
			{File: "generated.go", StartLine: 3, NumStmts: 5, Count: 1}, // file not indexed
		},
	}

	rep := attributeStatements(blocksByFile, byFile)

	if rep.filesMatched != 1 || rep.filesUnmatched != 1 {
		t.Fatalf("matched=%d unmatched=%d, want 1/1", rep.filesMatched, rep.filesUnmatched)
	}
	if rep.stmtsAttributed != 2 {
		t.Errorf("stmtsAttributed = %d, want 2", rep.stmtsAttributed)
	}
	if rep.stmtsUnattributed != 12 {
		t.Errorf("stmtsUnattributed = %d, want 12 (7 outside spans + 5 unindexed file)", rep.stmtsUnattributed)
	}

	gaps := rep.gaps()
	if len(gaps) != 2 {
		t.Fatalf("want 2 gap rows, got %d: %+v", len(gaps), gaps)
	}
	// Sorted by statements lost, descending: the unindexed file has 5, the
	// span gap has 7 → span gap first.
	if gaps[0].Path != "github.com/org/repo/src/billing/svc.go" || gaps[0].Stmts != 7 ||
		gaps[0].Reason != ReasonOutsideSymbolSpans {
		t.Errorf("gaps[0] = %+v, want svc.go/7/outside-symbol-spans", gaps[0])
	}
	if gaps[1].Path != "github.com/org/repo/src/billing/generated.go" || gaps[1].Stmts != 5 ||
		gaps[1].Reason != ReasonNoIndexedSymbol {
		t.Errorf("gaps[1] = %+v, want generated.go/5/no-indexed-symbol", gaps[1])
	}
}
