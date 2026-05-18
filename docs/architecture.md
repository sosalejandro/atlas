# Atlas Architecture

Authoritative design for the Atlas monorepo. This is the source of truth for
package boundaries, dependency direction, and the CLI surface. Anything that
contradicts this doc is wrong — fix the doc first, then the code.

Related references:

- Plan of record: `~/.claude/plans/abstract-questing-engelbart.md`
- Annotation grammar: `docs/annotations.md`
- SQLite schema: `docs/schema-v1.md`
- Migration from testreg: `docs/migration-from-testreg.md`

---

## 1. Vision

Atlas is a code-graph + coverage + audit toolkit for polyglot codebases, built
as a monorepo of SRP-focused libraries with a single `atlas` CLI on top. It
discovers what your codebase *is* by parsing source — call graphs, HTTP routes,
DI bindings, SQL queries, annotated features — and answers questions about
coverage, drift, and impact without depending on hand-maintained inventories.

Atlas supersedes testreg. Atlas is library-first: every CLI subcommand is a
thin wrapper around a `packages/<x>/` Go library that external consumers
(starting with `bmad-cli`) import directly with no subprocess tax.

---

## 2. Design principles

The following principles are load-bearing. Violating one of them is a
review-blocking defect, not a style preference.

**Code is the source of truth; derived data is a view.**
Annotations live in source (`// @atlas:feature auth.login`). The SQLite
state is a *cache* — nuke it and Atlas reconstructs it from a fresh scan.
There is no hand-maintained YAML registry to drift.

**Library-first, CLI-second.**
Every `cmd/atlas <verb>` is a ~50 LOC adapter that parses flags, calls into a
`packages/<x>` library, formats the result. No business logic in `internal/cli/`.
External Go programs that want Atlas's capabilities import the packages, never
shell out to the binary.

**Stable JSON output contracts.**
Every subcommand emits JSON tagged with `schema_version`. Schema changes are
additive: new fields appear alongside old ones; consumers can pin and migrate
on their own clock. See §6.

**No mandatory persistence.**
The SQLite store is an optimization (incremental re-scan, cross-command cache),
not a requirement. `atlas trace foo` works in a fresh checkout with no `.atlas/`
directory. Persistence is opt-in via `atlas init`.

**Each package is one SRP concern; no god-files.**
testreg had a 1,376-LOC `go_ast_scanner.go` doing scan + route + DI + frontend
orchestration. Atlas splits that across `codeindex/go/`, `routeparse/`,
`resolver/`, `codeindex/ts/` — each package owns one concern end-to-end.

**Detect drift; don't silently allow it.**
If annotations reference symbols Atlas can't find in the code, that's a
diagnostic, not a warning we swallow. The whole point of the tool is to surface
divergence between intent (annotations, contracts) and reality (the AST).

**Start tiny; grow only on demand.**
Python scanner and dashboard SPA are explicitly out of v0 (§9). New packages
land only when a real consumer needs them, not speculatively.

---

## 3. Package boundaries

All packages live under `packages/<name>/` and use the module path
`github.com/sosalejandro/atlas/packages/<name>`. Every package declares its
dependencies in §4 and is unit-testable in isolation. **The contract for each
package is its exported Go API plus, if it has a CLI subcommand, the JSON
schema tagged with `schema_version`.**

### 3.1 `shared/`

The kernel. Types every package depends on: `FilePosition`, `FeatureID`,
`SymbolID`, `SymbolKind`, structured `Error` wrappers, logger interface.

**Imports:** nothing from inside Atlas (stdlib only, plus
`golang.org/x/exp/slog` or zap if we settle on one).

**Does NOT touch:** SQLite, AST parsing, file I/O beyond `os.FileInfo`. If
`shared/` grows a dependency on anything else in the tree, that's a layering
violation.

```go
package shared

type FeatureID string                  // e.g. "auth.login"
type SymbolID string                   // e.g. "pkg/handler.Login"
type SymbolKind string                 // handler|service|repository|query|hook|component|endpoint|external

type FilePosition struct {
    Path string // repo-relative
    Line int
    Col  int    // 1-based, 0 if unknown
}
```

### 3.2 `codeindex/`

The foundation. Parses source files into an in-memory `*graph.Graph`. Three
language sub-areas:

#### `codeindex/go/`
Go AST scanner. Successor to testreg's `internal/adapters/go_ast_scanner.go`
(1,376 LOC). Walks `.go` files with `go/parser`, builds nodes for func/method
decls, extracts call edges from function bodies. No `go/types`, no `golang.org/x/tools`
— stdlib-only parsing keeps it fast and free of module-resolution headaches.

**Imports:** `shared`, `graph`, `resolver` (for DI-aware call resolution),
`sqlcmap` (for query method → SQL file edges), `routeparse` (for route → handler
edges).

**Does NOT touch:** SQLite, coverage data, HTTP, TypeScript.

#### `codeindex/ts/`
TypeScript scanner. Embeds testreg's `internal/adapters/embedded_ts_scanner.ts`
via `//go:embed`. Orchestrates a Node subprocess (`node --experimental-strip-types`)
and parses its JSON output into the same `graph.Graph` shape Go produces.
Supports React Router, TanStack Router, Expo Router. **No Next.js wrapper** —
not in the target stack.

**Imports:** `shared`, `graph`.

**Does NOT touch:** Go AST, SQL, DI introspection. Frontend nodes connect to
Go backend nodes only at the `endpoint:` boundary (the URL string is the join
key), and that join happens in `codeindex/` orchestration code, not inside
`ts/`.

#### `codeindex/annotations/`
Multi-language annotation parser. Successor to testreg's `annotation_parser.go`.
Recognises **both** annotation grammars:

```go
// @atlas:feature  auth.login   tag1 tag2     // canonical
// @testreg        auth.login   #tag1 #tag2   // legacy, still valid
```

Supports `.go`, `.ts`, `.tsx`, `.js`, `.jsx`, `.py`. Extracts function names
near each annotation (via per-language regex — full AST not needed for this).
Also handles `@api METHOD /path` for handler discovery and `//go:build e2e`
build-tag detection.

**Imports:** `shared` only.

**Does NOT touch:** the graph package, SQLite, anything that knows about
feature semantics. This package returns raw annotation records; mapping them to
features happens in `coverage/` and `audit/`.

### 3.3 `graph/`

Pure data structures: `Node`, `Edge`, `Graph`, `TraceResult`, `TraceNode`.
Adjacency lists are lazily built and invalidated on edge mutation (preserves
testreg's `internal/domain/graph.go` semantics verbatim — that file ports
1:1). Cycle detection on edge add; DFS-based trace and reverse-BFS path
finding (`FindPathTo`, `TraceCallersFrom`).

**Imports:** `shared`.

**Does NOT touch:** anything else. `graph/` is a passive data structure —
callers populate it; it does not parse, persist, or render.

```go
type Graph struct {
    Nodes map[SymbolID]*Node
    Edges []Edge
    // unexported adjacency caches
}

func (g *Graph) AddNode(n *Node)
func (g *Graph) AddEdge(from, to SymbolID)
func (g *Graph) TraceFrom(root SymbolID, maxDepth int) *TraceResult
func (g *Graph) FindPathTo(target SymbolID, routeHint string, maxDepth int) []SymbolID
```

### 3.4 `resolver/`

DI introspection. Two adapters:

- **Wire** — ports `internal/adapters/wire_resolver.go`. Parses `wire.go`
  files for `wire.NewSet`, `wire.Build`, `wire.Bind` calls; infers
  interface→concrete bindings from provider func return types when no explicit
  `Bind` exists.
- **Fx** — ports `internal/adapters/fx_resolver.go` (smaller; uber-fx pattern).

Returns `map[InterfaceID]ConcreteBinding`. Consumed by `codeindex/go/` to
follow `service.SomeInterface.Method()` calls into the concrete implementation.

**Imports:** `shared`.

**Does NOT touch:** the graph (it produces a *map*, not edges — `codeindex/go/`
turns those into edges). Does not touch SQLite or HTTP.

### 3.5 `sqlcmap/`

SQLC method ↔ SQL file mapper. Ports `internal/adapters/sqlc_mapper.go` ~as-is.
Reads `sqlc.yaml`, walks the queries directory, parses `-- name: GetX :one`
annotations, returns `map[GoMethodName]SQLCMapping{File, Line, QueryType}`.

**Imports:** `shared`, `gopkg.in/yaml.v3`.

**Does NOT touch:** Go AST (it operates on `.sql` files), the graph (the map
is consumed by `codeindex/go/` to add `service.Method` → `query:Name` edges).

### 3.6 `routeparse/`

HTTP route discovery. Ports `internal/adapters/route_parser.go`. Recognises
Chi (`r.Get`), Echo (`e.GET`), stdlib (`mux.HandleFunc("POST /p", h)`), and —
**new in Atlas** — Huma (`huma.Register[Input, Output](api, op, handler)`),
because the cutover target (nutrition-v2-go) is on Huma. Returns
`[]RouteMapping{Method, Path, HandlerRef, FilePosition}`.

**Imports:** `shared` (uses stdlib `go/ast`, `go/parser`).

**Does NOT touch:** the graph; route → handler edges are added by
`codeindex/go/` after `routeparse/` returns. Does not handle frontend routes —
those are `codeindex/ts/`'s job.

### 3.7 `store/`

SQLite-backed cache. The *only* package allowed to import `database/sql` and a
SQLite driver. Schema lives in `store/schema/NNNN_*.up.sql` files embedded via
`//go:embed schema/*.sql`, applied in numeric order by
[`golang-migrate/migrate/v4`](https://github.com/golang-migrate/migrate) using
its `iofs` source and the modernc-backed `sqlite` driver. Version tracking
lives in golang-migrate's default `schema_migrations` table.

Per-table SQL queries are generated by [`sqlc`](https://sqlc.dev/) (engine:
`sqlite`, driver: `modernc.org/sqlite`). Hand-written narrow ports wrap the
generated `*sqlc.Queries` type — the public surface (Features, Symbols,
Edges, …) stays domain-typed while the SQL stays declarative under
`packages/store/queries/*.sql`. Generated code lives under
`packages/store/sqlc/` and is committed; CI enforces freshness via
`sqlc diff`.

Tables (v1, see `docs/schema-v1.md` for the authoritative spec — schema-v1.md
is the source of truth for column-level details):

```
features          (feature_id, name, priority, owner, ...)
symbols           (symbol_id, kind, file, line, package)
edges             (from_symbol, to_symbol, ambiguous, cycle)
feature_symbols   (feature_id, symbol_id, role, source)   -- m:n bridge
annotations       (file, line, kind, value, source)
file_hashes       (path, sha256, scanned_at)              -- incremental scan
coverage_runs     (run_id, framework, started_at, ...)
coverage_results  (run_id, test_name, status, file, line, feature_ids)
audit_snapshots   (snapshot_id, taken_at, scores_json)
config            (key, value, updated_at)
schema_migrations (version, dirty)                        -- golang-migrate managed
```

**Imports:** `shared`. Schema migrations are pure SQL — no Go logic per
migration step (just `up.sql`; no `down.sql` for v1 — destructive rollbacks
should re-init from scratch).

**Does NOT touch:** parsers, the graph (it persists scan results *into* itself
and rehydrates them; it does not parse anything itself). The store is dumb on
purpose — it's a query layer over a known schema.

### 3.8 `coverage/`

Test result ingestion. One sub-package per framework:

```
coverage/gotest/      ingests `go test -json` output
coverage/playwright/  ingests Playwright JSON reporter output
coverage/vitest/      ingests Vitest JSON reporter output
coverage/jest/        ingests Jest JSON reporter output
coverage/maestro/     ingests Maestro test reports
```

Each adapter implements:

```go
type Ingester interface {
    Framework() string
    Ingest(r io.Reader) ([]TestResult, error)
}
```

A top-level `coverage/sync.go` orchestrator joins `TestResult.File:Line` with
`codeindex/annotations/` records to produce per-feature coverage rows, then
writes them to `store/`.

**Imports:** `shared`, `store`, `codeindex/annotations`.

**Does NOT touch:** the call graph, DI resolution, route parsing. Coverage
cares only about test files and the features they annotate.

### 3.9 `audit/`

Health scoring. Ports the algorithm from `internal/app/audit_feature.go`
(1,084 LOC of weighted layer-coverage math). Inputs: a feature's trace tree
(from `graph/`) + its test files (from `store/`). Outputs an
`AuditOutput{Score, LayerCoverage, Gaps, Recommendations}`.

Layer weights (preserved from testreg):

```
handler:    0.30
service:    0.30
repository: 0.25
query:      0.15
```

**Imports:** `shared`, `graph`, `store`, `codeindex` (for one-shot scans that
don't go through the store).

**Does NOT touch:** parsing directly; `audit/` operates on the data
`codeindex/` and `coverage/` already produced.

### 3.10 `sprintplan/`

Gap-weighted prioritization. Reads audit outputs from `store/`, ranks features
by `(priority × gap_size × age_of_last_change)`, emits a sprint-ready ordering.
Replaces testreg's ad-hoc CLI prioritization with a first-class package so
external tools (bmad-cli) can ask "what's the top-N to work on" without
parsing CLI text.

**Imports:** `shared`, `store`, `audit`.

**Does NOT touch:** parsing, the graph, file I/O outside the store.

### 3.11 `diff/`

Snapshot diff between two `audit_snapshots` rows (or two raw scan outputs).
Answers: "what changed between commit A and commit B in terms of features,
edges, coverage?" Used by CI to comment on PRs.

**Imports:** `shared`, `graph`, `store`, `audit`.

**Does NOT touch:** parsing. Diff operates on already-persisted snapshots.

### 3.12 `contract/`

API contract extraction. For each `@api METHOD /path` handler, walks
`codeindex/go/` and `routeparse/` outputs to produce a typed signature:
request body type, query params, response type, status codes. For TS, extracts
the matching client-side fetch signature. Output is a versioned JSON contract
(`openapi`-shaped but not OpenAPI — we control the schema).

**Imports:** `shared`, `graph`, `codeindex/go`, `codeindex/ts`, `routeparse`,
`store`.

**Does NOT touch:** coverage data; contracts are a structural extraction, not
a quality measure.

### 3.13 `diagnose/`

Error → code matching. Regex/symptom-to-symbol matching — given a log line or
error string, find the symbol(s) that could plausibly produce it. Ports
`internal/app/diagnose_feature.go` logic.

**Imports:** `shared`, `graph`, `store`.

**Does NOT touch:** parsing; works off the indexed graph.

---

## 4. Dependency direction

**Rule:** dependencies flow DOWN. `shared/` sits at the bottom; nothing
imports up; nothing imports sideways across the same layer.

```
                          ┌──────────────────────────────────────────────┐
   tier 4 (consumers)    │  cmd/atlas/  +  internal/cli/                │
                          │  external: bmad-cli                          │
                          └──────────────────────────────────────────────┘
                                            │  (imports any package below)
                                            ▼
                          ┌──────────────────────────────────────────────┐
   tier 3 (analyses)     │  coverage    audit    sprintplan             │
                          │  diff        contract diagnose               │
                          └──────────────────────────────────────────────┘
                                            │
                                            ▼
                          ┌──────────────────────────────────────────────┐
   tier 2 (foundation)   │  codeindex/{go, ts, annotations}             │
                          │  ↓                                            │
                          │  resolver   sqlcmap   routeparse             │
                          │  ↓                                            │
                          │  graph                                        │
                          └──────────────────────────────────────────────┘
                                            │
                                            ▼
                          ┌──────────────────────────────────────────────┐
   tier 1 (kernel)       │  shared                                       │
                          └──────────────────────────────────────────────┘

   side-channel:           store  — sits beside tier 2; importable by any
                                    tier-3 package; imports only shared
```

Specifics:

- `shared/` imports nothing from inside the repo.
- `graph/`, `resolver/`, `sqlcmap/`, `routeparse/`, `store/` import only
  `shared/`.
- `codeindex/` imports `shared/`, `graph/`, `resolver/`, `sqlcmap/`,
  `routeparse/`. It does **not** import `store/` directly — the orchestrator
  that wires `codeindex/` to `store/` lives in `coverage/sync.go` or in a CLI
  command, not inside `codeindex/`.
- Tier 3 (`coverage`, `audit`, `sprintplan`, `diff`, `contract`, `diagnose`)
  may import any tier-2 package + `store/`. Tier-3 packages **must not import
  each other** with one exception: `sprintplan/` → `audit/`, `diff/` →
  `audit/`. Anything beyond that needs a refactor (probably a new tier-3.5
  package).
- `internal/cli/` and `cmd/atlas/` can import anything below them. **No
  package outside `internal/cli/` may import `internal/cli/`.**

Enforced by `golangci-lint depguard` rules in CI (TODO Phase 7).

---

## 5. CLI shape

Single binary at `cmd/atlas/main.go`. Verb-namespaced subcommands, implemented
in `internal/cli/<verb>.go` using cobra. Every subcommand is a ~50 LOC
adapter: parse flags → call package API → format result.

```
atlas init                    Scan project, create .atlas/state.db, persist baseline
atlas scan                    Re-scan (incremental via file_hashes), update store
atlas trace <feature>         Show call-chain for a feature
atlas cov sync                Ingest test framework outputs into store
atlas cov status [<feature>]  Per-feature coverage summary
atlas audit [<feature>]       Health score + gap list
atlas sprint [--top N]        Gap-weighted feature prioritization
atlas diff <ref-a> <ref-b>    Snapshot diff between two refs
atlas contract <feature>      Extract API contract (req/resp types)
atlas diagnose <error-string> Match an error to candidate symbols
atlas migrate-annotations     Bulk-rename @testreg → @atlas (--dry-run|--apply)
```

Subcommand → packages composed:

| Subcommand | Packages |
|---|---|
| `init` | `codeindex`, `store` |
| `scan` | `codeindex`, `store` |
| `trace` | `codeindex` (or `store` rehydrate) → `graph` |
| `cov sync` | `coverage`, `codeindex/annotations`, `store` |
| `cov status` | `store`, `coverage` |
| `audit` | `audit` (composes `codeindex`, `graph`, `store`) |
| `sprint` | `sprintplan` |
| `diff` | `diff` |
| `contract` | `contract` |
| `diagnose` | `diagnose` |
| `migrate-annotations` | `codeindex/annotations` (write-back mode) |

Every subcommand accepts `--json` (default for non-tty stdout) and `--format
table` (default for tty). All errors go to stderr with non-zero exit; stdout
is reserved for the structured output.

---

## 6. JSON contract versioning

Every subcommand emits JSON whose top-level object contains:

```json
{
  "schema_version": "v1",
  "command": "trace",
  "generated_at": "2026-05-17T12:34:56Z",
  "data": { ... }
}
```

**Compatibility rules:**

1. **Additive within a major.** New fields can appear inside `data` at any
   time without bumping `schema_version`. Consumers MUST ignore unknown fields.
2. **Removals or type changes bump the major.** When `v2` ships, the old `v1`
   shape continues to be emittable for at least one minor release of the CLI
   via `--schema v1`.
3. **Coexistence during transitions.** Breaking changes ship behind a new
   top-level key (`data_v2`) alongside the old (`data`) for one minor release,
   then the old key is dropped at the next major. Consumers can read either
   key and migrate on their own clock.
4. **One schema per subcommand.** `atlas trace` and `atlas audit` version
   independently. There is no global "Atlas JSON schema v1" — the contract is
   per-verb.
5. **Schema docs live in `docs/api/<verb>.md`.** Each one is a JSON sample
   + a field-by-field reference + a "changes since" log.

This is the contract that lets a future dashboard SPA, external CI integrations,
or `bmad-cli` consume Atlas output without coupling to undocumented internals.

---

## 7. Schema versioning (SQLite)

The `store/` package owns the only persistent state. Schema lives in
`packages/store/schema/`, applied in numeric order (up-only — no `.down.sql` files):

```
packages/store/schema/
  0001_initial.up.sql        -- all v1 tables (features, symbols, edges,
                                feature_symbols, annotations, file_hashes,
                                coverage_runs, coverage_results,
                                audit_snapshots, config)
  0002_<future>.up.sql       -- forward-only schema evolution
  ...
```

Embedded via `//go:embed schema/*.sql` and applied by
[`golang-migrate/migrate/v4`](https://github.com/golang-migrate/migrate)
via its `iofs` source. Migration runner:

```go
// store.Open opens the SQLite file (creating it if missing), applies any
// pending migrations in numeric order via golang-migrate (which tracks
// applied versions in its default `schema_migrations` table), and returns
// a *Store ready for use.
func Open(ctx context.Context, path string) (*Store, error)
```

**Rules:**

- Migrations are append-only and **immutable** once shipped. Editing an
  applied migration changes the file content; golang-migrate doesn't
  checksum-verify by default, but Atlas's drift-detection layer (see §2
  "detect drift") tracks file SHAs to refuse a tampered migration.
- No `down.sql` for v1. golang-migrate tolerates the absent down files —
  it just loses the ability to step down past a version, which Atlas
  doesn't need. Rollback = delete `.atlas/state.db` and re-run `atlas init`.
  The store is a cache; the source-of-truth is the code.
- Schema reference docs live in `docs/schema-v1.md` and are regenerated from
  the SQL files by a `docs:schema` task (Phase 7).
- Per-table SQL queries live in `packages/store/queries/*.sql` and are
  compiled to Go by `sqlc` (engine: `sqlite`, driver: `modernc.org/sqlite`).
  Generated code under `packages/store/sqlc/` is committed; CI enforces
  freshness via `sqlc diff` so a queries edit without `sqlc generate`
  fails the build.

---

## 8. Cross-cutting concerns

**Logging.** `log/slog` (stdlib, Go 1.21+). `shared/logger.go` exports a thin
`Logger` interface with `Debug/Info/Warn/Error` methods; production code uses
the stdlib JSON handler; tests use a no-op logger. **No** `go.uber.org/zap`
dependency — slog is good enough and the dep matters when we ship a library.

**Context propagation.** Every package-level exported function that does I/O
takes `ctx context.Context` as its first argument. Parsers that operate purely
on bytes (e.g. `annotations.Parse([]byte)`) do not — there's nothing to cancel.

**Error wrapping.** `fmt.Errorf("doing X: %w", err)` throughout. Sentinel
errors live in `shared/errors.go`:

```go
var (
    ErrFeatureNotFound  = errors.New("feature not found")
    ErrSymbolNotFound   = errors.New("symbol not found")
    ErrSchemaDrift      = errors.New("schema migration checksum mismatch")
    ErrAnnotationInvalid = errors.New("annotation grammar invalid")
)
```

Callers test with `errors.Is(err, shared.ErrFeatureNotFound)`. No typed error
hierarchy beyond sentinels — keep it boring.

**Testing.** Three flavors:

1. **Table-driven unit tests** for pure-logic packages (`graph/`, `audit/`,
   `sprintplan/`).
2. **Real-file fixtures** for parsers — `packages/<x>/testdata/fixtures/`
   contains real `.go` / `.ts` / `.sql` files; tests scan them and assert
   on the structured output. Beats hand-written ASTs for catching real-world
   parser breakage. Source: testreg's existing test patterns.
3. **Integration tests** for `store/` (real SQLite file in `t.TempDir()`)
   and `cmd/atlas/` (binary built once, invoked as subprocess against a
   fixture project).

`go test ./...` from repo root runs everything; no build tags. Coverage gate is
`task ci:check` (Phase 7).

---

## 9. Out of scope for v0

The following are **deliberately deferred**. The conditions that would bring
them in are documented so we don't re-argue them.

**Python scanner.**
The cutover target (nutrition-v2-go) has 26 Python files vs 2,320 Go +
2,139 TS. Not enough to justify a Python runtime in Atlas's binary
distribution. **Re-evaluate when:** any Atlas consumer has ≥200 Python files
in a code path that needs trace/coverage, OR a Python-first consumer asks for
it. Estimated 1 week to add (`codeindex/py/` mirroring `codeindex/ts/`'s
subprocess shape).

**Dashboard SPA.**
CLI-only v0. The §6 stable JSON contract is the load-bearing piece — any
future dashboard reads `atlas <verb> --json` outputs without coupling to
internals. **Re-evaluate when:** at least one consumer is doing repeated
`atlas audit` / `atlas trace` interactively for exploration AND CLI ergonomics
become the blocker. Until then, terminal output beats a half-built web app.

**Server mode / daemon.**
testreg had `internal/server/` with htmx templates. Dropped. If incremental
scan latency ever becomes a problem (it shouldn't with the file_hashes cache),
we add `atlas serve` later.

**Cross-repo features.**
Atlas operates on a single repo root. Monorepo-of-monorepos / multi-repo
correlation is not in scope.

---

## 10. Glossary

**Feature** — A logically cohesive slice of behaviour identified by a
`FeatureID` (e.g. `auth.login`, `pantry.add-item`). Annotations attach
symbols (handlers, tests, hooks) to features. Features are *discovered* from
annotations — there is no central feature registry to maintain.

**Symbol** — A named code entity Atlas tracks as a graph node: Go func/method
decl, TS function/component/hook, SQL query, HTTP endpoint. Each has a
`SymbolID`, `SymbolKind`, and `FilePosition`.

**Edge** — A directed "calls" or "depends-on" relationship between two
symbols. Edges may be marked `Ambiguous` (heuristically resolved, e.g.
interface → concrete via DI) or `Cycle` (would close a loop in the DAG).

**FeatureID** — Stable string identifier for a feature. Convention:
dot-namespaced lowercase (`bounded-context.action`). Defined once via an
annotation; referenced from tests, contracts, and audit reports.

**FilePosition** — `(Path, Line, Col)` triple. `Path` is repo-relative.
`Col` is 1-based, 0 means "unknown". This is the only way Atlas refers to
source locations — no absolute paths, no `file://` URIs.

**SymbolKind** — Enum: `handler | service | repository | query | hook |
component | endpoint | external`. Drives layer-weighting in `audit/`.

**Annotation** — A source-comment record matching one of Atlas's recognised
grammars:

```
// @atlas:feature <feature-id> [tag ...]   (canonical, namespaced)
// @atlas:contract <contract-id>            (future)
// @atlas:owner    <team>                   (future)
// @testreg <feature-id> [#tag ...]         (legacy; still recognised)
// @api METHOD /path                        (handler discovery)
```

See `docs/annotations.md` for the full grammar reference.

**FeatureRegistry** — *Historical only.* testreg maintained a hand-written
YAML registry under `docs/testing/registry/`. Atlas does not. The word
"registry" in Atlas docs refers exclusively to the *derived* index Atlas
builds in the `store/` from code annotations — there is no human-maintained
inventory to keep in sync.
