-- name: InsertTestCoverage :exec
-- One row per (run, test, executed symbol). REPLACE so a re-ingest of the
-- same run is idempotent rather than a constraint violation.
INSERT OR REPLACE INTO test_coverage (run_id, test_symbol_id, symbol_id, covered_stmts, total_stmts)
VALUES (?, ?, ?, ?, ?);

-- name: ListSymbolsExecutedByTest :many
-- The production symbols one test ran. Union these over a feature's annotated
-- tests to get its implementation surface without walking the call graph.
SELECT symbol_id, covered_stmts, total_stmts
FROM test_coverage
WHERE run_id = ? AND test_symbol_id = ?;

-- name: ListTestsExecutingSymbol :many
-- The inverse: which tests ran a symbol. This is affected-test selection.
SELECT test_symbol_id, covered_stmts, total_stmts
FROM test_coverage
WHERE run_id = ? AND symbol_id = ?;

-- name: CountTestsInRun :one
SELECT COUNT(DISTINCT test_symbol_id) FROM test_coverage WHERE run_id = ?;

-- name: SymbolTestFanIn :many
-- symbol_id -> how many distinct tests executed it. A symbol executed by most
-- of the suite is framework, logging or DI plumbing, not feature code; the
-- ubiquity cutoff uses this to keep shared runtime out of every surface.
SELECT symbol_id, COUNT(DISTINCT test_symbol_id) AS test_count
FROM test_coverage
WHERE run_id = ?
GROUP BY symbol_id;
