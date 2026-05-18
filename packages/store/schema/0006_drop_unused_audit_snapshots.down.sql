-- Rollback for 0006: recreate the `audit_snapshots` table verbatim from
-- migration 0001 so a `migrate down` restores the pre-drop schema. The
-- table is intentionally re-created empty — production never wrote to it,
-- and any test logic that depended on the legacy port was migrated to
-- `audit_snapshot_runs` in the same commit that dropped the table.
CREATE TABLE audit_snapshots (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  taken_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  feature_id              TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
  score                   INTEGER NOT NULL,
  layer_scores_json       TEXT NOT NULL DEFAULT '{}',
  blocking_findings_json  TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX audit_snapshots_feature_idx ON audit_snapshots(feature_id, taken_at);
