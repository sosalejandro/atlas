-- name: GetConfig :one
SELECT value FROM config WHERE key = ?;

-- name: SetConfig :exec
INSERT INTO config (key, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE SET
  value      = excluded.value,
  updated_at = CURRENT_TIMESTAMP;

-- name: ListConfig :many
SELECT key, value, updated_at FROM config ORDER BY key;

-- name: DeleteConfig :exec
DELETE FROM config WHERE key = ?;
