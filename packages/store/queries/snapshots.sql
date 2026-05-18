-- name: InsertSnapshot :execresult
INSERT INTO snapshots (git_ref, captured_at, index_json, audit_json, notes)
VALUES (?, ?, ?, ?, ?);

-- name: GetSnapshot :one
SELECT id, git_ref, captured_at, index_json, audit_json, notes
FROM snapshots
WHERE id = ?;

-- name: ListSnapshotsByGitRef :many
SELECT id, git_ref, captured_at, index_json, audit_json, notes
FROM snapshots
WHERE git_ref = ?
ORDER BY captured_at DESC, id DESC;

-- name: ListAllSnapshots :many
SELECT id, git_ref, captured_at, index_json, audit_json, notes
FROM snapshots
ORDER BY captured_at DESC, id DESC;

-- name: DeleteSnapshot :execrows
DELETE FROM snapshots WHERE id = ?;
