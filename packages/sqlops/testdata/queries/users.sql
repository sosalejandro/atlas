-- name: ListUsers :many
-- Every user of a tenant. The set is small and bounded by the tenant.
-- atlas:sql-ignore sql.unbounded-list
SELECT id, email FROM users WHERE tenant_id = $1;

-- name: PageEvents :many
SELECT id FROM events LIMIT $1 OFFSET $2;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;
