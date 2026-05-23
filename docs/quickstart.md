# Atlas Quickstart — first 5 minutes

Atlas is a code-graph + coverage + audit toolkit for polyglot codebases. It
indexes Go, TypeScript, and Python sources into a per-project SQLite store
and exposes that index through read-only CLI verbs (`atlas trace`,
`atlas audit`, `atlas codebase find`, `atlas diagnose`).

This page walks through the canonical workflow: install, initialise the
state DB, scan, and run the four daily-driver query verbs. All output
shown below is captured from a small mixed-language fixture (Go + TS +
Python in one project).

For per-language gotchas, see [`docs/languages/`](./languages/). For the
full per-verb reference, see [`docs/commands/`](./commands/).

---

## 1. Install

```bash
go install github.com/sosalejandro/atlas/cmd/atlas@latest
```

For a specific tagged release, swap `@latest` for the version
(e.g. `@v0.3.0`). See the [release history](https://github.com/sosalejandro/atlas/releases)
for the full changelog.

### Verify

```
$ atlas --version
atlas version v0.3.0 (commit e938b2b, built 2026-05-22T...)
```

If the version reports `dev` you installed from a non-tag ref — pin a
tagged release for reproducibility.

### Optional runtime dependencies

The TypeScript and Python sub-scanners shell out to native runtimes. Each
is **optional** — atlas continues with the languages it can handle and
emits a single warning per missing runtime:

| Language    | Runtime    | Floor    | Default behavior if missing                |
| ----------- | ---------- | -------- | ------------------------------------------ |
| Go          | (none)     | —        | Always indexed; never disabled.            |
| TypeScript  | `node`     | 18+      | TS sources skipped; one warning emitted.   |
| Python      | `python3`  | 3.8+     | Python sources skipped; one warning.       |

The full per-language reference: [`docs/languages/`](./languages/).

---

## 2. Initialise — `atlas init`

`atlas init` performs the one-time bootstrap: it walks the project,
creates `.atlas/atlas.db`, applies the schema, and ingests the first
index.

```
# Run from: /tmp/atlas-fixture (a 3-file Go + TS + Python project)
$ atlas init
Atlas initialised /tmp/atlas-fixture/.atlas/atlas.db (root: /tmp/atlas-fixture)
  symbols=9 edges=2 annotations=7 file_hashes=4 pattern_matches=0
  features=3 feature_symbols=3 orphan_annotations=1
  files_scanned=4 files_skipped=0 duration=1ms
  warning: no router signal detected (react-router, tanstack, or expo)
```

Reading the summary:

- `symbols=9` — nine declared symbols across all languages (functions,
  classes, methods).
- `edges=2` — two call-graph edges.
- `annotations=7` — seven `@atlas:*` markers harvested.
- `features=3` — three distinct feature ids declared.
- `orphan_annotations=1` — one annotation (the file-level `@atlas:bc`)
  isn't bound to a symbol; that's the intended shape.
- The router-signal warning is normal on backend / fixture projects — see
  [`docs/languages/ts.md`](./languages/ts.md) for what triggers a TS
  router discovery.

The state DB lives at `.atlas/atlas.db`. Add it to `.gitignore` — it's
per-checkout state, not source.

---

## 3. Re-scan — `atlas scan`

After `init`, run `atlas scan` whenever sources change. It's hash-driven
and incremental:

```
# Run from: /tmp/atlas-fixture, immediately after `atlas init`
$ atlas scan
Atlas scan complete (root: /tmp/atlas-fixture, db: /tmp/atlas-fixture/.atlas/atlas.db)
  symbols=0 edges=0 annotations=0 file_hashes=4 pattern_matches=0
  files_scanned=4 files_skipped=4 duration=0ms
  warning: no router signal detected (react-router, tanstack, or expo)
```

`files_skipped=4` is the cache doing its job — every source has the same
SHA-256 as the previous scan, so the AST walker never fired. Warm scans
finish in single-digit milliseconds; the verb is safe to add to a
pre-commit hook.

For deeper detail: [`docs/commands/scan.md`](./commands/scan.md).

---

## 4. Query — the four daily-driver verbs

### Where is X? — `atlas codebase find`

```
# Run from: /tmp/atlas-fixture
$ atlas codebase find AuthHandler.Login
AuthHandler.Login  go/auth.go:14  [func]
```

`find` resolves a qualified symbol name to its source position. Suffix
matching is supported: `find Login` resolves the same symbol because
`Login` is the unique suffix of `AuthHandler.Login`.

Cross-language lookups share one namespace:

```
# Run from: /tmp/atlas-fixture
$ atlas codebase find py.billing.BillingService
py.billing.BillingService  py/billing.py:13  [type]
```

The `py.` prefix is the Python scanner's module-path namespace. See
[`docs/languages/py.md`](./languages/py.md) for the full Python symbol
shape.

### What does X call? — `atlas trace`

```
# Run from: /tmp/atlas-fixture
$ atlas trace auth.login
trace feature auth.login (3 nodes)
AuthHandler.Login  [func] go/auth.go:14
  AuthService.Authenticate  [func] go/auth.go:26
  AuthService.IssueToken  [func] go/auth.go:30
```

`trace` walks the call graph from a feature id (above), a SymbolID (e.g.
`AuthHandler.Login`), or an EDA saga (`saga:<id>`). The output is a
text tree; pass `--json` for the structured envelope.

The cached walk costs milliseconds because the adjacency list lives in
SQLite — `atlas trace --fresh` is the escape hatch when you suspect the
cache is wrong AND `atlas scan` hasn't caught the drift.

### What needs attention? — `atlas audit`

```
# Run from: /tmp/atlas-fixture
$ atlas audit --worst 2
billing.subscribe                                   score=  0.00
    - no audit signals available (no coverage, no aggregate, no contract, no annotation source)
auth.login                                          score=100.00
    annotation_freshness   100.00
```

`audit` scores every feature 0–100 and prints worst-first. The fixture
has only two features; on a real codebase `--worst 10` is the daily call.

The score is a weighted roll-up across audit components — coverage
pass-rate, annotation freshness, aggregate linkage, contract presence.
When all components are absent, atlas prints "no audit signals available"
instead of a misleading numeric zero.

### Where would this error come from? — `atlas diagnose`

```
# Run from: /tmp/atlas-fixture
$ atlas diagnose "Authenticate"
  0.450  AuthHandler.Login                                   go/auth.go:14  [feature=auth.login]
    matched whole symptom 2x in body; matched 2 symptom tokens
  0.425  AuthService.Authenticate                            go/auth.go:26  [feature=-]
    matched whole symptom 1x in body; matched 1 symptom tokens
```

`diagnose` is the triage tool. Pass it an error message or log-line
snippet; it ranks indexed symbols by likelihood of having produced that
text. Confidence is a 0–1 lexical match score; raise the floor with
`--min-confidence 0.3` to drop weaker candidates.

---

## Where to go next

- **Per-language guides** — language-specific prerequisites, what gets
  indexed, and gotchas:
  - [`docs/languages/go.md`](./languages/go.md) — Go AST scanner.
  - [`docs/languages/ts.md`](./languages/ts.md) — TypeScript scanner
    (router-aware).
  - [`docs/languages/py.md`](./languages/py.md) — Python AST scanner.
- **Per-command reference** — every verb, every flag, with examples:
  [`docs/commands/`](./commands/).
- **Annotation grammar** — the `@atlas:<kind> <id>` syntax that powers
  feature discovery: [`docs/annotations.md`](./annotations.md).
- **Architecture** — package boundaries and dependency direction:
  [`docs/architecture.md`](./architecture.md).
- **Coming from testreg?** —
  [`docs/migration-from-testreg.md`](./migration-from-testreg.md) is the
  cutover guide.

The canonical 4-verb workflow (`init` → `scan` → `codebase find` →
`trace`) handles roughly 80% of day-to-day atlas use. The remaining 20%
— `audit`, `diagnose`, `sprint`, `snapshot`, `diff`, `cov sync`,
`contract list`, `codebase emit/agg/bc/consumer/pattern` — is the
machinery for CI baselines, gap-weighted backlog ranking, EDA-pattern
introspection, and cross-team coverage reporting. Read the per-verb
docs as needed.
