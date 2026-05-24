-- 0008_edge_meta.down.sql
--
-- Reverses 0008_edge_meta.up.sql by dropping the `edge_meta` column
-- via the SQLite table-rebuild pattern. ALTER TABLE ... DROP COLUMN
-- is only available from SQLite 3.35.0+ and we keep the migration
-- portable to older runtimes.
--
-- Any edge_meta values present in the database are silently dropped
-- on downgrade — they are advisory metadata, the canonical source is
-- the scanner re-run on the next `atlas init`.

CREATE TABLE edges_legacy (
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

INSERT INTO edges_legacy (id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at)
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at FROM edges;

DROP TABLE edges;
ALTER TABLE edges_legacy RENAME TO edges;

CREATE INDEX edges_from_idx ON edges(from_symbol_id);
CREATE INDEX edges_to_idx   ON edges(to_symbol_id);
CREATE UNIQUE INDEX edges_dedupe_idx
  ON edges(from_symbol_id, to_symbol_id, kind, file_path, line);
