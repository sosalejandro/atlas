-- name: GetFeature :one
SELECT id, title, owner, kind, deprecated_since, introduced_in, created_at, updated_at
FROM features
WHERE id = ?;

-- name: ListAllFeatures :many
SELECT id, title, owner, kind, deprecated_since, introduced_in, created_at, updated_at
FROM features
ORDER BY id;

-- name: ListFeaturesByKind :many
SELECT id, title, owner, kind, deprecated_since, introduced_in, created_at, updated_at
FROM features
WHERE kind = ?
ORDER BY id;

-- name: UpsertFeature :exec
INSERT INTO features (id, title, owner, kind, deprecated_since, introduced_in, updated_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
  title            = excluded.title,
  owner            = excluded.owner,
  kind             = excluded.kind,
  deprecated_since = excluded.deprecated_since,
  introduced_in    = excluded.introduced_in,
  updated_at       = CURRENT_TIMESTAMP;

-- name: EnsureFeature :exec
-- Inserts a feature row from the ingest path. Pure INSERT OR IGNORE -- if
-- the row already exists with richer metadata (title/owner/kind/etc set
-- by a prior atlas migrate or test harness), the ingest pass MUST NOT
-- clobber it back to the id-as-title default.
--
-- Re-ingest of the same annotation produces zero row changes. Use the
-- explicit UpsertFeature path when callers genuinely want to overwrite.
INSERT OR IGNORE INTO features (id, title, kind)
VALUES (?, ?, ?);

-- name: DeleteFeature :execrows
DELETE FROM features WHERE id = ?;
