-- The annotation source enum gains 'grunnr', the name the tool now has.
--
-- The CHECK was `source IN ('atlas', 'testreg')`. Renaming the tool changed
-- the value the parser writes, and a CHECK constraint does not care what the
-- binary is called: every ingest failed with "constraint failed" and no hint
-- that a rename was the cause.
--
-- 'atlas' STAYS in the allowed set. Rows written before the rename are still
-- valid rows about code that has not changed, and a migration that made them
-- illegal would either fail on somebody's existing database or force a
-- rewrite of every annotation row to fix a cosmetic difference in a label.
-- The reader treats the two as the same source; only new rows use 'grunnr'.
--
-- SQLite cannot ALTER a CHECK constraint, so the table is rebuilt. The
-- 12-step procedure in the SQLite docs is followed: legacy_alter_table is
-- irrelevant here because the rename is of the constraint rather than the
-- table, and foreign_keys is left to the caller's transaction.

CREATE TABLE annotations_new (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  file_path     TEXT NOT NULL,
  line          INTEGER NOT NULL,
  kind          TEXT NOT NULL,
  value         TEXT NOT NULL,
  source        TEXT NOT NULL,
  parsed_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (file_path, line, kind),
  CHECK (source IN ('grunnr', 'atlas', 'testreg', 'api'))
);

INSERT INTO annotations_new (id, file_path, line, kind, value, source, parsed_at)
  SELECT id, file_path, line, kind, value, source, parsed_at FROM annotations;

DROP TABLE annotations;
ALTER TABLE annotations_new RENAME TO annotations;

CREATE INDEX IF NOT EXISTS annotations_file_idx  ON annotations(file_path);
CREATE INDEX IF NOT EXISTS annotations_value_idx ON annotations(value);
