-- name: InsertSymbol :execresult
INSERT OR IGNORE INTO symbols
  (qualified_name, kind, file_path, line, end_line, package, bc_path)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetSymbolIDByQualifiedName :one
SELECT id FROM symbols WHERE qualified_name = ?;

-- name: GetSymbolByQualifiedName :one
SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at, pattern_matches
FROM symbols
WHERE qualified_name = ?;

-- name: DeleteSymbolsByFile :exec
DELETE FROM symbols WHERE file_path = ?;

-- name: SetSymbolPatternMatches :exec
UPDATE symbols SET pattern_matches = ? WHERE id = ?;

-- name: SetSymbolPatternMatchesByQualifiedName :exec
UPDATE symbols SET pattern_matches = ? WHERE qualified_name = ?;

-- Note: List + FindByPattern use raw SQL in symbols.go because sqlc's
-- sqlite engine handles dynamic WHERE / JSON-substring matchers poorly.
