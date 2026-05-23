-- 0007_edges_polyglot_kinds.down.sql
--
-- Reverses 0007_edges_polyglot_kinds.up.sql by rebuilding the `edges` table
-- with the original CHECK constraint set (`call`, `implement`, `embed`,
-- `construct`). Any rows whose kind is one of the new polyglot kinds
-- ('inheritance', 'decorator', 'import') are silently dropped during the
-- rebuild — they would violate the narrower CHECK and the store is a
-- re-derivable cache, so losing them on downgrade is acceptable; a
-- subsequent `atlas init` re-ingests from source.

CREATE TABLE edges_legacy (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  from_symbol_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  to_symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  kind           TEXT    NOT NULL,
  file_path      TEXT    NOT NULL,
  line           INTEGER NOT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('call', 'implement', 'embed', 'construct'))
);

INSERT INTO edges_legacy (id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at)
SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at
FROM edges
WHERE kind IN ('call', 'implement', 'embed', 'construct');

DROP TABLE edges;
ALTER TABLE edges_legacy RENAME TO edges;

CREATE INDEX edges_from_idx ON edges(from_symbol_id);
CREATE INDEX edges_to_idx   ON edges(to_symbol_id);
CREATE UNIQUE INDEX edges_dedupe_idx
  ON edges(from_symbol_id, to_symbol_id, kind, file_path, line);
