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

// merge folds many per-test profiles into one report. Attributed statements
// must fold per FILE, not as a scalar maximum: two profiles that describe
// different files (per-package profiles, or a narrower -coverpkg) each carry a
// partial total, and the run attributed the union of them. Taking the larger
// scalar silently discards the other profile's work — and the number it feeds,
// coverage_runs.stmts_attributed, is the one the whole product quotes.
func TestAttributionReport_MergeFoldsAttributedPerFile(t *testing.T) {
	byFile := indexSymbolsByFile([]store.SymbolRow{
		{ID: 1, FilePath: "src/a.go", Line: 10, EndLine: ip(20)},
		{ID: 2, FilePath: "src/b.go", Line: 10, EndLine: ip(20)},
	})
	// Two per-package profiles: each names only its own package's file.
	repA := attributeStatements(map[string][]gocover.Block{
		"github.com/org/repo/src/a.go": {{File: "a.go", StartLine: 11, NumStmts: 7, Count: 1}},
	}, byFile)
	repB := attributeStatements(map[string][]gocover.Block{
		"github.com/org/repo/src/b.go": {{File: "b.go", StartLine: 11, NumStmts: 5, Count: 1}},
	}, byFile)

	merged := newAttributionReport()
	merged.merge(repA)
	merged.merge(repB)

	if merged.stmtsAttributed != 12 {
		t.Errorf("stmtsAttributed = %d, want 12 (7 in a.go + 5 in b.go); a scalar max would say 7",
			merged.stmtsAttributed)
	}
	if merged.filesMatched != 2 {
		t.Errorf("filesMatched = %d, want 2", merged.filesMatched)
	}

	// And the union property still has to hold: the same file seen twice with
	// the same attributable statements counts once, not twice.
	twice := newAttributionReport()
	twice.merge(repA)
	twice.merge(repA)
	if twice.stmtsAttributed != 7 {
		t.Errorf("stmtsAttributed = %d after merging one profile twice, want 7 — union, not sum",
			twice.stmtsAttributed)
	}
}
