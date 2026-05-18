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
