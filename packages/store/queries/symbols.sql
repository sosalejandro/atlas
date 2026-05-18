-- name: InsertSymbol :execresult
INSERT OR IGNORE INTO symbols
  (qualified_name, kind, file_path, line, end_line, package, bc_path)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetSymbolIDByQualifiedName :one
SELECT id FROM symbols WHERE qualified_name = ?;

-- name: GetSymbolByQualifiedName :one
SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at
FROM symbols
WHERE qualified_name = ?;

-- name: DeleteSymbolsByFile :exec
DELETE FROM symbols WHERE file_path = ?;
