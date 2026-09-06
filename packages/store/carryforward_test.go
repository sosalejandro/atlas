package store

import (
	"context"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// carryClock is the fixed "now" every carry test scores against, so a
// staleness bound is exercised by the offsets in the table and never by how
// long the test took to run.
var carryClock = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func carryOpts(o CarryOptions) CarryOptions {
	o.Now = func() time.Time { return carryClock }
	return o
}

// carrySeedSymbol inserts a symbol with a pinned span. end_line is what the
// carry validity check compares against (#120 pinned it for Go), so every
// fixture sets it.
func carrySeedSymbol(t *testing.T, s *Store, name, file string, line, endLine int) int64 {
	t.Helper()
	end := endLine
	id, err := s.Symbols().Insert(context.Background(), SymbolRow{
		QualifiedName: shared.SymbolID(name),
		Kind:          shared.KindFunc,
		FilePath:      file,
		Line:          line,
		EndLine:       &end,
	})
	if err != nil {
		t.Fatalf("insert symbol %q: %v", name, err)
	}
	return id
}

type carryStmt struct {
	covered int
	total   int
	status  CoverageStatus
}

// carrySeedRun writes one run finishing at carryClock-ago, so "ago" reads as
// the measurement's age at scoring time.
func carrySeedRun(t *testing.T, s *Store, group string, ago time.Duration, rows map[int64][]carryStmt) int64 {
	t.Helper()
	finished := carryClock.Add(-ago)
	run := CoverageRun{Framework: FrameworkGoTest, StartedAt: finished, FinishedAt: finished}
	if group != "" {
		run.RunGroup = &group
	}
	results := []CoverageResult{}
	for sid, blocks := range rows {
		v := sid
		for _, b := range blocks {
			st := b.status
			if st == "" {
				st = StatusPass
			}
			results = append(results, CoverageResult{
				SymbolID: &v, Status: st,
				CoveredStmts: b.covered, TotalStmts: b.total,
			})
		}
	}
	id, err := s.Coverage().InsertRunWithResults(context.Background(), run, results)
	if err != nil {
		t.Fatalf("insert run (group=%q): %v", group, err)
	}
	return id
}

func resolveLatest(t *testing.T, s *Store, opts CarryOptions) ResolvedCoverage {
	t.Helper()
	ctx := context.Background()
	f, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		t.Fatalf("LatestFrontier: %v", err)
	}
	got, err := s.CoverageCarry().Resolve(ctx, f, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return got
}

func carryFor(t *testing.T, r ResolvedCoverage, symbolID int64) CarriedResult {
	t.Helper()
	c, ok := r.CarriedBySymbol()[symbolID]
	if !ok {
		t.Fatalf("symbol %d was not carried; carried set = %d entries", symbolID, len(r.Carried))
	}
	return c
}

// The base case: a build that did not measure a symbol inherits the previous
// build's reading for it, intact and marked as evidence.
func TestCarry_InheritsUnmeasuredSymbolFromPreviousBuild(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 90*time.Minute, map[int64][]carryStmt{goSym: {{covered: 5, total: 100}}})
	carrySeedRun(t, s, "ci-1", 89*time.Minute, map[int64][]carryStmt{feSym: {{covered: 10, total: 10}}})
	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 10, total: 10}}})

	got := resolveLatest(t, s, carryOpts(CarryOptions{}))
	if len(got.Carried) != 1 {
		t.Fatalf("carried %d results, want 1 (the Go symbol the second build never measured)", len(got.Carried))
	}
	c := carryFor(t, got, goSym)
	if c.Mode != CarryEvidence || c.Reason != CarryFresh {
		t.Errorf("mode/reason = %s/%s, want %s/%s", c.Mode, c.Reason, CarryEvidence, CarryFresh)
	}
	if c.CoveredStmts != 5 || c.TotalStmts != 100 {
		t.Errorf("carried statements = %d/%d, want 5/100", c.CoveredStmts, c.TotalStmts)
	}
	if c.Status != StatusPass {
		t.Errorf("status = %q, want %q", c.Status, StatusPass)
	}
	if c.FromGroup != "ci-1" || c.BuildsBack != 1 {
		t.Errorf("source = %q %d builds back, want ci-1 1", c.FromGroup, c.BuildsBack)
	}
	if len(got.Pool()) != len(got.Observed)+1 {
		t.Errorf("pool = %d results, want observed(%d)+1", len(got.Pool()), len(got.Observed))
	}
}

// A symbol measured by several result rows in one run (one per coverprofile
// block) must be carried as the SUM. Carrying one block would understate the
// denominator, which is the same defect in miniature.
func TestCarry_SumsMultipleResultRowsForOneSymbol(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 90*time.Minute, map[int64][]carryStmt{goSym: {
		{covered: 2, total: 10, status: StatusFail},
		{covered: 8, total: 20, status: StatusPass},
	}})
	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	c := carryFor(t, resolveLatest(t, s, carryOpts(CarryOptions{})), goSym)
	if c.CoveredStmts != 10 || c.TotalStmts != 30 {
		t.Errorf("carried statements = %d/%d, want 10/30 (both blocks)", c.CoveredStmts, c.TotalStmts)
	}
	if c.Status != StatusPass {
		t.Errorf("status = %q, want %q (any passing block covers the symbol)", c.Status, StatusPass)
	}
}

// Beyond the build window the measurement stops being evidence, but the symbol
// keeps its place in the denominator: zero credit, last known size. Dropping it
// instead is what made coverage rise in the first place.
func TestCarry_BeyondBuildWindowHoldsDenominatorOnly(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 300*time.Minute, map[int64][]carryStmt{goSym: {{covered: 100, total: 100}}})
	for i, group := range []string{"ci-2", "ci-3", "ci-4"} {
		carrySeedRun(t, s, group, time.Duration(200-i*50)*time.Minute,
			map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})
	}
	carrySeedRun(t, s, "ci-5", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	c := carryFor(t, resolveLatest(t, s, carryOpts(CarryOptions{MaxBuilds: 2})), goSym)
	if c.Mode != CarryDenominator || c.Reason != CarryStale {
		t.Errorf("mode/reason = %s/%s, want %s/%s", c.Mode, c.Reason, CarryDenominator, CarryStale)
	}
	if c.CoveredStmts != 0 {
		t.Errorf("covered = %d, want 0 -- a stale carry credits nothing", c.CoveredStmts)
	}
	if c.TotalStmts != 100 {
		t.Errorf("total = %d, want 100 -- the symbol still holds its place", c.TotalStmts)
	}
	if c.Status != StatusFail {
		t.Errorf("status = %q, want %q (in the denominator, not in the numerator)", c.Status, StatusFail)
	}
}

// The wall-clock bound is the backstop for repos that build rarely: one build
// back is still too old when that build ran last month.
func TestCarry_BeyondMaxAgeHoldsDenominatorOnly(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 20*24*time.Hour, map[int64][]carryStmt{goSym: {{covered: 100, total: 100}}})
	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	c := carryFor(t, resolveLatest(t, s, carryOpts(CarryOptions{})), goSym)
	if c.BuildsBack != 1 {
		t.Fatalf("builds back = %d, want 1 -- the build bound must not be what rejected this", c.BuildsBack)
	}
	if c.Mode != CarryDenominator || c.Reason != CarryStale {
		t.Errorf("mode/reason = %s/%s, want %s/%s", c.Mode, c.Reason, CarryDenominator, CarryStale)
	}
}

// Nothing at all is looked at beyond the lookback horizon: the symbol is not
// carried, because a statement count from a quarter ago describes code the
// repo has moved on from.
func TestCarry_NothingBeyondLookbackHorizon(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", carryLookbackHorizon+time.Hour, map[int64][]carryStmt{goSym: {{covered: 9, total: 9}}})
	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	got := resolveLatest(t, s, carryOpts(CarryOptions{}))
	if len(got.Carried) != 0 {
		t.Errorf("carried %d results, want 0 beyond the %s horizon", len(got.Carried), carryLookbackHorizon)
	}
}

// A symbol whose span moved is not the symbol that was measured. The span
// snapshot (schema 0017) is what turns that from an assumption into a check.
func TestCarry_MovedSpanIsNotEvidence(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 90*time.Minute, map[int64][]carryStmt{goSym: {{covered: 100, total: 100}}})

	// A rescan rewrites the function: same qualified name and surrogate id,
	// different span (see ingest.upsertSymbolTx).
	moved := shared.Symbol{
		ID: "billing.Charge", Kind: shared.KindFunc,
		Position: shared.FilePosition{Path: "src/billing/charge.go", Line: 210},
		EndLine:  400,
	}
	g := graph.New()
	g.AddNode(&graph.Node{Symbol: moved})
	if _, err := s.Ingest(context.Background(), &codeindex.Index{
		Root: ".", Graph: g, Symbols: []shared.Symbol{moved},
		FileHashes: map[string]codeindex.FileHash{},
	}); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	c := carryFor(t, resolveLatest(t, s, carryOpts(CarryOptions{})), goSym)
	if c.Mode != CarryDenominator || c.Reason != CarrySpanChanged {
		t.Errorf("mode/reason = %s/%s, want %s/%s", c.Mode, c.Reason, CarryDenominator, CarrySpanChanged)
	}
	if c.CoveredStmts != 0 {
		t.Errorf("covered = %d, want 0 -- the old 100%% describes code that is gone", c.CoveredStmts)
	}
}

// An ungrouped frontier carries nothing. Without a run group there is no
// notion of a build, so there is no previous build to inherit from, and every
// store ingested before run groups keeps its old reading exactly.
func TestCarry_UngroupedFrontierCarriesNothing(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 90*time.Minute, map[int64][]carryStmt{goSym: {{covered: 5, total: 100}}})
	carrySeedRun(t, s, "", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	got := resolveLatest(t, s, carryOpts(CarryOptions{}))
	if len(got.Carried) != 0 {
		t.Errorf("carried %d results, want 0 for an ungrouped frontier", len(got.Carried))
	}
}

// The off switch has to produce exactly the pre-#136 reading, or there is no
// way to tell whether carryforward is what moved a number.
func TestCarry_DisabledReturnsFrontierOnly(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := carrySeedSymbol(t, s, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	carrySeedRun(t, s, "ci-1", 90*time.Minute, map[int64][]carryStmt{goSym: {{covered: 5, total: 100}}})
	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{feSym: {{covered: 1, total: 1}}})

	got := resolveLatest(t, s, carryOpts(CarryOptions{Disabled: true}))
	if len(got.Carried) != 0 {
		t.Fatalf("carried %d results with carryforward disabled, want 0", len(got.Carried))
	}
	if len(got.Pool()) != len(got.Observed) {
		t.Errorf("pool = %d, want the %d observed results unchanged", len(got.Pool()), len(got.Observed))
	}
}

// A symbol the current build DID measure is never carried, whatever an older
// build said about it -- the whole mechanism is about absence.
func TestCarry_MeasuredSymbolIsNeverCarried(t *testing.T) {
	s := openTestStore(t)
	goSym := carrySeedSymbol(t, s, "billing.Charge", "src/billing/charge.go", 10, 120)

	carrySeedRun(t, s, "ci-1", 90*time.Minute, map[int64][]carryStmt{goSym: {{covered: 100, total: 100}}})
	carrySeedRun(t, s, "ci-2", 10*time.Minute, map[int64][]carryStmt{goSym: {{covered: 1, total: 100}}})

	got := resolveLatest(t, s, carryOpts(CarryOptions{}))
	if len(got.Carried) != 0 {
		t.Errorf("carried %d results, want 0 -- this build measured the symbol itself", len(got.Carried))
	}
}
