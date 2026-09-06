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
| `id`               | TEXT PK   | Dotted lowercase, e.g. `auth.login`, `meals.create`, `plans-patient.export-pdf`. Grammar `[a-z0-9_-]+(\.[a-z0-9_-]+)*` — both snake (`meal_prep.batch_session`) and kebab (`email-relay.dlq`) segments are valid; dot is the segment separator. Stable across rescans; what annotations refer to. |
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
  pattern_matches TEXT,   -- Phase 6f, added in migration 0003
  CHECK (kind IN ('type', 'func', 'method', 'interface', 'var', 'const'))
);

CREATE INDEX symbols_file_idx     ON symbols(file_path);
CREATE INDEX symbols_package_idx  ON symbols(package);
CREATE INDEX symbols_bc_idx       ON symbols(bc_path);
```

| Column            | Type      | Notes                                                                                                                          |
| ----------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `id`              | INTEGER   | Surrogate PK; lets edges + feature_symbols use compact integer FKs.                                                            |
| `qualified_name`  | TEXT      | Language-aware symbol id. Go: `Type.Method` / `pkg.Func`, package-qualified (`contexts/billing/domain.Type.Method`) when that short form is already taken by another package — see *Symbol identity* below. TS: `apps/web/src/foo.tsx::useFoo`. **UNIQUE.** |
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

#### Symbol identity (Go)

The Go scanner registers a declaration under its **short** id — `Type.Method`
for methods, `pkg.Func` for plain functions — because that is what
`@atlas:feature` annotations, `atlas trace` arguments and stored feature links
refer to. Short ids are not globally unique: any monorepo where two bounded
contexts each declare a `Chat` or a `NewAvailabilityService` produces
collisions. When a short id is already taken by a declaration in a **different
file**, the scanner falls back, in order, to:

1. `<packageDir>.<Type>.<Method>` (or `<packageDir>.<Func>`), then
2. `<packageDir>.<Type>.<Method>#<file>.go` — two packages in one directory.

Walk order is lexical, so which declaration keeps the short id is stable for a
given file set, and every collision is reported as a scan warning. Before this
rule the second declaration was silently dropped from the graph — and with it
every other symbol in its file, which is what made ~25% of a coverage profile
unattributable (issue #85).

`end_line` matters for the same reason: the coverage ingest charges an
executed statement to the symbol whose `[line, end_line]` span contains it.
When `end_line` is NULL the span is guessed from the next symbol's start line,
so statements belonging to declarations atlas did not index get charged to
whichever neighbour precedes them, and the last symbol in a file absorbs
everything to EOF. The Go scanner always emits it.

### 5.5 `edges` — directed call / implement / embed / construct relationships

```sql
-- as of migration 0018
CREATE TABLE edges (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  from_symbol_id  INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  to_symbol_id    INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  kind            TEXT    NOT NULL,
  file_path       TEXT    NOT NULL,
  line            INTEGER NOT NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  edge_meta       TEXT,
  resolution_tier TEXT    NOT NULL,
  ambiguous       INTEGER NOT NULL DEFAULT 0,
  CHECK (kind IN (
    'call', 'implement', 'embed', 'construct',
    'inheritance', 'decorator', 'import'
  )),
  CHECK (resolution_tier IN (
    'typed', 'name_resolved', 'syntactic', 'imported'
  )),
  CHECK (ambiguous IN (0, 1))
);

CREATE INDEX edges_from_idx ON edges(from_symbol_id);
CREATE INDEX edges_to_idx   ON edges(to_symbol_id);
CREATE INDEX edges_tier_idx ON edges(resolution_tier);
CREATE UNIQUE INDEX edges_dedupe_idx
  ON edges(from_symbol_id, to_symbol_id, kind, file_path, line);
```

| Column            | Type    | Notes                                                                                                                  |
| ----------------- | ------- | ---------------------------------------------------------------------------------------------------------------------- |
| `id`              | INTEGER | Surrogate PK.                                                                                                          |
| `from_symbol_id`  | INTEGER | The caller / implementor / embedder / constructor. FK → `symbols(id)`. `ON DELETE CASCADE` so removing a symbol's edges is automatic. |
| `to_symbol_id`    | INTEGER | The callee / interface / embedded type / constructed type. FK → `symbols(id)`.                                          |
| `kind`            | TEXT    | `call` (function call), `implement` (type implements interface), `embed` (struct embeds another type), `construct` (Wire/Fx provider builds this type), plus the Python-scanner kinds `inheritance`, `decorator`, `import` (migration 0007). |
| `file_path`       | TEXT    | Where the edge was observed. Relative path. A single from→to pair can have multiple edges if invoked from multiple sites. |
| `line`            | INTEGER | 1-based line of the call/implement/embed/construct site.                                                                |
| `created_at`      | TIMESTAMP | First time this exact edge was recorded.                                                                              |
| `edge_meta`       | TEXT    | Optional kind-specific qualifier (migration 0008). Python `import` edges carry the lexical scope: `module`, `function`, `conditional`, `type_checking`, `try_guard`. NULL for every other kind. |
| `resolution_tier` | TEXT    | Which mechanism resolved this edge (migration 0018). See below.                                                          |
| `ambiguous`       | INTEGER | 0/1. The resolver had more than one candidate and picked one.                                                           |

The composite unique index on `(from, to, kind, file, line)` lets the
incremental scanner safely re-emit edges without producing duplicates —
re-indexing a single file is `DELETE FROM edges WHERE file_path = ?`
followed by `INSERT OR IGNORE`. Neither `edge_meta` nor
`resolution_tier` joins that key: two mechanisms that produce the same
relationship at the same call site are one relationship, and storing
both would double-count it in every walk and every histogram.

#### 5.5.1 `resolution_tier` — provenance per edge (issue #146)

Atlas derives the same relationship by mechanisms of wildly different
reliability. A callee found in the caller's package scope and a callee
guessed from a case-insensitive substring match on a variable name are
identical in every other column of this table. Without this one, no test
can see a resolver change: a name-heuristic edge and a type-checked edge
are the same row, so counts and `(from, to, kind, file, line)` set diffs
both report "unchanged" across exactly the migration that changes
everything.

The vocabulary is the A/B/C/D set defined in issue #105's tiering
addendum. It is closed — a second taxonomy would make two scanners'
histograms incomparable, which defeats the purpose.

| Tier            | Mechanism                                   | Claims                                                        | Produced today by |
| --------------- | ------------------------------------------- | ------------------------------------------------------------- | ----------------- |
| `typed`         | a type checker (`go/packages` + callgraph)  | exact, including interface dispatch and generic instantiation | nothing yet — this is what #87 lands |
| `name_resolved` | scope-aware name binding                    | the name was bound to a declaration atlas actually indexed    | the Go scanner (`resolveInScope` hits); the Python scanner (imports / base classes / decorators whose target resolved to an indexed symbol) |
| `syntactic`     | the shape of the source, no cross-file binding | a plausible target; it may not exist, and may be the wrong one of several same-named candidates | the Go scanner (fuzzy + DI + unresolved-guess paths, route and `@api` edges, external stubs); the TypeScript scanner (all edges); the Python scanner (all calls, and any target that did not resolve) |
| `imported`      | somebody else's indexer, via SCIP           | whatever that indexer knew                                    | nothing yet — #105 step 1 |

Rules:

- **The scanner that produced the edge sets the tier, explicitly.** No
  layer between the scanner and this table may infer, upgrade or supply
  one; only the scanner knows which mechanism ran.
- **There is no default.** The column is `NOT NULL` with no `DEFAULT`
  and a `CHECK` on the vocabulary, so an insert that omits it or passes
  an empty string fails at the database. `packages/store/edges.go`
  raises the same refusal earlier with a readable message. A default is
  how every edge ends up claiming to be typed.
- **There is deliberately no confidence score.** The prior art this
  borrows from (trace-mcp, credited on #105) seeds weights of
  1.0 / 1.0 / 0.95 / 0.7 / 0.4 from its tiers. Those are calibrated
  against that project's corpus; nobody here has measured ours, and this
  project does not ship numbers it has not measured.
- **Migration 0018 backfilled every pre-existing row at `syntactic`.**
  The tier lives in the resolver's control flow and is not recoverable
  from a stored row, so the choice was between backfilling optimistically
  and backfilling honestly. The weakest tier states the floor that is
  true of every such row, and biases the first post-#87 histogram
  against showing an improvement — the safe direction for a change
  detector to be wrong in. Real per-row tiers come from re-running
  `atlas scan`.

`ambiguous` is `graph.Edge.Ambiguous`, computed by the resolver since
v0.4 and, until 0018, discarded at the storage boundary. It is
orthogonal to the tier and both are kept: a `name_resolved` edge can be
ambiguous (two packages declare the short name) and a `syntactic` one
can be unambiguous (one substring matched — still a guess).

`atlas doctor`'s `index.edge_provenance` check reports the
(language, tier) histogram, with the language derived from the edge's
file extension. It warns when a language's edges are *all* syntactic —
the only threshold applied, and deliberately the degenerate one.

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

**Later migrations add to this table.** Migration 0011 (issue #100) adds the
attribution accounting, and 0012 (issue #86) adds the run group:

```sql
-- 0011_coverage_attribution
ALTER TABLE coverage_runs ADD COLUMN files_in_report     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN files_matched       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN files_unmatched     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN stmts_attributed    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN stmts_unattributed  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_runs ADD COLUMN gaps_truncated      INTEGER NOT NULL DEFAULT 0;

-- 0012_coverage_run_groups
ALTER TABLE coverage_runs ADD COLUMN run_group TEXT;
CREATE INDEX coverage_runs_group_idx ON coverage_runs(run_group, finished_at);
```

| Column               | Type    | Notes                                                                                                                                                                                                             |
| -------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `files_in_report`    | INTEGER | Files the coverage report named. Zero for a run with no accounting (pre-0011, or a pass/fail framework) -- see the all-zero rule below.                                                                            |
| `files_matched`      | INTEGER | Of those, files atlas resolved to indexed symbols.                                                                                                                                                                |
| `files_unmatched`    | INTEGER | Files whose execution atlas could not charge to any symbol.                                                                                                                                                       |
| `stmts_attributed`   | INTEGER | Statements charged to a symbol.                                                                                                                                                                                   |
| `stmts_unattributed` | INTEGER | Statements that ran but reached no symbol. This is the honest size of the coverage blind spot, and it stays exact however the per-file enumeration in `coverage_run_gaps` was capped.                              |
| `gaps_truncated`     | INTEGER | How many gap FILES did not fit the store's per-run cap. Written by the gap insert, not the run insert, so the count and the list are committed together.                                                           |
| `run_group`          | TEXT    | Nullable correlation key (a git SHA, a CI run id) tying several syncs into one measurement. Atlas never interprets it. NULL means the run stands alone, which is the pre-0012 behaviour every existing row keeps. |

An **all-zero counter set is not a perfect attribution**. A run predating 0011,
or one from a framework with no statement coverage, leaves every counter at 0;
readers must report "no accounting recorded" rather than "0 of 0 attributed",
which would read as "nothing was lost".

The **frontier** is what the audit scores: resolve the newest run, and if it
carries a `run_group`, take that whole group; otherwise take that run alone.
Resolving from the newest run OUTWARD (rather than from "the group with the
newest member") is what makes an operator who forgets `--run-group` fall back
to the old single-run semantics instead of silently merging into a stale
group.

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

Migration 0009 (Tier B) adds the statement fractions the line-weighted
coverage score reads:

```sql
ALTER TABLE coverage_results ADD COLUMN covered_stmts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE coverage_results ADD COLUMN total_stmts   INTEGER NOT NULL DEFAULT 0;
```

Both stay 0 for pre-0009 rows and for frameworks that carry no statement
counts (gotest pass/fail, playwright, ...). The audit falls back to the binary
pass model when `total_stmts` is zero across a feature's symbols, so a mixed
store scores each feature by the best evidence it has.

### 5.10 `audit_snapshots` — REMOVED (see migration 0006)

> **Status:** removed by migration `0006_drop_unused_audit_snapshots` (issue
> #21). The per-feature, per-snapshot shape this section originally described
> never matched a real workflow — nothing in Atlas ever wrote to the table.
> Phase 6a added `audit_snapshot_runs` (§5.12) for the whole-project JSON
> blob the algorithm actually persists; the legacy table was dropped as
> tech-debt cleanup in the follow-up. The original schema is preserved
> below for historical reference and is the verbatim content of
> `0006_drop_unused_audit_snapshots.down.sql`.

```sql
-- Removed by migration 0006. Recreated by the down migration so a rollback
-- restores the pre-drop schema (empty — production never wrote to it).
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
  CHECK (kind IN (
    'feature', 'contract', 'owner', 'deprecated', 'since',
    'bc', 'aggregate', 'aggregate-service', 'saga', 'consumer',
    'event-emit', 'outbox-publish'
  )),
  CHECK (source IN ('atlas', 'testreg'))
);

CREATE INDEX annotations_file_idx ON annotations(file_path);
CREATE UNIQUE INDEX annotations_dedupe_idx ON annotations(file_path, line, kind);
```

The seven EDA-pattern kinds (`bc`, `aggregate`, `aggregate-service`, `saga`,
`consumer`, `event-emit`, `outbox-publish`) were added in Phase 6e via
migration `0002_eda_annotation_kinds.up.sql`. SQLite cannot ALTER a CHECK
constraint in place, so the migration rebuilds the table with the extended
CHECK set and copies existing rows over.

| Column      | Type      | Notes                                                                                                                                       |
| ----------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`        | INTEGER   | Surrogate PK.                                                                                                                               |
| `file_path` | TEXT      | Project-relative path.                                                                                                                      |
| `line`      | INTEGER   | 1-based line of the comment.                                                                                                                |
| `kind`      | TEXT      | One of the twelve kinds in the CHECK clause above. Matches Atlas's annotation grammar verbs from `docs/annotations.md` §Known kinds.        |
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

### 5.12 `test_coverage` — per-test execution evidence (migration 0010)

```sql
CREATE TABLE test_coverage (
  run_id         INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  test_symbol_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  symbol_id      INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  covered_stmts  INTEGER NOT NULL DEFAULT 0,
  total_stmts    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, test_symbol_id, symbol_id)
) WITHOUT ROWID;

CREATE INDEX test_coverage_symbol_idx ON test_coverage(run_id, symbol_id);
```

A `coverage_results` row says "this symbol ran during the run". A
`test_coverage` row says "this symbol ran BECAUSE OF this test", which is the
difference between knowing a codebase is covered and knowing what a given
capability's tests actually exercise (issue #104).

That grain makes feature location a set operation rather than a graph walk:
the union of the symbols a feature's annotated tests executed, minus the
symbols nearly every test executes, IS the feature's implementation surface --
correct through interface dispatch, DI and reflection, none of which a static
call-edge walk can follow. The reverse index answers the inverse question --
which tests reach a changed symbol -- which is affected-test selection.

`WITHOUT ROWID` because the table is all key: a run of a few thousand tests
against a few thousand symbols is the largest table in the store, and the
composite PK is the only access path.

### 5.13 `coverage_run_gaps` — the files a run could not attribute (migration 0011)

```sql
CREATE TABLE coverage_run_gaps (
  run_id INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  path   TEXT    NOT NULL,
  stmts  INTEGER NOT NULL DEFAULT 0,
  reason TEXT    NOT NULL,
  PRIMARY KEY (run_id, path)
) WITHOUT ROWID;

CREATE INDEX coverage_run_gaps_loss_idx ON coverage_run_gaps(run_id, stmts DESC);
```

The per-file enumeration behind `coverage_runs.stmts_unattributed`: which
files executed statements atlas could not charge to any symbol, and why (no
indexed symbol for the file at all, or execution outside every known symbol
span).

The list is CAPPED per run, largest loss first, and the number of dropped
files is recorded in `coverage_runs.gaps_truncated`. The run-level totals stay
exact regardless, so the cap narrows the enumeration and never the accounting
-- a truncated list that claimed completeness is the exact failure mode this
table exists to prevent.

### 5.14 `coverage_history` — the per-commit measurement series (migration 0013)

```sql
CREATE TABLE coverage_history (
  id          INTEGER   PRIMARY KEY AUTOINCREMENT,
  commit_sha  TEXT      NOT NULL,
  measured_at TIMESTAMP NOT NULL,
  score       REAL,
  denominator INTEGER   NOT NULL DEFAULT 0,
  note        TEXT
);

CREATE UNIQUE INDEX coverage_history_commit_idx ON coverage_history(commit_sha);
CREATE INDEX coverage_history_measured_idx ON coverage_history(measured_at);

CREATE TABLE coverage_history_features (
  history_id  INTEGER NOT NULL REFERENCES coverage_history(id) ON DELETE CASCADE,
  feature_id  TEXT    NOT NULL,
  score       REAL,
  denominator INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (history_id, feature_id)
) WITHOUT ROWID;
```

Written by `atlas trend record` and by `atlas trend`'s backfill, read by
`atlas trend` and its `--compare-to` regression gate (issue #92). Every other
table here answers "what is true now"; this pair answers "is it getting
better or worse".

**Who writes rows.** `atlas trend record` writes the point for a commit,
scored through the audit. `atlas trend` additionally **backfills** the points
it can derive from `coverage_runs` / `coverage_results` that have no point
yet — statement coverage over each feature's linked impl symbols, one point
per run group, keyed by `run_group` (which CI is encouraged to set to the
commit sha) or by `coverage-run:<id>` when there is none. Backfill never
overwrites an existing point and is skipped under `--no-backfill`. Nothing
else writes here: `atlas cov sync` and `atlas audit` do not.

| Column        | Type      | Notes                                                                                                                                                       |
| ------------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `commit_sha`  | TEXT      | Free text like `snapshots.git_ref` -- Atlas never forks git to validate it, and CI systems legitimately record tags or synthetic ids. UNIQUE; see below.     |
| `measured_at` | TIMESTAMP | Orders the series and tells the reader how stale a point is. Updated on a re-measurement.                                                                    |
| `score`       | REAL      | **NULLABLE, and this is the load-bearing decision.** NULL means no coverage evidence at that commit, which is NOT the same fact as a score of zero.          |
| `denominator` | INTEGER   | The size of the surface the score was computed over, **in the same unit as the score** — statements when statement coverage exists, else scored symbols. See below. |
| `note`        | TEXT      | Optional free-form label (a CI run id, a branch name). Backfilled points carry `backfilled from coverage run <ids>` here.                                    |

**What `score` holds.** The audit's **coverage component**, not
`FeatureHealth.Score`. The overall audit score re-normalises a blend of
coverage, annotation freshness, pattern compliance and contract drift;
recording that in a table `atlas trend` gates on as a coverage regression
would fire the gate on a stale annotation and let a real coverage drop hide
behind another component rising.

**What `denominator` holds, and why the unit matters.** The denominator is
the guard against "deleting a thousand untested lines raises the number
without a single new test", and a guard in the wrong unit is not a guard. The
Tier B coverage score is a fraction of **statements**, so the denominator is
the statement total the coverage frontier reports for the feature's scored
(non-test-role) linked symbols. A denominator counted in `feature_symbols`
rows cannot see a statement-level deletion at all. When no statement data
exists for a feature — the gotest pass/fail model, playwright, maestro — the
coverage signal is itself a fraction of symbols, and the denominator falls
back to the count of scored symbols so the unit still matches the score.

**Why `score` is nullable.** Coverage evidence is routinely absent for a
commit: the docs-only PR nobody ran the suite on, the CI job that died before
`cov sync`. Storing 0 there would make `atlas trend` draw a cliff that never
happened and make the regression gate fail an innocent PR. NULL means "not
measured"; readers skip the point rather than plotting or comparing it as a
zero. `coverage_history_features.score` carries the same rule per feature.

**Why `commit_sha` is UNIQUE rather than `(commit_sha, measured_at)`.** The
logical key of a point is the pair, but a commit's score is a function of the
commit: a re-measurement is a correction, not a second observation. A CI job
that retries, or a developer who runs `atlas trend record` twice, must leave
ONE point behind. `store.History.Record` upserts on this index and replaces
the child rows, so last-write-wins is the recorded semantic and stale
per-feature rows never survive a shrinking feature set.

**Retention.** `atlas trend record --retain <window>` deletes points older
than the window; `ON DELETE CASCADE` carries the breakdown with them, so
there is no second statement to forget. Atlas does not roll old points up
into daily aggregates -- a rollup must pick a representative score per day,
and every choice makes the retained series disagree with the raw one it
replaced.

**Durability caveat.** The store is a re-derivable cache (§10), but this is
the one table that cannot be rebuilt from the working tree: deleting
`atlas.db` loses the series. Teams that need it durable should record it from
CI into a committed artifact as well.

### 5.15 `sql_operations` and friends — the data access layer as data (migration 0014)

```sql
CREATE TABLE sql_operations (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  ref               TEXT    NOT NULL UNIQUE,
  source            TEXT    NOT NULL,          -- 'go' | 'sql'
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

CREATE INDEX sql_operations_file_idx     ON sql_operations(file_path, line);
CREATE INDEX sql_operations_symbol_idx   ON sql_operations(symbol_id);
CREATE INDEX sql_operations_resolved_idx ON sql_operations(resolved);

CREATE TABLE sql_operation_tables (
  operation_id INTEGER NOT NULL REFERENCES sql_operations(id) ON DELETE CASCADE,
  table_name   TEXT    NOT NULL,
  access       TEXT    NOT NULL,               -- 'read' | 'write'
  PRIMARY KEY (operation_id, table_name, access),
  CHECK (access IN ('read', 'write'))
) WITHOUT ROWID;

CREATE INDEX sql_operation_tables_table_idx ON sql_operation_tables(table_name, access);

CREATE TABLE sql_operation_predicates (
  operation_id INTEGER NOT NULL REFERENCES sql_operations(id) ON DELETE CASCADE,
  clause       TEXT    NOT NULL,               -- 'where' | 'join'
  table_name   TEXT    NOT NULL DEFAULT '',
  column_name  TEXT    NOT NULL,
  operator     TEXT    NOT NULL,
  bound        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (operation_id, clause, table_name, column_name, operator),
  CHECK (clause IN ('where', 'join'))
) WITHOUT ROWID;

CREATE INDEX sql_operation_predicates_col_idx ON sql_operation_predicates(table_name, column_name);

CREATE TABLE sql_tables (
  name      TEXT    NOT NULL PRIMARY KEY,
  file_path TEXT    NOT NULL,
  line      INTEGER NOT NULL
) WITHOUT ROWID;

CREATE TABLE sql_indexes (
  table_name TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  columns    TEXT    NOT NULL,                 -- comma-separated, declaration order
  is_unique  INTEGER NOT NULL DEFAULT 0,
  predicate  TEXT    NOT NULL DEFAULT '',      -- partial-index WHERE, verbatim
  origin     TEXT    NOT NULL,                 -- 'create-index' | 'primary-key' | 'unique-constraint'
  file_path  TEXT    NOT NULL,
  line       INTEGER NOT NULL,
  PRIMARY KEY (table_name, name),
  CHECK (origin IN ('create-index', 'primary-key', 'unique-constraint'))
) WITHOUT ROWID;
```

Written by `atlas sql scan`, read by `atlas sql list` and `atlas sql advise`
(issue #126). Every other table here describes code; these describe what the
code *asks the database to do* — the statement kind, the tables read and
written, the columns filtered on, and whether the read is bounded.

**Who writes rows.** Only `atlas sql scan`, and it **replaces** both sets
wholesale: `sql_operations` in one transaction, `sql_tables` + `sql_indexes`
in another. An incremental upsert would leave rows behind for queries that
were deleted, and an advisory pointing at a line that no longer exists costs
more trust than the merge saves work. `atlas scan` and `atlas init` do not
write here.

**`resolved` is the load-bearing column.** A query Atlas could not statically
resolve — assembled by a builder, spliced across functions, read from config —
is stored with `resolved = 0` and an `unresolved_reason`, **not dropped**.
Every consumer must filter on it before reading the shape columns, because an
unresolved row's shape columns are all zero and a zero shape is
indistinguishable from "a `SELECT` with no `LIMIT`". Storing the unanalysable
half is what lets a report say "90% of the data layer was analysed" instead of
implying it saw all of it, and `sql_operations_resolved_idx` exists because
that count is the number every report leads with.

| Column | Notes |
| --- | --- |
| `ref` | Fingerprint: source, file, line, name, and — where one line holds more than one operation — a `#n` ordinal. Re-scanning an unchanged repository rewrites the same rows rather than accumulating duplicates. UNIQUE, which is why the ordinal is not optional: two `database/sql` calls written on one line fingerprint identically without it, and the second write silently replaces the first, shrinking both the inventory and the denominator of the resolved fraction. |
| `symbol_id` | **Nullable, `ON DELETE SET NULL`.** An operation outlives the symbol it was linked to (a rename, a scan that skipped a generated file); CASCADE would silently shrink the inventory. Resolved from `symbol_name` at write time. |
| `symbol_name` | Kept alongside the link so a row stays readable with no `symbol_id`. sqlc queries use the `sql:<QueryName>` id the Go scanner already anchors them under. |
| `kind` | `unknown` for an unresolved operation — the CHECK constraint would otherwise take an empty string. |
| `has_limit` / `has_offset` | Bounds on **this** statement. A `LIMIT` inside a subquery, a CTE body or an `IN (...)` list bounds that inner result set and is not written here: crediting it to the outer statement suppresses the unbounded-read advisory on the query that needs it most. |
| `keyset` | Cursor pagination: `has_limit`, plus a caller-bound range predicate on the column the statement orders by FIRST. Distinguished from `has_offset` because conflating the two gives opposite advice on identical-looking SQL. All three parts are required — a bound range predicate with an ORDER BY and no page size is a time window or a depth guard, not a cursor walk, and it returns every row past the cursor. |
| `offset_bound` | Where the OFFSET's value comes from. `parameter` is the one that degrades with depth. |
| `row_scan` | What the call site does with the rows. `slice` vs `single` is the difference between a LIMIT-less read that loads a table into memory and one that reads a row by primary key. |
| `interpolation` / `caller_data` | How the query text was built, and whether the spliced value traces to a parameter of the enclosing function. The injection advisory grades its confidence on the second. |
| `suppressions` | Comma-packed advisory codes from an `atlas:sql-ignore` directive at the call site. |

**Why predicates and table accesses are child tables** rather than JSON
columns: the questions they answer are set questions — "which capabilities
write to this table", "which columns does anything filter on" — and those want
an index, not a `LIKE`. `sql_operation_tables_table_idx` serves the reverse
lookup that is the data footprint of a feature, which is what a privacy or
migration review actually needs.

**The capability rollup** is that reverse lookup spelled out:
`feature_symbols → sql_operations → sql_operation_tables`, grouped by
`feature_id`, exposed as `SQLOps.CapabilityTables` and printed by
`atlas sql capabilities`. Two facts travel with every row and neither is
optional. Operations are counted `DISTINCT` on `sql_operations.id`, because a
symbol linked to a feature under two roles reaches it through two
`feature_symbols` rows and one query must not count as two. And the unresolved
count rides along, because a capability with unresolved queries has a table
set that is a **lower bound** — a table only those queries touch is missing
from it, and a review that reads the set as complete is being misled
confidently. A capability whose queries all failed to resolve keeps its row
with an empty set: "nothing readable" and "touches no data" are different
answers.

**Why `columns` and `suppressions` *are* comma-packed.** Both hold short
identifier lists that cannot contain a comma, and neither is ever queried by
element. Anything richer would want a child table.

**`sql_tables` and `sql_indexes` are what Atlas READ**, not a mirror of a live
database. `sql_indexes` includes the indexes implied by `PRIMARY KEY` and
`UNIQUE` declarations (`origin` says which), because without them every
lookup by primary key would report as unindexed. An index check consults
`sql_tables` first and reports "did not run" for a table that is absent:
claiming an index is missing from a schema Atlas never read is the one wrong
answer that looks authoritative.

They hold the schema **as of the last migration**, not the union of every
declaration ever made. `*.down.sql` files are skipped — a rollback undoes its
`up` sibling, and reading both leaves a table that was created once and
dropped once in the inventory. Within the files that are read, `DROP TABLE`,
`DROP INDEX` and `ALTER TABLE … RENAME TO` are applied in order: a dropped
table leaves and takes its indexes with it, a renamed one carries them across.
Column-level `ALTER`s are not applied, so the *columns* of an index row remain
additive; rewriting an index definition from a rename Atlas never re-read
would be a guess dressed as a fact.

### 5.16 control flow inside a symbol -- `cfg_*` (migration 0015)

Added by issue #127. Until this migration a symbol was an opaque box with a
line range: whether its body was a straight line, a five-way switch, or a loop
containing a database call was invisible. That blind spot is why atlas could
report 90% statement coverage on a function whose every error path was
unexercised -- statement coverage says the line ran, never that the branch was
taken both ways.

Five tables, every one of them keyed by `symbol_id` so that every result joins
the graph. An analysis nobody can join is an analysis nobody uses.

```sql
CREATE TABLE cfg_blocks (
  symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  block_index INTEGER NOT NULL,
  kind        TEXT    NOT NULL,   -- entry|body|branch|loop|exit
  start_line  INTEGER NOT NULL DEFAULT 0,
  end_line    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (symbol_id, block_index)
) WITHOUT ROWID;

CREATE TABLE cfg_edges (
  symbol_id  INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  edge_index INTEGER NOT NULL,
  from_block INTEGER NOT NULL,
  to_block   INTEGER NOT NULL,
  kind       TEXT    NOT NULL,   -- seq|true|false|loop-back|fallthrough
  condition  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (symbol_id, edge_index)
) WITHOUT ROWID;

CREATE TABLE cfg_symbols (
  symbol_id              INTEGER PRIMARY KEY REFERENCES symbols(id) ON DELETE CASCADE,
  complexity             INTEGER NOT NULL DEFAULT 1,
  decisions              INTEGER NOT NULL DEFAULT 0,
  branch_arms            INTEGER NOT NULL DEFAULT 0,
  conditions             INTEGER NOT NULL DEFAULT 0,
  conditions_independent INTEGER NOT NULL DEFAULT 0,
  defers                 INTEGER NOT NULL DEFAULT 0,
  unreachable_blocks     INTEGER NOT NULL DEFAULT 0,
  built_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cfg_decision_coverage (
  symbol_id          INTEGER PRIMARY KEY REFERENCES symbols(id) ON DELETE CASCADE,
  outcomes_total     INTEGER NOT NULL DEFAULT 0,
  outcomes_decidable INTEGER NOT NULL DEFAULT 0,
  outcomes_taken     INTEGER NOT NULL DEFAULT 0,
  source             TEXT    NOT NULL DEFAULT '',
  measured_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cfg_findings (
  symbol_id     INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  finding_index INTEGER NOT NULL,
  kind          TEXT    NOT NULL,   -- flow.query-in-loop|flow.unreachable|flow.untested-branch
  confidence    TEXT    NOT NULL,   -- high|medium|low
  line          INTEGER NOT NULL DEFAULT 0,
  related_line  INTEGER NOT NULL DEFAULT 0,
  detail        TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (symbol_id, finding_index)
) WITHOUT ROWID;
```

Indices: `cfg_edges_target_idx (symbol_id, to_block)` for "every edge into this
block"; `cfg_symbols_complexity_idx (complexity DESC)` for the hotspot ranking
(#93); `cfg_findings_kind_idx (kind, confidence)` for the diagnostics list.

**Block 0 is always the entry and block 1 always the exit.** Fixing them makes
a flowchart renderable without loading the whole function first, and gives
`E - N + 2` a single-entry/single-exit graph to be meaningful over.

**Why `cfg_edges` is keyed by `edge_index` and not by `(from, to, kind)`.**
The index is the edge's position in the built graph, so the write order is
reproducible AND two structurally identical edges cannot collapse into one
row. An edge count that quietly drops is a cyclomatic complexity that quietly
drops with it.

**Why `seq` is in the edge-kind set.** Most edges in a real function are
unconditional continuations. Labelling them `true` would make every straight
line look like a taken branch to anything counting branch arms.

**The three counters in `cfg_decision_coverage` are the whole point of the
table.** `outcomes_total` is every branch outcome the source has;
`outcomes_decidable` is how many of those a statement-coverage profile can
judge *at all*; `outcomes_taken` is how many of the decidable ones were taken.
Decision coverage is `taken / decidable` -- **never** `taken / total`, which
would charge a symbol for outcomes no instrumentation could have observed. A
row with `outcomes_decidable = 0` means "no judgement was possible", which is
a different fact from 0% and must not be rendered as one.

**UNDETERMINED is the third verdict, and it is stored, not implied.** Every
outcome is `taken`, `not-taken`, or `undetermined`; the undetermined ones are
exactly `outcomes_total - outcomes_decidable`, which the store exposes as
`DecisionCoverage.Undetermined()`. There is no fourth column because the
subtraction is exact, but there is also no reading of this table in which an
undetermined outcome may be counted as untaken: it never enters the
denominator and it never becomes a `flow.untested-branch` finding. The
outcomes that land there are the ones no arithmetic over statement counts can
recover -- a `&&` operand, a branch inside a loop, an `if` whose then-arm
reaches the successor on some paths and leaves the function on others, and
anything at all in a function containing a `goto` (see below).

**An absent row means "never measured", which is different again -- and it is
per FILE, not per run.** Supplying `--profile` is not the same as that profile
covering a given file: profiling one package, an integration-test profile, or
a package with no tests all produce a profile that names other files. The
symbols in those files get **no row**. Writing a zero-valued row for them
would record "measured, no branch taken" -- a claim about the tests that the
run cannot support, and one indistinguishable from a genuinely untested
function.

**There is deliberately no MC/DC column.** MC/DC is not derivable from Go's
statement coverage: the operands of `a && b` share one counter, so no profile
can show a condition independently affecting the outcome. What is recorded, in
`cfg_symbols`, is how many conditions exist and how many could in principle be
varied independently -- a property of the SOURCE, not a coverage verdict. A
column called `mcdc_percent` here would be read as the thing it is not, by
exactly the regulated-industry reader who can least afford to.

**Confidence is mandatory on every finding.** A query inside a loop is a smell
and not a proof -- one behind a cache is fine -- and a finding that does not
say how sure it is gets muted wholesale the first time it is wrong.

**`cfg_symbols.unreachable_blocks` is 0 for any function containing a `goto`,
and that 0 means "nothing claimed".** The builder does not draw goto edges, so
in such a function a label reached only by that goto has no predecessor in the
graph and a reachability walk would report live code as dead -- at confidence
`high`, which is the worst possible way to be wrong. `flow build` therefore
computes nothing for those functions: no `flow.unreachable` rows, a zero
count, and a note on the run saying how many functions were skipped and naming
some of them. The same omission makes every successor-difference inference in
that function `undetermined`.

**Cascade.** All five follow `symbols`: a re-scan that drops a symbol drops its
flow with it, so the store cannot accumulate orphan graphs no query can reach.

### 5.17 `skipped_files` — the exclusion ledger (migration 0016)

```sql
CREATE TABLE skipped_files (
  file_path  TEXT PRIMARY KEY,
  rule       TEXT NOT NULL,
  detail     TEXT,
  scanned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX skipped_files_rule_idx ON skipped_files(rule);
```

Written by `atlas scan` / `atlas init` inside the ingest transaction, read by
`atlas scan --skipped` (issue #137). It sits next to `file_hashes` because
both describe what the WALK did — one records the files that were indexed,
the other the files that were not.

**Why it exists.** Exclusion is silent by design: a file the scanner declined
to index does not appear as uncovered, unlinked or missing, it appears as
nothing at all. Without this table the only record of the decision is the
terminal output of the scan that made it, so an over-broad glob removes real
code from every coverage and audit number with no trace.

| Column       | Type      | Notes                                                                                                                                                     |
| ------------ | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `file_path`  | TEXT PK   | Project-relative, slash-separated — the same key space as `file_hashes.file_path` and `symbols.file_path`.                                                 |
| `rule`       | TEXT      | The scanner's `goscan.SkipReason` verbatim: `generated-header`, `generated-glob`, `generated-dir`, `ignored-package`. Not CHECKed; see below.              |
| `detail`     | TEXT      | The rule's parameter when it has one: the glob that matched for `generated-glob`, the directory segment for `generated-dir`. NULL otherwise.               |
| `scanned_at` | TIMESTAMP | When the scan that made the call ran. Tells the reader how stale the answer is.                                                                            |

**Why the rule, not a category.** "generated" tells an operator what happened
but not what to change. `generated-glob` + `**/*.pb.go` names the line of
`.atlas.yaml` that swallowed the file; `generated-header` says the file
declares itself generated and no config edit will help.

**Why `file_path` alone is the key.** The generated-code rules are applied
strongest-signal-first and exactly one of them claims a file, so a
`(file_path, rule)` key would let the same file sit under two rules at once —
destroying the only evidence that the ordering held. A file that matches the
header rule AND a glob is recorded once, under the header.

**Why the rule set is not CHECKed.** The column mirrors an enum owned by the
scanner. A CHECK here would mean every new detection rule needs a migration
before its exclusions can be audited, which is exactly backwards: the ledger
is descriptive, and a rule it cannot spell is a rule whose effects would go
unrecorded.

**Replaced, never appended.** Each ingest DELETEs the table and re-inserts the
current set, in the same transaction as the symbols. A file that stops being
skipped therefore leaves the ledger — otherwise it accumulates into a record
of every rule ever tried, and answers "why is this file not indexed?" with
files that are. Sharing the transaction is what keeps the ledger and the index
it explains describing the same scan.

**Every ingest owns it, so every ingest must walk the same way.** Because the
write is a replace and it rides `Store.Ingest`, ANY command that ingests
rewrites the ledger — `atlas init`, `atlas scan` and `atlas snapshot` all do.
That is only safe while they build their index with the same scan options: an
ingest from a differently-configured walk would leave `--skipped` answering
about a configuration the operator never ran. `atlas snapshot` therefore goes
through the same option-derivation path as `atlas scan` rather than assembling
its own `codeindex.Options`, and both pass `IngestOptions.GeneratedGlobs` so
`detail` names a real config line. An ingest given no globs still records the
rule; it just has no pattern to name.

**The `scan.skipped_ledger_written_at` marker.** Zero rows in this table has
two causes that mean opposite things: the last scan excluded nothing, or no
scan has ever written a ledger here (a fresh database, or one built before
migration 0016). The table cannot tell them apart — both are empty — so each
ledger write also stamps a `config` row:

| Key                              | Value                                                          |
| -------------------------------- | -------------------------------------------------------------- |
| `scan.skipped_ledger_written_at` | RFC 3339 (nanosecond) UTC instant of the most recent ledger write |

It is written through the ingest's transaction handle like the rows, so a
rolled-back ingest leaves neither behind, and it is the one `config` key not
written by `atlas config set`. `atlas scan --skipped` reads it before the
rows and reports "no exclusion ledger has been recorded" instead of "the last
scan excluded no files" when it is absent — an absence is not a measurement.
`--json` exposes the distinction as `ledger_present`.

**Key spelling.** `file_path` is project-relative and slash-separated, so a
lookup has to normalise an operator's input into that key space before
matching (`./api/x.pb.go` and an absolute path are neither). A key the ledger
does not contain means the last scan did not exclude it — NOT that the file
was indexed, which this table has no way to know.

**Not durable state.** Like the rest of this database it is a re-derivable
cache: the next scan rebuilds it.

### 5.18 `coverage_symbol_spans` — the span a result was measured against (migration 0017)

```sql
CREATE TABLE coverage_symbol_spans (
  run_id    INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  symbol_id INTEGER NOT NULL REFERENCES symbols(id)       ON DELETE CASCADE,
  file_path TEXT    NOT NULL,
  line      INTEGER NOT NULL,
  end_line  INTEGER,
  PRIMARY KEY (run_id, symbol_id)
);

CREATE TRIGGER coverage_results_span_snapshot
AFTER INSERT ON coverage_results
WHEN NEW.symbol_id IS NOT NULL
BEGIN
  INSERT OR IGNORE INTO coverage_symbol_spans (run_id, symbol_id, file_path, line, end_line)
  SELECT NEW.run_id, NEW.symbol_id, s.file_path, s.line, s.end_line
  FROM symbols s
  WHERE s.id = NEW.symbol_id;
END;
```

Written by the trigger, never by Go code. Read by
`packages/store/carryforward.go` (issue #136).

**Why it exists.** A run group unions the runs of one build (§5.8,
`coverage_runs.run_group`). A symbol that NO run in the group measured — the Go
job crashed, the e2e job timed out — is simply absent from the frontier's
results, and the line-weighted score sums only symbols that HAVE results. The
denominator shrinks to whatever the surviving job touched and coverage goes UP
because testing went DOWN. Carryforward fills the hole from the last build that
measured the symbol, which is only defensible if the inherited measurement can
be INVALIDATED when the symbol is no longer the symbol that was measured.

**Why a table and not a column on `symbols`.** The check needs the symbol's span
*as of the run that measured it*. `symbols` holds only the current span — a
rescan updates the row in place, keeping the surrogate id (§5.4) — so by the
time a carry is considered the measured span is already gone.

**Why a trigger and not Go code.** The snapshot has to hold for every path that
writes a coverage result: the sqlc-generated insert, the raw-SQL insert that
carries the migration-0009 statement columns, and any ingester added later.
Making it an invariant of the table removes the possibility of an ingest path
that forgets — which would turn every carry back into an article of faith. The
cost is one primary-key lookup and one `INSERT OR IGNORE` per result row, inside
the ingest's existing transaction.

| Column      | Type       | Notes                                                                                                                        |
| ----------- | ---------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `run_id`    | INTEGER    | FK → `coverage_runs(id)`, `ON DELETE CASCADE`. The snapshot cannot outlive the run it describes.                              |
| `symbol_id` | INTEGER    | FK → `symbols(id)`, `ON DELETE CASCADE`. A pruned symbol takes its snapshots with it, as it does its results.                 |
| `file_path` | TEXT       | The symbol's file at measurement time.                                                                                       |
| `line`      | INTEGER    | Its opening line at measurement time.                                                                                        |
| `end_line`  | INTEGER    | Its closing line at measurement time; NULL when the scanner could not pin one. Pinned for Go by issue #120.                   |

**Why `(run_id, symbol_id)` and `INSERT OR IGNORE`.** A symbol can produce
several result rows in one run — one per coverprofile block — and they all
describe the same declaration. The first row records the span; a plain `INSERT`
would abort the ingest transaction on the second block of any multi-block
function.

**Not backfilled.** Filling this table from the current `symbols` rows would
assert that today's span was the measured span, which is exactly the claim the
table exists to verify. Results ingested before migration 0017 therefore carry
no snapshot, and the carry layer treats a missing snapshot as *unverifiable*:
the symbol still holds its place in the denominator, but nothing is credited to
it. Refusing the carry outright is the direction that reproduces the bug.

**The validity rule.** A carry is EVIDENCE (its covered/total statements stand
in for this build's reading) only when the snapshot matches the symbol's current
`(file_path, line, end_line)` AND the measurement is inside the staleness window
(`store.DefaultCarryBuilds` = 3 grouped frontiers, `store.DefaultCarryMaxAge` =
72h). Otherwise it is DENOMINATOR-ONLY: `covered_stmts` is zeroed and the status
becomes `fail`, so the symbol counts in the denominator and not in the
numerator. Nothing older than a 30-day lookback horizon is read at all.

**The unit of a carry is a BUILD.** `ListCarrySources` names the newest RUN that
measured each unmeasured symbol, and its span snapshot is the one checked; but
the statement rollup (`ListCarrySymbolTotals`) groups by
`(coverage_runs.run_group, symbol_id)`, so the inherited covered/total is the
whole source build's, summed the way a live frontier sums the runs of the
current build. A build that measures one symbol from two runs — a unit job and
an integration job over the same package — would otherwise contribute only
whichever run finished last.

**Carryforward does not run at all on an ungrouped frontier**, which is the
default for any store that does not pass `cov sync --run-group`. That is
reported (`store.ResolvedCoverage.SkipReason`, surfaced as `carry.ran` /
`carry.skip_reason` in `atlas cov status --json`) rather than rendered as an
empty carry, because "nothing needed carrying" and "the question was never
asked" are the same empty list and different facts. For the same reason a
source build older than the ordinal scan reached reports
`builds_back = store.CarryBuildsBackBeyondWindow` (-1), never 0.

**Not durable state.** Like the rest of this database it is a re-derivable
cache — but re-deriving it means re-ingesting the coverage reports, because
nothing else records what a span used to be.

## 6. Partial Unique Indices and Invariants

| Invariant                                                                | Where enforced                                                          |
| ------------------------------------------------------------------------ | ----------------------------------------------------------------------- |
| One annotation per (file, line, kind)                                    | `annotations_dedupe_idx` (UNIQUE)                                       |
| One feature_symbols row per (feature, symbol, role)                      | `feature_symbols` PRIMARY KEY                                           |
| One edge per (from, to, kind, file, line)                                | `edges_dedupe_idx` (UNIQUE) — re-scans are idempotent                  |
| Every edge names the mechanism that resolved it                          | `edges.resolution_tier NOT NULL` with no DEFAULT + `CHECK` on the vocabulary; `store.requireTier` refuses it earlier with the from/to pair named |
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
SELECT h.commit_sha, h.measured_at, f.score, f.denominator
FROM coverage_history h
LEFT JOIN coverage_history_features f
       ON f.history_id = h.id AND f.feature_id = ?
ORDER BY h.measured_at DESC
LIMIT 20;
```

Drives `atlas trend --feature <id>`. The join is a LEFT JOIN on purpose: a
commit whose breakdown has no row for the feature is still a point on the
axis, with a NULL score. Inner-joining it away would silently close the gap
and make a feature that STOPPED being measured look continuous.

(The original form of this query read the per-feature `audit_snapshots`
table, which migration 0006 dropped as unwritten -- see §5.10.)

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
| `audit`          | `audit_snapshot_runs`                                     | `atlas audit` — one whole-project JSON blob per run (§5.10 for why the per-feature table went). |
| `trend`          | `coverage_history`, `coverage_history_features`           | `atlas trend record` — one point per commit, upserted so a CI retry corrects rather than appends. |
| `cfg`            | `cfg_blocks`, `cfg_edges`, `cfg_symbols`, `cfg_decision_coverage`, `cfg_findings` | `atlas flow build` -- one whole-symbol rewrite per function, so a rebuild that finds fewer blocks shrinks the stored graph rather than interleaving two generations. |
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
- A trend point: one tx covering the `coverage_history` upsert, the delete of
  the previous per-feature rows, and the new ones. A half-replaced point
  would report a feature set that never existed at any commit.

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
