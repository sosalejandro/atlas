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
// carried. FeatureHealth.Reasons is what `atlas audit --json` emits, so this
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

func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
