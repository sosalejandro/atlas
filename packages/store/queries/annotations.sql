-- name: UpsertAnnotation :exec
INSERT INTO annotations (file_path, line, kind, value, source)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(file_path, line, kind) DO UPDATE SET
  value     = excluded.value,
  source    = excluded.source,
  parsed_at = CURRENT_TIMESTAMP;

-- name: ListAnnotationsByFile :many
SELECT id, file_path, line, kind, value, source, parsed_at
FROM annotations
WHERE file_path = ?
ORDER BY line, kind;

-- name: DeleteAnnotationsByFile :exec
DELETE FROM annotations WHERE file_path = ?;
