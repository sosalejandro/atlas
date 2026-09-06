-- name: GetSkippedFile :one
SELECT file_path, rule, detail, scanned_at
FROM skipped_files
WHERE file_path = ?;

-- name: ListSkippedFiles :many
SELECT file_path, rule, detail, scanned_at
FROM skipped_files
ORDER BY file_path;

-- name: InsertSkippedFile :exec
-- DO NOTHING rather than DO UPDATE: within one scan a file is claimed by
-- exactly one rule, so a second row for the same path can only come from a
-- caller merging two scans. Keeping the first entry keeps the scanner's own
-- decision instead of letting the merge order rewrite it.
INSERT INTO skipped_files (file_path, rule, detail, scanned_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(file_path) DO NOTHING;

-- name: DeleteAllSkippedFiles :exec
DELETE FROM skipped_files;
