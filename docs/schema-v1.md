# Atlas SQLite Schema — v1 Reference (Phase 0)

Status: **draft / Phase 0 capture**. The schema described here is the target
shape for `packages/store/schema/0001_initial.up.sql`. It is the authoritative
reference for what the SQLite cache will look like when Phase 4 (SQLite store)
ships. Anything in `packages/store/` that disagrees with this document is a bug
in one of the two and must be reconciled before merge.

---

## 1. Purpose

Atlas persists **derived state** in a single SQLite database file per project.
The **source of truth lives in code** — `@atlas:<kind> <id>` annotations,
Go/TS/SQL source files, test outputs. SQLite is:

- A **cache** for parsed AST data (symbols, edges, file hashes) so subsequent
  runs only re-scan files that actually changed.
- A **query index** for fast lookup by feature ID, by symbol qualified name,
  by file path, or by coverage status — without re-parsing the whole tree.
- A **snapshot store** for coverage runs and audit results that need to be
  diffed across commits.

**Re-deleting the database is always safe.** Every row is re-derivable from
code. The only state that lives _only_ in SQLite is:

- Bulk-imported legacy YAML registries from the Phase 9 cutover (and even
  those are archived under `docs/testing/registry/_legacy/` for reference).
- Historical coverage runs (each run is also re-derivable by re-running the
  underlying test framework, but the SQLite copy is the indexed view).

If a developer's database gets into a weird state, the answer is always
`rm atlas-state.db && atlas init` — never schema surgery by hand.

---

## 2. Storage Location

Default path: `atlas-state.db` at the project root (sibling of `.atlas.yaml`).

Overrides (precedence high → low):

1. `--state <path>` CLI flag on any `atlas` subcommand.
2. `ATLAS_STATE` environment variable.
3. `state_path:` field in `.atlas.yaml`.
4. Default `./atlas-state.db`.

**Gitignored by default.** `atlas init` appends `atlas-state.db` (and any
`-wal` / `-shm` siblings) to the repo's `.gitignore` if not already present.
The database is local-only cache; committing it would create merge conflicts
on every branch and leak per-developer scan timestamps.

---

## 3. Connection Setup

Mirrors the pattern used by [`bmad-story-runner-cli`'s `db.go`][bmad-db]:

```go
dsn := fmt.Sprintf(
    "file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)",
    path,
)
conn, err := sql.Open("sqlite", dsn)
```

DSN pragmas, one per `_pragma=...` parameter:

| Pragma                | Value | Why                                                                                                                                                |
| --------------------- | ----- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `journal_mode`        | `WAL` | Concurrent readers don't block on a single writer; crash recovery is a checkpoint replay, not a full rollback.                                     |
| `foreign_keys`        | `1`   | SQLite ships with FK enforcement off by default. Atlas relies on `ON DELETE CASCADE` for `feature_symbols`, `coverage_results`, etc.               |
| `busy_timeout`        | `5000`| Five-second wait before `SQLITE_BUSY` surfaces. Lets a long-running scan finish before a read aborts.                                              |

One writer per process is the contract. Multiple Atlas processes against the
same DB file are not supported (Atlas always runs as a single CLI invocation).

`Open(ctx, path)` runs the embedded migration runner before returning a
`*DB`. Callers do not see un-migrated state.

[bmad-db]: https://github.com/sosalejandro/bmad-story-runner-cli/blob/main/infrastructure/state/sqlite/db.go

---

## 4. Migrations

Schema lives in `packages/store/schema/`. Files are numbered with a four-digit
prefix and a snake_case label:

```
packages/store/schema/
├── 0001_initial.up.sql
└── 0002_<future>.up.sql
```

Embedded into the binary via `//go:embed schema/*.sql` and applied by
[`github.com/golang-migrate/migrate/v4`][gm] (the `iofs` source + the
modernc-backed `sqlite` driver). The runner:

1. **Discover** — `iofs.New(schemaFS, "schema")` enumerates `NNNN_<name>.up.sql`
   from the embedded filesystem and orders them by the numeric prefix.
2. **Track** — golang-migrate creates and maintains its default
   `schema_migrations` table (`version BIGINT PRIMARY KEY, dirty BOOLEAN`)
   the first time `m.Up()` runs. Atlas never writes to that table directly;
   `Store.SchemaVersion(ctx)` is a read-only convenience that returns
   `MAX(version)`.
3. **Apply** — each pending migration runs in a per-statement transaction
   driven by the sqlite driver. On crash mid-migration the row is flagged
   `dirty=1`; resolving that state requires `migrate force <version>` at
   the CLI (Atlas does not auto-resolve dirty state — we surface it instead
   so the operator decides).

**Up-only migrations.** Atlas does NOT ship `*.down.sql` files (locked
decision in `docs/architecture.md` §3.7). golang-migrate tolerates their
absence — it simply loses the ability to step down past a version, which
Atlas doesn't need. Rollback is "delete the file and re-init" — safe
because the database is a re-derivable cache, not the source of truth.
If a migration needs to be reversed, ship a new forward-direction
migration that undoes it.

**Installing the tooling.** Developers who edit `packages/store/queries/*.sql`
must run `cd packages/store && sqlc generate` and commit the regenerated
`packages/store/sqlc/`. CI enforces this with `sqlc diff` (see
`.github/workflows/ci.yml`). If `sqlc` is not on `PATH`:

```bash
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
```

[gm]: https://github.com/golang-migrate/migrate

---

## 5. Initial Schema (Migration 0001)

All tables defined below live in `0001_initial.up.sql`. The
`schema_migrations` table is created automatically by golang-migrate the
first time `Open` runs — it is not part of `0001_initial.up.sql`.

### 5.1 `schema_migrations` (managed by golang-migrate)

Created and maintained by the migration runner. Atlas reads it via
`Store.SchemaVersion(ctx)` for diagnostics and CLI commands like
`atlas doctor`; nothing in Atlas writes to it directly.

| Column    | Type    | Notes                                                                  |
| --------- | ------- | ---------------------------------------------------------------------- |
| `version` | BIGINT  | Primary key. Numeric prefix from the latest applied migration filename. |
| `dirty`   | BOOLEAN | `1` if a migration crashed mid-apply. Resolve via `migrate force`.      |

### 5.2 `config` — runtime knobs (key/value)

```sql
CREATE TABLE config (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

| Column       | Type      | Notes                                                                                  |
| ------------ | --------- | -------------------------------------------------------------------------------------- |
| `key`        | TEXT      | Stable identifier, dot-namespaced. e.g. `log.level`, `scan.default_scope`, `cache.ttl_minutes`. |
| `value`      | TEXT      | Raw string. JSON-encoded for structured values; the application layer parses on read.   |
| `updated_at` | TIMESTAMP | Touched on every successful `INSERT OR REPLACE`.                                       |

Read-only at scan time. Written exclusively by `atlas config set <key>
<value>`. The initial population of this table is part of `atlas init` — the
fields from `.atlas.yaml` get mirrored here so the running binary doesn't
need to re-parse YAML for every CLI invocation.

Reserved keys (Atlas v0):

- `log.level` — `debug | info | warn | error`. Default `info`.
- `scan.default_scope` — comma-separated list of paths; e.g. `src,apps`.
- `cache.ttl_minutes` — integer. How long a file-hash row is trusted before
  Atlas re-stats the file. Default `60`.
- `annotations.legacy_testreg` — `true | false`. When true, `@testreg <id>`
  is accepted as an alias for `@atlas:feature <id>`. Default `true` until
  Phase 9 cutover completes, then settable to `false`.

### 5.3 `features` — Atlas's notion of a "feature"

```sql
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
```

| Column             | Type      | Notes                                                                                                            |
| ------------------ | --------- | ---------------------------------------------------------------------------------------------------------------- |
| `id`               | TEXT PK   | Dotted lower-snake-case, e.g. `auth.login`, `meals.create`. Stable across rescans; what annotations refer to.    |
| `title`            | TEXT      | Human-readable label. From `@atlas:feature <id> title="…"` or the YAML import. Falls back to a humanised `id`.   |
| `owner`            | TEXT      | Optional. From `@atlas:owner` annotation or YAML. Typically a team handle or maintainer name.                    |
| `kind`             | TEXT      | `feature` (default) — testable product behaviour. `contract` — an API contract surface (no separate test cycle). |
| `deprecated_since` | TEXT      | Optional. Free-form version/date string from `@atlas:deprecated`. Drives audit warnings.                          |
| `introduced_in`    | TEXT      | Optional. Free-form version/date string from `@atlas:since`. Useful for changelog generation.                    |
| `created_at`       | TIMESTAMP | First time Atlas saw this ID.                                                                                    |
| `updated_at`       | TIMESTAMP | Touched on any metadata change.                                                                                  |

Backed by the `Feature` domain type at
`internal/domain/feature.go`. Surfaces (web/mobile/API) and coverage shape
from the legacy YAML model are **NOT** stored as columns; they are
recomputed views (see §7 read patterns).

### 5.4 `symbols` — every named entity discovered by the scanner

```sql
CREATE TABLE symbols (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  qualified_name  TEXT    NOT NULL UNIQUE,
  kind            TEXT    NOT NULL,
  file_path       TEXT    NOT NULL,
  line            INTEGER NOT NULL,
  end_line        INTEGER,
  package         TEXT,
  bc_path         TEXT,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  pattern_matches TEXT,   -- Phase 6f, added in migration 0002
  CHECK (kind IN ('type', 'func', 'method', 'interface', 'var', 'const'))
);

CREATE INDEX symbols_file_idx     ON symbols(file_path);
CREATE INDEX symbols_package_idx  ON symbols(package);
CREATE INDEX symbols_bc_idx       ON symbols(bc_path);
```

| Column            | Type      | Notes                                                                                                                          |
| ----------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `id`              | INTEGER   | Surrogate PK; lets edges + feature_symbols use compact integer FKs.                                                            |
| `qualified_name`  | TEXT      | Fully qualified, language-aware. Go: `github.com/foo/bar/pkg.Type.Method`. TS: `apps/web/src/foo.tsx::useFoo`. **UNIQUE.**     |
| `kind`            | TEXT      | One of `type`, `func`, `method`, `interface`, `var`, `const`. Mirrors `domain.NodeKind` where applicable.                       |
| `file_path`       | TEXT      | Path **relative to the project root** so the DB is portable across worktrees.                                                  |
| `line`            | INTEGER   | 1-based first line of the symbol's declaration.                                                                                |
| `end_line`        | INTEGER   | 1-based last line. NULL if the scanner couldn't determine it (e.g. some TS expression contexts).                                |
| `package`         | TEXT      | Go: import path of the package. TS: the nearest `package.json`'s `name`. Optional.                                              |
| `bc_path`         | TEXT      | Bounded context path, e.g. `src/contexts/identity`. Computed once on insert from `file_path`. Optional for non-BC code.        |
| `created_at`      | TIMESTAMP | First time this symbol was indexed.                                                                                            |
| `pattern_matches` | TEXT      | JSON-encoded `[]patterns.Match` set produced by codeindex/patterns recognisers (Phase 6f). NULL when the symbol has no hits.    |

The unique constraint on `qualified_name` is the cache key. Re-scanning the
same file yields the same qualified name, so subsequent runs `INSERT OR
IGNORE` and skip duplicates without writes.

### 5.5 `edges` — directed call / implement / embed / construct relationships

```sql
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
```

| Column           | Type    | Notes                                                                                                                  |
| ---------------- | ------- | ---------------------------------------------------------------------------------------------------------------------- |
| `id`             | INTEGER | Surrogate PK.                                                                                                          |
| `from_symbol_id` | INTEGER | The caller / implementor / embedder / constructor. FK → `symbols(id)`. `ON DELETE CASCADE` so removing a symbol's edges is automatic. |
| `to_symbol_id`   | INTEGER | The callee / interface / embedded type / constructed type. FK → `symbols(id)`.                                          |
| `kind`           | TEXT    | `call` (function call), `implement` (type implements interface), `embed` (struct embeds another type), `construct` (Wire/Fx provider builds this type). |
| `file_path`      | TEXT    | Where the edge was observed. Relative path. A single from→to pair can have multiple edges if invoked from multiple sites. |
| `line`           | INTEGER | 1-based line of the call/implement/embed/construct site.                                                                |
| `created_at`     | TIMESTAMP | First time this exact edge was recorded.                                                                              |

The composite unique index on `(from, to, kind, file, line)` lets the
incremental scanner safely re-emit edges without producing duplicates —
re-indexing a single file is `DELETE FROM edges WHERE file_path = ?`
followed by `INSERT OR IGNORE`.

### 5.6 `feature_symbols` — link table between features and symbols

```sql
CREATE TABLE feature_symbols (
  feature_id TEXT    NOT NULL REFERENCES features(id) ON DELETE CASCADE,
  symbol_id  INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  role       TEXT    NOT NULL,
  source     TEXT    NOT NULL DEFAULT 'annotation',
  PRIMARY KEY (feature_id, symbol_id, role),
  CHECK (role IN ('test', 'impl', 'contract')),
  CHECK (source IN ('annotation', 'inferred'))
);
```

| Column       | Type    | Notes                                                                                                                                              |
| ------------ | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `feature_id` | TEXT    | FK → `features(id)`. Cascades on delete.                                                                                                           |
| `symbol_id`  | INTEGER | FK → `symbols(id)`. Cascades on delete.                                                                                                            |
| `role`       | TEXT    | `test` — symbol is a test that exercises the feature. `impl` — symbol is part of the feature's implementation. `contract` — symbol defines the feature's API surface. |
| `source`     | TEXT    | `annotation` — derived from an `@atlas:feature` (or legacy `@testreg`) comment. `inferred` — Atlas walked the graph and concluded membership.       |

The composite PK `(feature_id, symbol_id, role)` is the uniqueness
invariant: a symbol can be both an `impl` and a `test` for the same
feature, but cannot be listed twice as `impl`. Re-scanning is `INSERT OR
IGNORE`.

### 5.7 `file_hashes` — incremental-scan driver

```sql
CREATE TABLE file_hashes (
  file_path     TEXT    PRIMARY KEY,
  content_hash  TEXT    NOT NULL,
  mtime         TIMESTAMP NOT NULL,
  last_scanned  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX file_hashes_last_scanned_idx ON file_hashes(last_scanned);
```

| Column         | Type      | Notes                                                                                                                          |
| -------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `file_path`    | TEXT PK   | Project-relative path.                                                                                                         |
| `content_hash` | TEXT      | Hex SHA-256 of the file contents at last scan. Cheap to compute, sufficient to detect any edit.                                |
| `mtime`        | TIMESTAMP | File modification time at last scan. Lets the incremental scanner short-circuit (skip hash compute) when mtime is unchanged.    |
| `last_scanned` | TIMESTAMP | When Atlas last walked this file. Drives `cache.ttl_minutes` invalidation.                                                     |

The scan loop is:

```
for each candidate file:
    stat → if mtime ≤ row.mtime: skip
    sha256 → if hash == row.content_hash: update mtime + last_scanned; skip parse
    else: DELETE FROM symbols/edges WHERE file_path = ?
          re-parse, INSERT new rows
          UPDATE file_hashes SET content_hash, mtime, last_scanned
```

### 5.8 `coverage_runs` — one row per ingested test-framework run

```sql
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
```

| Column         | Type      | Notes                                                                                                            |
| -------------- | --------- | ---------------------------------------------------------------------------------------------------------------- |
| `id`           | INTEGER   | Surrogate PK.                                                                                                    |
| `framework`    | TEXT      | One of `go-test`, `playwright`, `vitest`, `jest`, `maestro`. Atlas's v0 supported set.                            |
| `started_at`   | TIMESTAMP | From the framework's report if available; otherwise the ingest start time.                                       |
| `finished_at`  | TIMESTAMP | From the report; otherwise the ingest end time.                                                                  |
| `raw_path`     | TEXT      | Optional. Path to the raw test output (e.g. `go test -json` JSONL file, Playwright HTML report dir).             |
| `summary_json` | TEXT      | JSON blob with framework-specific aggregate stats (pass/fail/skip counts, duration totals).                       |

### 5.9 `coverage_results` — per-test (or per-symbol) outcome rows

```sql
CREATE TABLE coverage_results (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id      INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  symbol_id   INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
  feature_id  TEXT    REFERENCES features(id) ON DELETE SET NULL,
  status      TEXT    NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  message     TEXT,
  CHECK (status IN ('pass', 'fail', 'skip'))
);

CREATE INDEX coverage_results_run_idx     ON coverage_results(run_id);
CREATE INDEX coverage_results_symbol_idx  ON coverage_results(symbol_id);
CREATE INDEX coverage_results_feature_idx ON coverage_results(feature_id);
```

| Column        | Type    | Notes                                                                                                                              |
| ------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `id`          | INTEGER | Surrogate PK.                                                                                                                      |
| `run_id`      | INTEGER | FK → `coverage_runs(id)`. Cascade so deleting a run removes its results.                                                            |
| `symbol_id`   | INTEGER | FK → `symbols(id)`. Nullable: Playwright/Maestro tests don't map to a Go symbol. Set to NULL if the symbol is later removed.        |
| `feature_id`  | TEXT    | FK → `features(id)`. Nullable: legacy tests without an `@atlas:feature` annotation may not map to a feature. Set to NULL on delete. |
| `status`      | TEXT    | `pass`, `fail`, or `skip`.                                                                                                         |
| `duration_ms` | INTEGER | Per-test runtime. `0` if the framework didn't report it.                                                                           |
| `message`     | TEXT    | Failure message / skip reason. NULL for `pass`.                                                                                    |

### 5.10 `audit_snapshots` — health-score history

```sql
CREATE TABLE audit_snapshots (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  taken_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  feature_id              TEXT NOT NULL REFERENCES features(id) ON DELETE CASCADE,
  score                   INTEGER NOT NULL,
  layer_scores_json       TEXT NOT NULL DEFAULT '{}',
  blocking_findings_json  TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX audit_snapshots_feature_idx ON audit_snapshots(feature_id, taken_at);
```

| Column                   | Type      | Notes                                                                                                          |
| ------------------------ | --------- | -------------------------------------------------------------------------------------------------------------- |
| `id`                     | INTEGER   | Surrogate PK.                                                                                                  |
| `taken_at`               | TIMESTAMP | When the audit ran.                                                                                            |
| `feature_id`             | TEXT      | FK → `features(id)`. Cascades.                                                                                 |
| `score`                  | INTEGER   | `0`–`100`. Computed by `packages/audit/score.go` from the ported `audit_feature.go` algorithm.                 |
| `layer_scores_json`      | TEXT      | JSON object, e.g. `{"handler": 80, "service": 70, "repo": 90}`. Matches `domain.LayerCoverage`.                |
| `blocking_findings_json` | TEXT      | JSON array of `domain.AuditGap`-shaped objects. Drives the "must-fix before release" list in `atlas audit`.    |

Snapshots accumulate over time so `atlas diff` can compare commits.

### 5.11 `annotations` — raw extracted annotations (pre-resolution)

```sql
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
```

| Column      | Type      | Notes                                                                                                                                       |
| ----------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`        | INTEGER   | Surrogate PK.                                                                                                                               |
| `file_path` | TEXT      | Project-relative path.                                                                                                                      |
| `line`      | INTEGER   | 1-based line of the comment.                                                                                                                |
| `kind`      | TEXT      | `feature`, `contract`, `owner`, `deprecated`, `since`. Matches Atlas's annotation grammar verbs from `docs/annotations.md`.                  |
| `value`     | TEXT      | Raw value after the kind keyword. e.g. for `// @atlas:feature auth.login`: `value = "auth.login"`. Tags are part of the raw value string.   |
| `source`    | TEXT      | `atlas` for new-style `@atlas:<kind>`, `testreg` for legacy `// @testreg <id>`. Lets the migration tool target only legacy rows.            |
| `parsed_at` | TIMESTAMP | When the parser saw this annotation.                                                                                                        |

The unique constraint `(file_path, line, kind)` enforces the invariant that
a single source line can carry at most one annotation of any given kind.
The same line CAN carry e.g. both `@atlas:feature auth.login` and
`@atlas:owner @auth-team` if they are on adjacent comment lines (different
`line` values), but a single line cannot redeclare the same kind.

This table is the raw extract, before resolution into `feature_symbols`.
The annotation parser writes here; a separate resolution pass reads from
here, looks up the nearest symbol below the annotation line, and emits the
appropriate `feature_symbols` row.

---

## 6. Partial Unique Indices and Invariants

| Invariant                                                                | Where enforced                                                          |
| ------------------------------------------------------------------------ | ----------------------------------------------------------------------- |
| One annotation per (file, line, kind)                                    | `annotations_dedupe_idx` (UNIQUE)                                       |
| One feature_symbols row per (feature, symbol, role)                      | `feature_symbols` PRIMARY KEY                                           |
| One edge per (from, to, kind, file, line)                                | `edges_dedupe_idx` (UNIQUE) — re-scans are idempotent                  |
| Symbol qualified names globally unique                                   | `symbols.qualified_name UNIQUE`                                         |
| One file_hashes row per file path                                        | `file_hashes.file_path` is the PRIMARY KEY                              |
| Schema versions never reapplied                                          | `schema_version.version` PRIMARY KEY + idempotent runner skip-logic     |

No partial unique indices are needed in v1 (none of the tables have an
"active vs reclaimed" lifecycle like bmad-cli's `env_allocations`). If
future schema versions introduce one — e.g. soft-deleted features — the
pattern from
`bmad-story-runner-cli/.../schema/0002_env_port_uniqueness.up.sql` is the
reference: `CREATE UNIQUE INDEX … WHERE deleted_at IS NULL`.

---

## 7. Read Patterns

The sample queries below cover the recurring application-layer reads.
Anything more exotic should be added here as it surfaces.

### 7.1 All symbols affected by feature X

```sql
SELECT s.id, s.qualified_name, s.kind, s.file_path, s.line, fs.role
FROM features f
JOIN feature_symbols fs ON fs.feature_id = f.id
JOIN symbols s          ON s.id          = fs.symbol_id
WHERE f.id = ?
ORDER BY fs.role, s.file_path, s.line;
```

Used by `atlas trace <feature-id>` to enumerate the implementation surface
before walking the edge graph.

### 7.2 Call chain from a graph entry point (recursive CTE)

```sql
WITH RECURSIVE chain(from_id, to_id, depth, path) AS (
  SELECT e.from_symbol_id, e.to_symbol_id, 1,
         s.qualified_name || ' → ' || t.qualified_name
  FROM edges e
  JOIN symbols s ON s.id = e.from_symbol_id
  JOIN symbols t ON t.id = e.to_symbol_id
  WHERE e.from_symbol_id = ?
    AND e.kind = 'call'
  UNION ALL
  SELECT c.to_id, e.to_symbol_id, c.depth + 1,
         c.path || ' → ' || t.qualified_name
  FROM chain c
  JOIN edges  e ON e.from_symbol_id = c.to_id AND e.kind = 'call'
  JOIN symbols t ON t.id = e.to_symbol_id
  WHERE c.depth < ?   -- maxDepth guardrail
)
SELECT depth, path FROM chain ORDER BY depth;
```

Replaces the in-memory graph walk in `domain.Graph.TraceFrom` for the
SQLite-backed path. The Go layer still wraps cycle detection — a recursive
CTE will happily revisit nodes; the application caps depth and dedupes.

### 7.3 Coverage status per feature

```sql
SELECT f.id,
       f.title,
       COUNT(DISTINCT fs.symbol_id)                            AS impl_symbols,
       COUNT(DISTINCT CASE WHEN cr.status = 'pass' THEN cr.id END) AS passing,
       COUNT(DISTINCT CASE WHEN cr.status = 'fail' THEN cr.id END) AS failing,
       COUNT(DISTINCT CASE WHEN cr.status = 'skip' THEN cr.id END) AS skipped
FROM features f
LEFT JOIN feature_symbols fs ON fs.feature_id = f.id AND fs.role = 'impl'
LEFT JOIN coverage_results cr ON cr.feature_id = f.id
GROUP BY f.id, f.title
ORDER BY f.id;
```

Powers `atlas cov status`. The `LEFT JOIN` pattern is deliberate: features
with zero coverage rows still appear in the output (as the "you should
write tests" list).

### 7.4 What changed since last scan

```sql
SELECT file_path, last_scanned
FROM file_hashes
WHERE last_scanned < datetime('now', '-' || ? || ' minutes')
ORDER BY last_scanned ASC;
```

Returns files whose cached hash is older than the configured
`cache.ttl_minutes`. The scanner re-stats only these (vs walking the entire
tree every run). The `?` parameter is bound from the `config` table.

### 7.5 Audit-score trend for a single feature

```sql
SELECT taken_at, score, layer_scores_json
FROM audit_snapshots
WHERE feature_id = ?
ORDER BY taken_at DESC
LIMIT 20;
```

Drives `atlas audit history <feature-id>` and the score-delta column of
`atlas diff`.

---

## 8. Write Patterns

Every write goes through one of the package boundaries below. Direct SQL
from `internal/cli/` is forbidden — the CLI layer always calls a package
API which is responsible for the SQL.

| Package          | Tables it writes                                          | Trigger                                                                                       |
| ---------------- | --------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| `codeindex/go`   | `symbols`, `edges`, `file_hashes`                         | `atlas scan` (or `atlas init`); subsequent runs only re-write rows for files whose hash changed. |
| `codeindex/ts`   | `symbols`, `edges`, `file_hashes`                         | Same as Go scanner, on the `apps/**` + `packages/**` trees.                                   |
| `codeindex/annotations` | `annotations`, `feature_symbols`                  | Runs after `codeindex/{go,ts}` so the symbols already exist for FK resolution.                |
| `coverage`       | `coverage_runs`, `coverage_results`                       | `atlas cov sync` after a framework-specific ingest.                                           |
| `audit`          | `audit_snapshots`                                         | `atlas audit` — one row per (feature, run) tuple.                                             |
| `cli/config`     | `config`                                                  | `atlas config set <key> <value>`. Read-only for everyone else.                                |
| `cli/init`       | `config`, `features`                                      | Bootstraps the DB; for YAML imports, also seeds `features` + `feature_symbols`.               |
| `migrate-annotations` | `annotations` (status flip from `testreg` → `atlas`) | `atlas migrate-annotations --apply`. Idempotent.                                              |

**Transaction discipline:**

- Schema migrations: one tx per migration (runner enforces).
- A single file's re-scan: one tx covering `DELETE FROM symbols/edges WHERE
  file_path = ?` plus the new INSERTs plus the `UPDATE file_hashes`. Either
  the file's state in SQLite matches the on-disk file, or it doesn't change
  at all.
- A coverage ingest run: one tx covering the `INSERT INTO coverage_runs` plus
  all the `INSERT INTO coverage_results`. A partial ingest is no ingest.

---

## 9. Future Schema Versions

**Prefer additive migrations.** `ALTER TABLE … ADD COLUMN` is non-blocking
on SQLite and works without disrupting in-flight read connections.
Reference examples:

- [`bmad-story-runner-cli/.../schema/0003_idempotency_and_claim.up.sql`][bmad-0003]
  — adds `idempotency_key` columns plus partial unique indices in one
  migration. Demonstrates how to layer new uniqueness constraints onto an
  existing table without losing pre-existing rows.
- [`bmad-story-runner-cli/.../schema/0004_story_type.up.sql`][bmad-0004] —
  single-line `ALTER TABLE stories ADD COLUMN story_type TEXT NOT NULL
  DEFAULT 'code'`. Demonstrates the smallest possible safe migration:
  default value provided so existing rows backfill instantly.

[bmad-0003]: https://github.com/sosalejandro/bmad-story-runner-cli/blob/main/infrastructure/state/sqlite/schema/0003_idempotency_and_claim.up.sql
[bmad-0004]: https://github.com/sosalejandro/bmad-story-runner-cli/blob/main/infrastructure/state/sqlite/schema/0004_story_type.up.sql

**Breaking migrations** (drop column, rename column, narrow a CHECK) are
last resort. SQLite's `ALTER TABLE` semantics are limited; a breaking
change usually requires:

1. `CREATE TABLE features_new (…)` with the new shape.
2. `INSERT INTO features_new SELECT … FROM features`.
3. `DROP TABLE features` and `ALTER TABLE features_new RENAME TO features`.
4. Recreate every index that referenced the old table.

All four steps live in a single `*.up.sql` file inside one transaction.
Because the DB is a re-derivable cache, there is no down-migration —
delete the file and re-init if you need to roll back, or ship a forward
migration that undoes the previous one.

Candidate v2+ migrations identified during Phase 0 (not yet committed):

- `0002_perf_results` — add a per-symbol benchmark table to support
  `domain.PerfGap` / `domain.PerfScore` (already modelled in
  `internal/domain/audit.go`). Likely needs `benchmark_runs` +
  `benchmark_results` mirroring the coverage pair.
- `0003_contract_types` — persist `domain.ContractType` /
  `domain.ContractField` so `atlas contract` doesn't re-extract on every
  invocation. Probably a `contract_layers` + `contract_fields` pair plus a
  `feature_contracts` link table.
- `0004_diagnose_symptoms` — store
  `internal/domain/symptom.go`'s symptom→symbol mapping so `atlas
  diagnose` is index-driven rather than regex-scanning on every call.

None of those land in v1; each gets its own migration file when the matching
package goes in.

---

## 10. Reset / Debugging

| Situation                                | Command                                       |
| ---------------------------------------- | --------------------------------------------- |
| DB corrupted or in a weird state         | `rm atlas-state.db && atlas init`             |
| Want a fully fresh scan                  | `atlas init --force` (recreates, re-scans)    |
| Inspect what's currently in the DB       | `atlas debug schema` (planned; dumps tables + row counts) |
| Need a one-off SQL session               | `sqlite3 atlas-state.db` (read-only safe)     |
| Just want the migration version          | `sqlite3 atlas-state.db 'SELECT MAX(version) FROM schema_version;'` |

`rm atlas-state.db` is **always safe**. The DB is per-developer cache; no
shared state is lost. The longest-running command in a recovery flow is the
re-scan itself, which Phase 4's acceptance criteria caps at <60s for first
run on nutrition-v2-go.

Because the WAL is a sidecar file, a full reset is technically:

```bash
rm -f atlas-state.db atlas-state.db-wal atlas-state.db-shm
```

The `-wal` and `-shm` files are auto-recreated by SQLite on next `Open`.
`atlas init --force` runs the equivalent removal internally.

---

## 11. Open Questions (Phase 0 hand-off to Phase 4)

These are flagged for the Phase 4 implementer; none block writing the
initial migration but each will need a one-line decision before merge.

1. **Should `coverage_results.duration_ms` be a `REAL` (seconds) instead of
   `INTEGER` (millis)?** Picked INTEGER for the same reason
   `bmad-story-runner-cli/.../dispatches` did (no float rounding).
   Revisit if any framework reports sub-millisecond.
2. **Should `audit_snapshots.layer_scores_json` be a separate table?**
   Storing it as JSON is faster to write but resists SQL aggregation. v1
   keeps it as JSON; a v2 normalised version becomes worthwhile only when
   someone runs `atlas audit trend --by-layer`.
3. **Cross-project DB sharing?** v0 says no — one DB per project root,
   gitignored. If a workspace ever needs a shared atlas DB across multiple
   project roots (e.g. a monorepo with multiple `.atlas.yaml` files), the
   scope key shifts from "project" to "(project_root, file_path)" and most
   FKs above need an additional `project_id` column. Out of scope for v1.
