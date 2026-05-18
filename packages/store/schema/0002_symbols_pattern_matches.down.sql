-- Phase 6f rollback: drop the pattern_matches column. Requires SQLite
-- 3.35+ (DROP COLUMN, March 2021). Atlas pins modernc.org/sqlite ≥ 1.30
-- per Phase 4, which bundles SQLite 3.45+, so this is safe.
ALTER TABLE symbols DROP COLUMN pattern_matches;
