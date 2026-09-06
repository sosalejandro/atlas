-- 0012_coverage_run_groups.up.sql
--
-- Adds an optional `run_group` column to `coverage_runs` so several syncs
-- can declare themselves one logical measurement (issue #86).
--
-- The bug this fixes: `audit` scored the single newest run, so in a
-- polyglot repo `cov sync --framework istanbul` right after
-- `cov sync --framework go-cover` made every Go capability score as if the
-- Go suite had never run. A coverage frontier in a polyglot repo is
-- inherently multi-source (Codecov calls the same idea "flags on one commit
-- report"); the tables held the data already, they just had no notion of
-- "these N runs belong together".
--
-- Schema-shape notes:
--
--   * NULLable, and NULL is the pre-existing behaviour. A store whose runs
--     all predate this column keeps scoring exactly as before: the audit
--     falls back to the newest single run when the newest run has no group.
--
--   * Free text, no CHECK and no separate `run_groups` table. The group is
--     a caller-chosen correlation key (a git SHA, a CI run id) with no
--     lifecycle of its own — a lookup table would buy referential integrity
--     over values Atlas never interprets, at the cost of a join on the
--     hottest audit read path. The Go store layer normalises "" to NULL so
--     a blank key cannot open a group every blank-key run joins.
--
--   * Indexed as (run_group, finished_at): the audit reads "every run in
--     group G, newest first", and `cov status --group` will want the same
--     ordering. finished_at as the second column keeps that read
--     index-only.
--
-- The store is a re-derivable cache (see store.go runMigrations) so a
-- failure here is recovered by deleting atlas.db and re-running.

ALTER TABLE coverage_runs ADD COLUMN run_group TEXT;

CREATE INDEX coverage_runs_group_idx ON coverage_runs(run_group, finished_at);
