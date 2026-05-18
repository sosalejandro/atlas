-- name: InsertAuditSnapshotRun :execresult
INSERT INTO audit_snapshot_runs (score_json) VALUES (?);

-- name: InsertAuditSnapshotRunWithTime :execresult
INSERT INTO audit_snapshot_runs (computed_at, score_json) VALUES (?, ?);

-- name: GetAuditSnapshotRun :one
SELECT id, computed_at, score_json
FROM audit_snapshot_runs
WHERE id = ?;

-- name: ListAuditSnapshotRuns :many
SELECT id, computed_at, score_json
FROM audit_snapshot_runs
ORDER BY computed_at DESC
LIMIT ?;

-- name: LatestAuditSnapshotRun :one
SELECT id, computed_at, score_json
FROM audit_snapshot_runs
ORDER BY computed_at DESC
LIMIT 1;
