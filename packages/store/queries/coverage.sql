-- name: InsertCoverageRun :execresult
INSERT INTO coverage_runs (
  framework, started_at, finished_at, raw_path, summary_json,
  files_in_report, files_matched, files_unmatched,
  stmts_attributed, stmts_unattributed
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCoverageRun :one
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated
FROM coverage_runs
WHERE id = ?;

-- name: ListAllCoverageRuns :many
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated
FROM coverage_runs
ORDER BY finished_at DESC, id DESC;

-- name: ListCoverageRunsByFramework :many
SELECT id, framework, started_at, finished_at, raw_path, summary_json,
       files_in_report, files_matched, files_unmatched,
       stmts_attributed, stmts_unattributed, gaps_truncated
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
