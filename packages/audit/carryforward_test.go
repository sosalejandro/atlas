package audit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// carryBase anchors every seeded run relative to the wall clock: run order is
// fixed by the offsets each test passes, while the ages stay inside the
// staleness window that the default carry policy applies against time.Now.
// Truncated to the second because finished_at is compared in SQL and the
// driver stores it as text.
var carryBase = time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

// stmtRow is one symbol's statement measurement inside a seeded run.
type stmtRow struct {
	covered int
	total   int
}

// seedFullStackFeature creates one capability with a Go implementation symbol
// and a front-end implementation symbol. This shape is the whole point of the
// test: the two symbols are measured by DIFFERENT CI jobs, so losing one job
// removes part of the feature's denominator rather than all of it.
func seedFullStackFeature(t *testing.T, s *store.Store, id shared.FeatureID) (goSym, feSym int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Features().Upsert(ctx, store.Feature{
		ID: id, Title: "Checkout", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("Upsert %q: %v", id, err)
	}
	insert := func(name, file string, line, endLine int) int64 {
		t.Helper()
		end := endLine
		sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(name),
			Kind:          shared.KindFunc,
			FilePath:      file,
			Line:          line,
			EndLine:       &end,
		})
		if err != nil {
			t.Fatalf("Insert symbol %q: %v", name, err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: id, SymbolID: sid,
			Role: store.RoleImpl, Source: store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("Link %q->%d: %v", id, sid, err)
		}
		return sid
	}
	goSym = insert("billing.Charge", "src/contexts/billing/charge.go", 10, 120)
	feSym = insert("billing.Checkout", "web/src/features/billing/checkout.ts", 1, 12)
	return goSym, feSym
}

// seedStmtRun writes one statement-coverage run (the Tier B shape: every
// result carries covered/total statements) finishing at carryBase+offset.
func seedStmtRun(
	t *testing.T,
	s *store.Store,
	framework store.Framework,
	group string,
	offset time.Duration,
	rows map[int64]stmtRow,
) int64 {
	t.Helper()
	finished := carryBase.Add(offset)
	run := store.CoverageRun{Framework: framework, StartedAt: finished, FinishedAt: finished}
	if group != "" {
		run.RunGroup = &group
	}
	results := make([]store.CoverageResult, 0, len(rows))
	for sid, r := range rows {
		v := sid
		status := store.StatusPass
		if r.covered == 0 {
			status = store.StatusFail
		}
		results = append(results, store.CoverageResult{
			SymbolID: &v, Status: status,
			CoveredStmts: r.covered, TotalStmts: r.total,
		})
	}
	id, err := s.Coverage().InsertRunWithResults(context.Background(), run, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults(%s, group=%q): %v", framework, group, err)
	}
	return id
}

func reasonsOf(t *testing.T, s *store.Store, id shared.FeatureID) []string {
	t.Helper()
	got, err := New(s, Options{}).ScoreFeature(context.Background(), id)
	if err != nil {
		t.Fatalf("ScoreFeature %q: %v", id, err)
	}
	return got.Reasons
}

// THE test for issue #136. A capability measured by two CI jobs loses one of
// them: the Go suite crashes, only the front-end sync lands under the build's
// run group. Nothing about the code changed, and nothing about the testing got
// better -- so the coverage reading must not go UP.
//
// Without carryforward it does exactly that: the line-weighted score sums only
// the symbols that appear in the frontier's results, so dropping the Go run
// drops 100 statements out of the denominator and the feature reads 100%
// on the strength of a 10-statement front-end file.
func TestAudit_Issue136_LostRunMustNotRaiseCoverage(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	goSym, feSym := seedFullStackFeature(t, s, feature)

	// Build 1: both jobs land under one run group.
	seedStmtRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute,
		map[int64]stmtRow{goSym: {covered: 5, total: 100}})
	seedStmtRun(t, s, store.FrameworkVitest, "ci-1", 2*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})

	before, ok := coverageOf(t, s, feature)
	if !ok {
		t.Fatal("build 1: coverage signal absent, want present")
	}
	if want := 100.0 * 15 / 110; !approxEqual(before, want) {
		t.Fatalf("build 1 coverage = %.2f, want %.2f (15 of 110 statements)", before, want)
	}

	// Build 2: the Go job crashed. Only the front-end sync lands.
	seedStmtRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})

	after, ok := coverageOf(t, s, feature)
	if !ok {
		t.Fatal("build 2: coverage signal absent, want present")
	}
	if after > before+1e-9 {
		t.Fatalf("coverage ROSE from %.2f to %.2f when the Go run vanished from the group; "+
			"testing went down, the number must not go up", before, after)
	}
	if !approxEqual(after, before) {
		t.Errorf("coverage = %.2f, want %.2f -- the unmeasured Go symbol should be carried, not dropped",
			after, before)
	}
}

// A number assembled from two builds must not present itself as one
// measurement: the reason string has to say how much of the reading is
// carried. FeatureHealth.Reasons is what `atlas health --json` emits, so this
// covers the "distinguishable in both the note and --json" acceptance.
func TestAudit_Issue136_NoteReportsCarriedShare(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	goSym, feSym := seedFullStackFeature(t, s, feature)

	seedStmtRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute,
		map[int64]stmtRow{goSym: {covered: 5, total: 100}})
	seedStmtRun(t, s, store.FrameworkVitest, "ci-1", 2*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})
	if joined := strings.Join(reasonsOf(t, s, feature), " | "); strings.Contains(joined, "carried") {
		t.Fatalf("build 1 reasons mention a carry with nothing to carry: %s", joined)
	}

	seedStmtRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})

	joined := strings.Join(reasonsOf(t, s, feature), " | ")
	if !strings.Contains(joined, "carried") {
		t.Errorf("reasons = %q, want a note saying part of the reading is carried", joined)
	}
	if !strings.Contains(joined, "observed") {
		t.Errorf("reasons = %q, want the observed-only fraction a CI gate should read", joined)
	}
}

// A symbol whose span moved is not the symbol that was measured. The scanner
// pins end_line (#120), so the carry can be refused on evidence rather than on
// faith -- and refusing it must not quietly restore the pre-#136 rise either:
// the symbol still holds its place in the denominator, uncovered.
func TestAudit_Issue136_MovedSpanIsNotCarriedAsEvidence(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	goSym, feSym := seedFullStackFeature(t, s, feature)

	seedStmtRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute,
		map[int64]stmtRow{goSym: {covered: 100, total: 100}})
	seedStmtRun(t, s, store.FrameworkVitest, "ci-1", 2*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})

	// The Go function is rewritten: same name, different span. A rescan
	// updates the row in place (ingest.upsertSymbolTx), keeping the id — so
	// the carry cannot be invalidated by the surrogate id changing, only by
	// the span.
	g := graph.New()
	moved := shared.Symbol{
		ID: "billing.Charge", Kind: shared.KindFunc,
		Position: shared.FilePosition{Path: "src/contexts/billing/charge.go", Line: 210},
		EndLine:  400,
	}
	g.AddNode(&graph.Node{Symbol: moved})
	idx := &codeindex.Index{
		Root: ".", Graph: g, Symbols: []shared.Symbol{moved},
		FileHashes: map[string]codeindex.FileHash{},
	}
	if _, err := s.Ingest(context.Background(), idx); err != nil {
		t.Fatalf("rescan the moved symbol: %v", err)
	}
	_ = goSym

	seedStmtRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})

	after, ok := coverageOf(t, s, feature)
	if !ok {
		t.Fatal("coverage signal absent, want present")
	}
	if after >= 100 {
		t.Errorf("coverage = %.2f, want < 100 -- a rewritten symbol's old 100%% must not be carried as evidence", after)
	}
}

// seedDynamicFeature links only the feature's TEST symbols, which is what
// makes the dynamic surface (#104) the tier that resolves its denominator: the
// symbol set comes from what those tests were observed to execute, not from a
// link table. Returns the two test symbols and the two impl symbols they run.
func seedDynamicFeature(t *testing.T, s *store.Store, id shared.FeatureID) (goTest, feTest, goImpl, feImpl int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Features().Upsert(ctx, store.Feature{
		ID: id, Title: "Checkout", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("Upsert %q: %v", id, err)
	}
	insert := func(name, file string, line, endLine int, role store.FeatureSymbolRole) int64 {
		t.Helper()
		end := endLine
		sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(name),
			Kind:          shared.KindFunc,
			FilePath:      file,
			Line:          line,
			EndLine:       &end,
		})
		if err != nil {
			t.Fatalf("Insert symbol %q: %v", name, err)
		}
		if role != "" {
			if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
				FeatureID: id, SymbolID: sid,
				Role: role, Source: store.SourceAnnotation,
			}); err != nil {
				t.Fatalf("Link %q->%d: %v", id, sid, err)
			}
		}
		return sid
	}
	goTest = insert("billing.TestCharge", "src/contexts/billing/charge_test.go", 5, 40, store.RoleTest)
	feTest = insert("billing.checkoutSpec", "web/src/features/billing/checkout.spec.ts", 3, 30, store.RoleTest)
	goImpl = insert("billing.Charge", "src/contexts/billing/charge.go", 10, 120, "")
	feImpl = insert("billing.Checkout", "web/src/features/billing/checkout.ts", 1, 12, "")
	return goTest, feTest, goImpl, feImpl
}

// seedExecutions writes the per-test execution evidence (schema 0010) the
// dynamic surface is derived from.
func seedExecutions(t *testing.T, s *store.Store, runID int64, rows []store.TestExecution) {
	t.Helper()
	if err := s.TestCoverage().Insert(context.Background(), runID, rows); err != nil {
		t.Fatalf("TestCoverage.Insert(run %d): %v", runID, err)
	}
}

// THE load-bearing test for issue #136 once the surface tiers are in play.
//
// The carry only helps if the symbol set the score is computed over survives
// the lost job. The preferred tier (SurfaceDynamic, #104) derives that set from
// the per-test evidence of the frontier's own runs -- so when the Go job dies,
// the set shrinks by exactly the symbols the Go job measured, and the carried
// results standing in for them are filtered straight back out. Coverage rises
// anyway, which is issue #136 with the fix applied and not firing.
func TestAudit_Issue136_LostRunMustNotRaiseCoverage_DynamicSurface(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	goTest, feTest, goImpl, feImpl := seedDynamicFeature(t, s, feature)

	// Build 1: both jobs land under one run group, each with its own evidence.
	goRun := seedStmtRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute,
		map[int64]stmtRow{goImpl: {covered: 5, total: 100}})
	seedExecutions(t, s, goRun, []store.TestExecution{
		{TestSymbolID: goTest, SymbolID: goImpl, CoveredStmts: 5, TotalStmts: 100},
	})
	feRun := seedStmtRun(t, s, store.FrameworkVitest, "ci-1", 2*time.Minute,
		map[int64]stmtRow{feImpl: {covered: 10, total: 10}})
	seedExecutions(t, s, feRun, []store.TestExecution{
		{TestSymbolID: feTest, SymbolID: feImpl, CoveredStmts: 10, TotalStmts: 10},
	})

	before, ok := coverageOf(t, s, feature)
	if !ok {
		t.Fatal("build 1: coverage signal absent, want present")
	}
	if want := 100.0 * 15 / 110; !approxEqual(before, want) {
		t.Fatalf("build 1 coverage = %.2f, want %.2f (15 of 110 statements)", before, want)
	}

	// Build 2: the Go job crashed. Only the front-end sync lands, so only the
	// front-end evidence is in the frontier.
	feRun2 := seedStmtRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute,
		map[int64]stmtRow{feImpl: {covered: 10, total: 10}})
	seedExecutions(t, s, feRun2, []store.TestExecution{
		{TestSymbolID: feTest, SymbolID: feImpl, CoveredStmts: 10, TotalStmts: 10},
	})

	after, ok := coverageOf(t, s, feature)
	if !ok {
		t.Fatal("build 2: coverage signal absent, want present")
	}
	if after > before+1e-9 {
		t.Fatalf("coverage ROSE from %.2f to %.2f when the Go run vanished from the group; "+
			"the dynamic surface shrank with it, so the carried Go symbol was filtered back out",
			before, after)
	}
	if !approxEqual(after, before) {
		t.Errorf("coverage = %.2f, want %.2f -- the carried Go symbol must stay in the denominator",
			after, before)
	}
}

// A carried result on a TEST symbol moves the score: it is what
// classifyCoverageResults reads as "the feature's test passed", which credits
// the feature under the gotest pass/fail model (#82). Counting only carries
// whose symbol is in `wanted` left that movement out of the "how much of this
// is carried" number entirely -- so a 100 assembled from another build's test
// run presented itself as this build's measurement, which is the one thing
// carryforward exists to prevent.
func TestAudit_Issue136_CarriedTestPassIsCountedInTheShare(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	ctx := context.Background()

	if err := s.Features().Upsert(ctx, store.Feature{
		ID: feature, Title: "Checkout", Kind: store.FeatureKindFeature,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	insert := func(name, file string, line, endLine int, role store.FeatureSymbolRole) int64 {
		t.Helper()
		end := endLine
		sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(name), Kind: shared.KindFunc,
			FilePath: file, Line: line, EndLine: &end,
		})
		if err != nil {
			t.Fatalf("Insert %q: %v", name, err)
		}
		if role != "" {
			if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
				FeatureID: feature, SymbolID: sid, Role: role, Source: store.SourceAnnotation,
			}); err != nil {
				t.Fatalf("Link %q: %v", name, err)
			}
		}
		return sid
	}
	// Only a TEST symbol is annotated -- the #82 shape, where a passing
	// annotated test IS the coverage signal. A test symbol is never in
	// `wanted`, so a carry on it is invisible to a share computed over
	// `wanted` alone.
	testSym := insert("billing.TestCharge", "src/contexts/billing/charge_test.go", 5, 40, store.RoleTest)
	otherSym := insert("shipping.Ship", "src/contexts/shipping/ship.go", 3, 60, "")

	// Build 1: the Go job reports the annotated test passing.
	seedStatusRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute, map[int64]store.CoverageStatus{
		testSym:  store.StatusPass,
		otherSym: store.StatusPass,
	})
	// Build 2: that job never landed. Only an unrelated symbol was measured,
	// so the test result is carried.
	seedStatusRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute, map[int64]store.CoverageStatus{
		otherSym: store.StatusPass,
	})

	score, ok := coverageOf(t, s, feature)
	if !ok {
		t.Fatal("coverage signal absent, want present -- the carried test pass is the signal")
	}
	if !approxEqual(score, 100) {
		t.Fatalf("coverage = %.2f, want 100 -- the carried test pass is what produces this "+
			"score; if it does not, this test is no longer about the share", score)
	}
	joined := strings.Join(reasonsOf(t, s, feature), " | ")
	if !strings.Contains(joined, "test result(s) carried") {
		t.Errorf("reasons = %q, want the carried TEST result counted in the share", joined)
	}
	if !strings.Contains(joined, "credit this feature") {
		t.Errorf("reasons = %q, want the note to say the carried test result is what moved the score", joined)
	}
}

// Resolution is a property of the frontier, not of the feature: it costs three
// grouped scans over the carry window, and ScoreAll paying that once per
// feature multiplies out on a repo with many capabilities.
func TestAudit_Issue136_CoveragePoolIsMemoisedPerFrontier(t *testing.T) {
	s := openTestStore(t)
	const feature shared.FeatureID = "billing.checkout"
	goSym, feSym := seedFullStackFeature(t, s, feature)
	seedStmtRun(t, s, store.FrameworkGoTest, "ci-1", 1*time.Minute,
		map[int64]stmtRow{goSym: {covered: 5, total: 100}})
	seedStmtRun(t, s, store.FrameworkVitest, "ci-2", 10*time.Minute,
		map[int64]stmtRow{feSym: {covered: 10, total: 10}})

	ctx := context.Background()
	a, ok := New(s, Options{}).(*auditImpl)
	if !ok {
		t.Fatal("New did not return *auditImpl")
	}
	frontier, err := a.latestCoverageFrontier(ctx)
	if err != nil {
		t.Fatalf("latestCoverageFrontier: %v", err)
	}
	first, err := a.resolveCoveragePool(ctx, frontier)
	if err != nil {
		t.Fatalf("resolveCoveragePool: %v", err)
	}
	if len(first.results) == 0 {
		t.Fatal("fixture produced no pooled results; the memoisation check would be vacuous")
	}

	// Poison the cache with a value no store read could return. A second call
	// for the SAME frontier must hand it back rather than re-scanning.
	a.covPool = coveragePool{}
	second, err := a.resolveCoveragePool(ctx, frontier)
	if err != nil {
		t.Fatalf("resolveCoveragePool (second): %v", err)
	}
	if len(second.results) != 0 {
		t.Errorf("the same frontier was resolved twice: got %d results, want the memoised (poisoned) 0",
			len(second.results))
	}

	// A DIFFERENT frontier must not be served the cached answer.
	other := frontier
	other.Newest = frontier.Newest + 1000
	third, err := a.resolveCoveragePool(ctx, other)
	if err != nil {
		t.Fatalf("resolveCoveragePool (other frontier): %v", err)
	}
	if len(third.results) == 0 {
		t.Error("a different frontier was served the cached pool; the key does not identify the frontier")
	}
}

// seedStatusRun writes a binary (no statement counts) coverage run, which is
// the shape the gotest pass/fail model reads.
func seedStatusRun(
	t *testing.T,
	s *store.Store,
	framework store.Framework,
	group string,
	offset time.Duration,
	statuses map[int64]store.CoverageStatus,
) int64 {
	t.Helper()
	finished := carryBase.Add(offset)
	run := store.CoverageRun{Framework: framework, StartedAt: finished, FinishedAt: finished}
	if group != "" {
		run.RunGroup = &group
	}
	results := make([]store.CoverageResult, 0, len(statuses))
	for sid, st := range statuses {
		v := sid
		results = append(results, store.CoverageResult{SymbolID: &v, Status: st})
	}
	id, err := s.Coverage().InsertRunWithResults(context.Background(), run, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults(%s, group=%q): %v", framework, group, err)
	}
	return id
}

func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
