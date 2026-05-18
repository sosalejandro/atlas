-- name: GetFileHash :one
SELECT file_path, content_hash, mtime, last_scanned
FROM file_hashes
WHERE file_path = ?;

-- name: UpsertFileHash :exec
INSERT INTO file_hashes (file_path, content_hash, mtime, last_scanned)
VALUES (?, ?, ?, ?)
ON CONFLICT(file_path) DO UPDATE SET
  content_hash = excluded.content_hash,
  mtime        = excluded.mtime,
  last_scanned = excluded.last_scanned;

-- name: ListFileHashes :many
SELECT file_path, content_hash, mtime, last_scanned
FROM file_hashes
ORDER BY file_path;

-- name: DeleteFileHash :exec
DELETE FROM file_hashes WHERE file_path = ?;
