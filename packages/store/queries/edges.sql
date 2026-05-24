-- name: InsertEdge :execresult
-- edge_meta is a NULLable kind-specific qualifier. Python import edges populate it with a scope tag (module/function/conditional/type_checking/try_guard) via migration 0008 - issue #16. Non-import edges pass NULL.
INSERT OR IGNORE INTO edges
  (from_symbol_id, to_symbol_id, kind, file_path, line, edge_meta)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetEdgeID :one
SELECT id FROM edges
WHERE from_symbol_id = ?
  AND to_symbol_id = ?
  AND kind = ?
  AND file_path = ?
  AND line = ?;

-- name: ListEdgesOut :many
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, edge_meta, created_at
FROM edges
WHERE from_symbol_id = ?
ORDER BY file_path, line;

-- name: ListEdgesIn :many
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, edge_meta, created_at
FROM edges
WHERE to_symbol_id = ?
ORDER BY file_path, line;

-- name: DeleteEdgesByFile :exec
DELETE FROM edges WHERE file_path = ?;
