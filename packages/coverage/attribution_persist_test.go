package coverage_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// storeWithBillingSymbol opens a fresh store holding exactly one indexed
// symbol, so a profile naming a second file is guaranteed to produce a gap.
func storeWithBillingSymbol(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	end := 20
	if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "billing.Total",
		Kind:          shared.KindFunc,
		FilePath:      "billing/order.go",
		Line:          10,
		EndLine:       &end,
	}); err != nil {
		t.Fatalf("seed symbol: %v", err)
	}
	return s
}

// latestRunGaps reads back what the ingest persisted, the way any consumer
// that did not run the ingest itself has to.
func latestRunGaps(t *testing.T, s *store.Store, runID int64) (store.CoverageRun, []store.CoverageGap) {
	t.Helper()
	ctx := context.Background()
	run, err := s.Coverage().GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	gaps, err := s.CoverageGaps().List(ctx, runID)
	if err != nil {
		t.Fatalf("CoverageGaps.List: %v", err)
	}
	return run, gaps
}

// The profile below charges 4 statements to the indexed billing symbol,
// loses 2 to a line outside every symbol span, and loses 9 whole statements
// to a file atlas has no symbols for.
const gapProfile = `mode: set
github.com/example/app/billing/order.go:11.29,13.2 3 1
github.com/example/app/billing/order.go:15.23,16.2 1 0
github.com/example/app/billing/order.go:80.2,81.2 2 1
github.com/example/app/generated/queries.sql.go:5.1,9.2 9 1
`

// Issue #100: the accounting IngestGoProfile computes must land on the run
// row and in coverage_run_gaps, or it is printed once and lost forever.
func TestIngestGoProfile_PersistsAttribution(t *testing.T) {
	ctx := context.Background()
	s := storeWithBillingSymbol(t)

	stats, err := coverage.IngestGoProfile(ctx, s, coverage.RunMeta{Framework: store.FrameworkGoTest}, strings.NewReader(gapProfile))
	if err != nil {
		t.Fatalf("IngestGoProfile: %v", err)
	}

	run, gaps := latestRunGaps(t, s, stats.RunID)
	if run.FilesInReport != stats.FilesInProfile ||
		run.FilesMatched != stats.FilesMatched ||
		run.FilesUnmatched != stats.FilesUnmatched ||
		run.StmtsAttributed != stats.StmtsAttributed ||
		run.StmtsUnattributed != stats.StmtsUnattributed {
		t.Errorf("persisted run accounting %+v does not match the ingest's %+v", run, stats)
	}
	if run.StmtsUnattributed != 11 {
		t.Errorf("stmts_unattributed = %d, want 11 (9 unindexed + 2 outside spans)", run.StmtsUnattributed)
	}
	if run.StmtsAttributed != 4 {
		t.Errorf("stmts_attributed = %d, want 4", run.StmtsAttributed)
	}
	if run.GapsTruncated != 0 {
		t.Errorf("gaps_truncated = %d, want 0", run.GapsTruncated)
	}

	if len(gaps) != len(stats.Gaps) {
		t.Fatalf("persisted %d gap rows, ingest reported %d", len(gaps), len(stats.Gaps))
	}
	for i := range gaps {
		if gaps[i].Path != stats.Gaps[i].Path ||
			gaps[i].Stmts != stats.Gaps[i].Stmts ||
			gaps[i].Reason != stats.Gaps[i].Reason {
			t.Errorf("gap %d = %+v, want %+v", i, gaps[i], stats.Gaps[i])
		}
	}
	if gaps[0].Reason != coverage.ReasonNoIndexedSymbol {
		t.Errorf("largest gap reason = %q, want %q", gaps[0].Reason, coverage.ReasonNoIndexedSymbol)
	}
}

// The istanbul track carries the same contract as the Go track, so the FE
// half of the index-trust panel reads from the same two places.
func TestIngestIstanbul_PersistsAttribution(t *testing.T) {
	ctx := context.Background()
	s := storeWithBillingSymbol(t)

	// One statement inside the indexed span, two in a file atlas never saw.
	report := `{
      "/repo/billing/order.go": {
        "path": "/repo/billing/order.go",
        "statementMap": {"0": {"start": {"line": 11}, "end": {"line": 11}}},
        "s": {"0": 1}
      },
      "/repo/vendor/untracked.ts": {
        "path": "/repo/vendor/untracked.ts",
        "statementMap": {"0": {"start": {"line": 1}, "end": {"line": 1}},
                         "1": {"start": {"line": 2}, "end": {"line": 2}}},
        "s": {"0": 1, "1": 0}
      }
    }`

	stats, err := coverage.IngestIstanbul(ctx, s, coverage.RunMeta{Framework: store.FrameworkVitest}, strings.NewReader(report))
	if err != nil {
		t.Fatalf("IngestIstanbul: %v", err)
	}

	run, gaps := latestRunGaps(t, s, stats.RunID)
	if run.FilesInReport != stats.FilesInReport || run.FilesUnmatched != stats.FilesUnmatched {
		t.Errorf("persisted file counters %+v do not match the ingest's %+v", run, stats)
	}
	if run.StmtsUnattributed != 2 {
		t.Errorf("stmts_unattributed = %d, want 2", run.StmtsUnattributed)
	}
	if len(gaps) != 1 || gaps[0].Stmts != 2 {
		t.Fatalf("persisted gaps = %+v, want one 2-statement row", gaps)
	}
}

// Per-test ingest folds many profiles that all describe the SAME codebase.
// Summing their gaps would multiply one blind spot by the number of tests and
// persist a number larger than the code that exists; the union is the truth.
func TestIngestGoProfilePerTest_PersistsUnionNotSumOfGaps(t *testing.T) {
	ctx := context.Background()
	s := storeWithBillingSymbol(t)
	end := 20
	if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "billing.TestTotal",
		Kind:          shared.KindFunc,
		FilePath:      "billing/order_test.go",
		Line:          5,
		EndLine:       &end,
	}); err != nil {
		t.Fatalf("seed test symbol: %v", err)
	}

	// Both profiles are `-coverpkg=./...` shaped: each names the whole
	// codebase, so both carry the identical 9-statement unindexed file.
	stats, err := coverage.IngestGoProfilePerTest(ctx, s, coverage.RunMeta{Framework: store.FrameworkGoTest}, []coverage.PerTestProfile{
		{Test: "billing.TestTotal", Profile: strings.NewReader(gapProfile)},
		{Test: "billing.Total", Profile: strings.NewReader(gapProfile)},
	})
	if err != nil {
		t.Fatalf("IngestGoProfilePerTest: %v", err)
	}
	if stats.TestsIngested != 2 {
		t.Fatalf("TestsIngested = %d, want 2", stats.TestsIngested)
	}

	run, gaps := latestRunGaps(t, s, stats.RunID)
	if run.StmtsUnattributed != 11 {
		t.Errorf("stmts_unattributed = %d, want 11 — two tests seeing the same "+
			"blind spot is one blind spot, not two", run.StmtsUnattributed)
	}
	if run.FilesInReport != 2 || run.FilesMatched != 1 || run.FilesUnmatched != 1 {
		t.Errorf("file counters = %d/%d/%d, want in=2 matched=1 unmatched=1",
			run.FilesInReport, run.FilesMatched, run.FilesUnmatched)
	}
	if len(gaps) != 2 {
		t.Fatalf("persisted %d gap rows, want 2", len(gaps))
	}
	if gaps[0].Stmts != 9 {
		t.Errorf("largest gap = %d stmts, want 9 (not 18)", gaps[0].Stmts)
	}
}
