package store

import (
	"context"
	"fmt"
	"testing"
)

// insertAttributedRun seeds a run carrying the accounting an ingest computes.
func insertAttributedRun(t *testing.T, s *Store, a CoverageRun) int64 {
	t.Helper()
	a.Framework = FrameworkGoTest
	id, err := s.Coverage().InsertRun(context.Background(), a)
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	return id
}

// The run-level counters are the headline of issue #100: without them the
// question "how much of what ran can atlas actually see?" is answerable only
// by re-running the ingest. They must survive a write/read round trip on
// every read path (GetRun and ListRuns both feed consumers).
func TestCoverageRun_AttributionRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	id := insertAttributedRun(t, s, CoverageRun{
		FilesInReport:     852,
		FilesMatched:      641,
		FilesUnmatched:    211,
		StmtsAttributed:   812004,
		StmtsUnattributed: 392327,
	})

	got, err := s.Coverage().GetRun(ctx, id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.FilesInReport != 852 || got.FilesMatched != 641 || got.FilesUnmatched != 211 {
		t.Errorf("file counters = %d/%d/%d, want 852/641/211",
			got.FilesInReport, got.FilesMatched, got.FilesUnmatched)
	}
	if got.StmtsAttributed != 812004 || got.StmtsUnattributed != 392327 {
		t.Errorf("stmt counters = %d/%d, want 812004/392327",
			got.StmtsAttributed, got.StmtsUnattributed)
	}

	runs, err := s.Coverage().ListRuns(ctx, FrameworkGoTest)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns returned %d runs, want 1", len(runs))
	}
	if runs[0].StmtsUnattributed != 392327 {
		t.Errorf("ListRuns dropped the accounting: stmts_unattributed = %d, want 392327",
			runs[0].StmtsUnattributed)
	}
}

// Gaps come back biggest-loss-first with ties broken by path, so a consumer
// paging the list sees a stable order rather than SQLite's row order.
func TestCoverageGaps_RoundTripIsOrderedAndDeterministic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	runID := insertAttributedRun(t, s, CoverageRun{})

	in := []CoverageGap{
		{Path: "b.go", Stmts: 10, Reason: "no-indexed-symbol"},
		{Path: "a.go", Stmts: 10, Reason: "outside-symbol-spans"},
		{Path: "c.go", Stmts: 99, Reason: "no-indexed-symbol"},
	}
	dropped, err := s.CoverageGaps().Insert(ctx, runID, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}

	got, err := s.CoverageGaps().List(ctx, runID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"c.go", "a.go", "b.go"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Path != w {
			t.Errorf("row %d path = %q, want %q", i, got[i].Path, w)
		}
	}
	if got[0].Stmts != 99 || got[0].Reason != "no-indexed-symbol" {
		t.Errorf("row 0 = %+v, want stmts=99 reason=no-indexed-symbol", got[0])
	}
}

// A truncated list that claims completeness is the exact failure mode this
// issue exists to fix: the cap must keep the biggest losses AND record how
// many files it dropped, on the run row where a consumer will see it.
func TestCoverageGaps_TruncationIsRecordedNotHidden(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	runID := insertAttributedRun(t, s, CoverageRun{})

	const overflow = 7
	in := make([]CoverageGap, 0, MaxRunGapRows+overflow)
	for i := 0; i < MaxRunGapRows+overflow; i++ {
		in = append(in, CoverageGap{
			Path:   fmt.Sprintf("pkg/f%04d.go", i),
			Stmts:  i + 1, // ascending, so the cap must sort before it cuts
			Reason: "no-indexed-symbol",
		})
	}
	dropped, err := s.CoverageGaps().Insert(ctx, runID, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if dropped != overflow {
		t.Errorf("dropped = %d, want %d", dropped, overflow)
	}

	got, err := s.CoverageGaps().List(ctx, runID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != MaxRunGapRows {
		t.Fatalf("stored %d gap rows, want the cap %d", len(got), MaxRunGapRows)
	}
	// The cap keeps the worst offenders, not an arbitrary prefix.
	if got[0].Stmts != MaxRunGapRows+overflow {
		t.Errorf("largest stored gap = %d stmts, want %d", got[0].Stmts, MaxRunGapRows+overflow)
	}

	run, err := s.Coverage().GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.GapsTruncated != overflow {
		t.Errorf("run.GapsTruncated = %d, want %d — a capped list that reports "+
			"zero truncation is indistinguishable from a complete one",
			run.GapsTruncated, overflow)
	}
}

// Re-ingesting the same run replaces its gap list rather than appending to it,
// so a retried ingest cannot leave two generations of rows interleaved.
func TestCoverageGaps_InsertReplacesPriorList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	runID := insertAttributedRun(t, s, CoverageRun{})

	if _, err := s.CoverageGaps().Insert(ctx, runID, []CoverageGap{
		{Path: "stale.go", Stmts: 5, Reason: "no-indexed-symbol"},
	}); err != nil {
		t.Fatalf("Insert #1: %v", err)
	}
	if _, err := s.CoverageGaps().Insert(ctx, runID, []CoverageGap{
		{Path: "fresh.go", Stmts: 3, Reason: "outside-symbol-spans"},
	}); err != nil {
		t.Fatalf("Insert #2: %v", err)
	}

	got, err := s.CoverageGaps().List(ctx, runID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Path != "fresh.go" {
		t.Fatalf("List = %+v, want only fresh.go", got)
	}
}

// The gap list describes one run and must not outlive it.
func TestCoverageGaps_CascadeWithRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	runID := insertAttributedRun(t, s, CoverageRun{})
	if _, err := s.CoverageGaps().Insert(ctx, runID, []CoverageGap{
		{Path: "a.go", Stmts: 1, Reason: "no-indexed-symbol"},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if _, err := s.sqlDB().ExecContext(ctx,
		`DELETE FROM coverage_runs WHERE id = ?`, runID); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	var n int
	if err := s.sqlDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM coverage_run_gaps WHERE run_id = ?`, runID).Scan(&n); err != nil {
		t.Fatalf("count gaps: %v", err)
	}
	if n != 0 {
		t.Errorf("%d gap row(s) survived the run delete, want 0", n)
	}
}

// An empty gap list is a legitimate result (everything was attributed) and
// must still clear whatever was there before without erroring.
func TestCoverageGaps_InsertEmptyClearsAndReportsZero(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	runID := insertAttributedRun(t, s, CoverageRun{})
	if _, err := s.CoverageGaps().Insert(ctx, runID, []CoverageGap{
		{Path: "a.go", Stmts: 1, Reason: "no-indexed-symbol"},
	}); err != nil {
		t.Fatalf("Insert #1: %v", err)
	}

	dropped, err := s.CoverageGaps().Insert(ctx, runID, nil)
	if err != nil {
		t.Fatalf("Insert(nil): %v", err)
	}
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	got, err := s.CoverageGaps().List(ctx, runID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %+v, want empty", got)
	}
}
