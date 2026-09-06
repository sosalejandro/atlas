package coverage

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// PerTestProfile is one test's own coverage profile: what the codebase
// executed while that single test ran.
type PerTestProfile struct {
	// Test is the qualified name of the test symbol, exactly as the scanner
	// indexed it (e.g. "billing.TestCheckout_Idempotent"). A name atlas has
	// no symbol for is reported as unresolved rather than silently dropped —
	// the whole point of this ingest is that nothing goes missing quietly.
	Test shared.SymbolID
	// Profile is that test's coverprofile.
	Profile io.Reader
}

// PerTestIngestStats summarises a per-test ingest.
type PerTestIngestStats struct {
	RunID int64
	// TestsIngested / TestsUnresolved split the profiles by whether their
	// test name resolved to an indexed symbol.
	TestsIngested   int
	TestsUnresolved []shared.SymbolID
	// Rows is the number of (test, symbol) execution pairs written.
	Rows int
	// SymbolsExecuted is the size of the union across all tests — the same
	// number a whole-run ingest would report.
	SymbolsExecuted int
	// StmtsUnattributed is the statements no symbol claimed, unioned across
	// profiles (issue #85's accounting). Union rather than sum: every
	// per-test profile names the whole codebase, so a file atlas cannot
	// index shows up once per test and summing would report a blind spot
	// as many times too large as there are tests.
	StmtsUnattributed int
	// Gaps enumerates unattributed execution across all profiles.
	Gaps []FileGap
}

// accumulate folds one test's attribution into the run-wide union and returns
// that test's execution rows.
//
// The two outputs deliberately disagree about zero-coverage symbols. The union
// keeps EVERY symbol the profile mentions, including ones no statement of
// which ran: a function no test ever exercised must stay in the denominator,
// or coverage would improve as testing got worse — the failure mode issue #85
// was about. Per-test evidence excludes them, because "the test compiled this
// symbol in" is not evidence of a relationship, and recording it would drag
// every package a test merely imports into that feature's surface.
func accumulate(rep attributionReport, testID int64, union map[int64]symbolCounts) []store.TestExecution {
	rows := make([]store.TestExecution, 0, len(rep.counts))
	for _, sid := range sortedCountKeys(rep.counts) {
		c := rep.counts[sid]
		u := union[sid]
		if c.covered > u.covered {
			u.covered = c.covered
		}
		if c.total > u.total {
			u.total = c.total
		}
		union[sid] = u

		if c.covered == 0 {
			continue
		}
		rows = append(rows, store.TestExecution{
			TestSymbolID: testID,
			SymbolID:     sid,
			CoveredStmts: c.covered,
			TotalStmts:   c.total,
		})
	}
	return rows
}

// IngestGoProfilePerTest records, for every test, which production symbols
// that test executed — the evidence a feature's implementation surface is
// derived from (issue #104).
//
// This is the dynamic feature-location technique: rather than walking call
// edges out of an annotated test and hoping the scanner resolved them, it
// observes what the test actually ran. Interface dispatch, DI containers,
// reflection and string-routed handlers are all invisible to a static walk
// and all perfectly visible here.
//
// It also writes the ordinary union run (`coverage_results`), so a store that
// has per-test evidence is a strict superset of one that does not and every
// existing consumer keeps working unchanged.
func IngestGoProfilePerTest(
	ctx context.Context,
	s *store.Store,
	framework store.Framework,
	profiles []PerTestProfile,
) (PerTestIngestStats, error) {
	var stats PerTestIngestStats

	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return stats, fmt.Errorf("coverage: list symbols: %w", err)
	}
	byFile := indexSymbolsByFile(syms)
	testIDs := make(map[shared.SymbolID]int64, len(syms))
	for _, sym := range syms {
		testIDs[sym.QualifiedName] = sym.ID
	}

	// union accumulates the max coverage seen per symbol across every test,
	// which is what the whole-run view means: a statement that ran under any
	// test ran. Summing would double-count shared code.
	union := map[int64]symbolCounts{}
	perTest := make([]store.TestExecution, 0, len(profiles)*32)
	merged := newAttributionReport()

	for _, p := range profiles {
		testID, ok := testIDs[p.Test]
		if !ok {
			stats.TestsUnresolved = append(stats.TestsUnresolved, p.Test)
			continue
		}
		blocks, err := gocover.Parse(p.Profile)
		if err != nil {
			return stats, fmt.Errorf("coverage: parse profile for %s: %w", p.Test, err)
		}
		rep := attributeStatements(gocover.BlocksByFile(gocover.MergeBlocks(blocks)), byFile)
		stats.TestsIngested++
		merged.merge(rep)
		perTest = append(perTest, accumulate(rep, testID, union)...)
	}

	stats.Rows = len(perTest)
	stats.SymbolsExecuted = len(union)
	stats.StmtsUnattributed = merged.stmtsUnattributed
	stats.Gaps = merged.gaps()

	results := make([]store.CoverageResult, 0, len(union))
	for _, sid := range sortedCountKeys(union) {
		v := sid
		c := union[sid]
		status := store.StatusFail
		if c.covered > 0 {
			status = store.StatusPass
		}
		results = append(results, store.CoverageResult{
			SymbolID: &v, Status: status,
			CoveredStmts: c.covered, TotalStmts: c.total,
		})
	}
	now := time.Now().UTC()
	runID, err := s.Coverage().InsertRunWithResults(ctx, merged.withAttribution(store.CoverageRun{
		Framework: framework, StartedAt: now, FinishedAt: now,
	}), results)
	if err != nil {
		return stats, fmt.Errorf("coverage: persist per-test run: %w", err)
	}
	stats.RunID = runID

	if err := s.TestCoverage().Insert(ctx, runID, perTest); err != nil {
		return stats, fmt.Errorf("coverage: persist per-test evidence: %w", err)
	}
	if _, err := s.CoverageGaps().Insert(ctx, runID, merged.gapRows()); err != nil {
		return stats, fmt.Errorf("coverage: persist per-test gaps: %w", err)
	}
	sort.Slice(stats.TestsUnresolved, func(i, j int) bool {
		return stats.TestsUnresolved[i] < stats.TestsUnresolved[j]
	})
	if s.Logger() != nil && len(stats.TestsUnresolved) > 0 {
		s.Logger().Debug(ctx, "per-test ingest: unresolved test names",
			"ingested", stats.TestsIngested, "unresolved", len(stats.TestsUnresolved))
	}
	return stats, nil
}
