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

// -----------------------------------------------------------------------------
// Run groups (issue #86)
// -----------------------------------------------------------------------------

// seedGroupedRun inserts one run finishing at `finished` with an optional run
// group ("" means ungrouped) plus one passing result per symbol id.
func seedGroupedRun(t *testing.T, cov Coverage, fw Framework, group string, finished time.Time, symbols ...int64) int64 {
	t.Helper()
	run := CoverageRun{Framework: fw, StartedAt: finished, FinishedAt: finished}
	if group != "" {
		run.RunGroup = &group
	}
	results := make([]CoverageResult, 0, len(symbols))
	for _, sid := range symbols {
		v := sid
		results = append(results, CoverageResult{SymbolID: &v, Status: StatusPass})
	}
	id, err := cov.InsertRunWithResults(context.Background(), run, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults(group=%q): %v", group, err)
	}
	return id
}

func seedCoverageSymbol(t *testing.T, s *Store, qn shared.SymbolID, path string) int64 {
	t.Helper()
	id, err := s.Symbols().Insert(context.Background(), SymbolRow{
		QualifiedName: qn, Kind: shared.KindFunc, FilePath: path, Line: 1,
	})
	if err != nil {
		t.Fatalf("Symbols Insert %q: %v", qn, err)
	}
	return id
}

func TestCoverage_RunGroupRoundTrip(t *testing.T) {
	cov := openTestStore(t).Coverage()
	ctx := context.Background()
	now := time.Now().UTC()

	group := "ci-abc123"
	groupedID, err := cov.InsertRun(ctx, CoverageRun{
		Framework: FrameworkGoTest, StartedAt: now, FinishedAt: now, RunGroup: &group,
	})
	if err != nil {
		t.Fatalf("InsertRun grouped: %v", err)
	}
	plainID, err := cov.InsertRun(ctx, CoverageRun{
		Framework: FrameworkVitest, StartedAt: now, FinishedAt: now,
	})
	if err != nil {
		t.Fatalf("InsertRun ungrouped: %v", err)
	}

	got, err := cov.GetRun(ctx, groupedID)
	if err != nil {
		t.Fatalf("GetRun grouped: %v", err)
	}
	if got.RunGroup == nil || *got.RunGroup != group {
		t.Errorf("grouped RunGroup = %v, want %q", got.RunGroup, group)
	}

	got, err = cov.GetRun(ctx, plainID)
	if err != nil {
		t.Fatalf("GetRun ungrouped: %v", err)
	}
	if got.RunGroup != nil {
		t.Errorf("ungrouped RunGroup = %q, want nil", *got.RunGroup)
	}
}

// An empty group string must persist as NULL. Otherwise a caller that hands us
// a pointer to "" opens a group named "" that every other blank-group run
// silently joins — the opposite of the isolation groups exist to provide.
func TestCoverage_RunGroupEmptyStringIsNull(t *testing.T) {
	cov := openTestStore(t).Coverage()
	ctx := context.Background()
	now := time.Now().UTC()

	blank := ""
	id, err := cov.InsertRun(ctx, CoverageRun{
		Framework: FrameworkGoTest, StartedAt: now, FinishedAt: now, RunGroup: &blank,
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	got, err := cov.GetRun(ctx, id)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.RunGroup != nil {
		t.Errorf("RunGroup = %q, want nil (an empty group must not open a group)", *got.RunGroup)
	}
}

func TestCoverage_LatestFrontier(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	type runSpec struct {
		framework Framework
		group     string
		minute    int
	}
	tests := []struct {
		name        string
		runs        []runSpec
		wantGroup   string
		wantMinutes []int
	}{
		{
			name: "no runs at all",
		},
		{
			name: "ungrouped runs fall back to the newest single run",
			runs: []runSpec{
				{FrameworkGoTest, "", 1},
				{FrameworkVitest, "", 3},
				{FrameworkPlaywright, "", 2},
			},
			wantMinutes: []int{3},
		},
		{
			name: "newest run grouped pulls in its siblings",
			runs: []runSpec{
				{FrameworkGoTest, "", 0},
				{FrameworkGoTest, "ci-a", 1},
				{FrameworkVitest, "ci-a", 2},
			},
			wantGroup:   "ci-a",
			wantMinutes: []int{1, 2},
		},
		{
			name: "a newer group supersedes the older one entirely",
			runs: []runSpec{
				{FrameworkGoTest, "ci-a", 1},
				{FrameworkVitest, "ci-a", 2},
				{FrameworkGoTest, "ci-b", 3},
			},
			wantGroup:   "ci-b",
			wantMinutes: []int{3},
		},
		{
			name: "an ungrouped run newer than the group is its own frontier",
			runs: []runSpec{
				{FrameworkGoTest, "ci-a", 1},
				{FrameworkVitest, "ci-a", 2},
				{FrameworkPlaywright, "", 3},
			},
			wantMinutes: []int{3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cov := openTestStore(t).Coverage()
			for _, r := range tc.runs {
				seedGroupedRun(t, cov, r.framework, r.group, base.Add(time.Duration(r.minute)*time.Minute))
			}

			front, err := cov.LatestFrontier(context.Background())
			if err != nil {
				t.Fatalf("LatestFrontier: %v", err)
			}
			switch {
			case tc.wantGroup == "" && front.Group != nil:
				t.Errorf("Group = %q, want nil", *front.Group)
			case tc.wantGroup != "" && (front.Group == nil || *front.Group != tc.wantGroup):
				t.Errorf("Group = %v, want %q", front.Group, tc.wantGroup)
			}
			if len(front.Runs) != len(tc.wantMinutes) {
				t.Fatalf("len(Runs) = %d, want %d", len(front.Runs), len(tc.wantMinutes))
			}
			gotMinutes := make(map[int]bool, len(front.Runs))
			for _, r := range front.Runs {
				gotMinutes[int(r.FinishedAt.Sub(base).Minutes())] = true
			}
			for _, m := range tc.wantMinutes {
				if !gotMinutes[m] {
					t.Errorf("frontier missing the run finishing at base+%dm (got %v)", m, gotMinutes)
				}
			}
			if front.Empty() != (len(tc.wantMinutes) == 0) {
				t.Errorf("Empty() = %v, want %v", front.Empty(), len(tc.wantMinutes) == 0)
			}
		})
	}
}

// The point of a group: every run in it contributes to one pool of results, so
// a Go run and a frontend run ingested under one group are read together.
func TestCoverage_ListFrontierResults_UnionsGroupedRuns(t *testing.T) {
	s := openTestStore(t)
	cov := s.Coverage()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	goSym := seedCoverageSymbol(t, s, "svc.Charge", "src/billing/charge.go")
	feSym := seedCoverageSymbol(t, s, "ui.Checkout", "web/src/checkout.ts")
	staleSym := seedCoverageSymbol(t, s, "svc.Stale", "src/legacy/stale.go")

	seedGroupedRun(t, cov, FrameworkGoTest, "", base, staleSym)
	goRun := seedGroupedRun(t, cov, FrameworkGoTest, "ci-a", base.Add(time.Minute), goSym)
	feRun := seedGroupedRun(t, cov, FrameworkVitest, "ci-a", base.Add(2*time.Minute), feSym)

	front, err := cov.LatestFrontier(ctx)
	if err != nil {
		t.Fatalf("LatestFrontier: %v", err)
	}
	results, err := cov.ListFrontierResults(ctx, front)
	if err != nil {
		t.Fatalf("ListFrontierResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 (one per grouped run)", len(results))
	}
	// Deterministic order: run id, then result id.
	if results[0].RunID != goRun || results[1].RunID != feRun {
		t.Errorf("run ids = [%d %d], want [%d %d] (ordered by run id)",
			results[0].RunID, results[1].RunID, goRun, feRun)
	}
	for _, r := range results {
		if r.SymbolID != nil && *r.SymbolID == staleSym {
			t.Error("frontier leaked a result from the ungrouped run outside the group")
		}
	}
}

func TestCoverage_ListFrontierResults_EmptyFrontier(t *testing.T) {
	cov := openTestStore(t).Coverage()
	results, err := cov.ListFrontierResults(context.Background(), CoverageFrontier{})
	if err != nil {
		t.Fatalf("ListFrontierResults: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}
