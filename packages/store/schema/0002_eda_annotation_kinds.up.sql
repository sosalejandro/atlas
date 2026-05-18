-- 0002_eda_annotation_kinds.up.sql
--
-- Extends the §5.11 `annotations` table CHECK(kind) set with the seven
-- EDA-pattern kinds introduced in Phase 6e:
--   bc, aggregate, aggregate-service, saga, consumer, event-emit, outbox-publish
--
-- SQLite cannot ALTER a CHECK constraint in place, so we follow the standard
-- table-rebuild pattern:
--   1. Create new annotations table with extended CHECK set
--   2. Copy rows from old table
--   3. Drop old table, rename new one
--   4. Recreate indexes
--
-- The Store is a re-derivable cache (see store.go §runMigrations) so a
-- failure here is recovered by deleting atlas-state.db and re-running.

CREATE TABLE annotations_new (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  file_path TEXT    NOT NULL,
  line      INTEGER NOT NULL,
  kind      TEXT    NOT NULL,
  value     TEXT    NOT NULL,
  source    TEXT    NOT NULL DEFAULT 'atlas',
  parsed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN (
    'feature', 'contract', 'owner', 'deprecated', 'since',
    'bc', 'aggregate', 'aggregate-service', 'saga', 'consumer',
    'event-emit', 'outbox-publish'
  )),
  CHECK (source IN ('atlas', 'testreg'))
);

INSERT INTO annotations_new (id, file_path, line, kind, value, source, parsed_at)
SELECT id, file_path, line, kind, value, source, parsed_at FROM annotations;

DROP TABLE annotations;
ALTER TABLE annotations_new RENAME TO annotations;

CREATE INDEX annotations_file_idx ON annotations(file_path);
CREATE UNIQUE INDEX annotations_dedupe_idx ON annotations(file_path, line, kind);
