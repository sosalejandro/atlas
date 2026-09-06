-- name: InsertEdge :execresult
-- edge_meta is a NULLable kind-specific qualifier. Python import edges populate it with a scope tag (module/function/conditional/type_checking/try_guard) via migration 0008 - issue #16. Non-import edges pass NULL.
-- resolution_tier is NOT NULL with no default (migration 0018 - issue #146): the caller states which mechanism resolved this edge. Passing an empty or unknown value is a CHECK violation, on purpose - an edge whose provenance nobody stated must not land looking like one that was type-checked.
-- ambiguous is the graph layer's per-edge "the resolver had more than one candidate" flag, persisted rather than recomputed.
INSERT OR IGNORE INTO edges
  (from_symbol_id, to_symbol_id, kind, file_path, line, edge_meta, resolution_tier, ambiguous)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEdgeID :one
SELECT id FROM edges
WHERE from_symbol_id = ?
  AND to_symbol_id = ?
  AND kind = ?
  AND file_path = ?
  AND line = ?;

-- name: ListEdgesOut :many
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at, edge_meta, resolution_tier, ambiguous
FROM edges
WHERE from_symbol_id = ?
ORDER BY file_path, line;

-- name: ListEdgesIn :many
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at, edge_meta, resolution_tier, ambiguous
FROM edges
WHERE to_symbol_id = ?
ORDER BY file_path, line;

-- name: DeleteEdgesByFile :exec
DELETE FROM edges WHERE file_path = ?;
