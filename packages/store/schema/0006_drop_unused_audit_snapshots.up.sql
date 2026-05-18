-- Phase 6a follow-up (closes #21): drop the unused legacy audit_snapshots
-- table.
--
-- Migration 0001 created `audit_snapshots` for a per-feature, per-snapshot
-- history shape (docs/schema-v1.md §5.10). Nothing in Atlas ever wrote to it
-- — the audit/ package (Phase 6a) instead persists whole-project JSON-blob
-- snapshots into `audit_snapshot_runs` (added by migration 0005). The legacy
-- table has remained dead schema since.
--
-- This migration removes the table and its index so future readers don't
-- have to puzzle out which of the two audit-snapshot tables is live. The
-- corresponding Go port (store.AuditSnapshots), sqlc queries, and tests are
-- dropped alongside this migration in the same commit.
DROP INDEX IF EXISTS audit_snapshots_feature_idx;
DROP TABLE IF EXISTS audit_snapshots;
