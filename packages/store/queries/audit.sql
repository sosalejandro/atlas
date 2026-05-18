-- name: InsertAuditSnapshot :execresult
INSERT INTO audit_snapshots (feature_id, score, layer_scores_json, blocking_findings_json)
VALUES (?, ?, ?, ?);

-- name: InsertAuditSnapshotWithTime :execresult
INSERT INTO audit_snapshots (taken_at, feature_id, score, layer_scores_json, blocking_findings_json)
VALUES (?, ?, ?, ?, ?);

-- name: ListAuditSnapshotsByFeature :many
SELECT id, taken_at, feature_id, score, layer_scores_json, blocking_findings_json
FROM audit_snapshots
WHERE feature_id = ?
ORDER BY taken_at DESC
LIMIT ?;

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
