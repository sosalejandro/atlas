# Migration from testreg → Atlas

This is the cutover guide for projects that adopted testreg (the predecessor)
and are moving to Atlas. It covers annotation compatibility, the YAML registry
retirement, the CLI rename map, config + taskfile rewrites, the ordered
cutover checklist, rollback, and a FAQ at the end.

If you are starting greenfield with Atlas and never used testreg, you do
not need this document — read `docs/architecture.md` and `docs/annotations.md`
instead.

---

## Cutover quickstart (read this first)

If you just want the happy path, this is it. Five steps, in order:

1. `go install github.com/sosalejandro/atlas/cmd/atlas@latest`
2. `cd path/to/your/repo`
3. `atlas init` — creates `.atlas/atlas.db` and a minimal `.atlas.yaml`.
4. `atlas audit --worst 10` — health-scored top offenders for the codebase.
5. `atlas trace <a-feature-id>` — walk the call graph for any feature you
   already annotated. Saga walks use the `saga:<id>` prefix:
   `atlas trace saga:meal-prep-flow`.

The sections below explain the cutover in detail: annotation grammar, what
happens to the YAML registry, the CLI rename map, the `.atlas.yaml` schema,
taskfile rewrites, and rollback.

---

## 1. Why Atlas (one paragraph)

testreg grew into a single 36k-LOC Go binary covering 8+ distinct concerns
(AST parsing, registry storage, coverage analysis, sprint planning,
dashboards, diagnostics, contract extraction, dependency tracing). It works,
but the surface area is too large for one tool: every concern lives in one
binary's release cycle, and the hand-maintained YAML registry was the
primary drift point because it required parallel human upkeep alongside
code changes. Atlas keeps every testreg strength (the AST scanners, the
graph model, the DI resolvers, the health-scoring algorithm) but splits
them into SRP-focused library packages under one `atlas` CLI, and moves
the registry into a derived SQLite store re-built from code annotations
on every scan — so feature membership cannot drift from code.

---

## 2. Annotation compatibility

**Existing `// @testreg <id>` annotations continue to work, untouched.**
You do not have to rename anything to adopt Atlas. The scanner accepts both
grammars:

| Form                            | Status                | Notes                          |
| ------------------------------- | --------------------- | ------------------------------ |
| `// @testreg <id>`              | Supported (legacy)    | Read as `@atlas:feature <id>`. |
| `// @testreg <id> #tag`         | Supported (legacy)    | Tag preserved.                 |
| `// @atlas:feature <id> [tags]` | Preferred (canonical) | Recommended for new code.      |
| `// @atlas:contract <id>`       | Preferred             | New kind, not in testreg.      |
| `// @atlas:owner <team>`        | Preferred             | New kind, not in testreg.      |

The `@atlas:<kind> <id>` form is namespaced. It reserves grammar for
future kinds (`@atlas:deprecated`, `@atlas:since`, etc.) without breaking
the parser. The 9-character cost per annotation is paid once.

### Bulk renaming when ready (opt-in)

```bash
atlas migrate-annotations --dry-run         # show what would change
atlas migrate-annotations --apply           # rewrite in place
atlas migrate-annotations --apply --path src/contexts/identity
```

Renaming is **not required** at any phase. Defer it if you're mid-refactor or
want a clean blame; do it when you want the new namespaced kinds
(`@atlas:contract`, `@atlas:owner`) or grep-consistency. Both forms are
first-class through v0 and v1.

---

## 3. YAML registry retirement

testreg used `docs/testing/registry/*.yaml` files as the **source of truth**
for which symbols belonged to which feature. Humans hand-maintained these
files; every code-level membership change required a parallel YAML edit.
This was the dominant drift source.

**Atlas does not use the YAML registry as source of truth.** Feature
membership is re-derived from code annotations on every `atlas scan`. In
Phase 9 the source of truth is in-code annotations; the legacy YAML files
contain no data Atlas doesn't already pick up from `@testreg` / `@atlas:*`
comments on the symbols themselves.

There is no YAML import step. Run a normal scan and the SQLite store
materializes from annotations alone:

```bash
atlas init        # creates .atlas/atlas.db + minimal .atlas.yaml
atlas scan        # populates symbols, annotations, features
```

Then move the legacy YAML directory aside so it isn't accidentally edited:

```bash
mkdir -p docs/testing/registry/_legacy
git mv docs/testing/registry/*.yaml docs/testing/registry/_legacy/
```

The `_legacy/` directory is reference-only post-cutover. It is kept for:

- Rollback (see §8).
- Historical reference — what the registry *thought* a feature contained
  vs. what code annotations actually say. Useful for catching annotations
  that got lost during the testreg era.

`atlas dump --format yaml --feature <id>` can re-emit a YAML view of any
feature from the SQLite store at any time. The YAML format is now an
**output**, not an input.

---

## 4. CLI rename map

| testreg command          | Atlas equivalent                | Notes                                                              |
| ------------------------ | ------------------------------- | ------------------------------------------------------------------ |
| `testreg scan`           | `atlas scan`                    | Same role: walk source, refresh symbol graph.                      |
| `testreg trace <id>`     | `atlas trace <id>`              | Same chain output; default format `text`, `--format json` stable.  |
| `testreg audit`          | `atlas audit`                   | Same health-scoring algo (regressed within ±5%).                   |
| `testreg sprint`         | `atlas sprint`                  | Same gap-weighted prioritization.                                  |
| `testreg init`           | `atlas init`                    | Creates `.atlas/atlas.db` + a minimal `.atlas.yaml`. No YAML import — annotations are the source of truth.  |
| `testreg serve`          | **DROPPED**                     | No dashboard in v0. JSON outputs are stable; see §9.               |
| `testreg gaps`           | `atlas cov status --uncovered`  | Subsumed under the `cov` verb namespace.                           |
| `testreg report`         | `atlas audit --format markdown` | Or `--format json`. Same data, new flag plumbing.                  |
| `testreg diff`           | `atlas diff`                    | Snapshot diff; same semantics.                                     |
| `testreg contract`       | `atlas contract`                | Contract extraction; Huma router added in addition to Chi/Echo.    |
| `testreg diagnose`       | `atlas diagnose`                | Error → code matching; unchanged.                                  |
| `testreg debug-scan`     | `atlas scan --debug`            | Promoted from sub-binary verb to a flag.                           |
| `testreg update --gotest`| `atlas cov ingest --gotest`     | Coverage ingestion lives under `cov`.                              |
| `testreg update --playwright` | `atlas cov ingest --playwright` | Same for every framework.                                    |
| `testreg update --vitest`     | `atlas cov ingest --vitest`     |                                                                |
| `testreg update --jest`       | `atlas cov ingest --jest`       |                                                                |
| `testreg update --maestro`    | `atlas cov ingest --maestro`    |                                                                |

**Verb-namespaced subcommands** (matches bmad-cli ergonomics):

- `atlas trace` / `atlas scan` / `atlas init` / `atlas diff` / `atlas audit` /
  `atlas sprint` / `atlas contract` / `atlas diagnose` / `atlas migrate-annotations`
- `atlas cov ingest` / `atlas cov status` / `atlas cov sync`
- `atlas dump` (read-only views from SQLite store)

Saga walks reuse the `trace` verb with a `saga:<id>` prefix on the argument
(there is no separate `atlas codebase saga` verb):

```bash
# Walk a named saga's step sequence
atlas trace saga:meal-prep-flow
```

Every subcommand has stable JSON output behind `--format json`. The schema
is documented under `docs/api/` and is part of the v0 contract — see §9.

---

## 5. Config rename — `.testreg.yaml` → `.atlas.yaml`

The repo-root config file is renamed. The **v0 schema is intentionally
narrow** — Atlas only reads the fields it actively uses. Anything the binary
doesn't consume is silently ignored, so a kitchen-sink config copied from
testreg won't error, but it won't do anything either. The supported fields
are defined by the `Config` struct in
[`internal/cli/config.go`](../internal/cli/config.go).

### Supported fields (Phase 7)

| Field                            | Type        | Default                  | Purpose                                                            |
| -------------------------------- | ----------- | ------------------------ | ------------------------------------------------------------------ |
| `db_path`                        | string      | `.atlas/atlas.db`        | SQLite store path. Relative paths anchor at the git repo root.     |
| `scan.skip_dirs`                 | []string    | `[vendor, node_modules, dist, build]` | Directory names skipped by the scanner.                |
| `scan.skip_ts`                   | bool        | `false`                  | Skip the TS scanner entirely (Go-only mode).                       |
| `audit.freshness_window_days`    | int         | `30`                     | How recently a feature must have been touched to count "fresh".    |
| `audit.contract_drift_window_days` | int       | `30`                     | Grace window before contract drift gets flagged.                   |
| `sprint.default_top_n`           | int         | `10`                     | Default `--top` for `atlas sprint` when the flag is omitted.       |

Additional config fields will be added as new phases require them; testreg's
broader config surface is intentionally pared back for v0. Fields the binary
doesn't read (`scan.roots`, `scan.ignore`, `scan.max_depth`, `contract.exempt`,
`layer_rules`, `ignore_symbols`, `project_name`, `cache_dir`, `output_dir`)
were doc aspirations from earlier phases and are not currently honored — do
not write them into your config.

### Transition behavior

Atlas reads `.atlas.yaml` at the repo root (the directory `git rev-parse
--show-toplevel` reports), falling back to the current working directory when
not inside a repo. If no config file exists, the built-in defaults apply —
running with no config is a supported workflow. Specify a non-default path
with the global `--config <path>` flag.

There is no `.testreg.yaml` fallback in v0; rename the file as part of the
cutover commit.

### Example `.atlas.yaml`

```yaml
# All fields are optional; this example shows every supported key with the
# default value spelled out. Omit any field to take the default.
db_path: .atlas/atlas.db

scan:
  skip_dirs: [vendor, node_modules, dist, build]
  skip_ts: false

audit:
  freshness_window_days: 30
  contract_drift_window_days: 30

sprint:
  default_top_n: 10
```

---

## 6. Taskfile rewrite — `taskfiles/testreg.yml` → `taskfiles/atlas.yml`

Atlas subcommands map cleanly onto testreg's task targets. Below are
BEFORE / AFTER side-by-sides for the three most common tasks. The full
rewrite of `taskfiles/testreg.yml` follows the same shape.

### Task `sync` (the daily driver)

**BEFORE — `taskfiles/testreg.yml`:**

```yaml
sync:
  desc: Run tests, ingest results, audit
  cmds:
    - task: test:go
    - task: import:go
    - task: import:playwright
    - "{{.TESTREG_BIN}} audit"
```

**AFTER — `taskfiles/atlas.yml`:**

```yaml
sync:
  desc: Scan, ingest test results, audit
  cmds:
    - atlas scan
    - atlas cov sync               # discovers + ingests all framework outputs
    - atlas audit
```

`atlas cov sync` replaces the manual `import:go` / `import:playwright` /
`import:vitest` chain — it walks `.atlas-results/`, detects each framework's
JSON shape, and ingests in one pass.

### Task `audit`

**BEFORE:**

```yaml
audit:
  desc: Print health audit
  cmds:
    - "{{.TESTREG_BIN}} audit"
    - "{{.TESTREG_BIN}} audit --format markdown > audit-report.md"
```

**AFTER:**

```yaml
audit:
  desc: Print health audit + write markdown report
  cmds:
    - atlas audit
    - atlas audit --format markdown > audit-report.md
```

### Task `gaps`

**BEFORE:**

```yaml
gaps:
  desc: List uncovered features
  cmds:
    - "{{.TESTREG_BIN}} gaps"
```

**AFTER:**

```yaml
gaps:
  desc: List uncovered features
  cmds:
    - atlas cov status --uncovered
```

### Tasks that are dropped

| Dropped task                     | Why                                                  |
| -------------------------------- | ---------------------------------------------------- |
| `dashboard`                      | `serve` is dropped from v0. See §9.                  |
| `import:*` (per-framework)       | Subsumed by `atlas cov sync`.                        |
| Any explicit YAML edit task      | YAML registry is retired; derived from code.         |

### Full template

```yaml
version: '3'

vars:
  ATLAS_RESULTS: .atlas-results

tasks:
  setup:    { cmds: [mkdir -p {{.ATLAS_RESULTS}}], status: [test -d {{.ATLAS_RESULTS}}] }
  scan:     { desc: Refresh code-graph,         cmds: [atlas scan] }
  sync:     { desc: Scan + ingest + audit,      cmds: [atlas scan, atlas cov sync, atlas audit] }
  audit:    { desc: Health audit,               cmds: [atlas audit, "atlas audit --format markdown > audit-report.md"] }
  gaps:     { desc: List uncovered features,    cmds: ["atlas cov status --uncovered"] }
  trace:    { desc: "Trace feature (FEATURE=)", cmds: ["atlas trace {{.FEATURE}}"] }
  sprint:   { desc: Gap-weighted sprint plan,   cmds: [atlas sprint] }

  test:go:
    deps: [setup]
    dir: src
    cmds:
      - go test -json -race -count=1 ./... > ../{{.ATLAS_RESULTS}}/go-test.json 2>&1 || true

  test:playwright:
    deps: [setup]
    cmds:
      - pnpm --filter=@nutrition-platform/web-nutritionist exec playwright test --reporter=json > {{.ATLAS_RESULTS}}/playwright-nutritionist.json 2>&1 || true
      - pnpm --filter=@nutrition-platform/web-patient      exec playwright test --reporter=json > {{.ATLAS_RESULTS}}/playwright-patient.json      2>&1 || true
```

---

## 7. Cutover steps (Atlas-first, not parallel-run)

**Ordered checklist.** Do these in one PR so the repo never sits in a
half-migrated state on `main`.

1. **Install Atlas locally:**
   ```bash
   go install github.com/sosalejandro/atlas/cmd/atlas@latest
   atlas --version    # confirm it's on PATH
   ```

2. **Rename the config file:**
   ```bash
   git mv .testreg.yaml .atlas.yaml
   ```
   Then edit the fields as per §5 (field names changed slightly; values
   transfer 1:1).

3. **Rewrite the taskfile:**
   ```bash
   git mv taskfiles/testreg.yml taskfiles/atlas.yml
   # then edit per the template in §6
   ```
   Update `Taskfile.yml`'s `includes:` block to point at `taskfiles/atlas.yml`
   in the same commit.

4. **Build the SQLite store from code annotations:**
   ```bash
   atlas init     # creates .atlas/atlas.db + minimal .atlas.yaml
   atlas scan     # walks the repo, materializes features from annotations
   ```
   In Phase 9 the source of truth is in-code annotations. There is no YAML
   import step — the 40-ish YAML files under `docs/testing/registry/`
   contain no data atlas doesn't already pick up from the `@testreg` /
   `@atlas:*` comments on the symbols themselves. First scan takes
   ~30–60s on a 1k-feature codebase; subsequent runs are incremental (~5s).

5. **Archive the YAML registry:**
   ```bash
   mkdir -p docs/testing/registry/_legacy
   git mv docs/testing/registry/*.yaml docs/testing/registry/_legacy/
   ```
   Keep `_legacy/` checked in as reference-only material so rollback (§8) is
   one `git revert` away. Nothing in Atlas reads from `_legacy/`.

6. **Verify parity vs. the last testreg sync:**
   ```bash
   task atlas:sync
   # diff against your last captured `task testreg:sync` output
   ```
   Acceptance: `atlas audit` health scores should be within ±5% of the
   last testreg audit. Feature counts should match exactly (annotations
   are the source of truth in both tools).

7. **Open the cutover PR.** Remove `taskfiles/testreg.yml`, `.testreg.yaml`,
   and any CI workflow that invokes `testreg` directly — all in the same
   commit. Reviewer should see: config renamed, taskfile rewritten, YAMLs
   moved to `_legacy/`, no orphaned `testreg` references via
   `grep -r testreg .github taskfiles Taskfile.yml`.

8. **After merge — uninstall the old binary:**
   ```bash
   rm $(which testreg)
   # or, if installed via `go install`, just delete from $GOPATH/bin
   ```
   Future `go install` won't reinstall it because nothing depends on it.

---

## 8. Rollback plan

If a regression surfaces post-cutover and you need to bail:

1. **Revert the cutover commit:**
   ```bash
   git revert <cutover-sha>
   git push
   ```
   This restores `.testreg.yaml`, `taskfiles/testreg.yml`, and (because
   the YAMLs were `git mv`'d into `_legacy/`, not deleted) the YAML
   registry is recoverable by reversing the move.

2. **Recover the YAML registry:**
   ```bash
   git mv docs/testing/registry/_legacy/*.yaml docs/testing/registry/
   ```
   testreg will read it on next scan; no data was lost (Atlas only ever
   imported from it, never wrote to it).

3. **Reinstall testreg:**
   ```bash
   go install github.com/sosalejandro/testreg/cmd/testreg@latest
   ```
   testreg's archived repo is still installable; the archive freezes the
   binary, it doesn't remove it.

4. **File the Atlas regression on `sosalejandro/atlas`** (not on testreg,
   which is archived and not accepting issues). Tag the issue
   `regression-during-cutover` so the maintainer can prioritize.

**Important:** the project policy is **hotfix Atlas, don't fall back**.
Rollback exists for emergency-only use (production-impacting regression
with no Atlas fix in sight within 24h). The cutover is intentionally
not parallel-run precisely because parallel-run breeds drift; we'd
rather take the hit, fix forward, and stay on one tool.

---

## 9. What testreg features are NOT in Atlas v0

| testreg feature      | Atlas v0 status | Replacement / plan                                   |
| -------------------- | --------------- | ---------------------------------------------------- |
| `testreg serve` dashboard (htmx + Go templates) | **DROPPED** | CLI-only. Future React SPA can layer on stable JSON. |
| Python scanner       | **DROPPED**     | 26 files in primary consumer doesn't justify the runtime dep. Revisit if Python codebase grows. |
| YAML registry as source-of-truth | **REPLACED** | Code annotations are now the only source of truth; YAML is an output (`atlas dump --format yaml`). |
| `testreg update --metrics` flag  | **REPLACED** | `atlas cov ingest` always emits metrics; flag removed. |

### The JSON output contract (what keeps the dashboard door open)

Every Atlas subcommand emits stable JSON behind `--format json`. The
schema is versioned (`schema_version` field on every payload) and lives
under `docs/api/`. **A future dashboard can be built by any team without
touching Atlas core** — it can shell out to `atlas trace ... --format json`,
or import the `packages/store` SQLite reader directly, and assemble its
own UI.

This is the explicit deal: dashboards are out of v0 scope, but the JSON
contract is in scope and treated as a public API. Breaking-change rules:

- Adding fields → minor bump, non-breaking.
- Renaming / removing fields → major bump, documented in changelog.
- Restructuring nesting → major bump.

---

## 10. FAQ

**Q. What about my 1,000+ existing `@testreg` annotations?**

Atlas reads them. No forced rename. They behave identically to
`@atlas:feature <id>` — same id resolution, same graph membership, same
audit weight. Run `atlas migrate-annotations --apply` on your own
schedule, or never. Both grammars are first-class for v0 and v1.

---

**Q. Can I run testreg and atlas in parallel during cutover?**

Discouraged. The cutover model is **Atlas-first, testreg archived from day
one of your cutover PR**. Parallel-run breeds the exact drift Atlas was built
to eliminate (two registries, neither trusted). If Atlas regresses post-cutover,
hotfix Atlas — don't fall back. §8's rollback is for emergencies only.

---

**Q. Where did my dashboard go?**

Dropped from v0. The htmx-based `testreg serve` console is reference-only in
the atlas repo (`internal/server/`) until Phase 7 cleanup. The stable
`--format json` contract (§9) lets a future dashboard (React SPA, TUI, Slack
bot, anything) layer on without touching Atlas core.

---

**Q. Will my CI break?**

Only if your CI directly invokes `testreg` — rewrite those invocations
to `atlas` per §4's command map. The most common cases:

- `testreg audit` in a PR check → `atlas audit`.
- `testreg scan` in a nightly job → `atlas scan`.
- `testreg gaps` in a release gate → `atlas cov status --uncovered`.

If your CI invokes `task testreg:sync`, rename the task to
`task atlas:sync` (per §6) and the CI doesn't need to know about the
underlying binary swap.

---

**Q. Why no Python support yet?**

Small footprint in the primary consumer codebase (26 .py vs. 2,320 Go and
2,139 TS/TSX — under 1%). Revisit if Python grows past ~10% or a second
consumer adopts Atlas with a Python-heavy codebase. Tracking issue:
`sosalejandro/atlas#future-python`.

---

**Q. Is the YAML registry totally gone?**

**As a source of truth, yes** — Atlas re-derives feature membership from code
annotations on every scan. **As an output format, available** via
`atlas dump --format yaml --feature <id>` (or `--all`) for tooling that still
consumes the YAML shape. Nobody hand-edits it anymore (no more drift).

---

**Q. What about my `task testreg:dashboard` muscle memory?**

Replaced by `atlas serve` in a future phase (no ETA — gated on dashboard SPA
work being in scope). For now: use the CLI, or pipe `--format json` into your
editor / Slack / whatever you actually use. The dashboard was the
least-used surface of testreg, so dropping it is intentional, not incidental.

---

**Q. Does Atlas support Huma routers?**

Yes. testreg's `route_parser.go` handled Chi, Echo, and net/http; Atlas
adds Huma in `packages/routeparse` (the primary consumer migrated to
Huma during 2025). Other router parsers from testreg are carried over
unchanged.

---

## 11. See also

- [`docs/architecture.md`](./architecture.md) — package boundaries + dependency direction
- [`docs/annotations.md`](./annotations.md) — `@atlas:<kind> <id>` grammar in full
- [`docs/schema-v1.md`](./schema-v1.md) — SQLite schema reference (what gets persisted)
- [`docs/api/`](./api/) — per-subcommand JSON output contract
