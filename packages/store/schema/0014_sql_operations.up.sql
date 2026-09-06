-- 0014_sql_operations.up.sql
--
-- The data access layer, as data. Atlas could already say that a repository
-- method is covered; it could not say that the query inside it has no LIMIT,
-- takes a caller-supplied offset, and filters on a column no index leads with
-- (issue #126). These tables hold the shape of every SQL operation atlas found
-- so those questions become joins rather than greps.
--
-- Shape notes, in the order they will surprise a reader:
--
--   * `resolved` is the load-bearing column. A query assembled by a builder or
--     across functions is stored with resolved = 0 and an
--     `unresolved_reason`, NOT dropped. Every consumer must filter on it: the
--     shape columns of an unresolved row are all zero, and zero looks exactly
--     like "a SELECT with no LIMIT". Storing the unanalysable half is what
--     lets a report say "78% of the data layer was analysed" instead of
--     implying it saw all of it.
--
--   * `symbol_id` is nullable and ON DELETE SET NULL rather than CASCADE. An
--     operation outlives the symbol row it was linked to (a rename, a scan
--     that skipped a generated file), and losing the operation would silently
--     shrink the inventory. `symbol_name` is kept alongside so a row is
--     readable even with no link.
--
--   * `ref` is the fingerprint: source, file, line and name. Re-scanning an
--     unchanged repository rewrites the same rows instead of accumulating
--     duplicates. The writer replaces the whole inventory per scan, so the ref
--     is a uniqueness guard rather than an upsert key.
--
--   * Predicates and table accesses are child tables rather than JSON columns
--     because the questions they answer are set questions -- "which
--     capabilities write to this table", "which columns does anything filter
--     on" -- and those want an index, not a LIKE.
--
--   * `sql_tables` and `sql_indexes` are what atlas READ from the DDL, not a
--     mirror of a live database. An index check consults `sql_tables` first
--     and reports "did not run" for a table that is absent, because claiming
--     an index is missing from a schema atlas never read is the one wrong
--     answer that looks authoritative.
--
--   * Migrations are one-way (.up.sql only) -- the store is a re-derivable
--     cache; recover by deleting atlas.db and re-running atlas init.

CREATE TABLE sql_operations (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  ref               TEXT    NOT NULL UNIQUE,
  source            TEXT    NOT NULL,
  name              TEXT    NOT NULL DEFAULT '',
  file_path         TEXT    NOT NULL,
  line              INTEGER NOT NULL,
  symbol_id         INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
  symbol_name       TEXT    NOT NULL DEFAULT '',
  kind              TEXT    NOT NULL DEFAULT 'unknown',
  resolved          INTEGER NOT NULL DEFAULT 0,
  unresolved_reason TEXT    NOT NULL DEFAULT '',
  sql_text          TEXT    NOT NULL DEFAULT '',
  row_scan          TEXT    NOT NULL DEFAULT 'unknown',
  param_count       INTEGER NOT NULL DEFAULT 0,
  interpolation     TEXT    NOT NULL DEFAULT '',
  caller_data       INTEGER NOT NULL DEFAULT 0,
  has_limit         INTEGER NOT NULL DEFAULT 0,
  has_offset        INTEGER NOT NULL DEFAULT 0,
  has_order_by      INTEGER NOT NULL DEFAULT 0,
  keyset            INTEGER NOT NULL DEFAULT 0,
  select_star       INTEGER NOT NULL DEFAULT 0,
  offset_bound      TEXT    NOT NULL DEFAULT 'none',
  suppressions      TEXT    NOT NULL DEFAULT '',
  created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (source IN ('go', 'sql')),
  CHECK (kind IN ('select', 'insert', 'update', 'delete', 'other', 'unknown')),
  CHECK (row_scan IN ('slice', 'single', 'exec', 'unknown')),
  CHECK (offset_bound IN ('none', 'parameter', 'literal', 'expression'))
);

CREATE INDEX sql_operations_file_idx   ON sql_operations(file_path, line);
CREATE INDEX sql_operations_symbol_idx ON sql_operations(symbol_id);
-- "which operations did atlas fail to read" is a first-class query, not a
-- full scan: it is the number every report leads with.
CREATE INDEX sql_operations_resolved_idx ON sql_operations(resolved);

CREATE TABLE sql_operation_tables (
  operation_id INTEGER NOT NULL REFERENCES sql_operations(id) ON DELETE CASCADE,
  table_name   TEXT    NOT NULL,
  access       TEXT    NOT NULL,
  PRIMARY KEY (operation_id, table_name, access),
  CHECK (access IN ('read', 'write'))
) WITHOUT ROWID;

-- The reverse lookup is the data footprint of a feature: which tables a
-- capability reads and which it writes, which is what a privacy review or a
-- migration plan actually needs.
CREATE INDEX sql_operation_tables_table_idx ON sql_operation_tables(table_name, access);

CREATE TABLE sql_operation_predicates (
  operation_id INTEGER NOT NULL REFERENCES sql_operations(id) ON DELETE CASCADE,
  clause       TEXT    NOT NULL,
  table_name   TEXT    NOT NULL DEFAULT '',
  column_name  TEXT    NOT NULL,
  operator     TEXT    NOT NULL,
  bound        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (operation_id, clause, table_name, column_name, operator),
  CHECK (clause IN ('where', 'join'))
) WITHOUT ROWID;

CREATE INDEX sql_operation_predicates_col_idx
  ON sql_operation_predicates(table_name, column_name);

CREATE TABLE sql_tables (
  name      TEXT    NOT NULL PRIMARY KEY,
  file_path TEXT    NOT NULL,
  line      INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE sql_indexes (
  table_name TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  columns    TEXT    NOT NULL,
  is_unique  INTEGER NOT NULL DEFAULT 0,
  predicate  TEXT    NOT NULL DEFAULT '',
  origin     TEXT    NOT NULL,
  file_path  TEXT    NOT NULL,
  line       INTEGER NOT NULL,
  PRIMARY KEY (table_name, name),
  CHECK (origin IN ('create-index', 'primary-key', 'unique-constraint'))
) WITHOUT ROWID;
