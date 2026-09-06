package trend

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// ---------------------------------------------------------------------------
// Regression tests for the confirmed defects in `atlas trend` (issue #92).
// ---------------------------------------------------------------------------

// linkSymbol inserts a symbol and links it to a feature under the given role,
// returning the symbol's id so a coverage result can be keyed to it.
func linkSymbol(t *testing.T, s *store.Store, feature, name string, role store.FeatureSymbolRole) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(name),
		Kind:          shared.KindFunc,
		FilePath:      feature + ".go",
		Line:          1,
	})
	if err != nil {
		t.Fatalf("insert symbol %s: %v", name, err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: shared.FeatureID(feature),
		SymbolID:  id,
		Role:      role,
		Source:    store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("link symbol %s: %v", name, err)
	}
	return id
}

func upsertFeature(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.Features().Upsert(context.Background(), store.Feature{
		ID: shared.FeatureID(id), Title: id, Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("upsert feature %s: %v", id, err)
	}
}

// stmtResult builds one statement-carrying coverage result for a symbol.
func stmtResult(symbolID int64, covered, total int) store.CoverageResult {
	sid := symbolID
	return store.CoverageResult{
		SymbolID:     &sid,
		Status:       store.StatusPass,
		CoveredStmts: covered,
		TotalStmts:   total,
	}
}

// seedRun writes one coverage run with its results and returns the run id.
func seedRun(t *testing.T, s *store.Store, group string, finished time.Time, results []store.CoverageResult) int64 {
	t.Helper()
	run := store.CoverageRun{
		Framework:  store.FrameworkGoTest,
		StartedAt:  finished.Add(-time.Minute),
		FinishedAt: finished,
	}
	if group != "" {
		run.RunGroup = &group
	}
	id, err := s.Coverage().InsertRunWithResults(context.Background(), run, results)
	if err != nil {
		t.Fatalf("insert coverage run: %v", err)
	}
	return id
}

// --- Finding 1 -------------------------------------------------------------

// The docs promise the series records "only the coverage-backed component".
// Recording FeatureHealth.Score instead records the audit's re-normalised
// blend of coverage, annotation freshness, pattern compliance and contract
// drift — so a "coverage regression" gate fires on an annotation going stale,
// and a real coverage drop hides behind another component rising.
func TestCollect_RecordsTheCoverageComponentNotTheBlend(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "f.blend")
	linkSymbol(t, s, "f.blend", "f.blend.impl", store.RoleImpl)

	blended := audit.FeatureHealth{
		FeatureID: "f.blend",
		// The blend is respectable because three other signals are strong.
		Score: 90,
		Components: map[string]float64{
			audit.SignalVerification:      40,
			audit.SignalAnnotationFresh:   100,
			audit.SignalPatternCompliance: 100,
			audit.SignalContractDrift:     100,
		},
	}

	got, err := Collect(context.Background(), s,
		stubScorer{healths: []audit.FeatureHealth{blended}},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Features) != 1 {
		t.Fatalf("Features = %d, want 1", len(got.Features))
	}
	if got.Features[0].Score == nil {
		t.Fatal("feature score is nil; the audit produced a coverage component")
	}
	if *got.Features[0].Score != 40 {
		t.Errorf("recorded feature score = %v, want 40 (the coverage component, not the 90-point blend)",
			*got.Features[0].Score)
	}
	if got.Score == nil {
		t.Fatal("project score is nil")
	}
	if *got.Score != 40 {
		t.Errorf("recorded project score = %v, want 40", *got.Score)
	}
}

// --- Finding 2 -------------------------------------------------------------

// The denominator is a guard against "deleting a thousand untested lines
// raises the number without a single new test". The Tier B score is computed
// over STATEMENTS, so a denominator counted in symbols cannot see a
// statement-level deletion and the guard never fires.
func TestCollect_DenominatorIsStatementsWhenTheScoreIs(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "f.stmts")
	a := linkSymbol(t, s, "f.stmts", "f.stmts.a", store.RoleImpl)
	b := linkSymbol(t, s, "f.stmts", "f.stmts.b", store.RoleImpl)
	// A test-role link is a test, not something coverage is scored over. It
	// carries statements of its own and must not reach the denominator.
	tst := linkSymbol(t, s, "f.stmts", "f.stmts.Test", store.RoleTest)

	seedRun(t, s, "", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), []store.CoverageResult{
		stmtResult(a, 30, 100),
		stmtResult(b, 25, 50),
		stmtResult(tst, 9, 9),
	})

	health := audit.FeatureHealth{
		FeatureID:  "f.stmts",
		Score:      55,
		Components: map[string]float64{audit.SignalVerification: 55},
	}
	got, err := Collect(context.Background(), s,
		stubScorer{healths: []audit.FeatureHealth{health}},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Features[0].Denominator != 150 {
		t.Errorf("feature denominator = %d, want 150 statements (100 + 50; the test symbol's 9 do not count)",
			got.Features[0].Denominator)
	}
	if got.Denominator != 150 {
		t.Errorf("project denominator = %d, want 150", got.Denominator)
	}
}

// Deleting untested code is the hazard the denominator exists to catch: the
// score rises and the surface shrinks, and only a statement-unit denominator
// notices.
func TestCollect_DeletingUntestedStatementsMovesTheDenominator(t *testing.T) {
	before := measureOneFeature(t, []stmtSpec{{covered: 30, total: 100}, {covered: 0, total: 900}})
	after := measureOneFeature(t, []stmtSpec{{covered: 30, total: 100}})

	if before == after {
		t.Fatalf("denominator unchanged at %d after 900 untested statements were deleted", before)
	}
	if after != 100 || before != 1000 {
		t.Errorf("denominators = %d then %d, want 1000 then 100", before, after)
	}
}

type stmtSpec struct{ covered, total int }

// measureOneFeature builds a fresh store holding one feature whose impl
// symbols carry the given statement counts, and returns the denominator
// Collect records for it.
func measureOneFeature(t *testing.T, specs []stmtSpec) int64 {
	t.Helper()
	s := openStore(t)
	upsertFeature(t, s, "f.one")
	results := make([]store.CoverageResult, 0, len(specs))
	for i, spec := range specs {
		id := linkSymbol(t, s, "f.one", "f.one.s"+string(rune('a'+i)), store.RoleImpl)
		results = append(results, stmtResult(id, spec.covered, spec.total))
	}
	seedRun(t, s, "", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), results)

	got, err := Collect(context.Background(), s,
		stubScorer{healths: []audit.FeatureHealth{{
			FeatureID:  "f.one",
			Components: map[string]float64{audit.SignalVerification: 50},
		}}},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return got.Denominator
}

// With no statement data anywhere the coverage signal is a fraction of
// SYMBOLS, so the denominator has to be too — but still only the symbols the
// score is computed over, never the test-role rows.
func TestCollect_DenominatorFallsBackToScoredSymbols(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "f.passfail")
	linkSymbol(t, s, "f.passfail", "f.passfail.a", store.RoleImpl)
	linkSymbol(t, s, "f.passfail", "f.passfail.b", store.RoleImpl)
	linkSymbol(t, s, "f.passfail", "f.passfail.Test", store.RoleTest)

	got, err := Collect(context.Background(), s,
		stubScorer{healths: []audit.FeatureHealth{{
			FeatureID:  "f.passfail",
			Components: map[string]float64{audit.SignalVerification: 100},
		}}},
		CollectOptions{CommitSHA: "abc", MeasuredAt: day(0)})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Features[0].Denominator != 2 {
		t.Errorf("denominator = %d, want 2 scored symbols (the test-role link is not one)",
			got.Features[0].Denominator)
	}
}

// --- Finding 5 -------------------------------------------------------------

// Issue #92 asked for a series over tables that already exist. Without a
// backfill, `atlas trend` on a store with a year of coverage runs prints
// "no history recorded".
func TestBackfill_DerivesPointsFromExistingCoverageRuns(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	upsertFeature(t, s, "f.a")
	a := linkSymbol(t, s, "f.a", "f.a.impl", store.RoleImpl)

	seedRun(t, s, "sha-old", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), []store.CoverageResult{
		stmtResult(a, 20, 100),
	})
	seedRun(t, s, "sha-new", time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), []store.CoverageResult{
		stmtResult(a, 60, 100),
	})

	res, err := Backfill(ctx, s)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Added != 2 {
		t.Fatalf("Added = %d, want 2 (one per coverage run group)", res.Added)
	}

	page, err := s.History().List(ctx, store.HistoryFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Points) != 2 {
		t.Fatalf("series = %d points, want 2", len(page.Points))
	}
	// Oldest first, keyed by the run group — which CI sets to the commit sha.
	if page.Points[0].CommitSHA != "sha-old" || page.Points[1].CommitSHA != "sha-new" {
		t.Errorf("series = %q,%q; want sha-old,sha-new", page.Points[0].CommitSHA, page.Points[1].CommitSHA)
	}
	if page.Points[0].Score == nil || *page.Points[0].Score != 20 {
		t.Errorf("old point score = %v, want 20 (20 of 100 statements)", page.Points[0].Score)
	}
	if page.Points[1].Score == nil || *page.Points[1].Score != 60 {
		t.Errorf("new point score = %v, want 60", page.Points[1].Score)
	}
	if page.Points[1].Denominator != 100 {
		t.Errorf("denominator = %d, want 100 statements", page.Points[1].Denominator)
	}
	if page.Points[0].Note == nil {
		t.Error("a backfilled point carries no note saying so")
	}
}

// Backfill must never overwrite a point `atlas trend record` wrote: that one
// is the better measurement, and a second run of the command must be a no-op.
func TestBackfill_IsIdempotentAndNeverOverwritesARecordedPoint(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	upsertFeature(t, s, "f.a")
	a := linkSymbol(t, s, "f.a", "f.a.impl", store.RoleImpl)
	seedRun(t, s, "sha-one", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), []store.CoverageResult{
		stmtResult(a, 20, 100),
	})

	if _, err := s.History().Record(ctx, store.HistoryPoint{
		CommitSHA: "sha-one", MeasuredAt: day(0), Score: f64(77), Denominator: 100,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	res, err := Backfill(ctx, s)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Added != 0 || res.Skipped != 1 {
		t.Errorf("Added/Skipped = %d/%d, want 0/1", res.Added, res.Skipped)
	}
	got, err := s.History().Get(ctx, "sha-one")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Score == nil || *got.Score != 77 {
		t.Errorf("recorded point score = %v, want the recorded 77 left intact", got.Score)
	}

	// Twice is the same as once.
	second, err := Backfill(ctx, s)
	if err != nil {
		t.Fatalf("Backfill #2: %v", err)
	}
	if second.Added != 0 {
		t.Errorf("second Backfill added %d point(s), want 0", second.Added)
	}
}

// Runs sharing a run_group are ONE measurement — a polyglot repo syncs
// go-cover and istanbul separately and means one number. Two points would
// each see half the repo.
func TestBackfill_RunsInOneGroupAreOnePoint(t *testing.T) {
	s := openStore(t)
	upsertFeature(t, s, "f.a")
	a := linkSymbol(t, s, "f.a", "f.a.impl", store.RoleImpl)
	b := linkSymbol(t, s, "f.a", "f.a.other", store.RoleImpl)

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedRun(t, s, "sha-grouped", base, []store.CoverageResult{stmtResult(a, 50, 100)})
	seedRun(t, s, "sha-grouped", base.Add(time.Hour), []store.CoverageResult{stmtResult(b, 100, 100)})

	res, err := Backfill(context.Background(), s)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if res.Added != 1 {
		t.Fatalf("Added = %d, want 1 point for the two runs sharing a group", res.Added)
	}
	point := res.Points[0]
	if point.Denominator != 200 {
		t.Errorf("denominator = %d, want 200 (both runs pooled)", point.Denominator)
	}
	if point.Score == nil || *point.Score != 75 {
		t.Errorf("score = %v, want 75 (150 of 200 statements)", point.Score)
	}
}
