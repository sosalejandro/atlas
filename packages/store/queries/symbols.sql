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

-- name: LookupSymbolAtOrAfterLine :one
-- Resolves an annotation at file:line to the symbol it attaches to. Atlas
-- annotations sit in the comment block immediately above their target
-- (Go: doc comment above the func decl). The "nearest symbol at or after
-- the annotation line, in the same file, within `max_lookahead` rows"
-- rule is the simplest invariant that captures both `@atlas:feature`
-- (one line above the func) and multi-line doc-block annotations
-- (several lines above).
--
-- Bounds are deliberate: drifting more than ~30 lines past the annotation
-- almost always indicates the annotation is orphan (comment-only file or
-- markdown), not a legitimate attach to a faraway function.
SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at, pattern_matches
FROM symbols
WHERE file_path = sqlc.arg(file_path)
  AND line >= sqlc.arg(line)
  AND line <= sqlc.arg(line) + sqlc.arg(max_lookahead)
ORDER BY line ASC
LIMIT 1;

-- Note: List + FindByPattern use raw SQL in symbols.go because sqlc's
-- sqlite engine handles dynamic WHERE / JSON-substring matchers poorly.
