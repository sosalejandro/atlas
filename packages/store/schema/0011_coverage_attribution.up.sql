-- 0011_coverage_attribution.up.sql
--
-- Persist the attribution accounting an ingest computes (issue #100).
--
-- The counters added for issue #85 -- files matched/unmatched, statements
-- attributed/unattributed, and the per-file gap list with reasons -- lived
-- only in ProfileIngestStats: returned to the CLI, printed, and dropped. That
-- made the single most important question a consumer can ask ("how much of
-- what ran can atlas actually see?") answerable only by re-ingesting the
-- profile, which blocks every UI surface, `atlas doctor`, a CI gate on
-- unattributed percentage, and the attribution-quality trend over time.
--
-- Schema-shape notes:
--
--   * The run-level counters go on coverage_runs rather than in summary_json.
--     They are the columns a CI gate and a trend query filter and aggregate
--     on; JSON blob extraction for that is a query planner's worst case.
--
--   * Every counter is NOT NULL DEFAULT 0. Runs ingested before this
--     migration, and frameworks with no statement coverage at all (playwright,
--     maestro, the gotest pass/fail model), leave them at 0. Readers treat
--     an all-zero run as "no accounting recorded" and say so, rather than
--     reporting a perfect 0-of-0 attribution.
--
--   * gaps_truncated counts the gap FILES that did not fit the per-run cap.
--     The statements they lost are still in stmts_unattributed, so the
--     run-level total stays exact no matter how the list was capped -- a
--     truncated list that claims completeness is the failure mode this
--     migration exists to prevent.
--
--   * coverage_run_gaps is keyed by (run_id, path): one row per file, which
--     is the grain the ingest reports. WITHOUT ROWID -- the primary key IS
--     the row, and every read is a prefix scan on it.
--
--   * The cascade follows the run: deleting an old coverage run takes its
--     gap list with it, so the table cannot outlive what it describes.
--
--   * Migrations are one-way (.up.sql only) -- the store is a re-derivable
--     cache; recover by deleting atlas.db and re-running atlas init.

ALTER TABLE coverage_runs ADD COLUMN files_in_report INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN files_matched INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN files_unmatched INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN stmts_attributed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN stmts_unattributed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN gaps_truncated INTEGER NOT NULL DEFAULT 0;

CREATE TABLE coverage_run_gaps (
  run_id INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  path   TEXT    NOT NULL,
  stmts  INTEGER NOT NULL DEFAULT 0,
  reason TEXT    NOT NULL,
  PRIMARY KEY (run_id, path)
) WITHOUT ROWID;

-- Reads are always "this run's gaps, biggest loss first" -- the order the
-- ingest reports them in and the order a capped view must preserve.
CREATE INDEX coverage_run_gaps_loss_idx ON coverage_run_gaps(run_id, stmts DESC);
