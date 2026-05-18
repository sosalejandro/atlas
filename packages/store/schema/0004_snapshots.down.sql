-- Phase 6b rollback: drop the snapshots table.
DROP INDEX IF EXISTS idx_snapshots_git_ref;
DROP TABLE IF EXISTS snapshots;
