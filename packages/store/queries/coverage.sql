-- name: InsertCoverageRun :execresult
INSERT INTO coverage_runs (
  framework, started_at, finished_at, raw_path, summary_json, run_group,
  files_in_report, files_matched, files_unmatched,
  stmts_attributed, stmts_unattributed
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCoverageRun :one
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated, run_group
FROM coverage_runs
WHERE id = ?;

-- name: ListAllCoverageRuns :many
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated, run_group
FROM coverage_runs
ORDER BY finished_at DESC, id DESC;

-- name: ListCoverageRunsByFramework :many
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated, run_group
FROM coverage_runs
WHERE framework = ?
ORDER BY finished_at DESC, id DESC;

-- name: InsertCoverageResult :exec
INSERT INTO coverage_results (run_id, symbol_id, feature_id, status, duration_ms, message)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListCoverageResults :many
SELECT id, run_id, symbol_id, feature_id, status, duration_ms, message
FROM coverage_results
WHERE run_id = ?
ORDER BY id;

-- name: NewestCoverageRun :one
-- The single most recent run, used as the seed for the coverage frontier: its
-- group (if any) is what the audit scores over.
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated, run_group
FROM coverage_runs
ORDER BY finished_at DESC, id DESC
LIMIT 1;

-- name: ListCoverageRunsByGroup :many
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated, run_group
FROM coverage_runs
WHERE run_group = ?
ORDER BY finished_at DESC, id DESC;

-- name: ListCoverageResultsByGroup :many
-- One join rather than a query per run: the audit reads a frontier once per
-- feature, so a round trip per framework would multiply across a large repo.
SELECT r.id, r.run_id, r.symbol_id, r.feature_id, r.status, r.duration_ms, r.message,
       r.covered_stmts, r.total_stmts
FROM coverage_results r
JOIN coverage_runs g ON g.id = r.run_id
WHERE g.run_group = ?
ORDER BY r.run_id, r.id;
