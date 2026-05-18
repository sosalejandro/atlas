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

-- name: ListSymbols :many
-- Returns rows that match every non-empty filter, ordered deterministically.
-- Each filter is opt-in via the sentinel-empty-string idiom: pass the empty
-- string for a column to disable that predicate; pass a value to match it
-- exactly. We use sentinels instead of sqlc.narg because as of sqlc v1.31.1
-- the sqlite engine ANTLR grammar rejects the post-substituted placeholders
-- the narg lowering emits (see sqlc-dev/sqlc#1881, #3508).
--
-- None of these columns store the empty value as a legitimate row: the
-- parser layer always populates file_path + kind, and package / bc_path
-- are either non-empty or NULL.
--
-- Callers must normalize Kind to the closed schema-v1 set BEFORE binding
-- (see normalizeKind) so the equality match never silently misses an
-- audit-layer value that would have collapsed at insert time.
SELECT id, qualified_name, kind, file_path, line, end_line, package, bc_path, created_at, pattern_matches
FROM symbols
WHERE (sqlc.arg(file_path) = '' OR file_path = sqlc.arg(file_path))
  AND (sqlc.arg(package)   = '' OR package   = sqlc.arg(package))
  AND (sqlc.arg(bc_path)   = '' OR bc_path   = sqlc.arg(bc_path))
  AND (sqlc.arg(kind)      = '' OR kind      = sqlc.arg(kind))
ORDER BY file_path, line, qualified_name;

-- Note: FindByPattern still uses raw SQL in symbols.go because sqlc's
-- sqlite engine handles JSON-substring matchers poorly.
