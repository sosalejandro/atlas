-- Phase 6a: audit snapshot runs.
--
-- The per-feature `audit_snapshots` table from migration 0001 stores ONE row
-- per (feature, snapshot) pair and is well-suited to per-feature history. The
-- `audit/` package (Phase 6a) instead writes WHOLE-PROJECT snapshots — a
-- single JSON blob containing the FeatureHealth slice for every feature at a
-- point in time. The two tables coexist:
--
--   audit_snapshots       — per-feature row, per-snapshot history (Phase 0 spec)
--   audit_snapshot_runs   — whole-project blob, per-snapshot (Phase 6a addition)
--
-- This shape keeps `PersistSnapshot([]FeatureHealth)` / `LoadSnapshot(id)`
-- O(1) on the read path (one row → one Unmarshal) and avoids paying the
-- N-row INSERT-per-feature cost on every snapshot write. Schema stays small;
-- the audit/ algorithm can evolve the FeatureHealth JSON shape without a
-- migration (the column is just TEXT).
CREATE TABLE audit_snapshot_runs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  computed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  score_json  TEXT      NOT NULL DEFAULT '[]'
);

CREATE INDEX audit_snapshot_runs_computed_idx ON audit_snapshot_runs(computed_at);
