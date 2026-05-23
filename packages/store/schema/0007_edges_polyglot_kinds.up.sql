-- 0007_edges_polyglot_kinds.up.sql
--
-- Extends the §5.5 `edges` table CHECK(kind) set with the three
-- Python-scanner-emitted kinds introduced after the v0.4 closure of
-- issue #57 (Python scanner: edges silently dropped — Go orchestrator
-- strips kind field):
--
--   inheritance, decorator, import
--
-- These kinds are emitted by `packages/codeindex/py/scanner.py` and
-- preserved through the Go orchestrator into the store. Without this
-- migration the legacy CHECK constraint (`call`, `implement`, `embed`,
-- `construct`) would reject every Python edge whose normalised kind is
-- non-call, surfacing as a hard SQLITE_CONSTRAINT_CHECK at ingest time.
--
-- SQLite cannot ALTER a CHECK constraint in place, so we follow the
-- table-rebuild pattern from migration 0002:
--   1. Create new edges table with extended CHECK set
--   2. Copy rows from old table (preserves surrogate ids — the FK from
--      feature_symbols.symbol_id → symbols.id is unaffected; nothing
--      else FKs into edges)
--   3. Drop old table, rename new one
--   4. Recreate indexes (the indexes are defined on `edges` not
--      `edges_new`, so they must be re-issued post-rename)
--
-- The Store is a re-derivable cache (see store.go §runMigrations) so a
-- failure here is recovered by deleting atlas.db and re-running.

CREATE TABLE edges_new (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  from_symbol_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  to_symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  kind           TEXT    NOT NULL,
  file_path      TEXT    NOT NULL,
  line           INTEGER NOT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN (
    'call', 'implement', 'embed', 'construct',
    'inheritance', 'decorator', 'import'
  ))
);

INSERT INTO edges_new (id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at)
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at FROM edges;

DROP TABLE edges;
ALTER TABLE edges_new RENAME TO edges;

CREATE INDEX edges_from_idx ON edges(from_symbol_id);
CREATE INDEX edges_to_idx   ON edges(to_symbol_id);
CREATE UNIQUE INDEX edges_dedupe_idx
  ON edges(from_symbol_id, to_symbol_id, kind, file_path, line);
