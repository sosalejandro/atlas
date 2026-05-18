package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

func TestCoverage_InsertRunAndResults(t *testing.T) {
	s := openTestStore(t)
	cov := s.Coverage()
	ctx := context.Background()

	// FK on coverage_results.feature_id requires the feature to exist.
	if err := s.Features().Upsert(ctx, Feature{ID: "auth.login", Title: "Login"}); err != nil {
		t.Fatalf("Features Upsert: %v", err)
	}

	runID, err := cov.InsertRun(ctx, CoverageRun{
		Framework:   FrameworkGoTest,
		StartedAt:   time.Now().UTC().Add(-1 * time.Minute),
		FinishedAt:  time.Now().UTC(),
		SummaryJSON: `{"pass":10,"fail":1}`,
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	if runID == 0 {
		t.Fatal("InsertRun returned 0 id")
	}

	feat := shared.FeatureID("auth.login")
	if err := cov.InsertResults(ctx, runID, []CoverageResult{
		{Status: StatusPass, DurationMS: 12, FeatureID: &feat},
		{Status: StatusFail, DurationMS: 30, Message: mustPtr("boom")},
		{Status: StatusSkip, DurationMS: 0},
	}); err != nil {
		t.Fatalf("InsertResults: %v", err)
	}

	results, err := cov.ListResults(ctx, runID)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	if results[0].FeatureID == nil || *results[0].FeatureID != "auth.login" {
		t.Errorf("first result FeatureID = %+v, want auth.login", results[0].FeatureID)
	}
	if results[1].Message == nil || *results[1].Message != "boom" {
		t.Errorf("second result Message = %+v, want \"boom\"", results[1].Message)
	}
}

func TestCoverage_ListRunsFilter(t *testing.T) {
	cov := openTestStore(t).Coverage()
	ctx := context.Background()

	now := time.Now().UTC()
	_, _ = cov.InsertRun(ctx, CoverageRun{Framework: FrameworkGoTest, StartedAt: now, FinishedAt: now})
	_, _ = cov.InsertRun(ctx, CoverageRun{Framework: FrameworkPlaywright, StartedAt: now, FinishedAt: now})
	_, _ = cov.InsertRun(ctx, CoverageRun{Framework: FrameworkGoTest, StartedAt: now, FinishedAt: now})

	runs, err := cov.ListRuns(ctx, FrameworkGoTest)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Errorf("ListRuns(go-test) len = %d, want 2", len(runs))
	}
}

func TestCoverage_GetRunMissing(t *testing.T) {
	_, err := openTestStore(t).Coverage().GetRun(context.Background(), 9999)
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("GetRun(9999) err = %v, want ErrNotFound", err)
	}
}

func TestCoverage_InsertRunWithResults_Atomic(t *testing.T) {
	s := openTestStore(t)
	cov := s.Coverage()
	ctx := context.Background()

	// Required by the FK on coverage_results.feature_id.
	if err := s.Features().Upsert(ctx, Feature{ID: "auth.login", Title: "Login"}); err != nil {
		t.Fatalf("Features Upsert: %v", err)
	}

	feat := shared.FeatureID("auth.login")
	runID, err := cov.InsertRunWithResults(ctx,
		CoverageRun{
			Framework:   FrameworkGoTest,
			StartedAt:   time.Now().UTC().Add(-1 * time.Minute),
			FinishedAt:  time.Now().UTC(),
			SummaryJSON: `{"pass":2,"fail":1}`,
		},
		[]CoverageResult{
			{Status: StatusPass, DurationMS: 11, FeatureID: &feat},
			{Status: StatusFail, DurationMS: 27, Message: mustPtr("boom")},
			{Status: StatusSkip, DurationMS: 0},
		},
	)
	if err != nil {
		t.Fatalf("InsertRunWithResults: %v", err)
	}
	if runID == 0 {
		t.Fatal("InsertRunWithResults returned 0 id")
	}

	got, err := cov.ListResults(ctx, runID)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(got))
	}
}

// Atomicity: a bad result in the batch must roll back the run insert too.
// Otherwise a crash mid-ingest could leave an orphan coverage_runs row.
func TestCoverage_InsertRunWithResults_RollsBackRunOnResultError(t *testing.T) {
	s := openTestStore(t)
	cov := s.Coverage()
	ctx := context.Background()

	before, err := cov.ListRuns(ctx, FrameworkGoTest)
	if err != nil {
		t.Fatalf("ListRuns before: %v", err)
	}

	_, err = cov.InsertRunWithResults(ctx,
		CoverageRun{Framework: FrameworkGoTest},
		[]CoverageResult{
			{Status: StatusPass, DurationMS: 1},
			// Empty status fails validation inside the tx — should roll back the run too.
			{Status: "", DurationMS: 1},
		},
	)
	if err == nil {
		t.Fatal("InsertRunWithResults: expected error from empty status, got nil")
	}

	after, err := cov.ListRuns(ctx, FrameworkGoTest)
	if err != nil {
		t.Fatalf("ListRuns after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("ListRuns len after = %d, before = %d — orphan coverage_runs row leaked through rollback", len(after), len(before))
	}
}
