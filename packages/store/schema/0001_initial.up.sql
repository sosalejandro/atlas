-- Initial Atlas v1 schema per docs/schema-v1.md §5. The schema_version table
-- is bootstrapped out-of-band by the embedded migration runner before this
-- file is applied, so the very first migration can co-exist with version
-- tracking.

-- ---------- Config (runtime knobs, key/value; spec §5.2) ----------
CREATE TABLE config (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ---------- Features (spec §5.3) ----------
CREATE TABLE features (
  id               TEXT    PRIMARY KEY,
  title            TEXT    NOT NULL,
  owner            TEXT,
  kind             TEXT    NOT NULL DEFAULT 'feature',
  deprecated_since TEXT,
  introduced_in    TEXT,
  created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('feature', 'contract'))
);

-- ---------- Symbols (spec §5.4) ----------
CREATE TABLE symbols (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  qualified_name TEXT    NOT NULL UNIQUE,
  kind           TEXT    NOT NULL,
  file_path      TEXT    NOT NULL,
  line           INTEGER NOT NULL,
  end_line       INTEGER,
  package        TEXT,
  bc_path        TEXT,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('type', 'func', 'method', 'interface', 'var', 'const'))
);

CREATE INDEX symbols_file_idx    ON symbols(file_path);
CREATE INDEX symbols_package_idx ON symbols(package);
CREATE INDEX symbols_bc_idx      ON symbols(bc_path);

-- ---------- Edges (spec §5.5) ----------
CREATE TABLE edges (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  from_symbol_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  to_symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  kind           TEXT    NOT NULL,
  file_path      TEXT    NOT NULL,
  line           INTEGER NOT NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('call', 'implement', 'embed', 'construct'))
);

CREATE INDEX edges_from_idx ON edges(from_symbol_id);
CREATE INDEX edges_to_idx   ON edges(to_symbol_id);
CREATE UNIQUE INDEX edges_dedupe_idx
  ON edges(from_symbol_id, to_symbol_id, kind, file_path, line);

-- ---------- Feature ↔ Symbol link (spec §5.6) ----------
CREATE TABLE feature_symbols (
  feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
  symbol_id  INTEGER NOT NULL REFERENCES symbols(id)  ON DELETE CASCADE,
  role       TEXT    NOT NULL,
  source     TEXT    NOT NULL DEFAULT 'annotation',
  PRIMARY KEY (feature_id, symbol_id, role),
  CHECK (role IN ('test', 'impl', 'contract')),
  CHECK (source IN ('annotation', 'inferred'))
);

-- ---------- File hashes (spec §5.7; incremental-scan driver) ----------
CREATE TABLE file_hashes (
  file_path     TEXT    PRIMARY KEY,
  content_hash  TEXT    NOT NULL,
  mtime         TIMESTAMP NOT NULL,
  last_scanned  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX file_hashes_last_scanned_idx ON file_hashes(last_scanned);

-- ---------- Coverage runs (spec §5.8) ----------
CREATE TABLE coverage_runs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  framework    TEXT    NOT NULL,
  started_at   TIMESTAMP NOT NULL,
  finished_at  TIMESTAMP NOT NULL,
  raw_path     TEXT,
  summary_json TEXT    NOT NULL DEFAULT '{}',
  CHECK (framework IN ('go-test', 'playwright', 'vitest', 'jest', 'maestro'))
);

CREATE INDEX coverage_runs_framework_idx ON coverage_runs(framework, finished_at);

-- ---------- Coverage results (spec §5.9) ----------
CREATE TABLE coverage_results (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id      INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  symbol_id   INTEGER REFERENCES symbols(id)  ON DELETE SET NULL,
  feature_id  TEXT    REFERENCES features(id) ON DELETE SET NULL,
  status      TEXT    NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  message     TEXT,
  CHECK (status IN ('pass', 'fail', 'skip'))
);

CREATE INDEX coverage_results_run_idx     ON coverage_results(run_id);
CREATE INDEX coverage_results_symbol_idx  ON coverage_results(symbol_id);
CREATE INDEX coverage_results_feature_idx ON coverage_results(feature_id);

-- ---------- Audit snapshots (spec §5.10) ----------
CREATE TABLE audit_snapshots (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  taken_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  feature_id              TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
  score                   INTEGER NOT NULL,
  layer_scores_json       TEXT NOT NULL DEFAULT '{}',
  blocking_findings_json  TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX audit_snapshots_feature_idx ON audit_snapshots(feature_id, taken_at);

-- ---------- Annotations (spec §5.11; raw pre-resolution rows) ----------
CREATE TABLE annotations (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  file_path TEXT    NOT NULL,
  line      INTEGER NOT NULL,
  kind      TEXT    NOT NULL,
  value     TEXT    NOT NULL,
  source    TEXT    NOT NULL DEFAULT 'atlas',
  parsed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (kind IN ('feature', 'contract', 'owner', 'deprecated', 'since')),
  CHECK (source IN ('atlas', 'testreg'))
);

CREATE INDEX annotations_file_idx ON annotations(file_path);
CREATE UNIQUE INDEX annotations_dedupe_idx ON annotations(file_path, line, kind);
