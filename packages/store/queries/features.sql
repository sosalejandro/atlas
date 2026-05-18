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

-- name: DeleteFeature :execrows
DELETE FROM features WHERE id = ?;
