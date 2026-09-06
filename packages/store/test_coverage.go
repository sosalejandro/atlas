package store

import (
	"context"
	"fmt"

	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// TestExecution is one test's execution of one production symbol: the
// statements of `SymbolID` that ran while `TestSymbolID` executed.
//
// The grain matters. A `coverage_results` row says "this symbol ran during the
// run"; a TestExecution row says "this symbol ran *because of this test*",
// which is the difference between knowing a codebase is covered and knowing
// what a given capability's tests actually exercise.
type TestExecution struct {
	TestSymbolID int64 `json:"test_symbol_id"`
	SymbolID     int64 `json:"symbol_id"`
	CoveredStmts int   `json:"covered_stmts"`
	TotalStmts   int   `json:"total_stmts"`
}

// TestCoverage is the port for per-test execution evidence (schema 0010).
//
// It exists to make feature location a set operation instead of a graph walk:
// the union of the symbols a feature's annotated tests executed, minus the
// symbols nearly every test executes, IS the feature's implementation surface
// — correct through interface dispatch, DI and reflection, none of which a
// static call-edge walk can follow (issues #84, #104).
//
// The same table answers the inverse question — which tests reach a changed
// symbol — which is affected-test selection (#90).
type TestCoverage interface {
	// Insert writes per-test execution rows for a run. Re-ingesting the same
	// run replaces rather than conflicts, so a retried ingest is safe.
	Insert(ctx context.Context, runID int64, rows []TestExecution) error

	// SymbolsExecutedBy returns the symbols one test ran.
	SymbolsExecutedBy(ctx context.Context, runID, testSymbolID int64) ([]TestExecution, error)

	// TestsExecuting returns the tests that ran one symbol.
	TestsExecuting(ctx context.Context, runID, symbolID int64) ([]TestExecution, error)

	// CountTests returns how many distinct tests contributed evidence to a
	// run — the denominator for the ubiquity cutoff.
	CountTests(ctx context.Context, runID int64) (int, error)

	// FanIn returns symbol id -> number of distinct tests that executed it.
	// A symbol executed by most of the suite is shared runtime (logging, DI,
	// config, framework glue), not any one feature's implementation.
	FanIn(ctx context.Context, runID int64) (map[int64]int, error)
}

var _ TestCoverage = (*testCoverageStore)(nil)

// TestCoverage returns the Store's TestCoverage port.
func (s *Store) TestCoverage() TestCoverage {
	return &testCoverageStore{db: s, q: s.queries()}
}

type testCoverageStore struct {
	db *Store
	q  *sqlc.Queries
}

func (t *testCoverageStore) Insert(ctx context.Context, runID int64, rows []TestExecution) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := t.db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("test coverage insert: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := t.q.WithTx(tx)
	for _, r := range rows {
		if err := qtx.InsertTestCoverage(ctx, sqlc.InsertTestCoverageParams{
			RunID:        runID,
			TestSymbolID: r.TestSymbolID,
			SymbolID:     r.SymbolID,
			CoveredStmts: int64(r.CoveredStmts),
			TotalStmts:   int64(r.TotalStmts),
		}); err != nil {
			return fmt.Errorf("test coverage insert (test %d, symbol %d): %w", r.TestSymbolID, r.SymbolID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("test coverage insert: commit: %w", err)
	}
	return nil
}

func (t *testCoverageStore) SymbolsExecutedBy(ctx context.Context, runID, testSymbolID int64) ([]TestExecution, error) {
	rows, err := t.q.ListSymbolsExecutedByTest(ctx, sqlc.ListSymbolsExecutedByTestParams{
		RunID: runID, TestSymbolID: testSymbolID,
	})
	if err != nil {
		return nil, fmt.Errorf("symbols executed by test %d: %w", testSymbolID, err)
	}
	out := make([]TestExecution, 0, len(rows))
	for _, r := range rows {
		out = append(out, TestExecution{
			TestSymbolID: testSymbolID,
			SymbolID:     r.SymbolID,
			CoveredStmts: int(r.CoveredStmts),
			TotalStmts:   int(r.TotalStmts),
		})
	}
	return out, nil
}

func (t *testCoverageStore) TestsExecuting(ctx context.Context, runID, symbolID int64) ([]TestExecution, error) {
	rows, err := t.q.ListTestsExecutingSymbol(ctx, sqlc.ListTestsExecutingSymbolParams{
		RunID: runID, SymbolID: symbolID,
	})
	if err != nil {
		return nil, fmt.Errorf("tests executing symbol %d: %w", symbolID, err)
	}
	out := make([]TestExecution, 0, len(rows))
	for _, r := range rows {
		out = append(out, TestExecution{
			TestSymbolID: r.TestSymbolID,
			SymbolID:     symbolID,
			CoveredStmts: int(r.CoveredStmts),
			TotalStmts:   int(r.TotalStmts),
		})
	}
	return out, nil
}

func (t *testCoverageStore) CountTests(ctx context.Context, runID int64) (int, error) {
	n, err := t.q.CountTestsInRun(ctx, runID)
	if err != nil {
		return 0, fmt.Errorf("count tests in run %d: %w", runID, err)
	}
	return int(n), nil
}

func (t *testCoverageStore) FanIn(ctx context.Context, runID int64) (map[int64]int, error) {
	rows, err := t.q.SymbolTestFanIn(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("symbol test fan-in for run %d: %w", runID, err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.SymbolID] = int(r.TestCount)
	}
	return out, nil
}
