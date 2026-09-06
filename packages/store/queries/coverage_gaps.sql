-- name: InsertCoverageRunGap :exec
-- One row per file whose execution could not be charged to a symbol.
-- REPLACE so a retried ingest of the same run is idempotent.
INSERT OR REPLACE INTO coverage_run_gaps (run_id, path, stmts, reason)
VALUES (?, ?, ?, ?);

-- name: DeleteCoverageRunGaps :exec
-- Clears a run's list before rewriting it, so a re-ingest cannot leave two
-- generations of rows interleaved.
DELETE FROM coverage_run_gaps WHERE run_id = ?;

-- name: ListCoverageRunGaps :many
-- Biggest loss first, ties broken by path: a consumer reading only the head
-- of the list still sees the worst offenders, in a stable order.
SELECT path, stmts, reason
FROM coverage_run_gaps
WHERE run_id = ?
ORDER BY stmts DESC, path ASC;

-- name: SetCoverageRunGapsTruncated :exec
-- Records how many gap files did not fit the per-run cap. Written in the same
-- transaction as the rows themselves, so the count and the list can never
-- disagree about whether the list is complete.
UPDATE coverage_runs SET gaps_truncated = ? WHERE id = ?;
