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
