-- name: UpsertHistoryPoint :exec
-- coverage_history is the measurement series behind `atlas trend` (#92).
-- A re-measurement of a commit CORRECTS its point, it does not append a
-- second one, so the write is an upsert on the unique commit_sha index.
-- last_insert_rowid() is not updated on the DO UPDATE path, which is why
-- the caller reads the id back with HistoryPointIDByCommit instead of
-- trusting an execresult here.
INSERT INTO coverage_history (commit_sha, measured_at, score, denominator, note)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(commit_sha) DO UPDATE SET
  measured_at = excluded.measured_at,
  score       = excluded.score,
  denominator = excluded.denominator,
  note        = excluded.note;

-- name: HistoryPointIDByCommit :one
SELECT id FROM coverage_history WHERE commit_sha = ?;

-- name: GetHistoryPoint :one
-- Column order matches the table declaration so sqlc reuses the generated
-- model type rather than inventing a per-query row type. Same for every
-- SELECT below.
SELECT id, commit_sha, measured_at, score, denominator, note
FROM coverage_history
WHERE commit_sha = ?;

-- name: ListHistoryPoints :many
-- Newest first with a LIMIT so a cap keeps the most RECENT window; the port
-- reverses into oldest-first, which is how a series reads.
SELECT id, commit_sha, measured_at, score, denominator, note
FROM coverage_history
WHERE measured_at >= ?
ORDER BY measured_at DESC, id DESC
LIMIT ?;

-- name: PruneHistoryBefore :execrows
-- Retention. The per-feature rows go with the point via ON DELETE CASCADE,
-- so there is no second statement to forget.
DELETE FROM coverage_history WHERE measured_at < ?;

-- name: DeleteHistoryFeatures :exec
DELETE FROM coverage_history_features WHERE history_id = ?;

-- name: InsertHistoryFeature :exec
INSERT INTO coverage_history_features (history_id, feature_id, score, denominator)
VALUES (?, ?, ?, ?);

-- name: ListHistoryFeatures :many
SELECT history_id, feature_id, score, denominator
FROM coverage_history_features
WHERE history_id = ?
ORDER BY feature_id;
