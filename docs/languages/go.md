# Go language guide

The Go scanner is atlas's reference implementation. It's always on (no
runtime dependency), reads from
[`packages/codeindex/go/`](../../packages/codeindex/go/), and produces
the canonical `shared.Symbol` + `graph.Edge` shape every other scanner
mirrors.

## Prerequisites

- Atlas itself (`go install github.com/sosalejandro/atlas/cmd/atlas@latest`).
- The Go toolchain is **not** required at scan time — atlas parses `.go`
  files directly via `go/ast` baked into the atlas binary. You can run
  `atlas` against a Go codebase from a machine that doesn't have `go`
  installed.

There is no `go.mod` requirement; atlas indexes loose `.go` files in any
layout.

## What gets indexed

The Go scanner walks every `.go` file under the project root (including
`_test.go`) and surfaces:

| What                                                  | Symbol kind     | Notes                                                                        |
| ----------------------------------------------------- | --------------- | ---------------------------------------------------------------------------- |
| Package-level functions                               | `function`      | `func DoThing(...) {...}`                                                    |
| Methods on a receiver                                 | `function`      | `func (h *Handler) Login(...) {...}` — qualified name includes receiver.    |
| Struct type declarations                              | `type`          | Carry `@atlas:aggregate` annotations when applicable.                        |
| Interface declarations                                | `type`          |                                                                              |
| `@atlas:*` annotation comments                        | (annotation)    | Bound to the next declared symbol on the same/following line.                |
| Call-graph edges from one function to another         | `call`          | Limited to in-package + qualified `pkg.Func()` calls atlas can resolve.      |
| Wire / Fx DI bindings                                 | `dep_inject`    | Wire `wire.Build` sets + Fx `fx.Provide` calls.                              |
| SQLC method ↔ SQL file mappings                       | `sql_query`     | Joins generated `*.sql.go` methods to their `*.sql` files.                   |
| HTTP route declarations (Chi, Echo, stdlib, Huma)     | (extracted by `atlas contract list`) | Surfaces as `route` contracts, not raw symbols. |

The scanner skips by default:

- Directories named `vendor/`, `node_modules/`, or starting with `.`
  (`.git/`, `.atlas/`, etc.).
- Files inside any `generated/` subdirectory (atlas considers them
  derived — annotate the source they're generated from instead).

`_test.go` files are **included** by default — they're where
`@atlas:feature` lives most often. Pass
`codeindex/go.Options.SkipTests = true` via the library API if a caller
needs production-only indexing.

## Sample project layout

A minimal Go project the scanner happily indexes:

```
my-go-svc/
├── go.mod
├── auth/
│   ├── handler.go          ← @atlas:feature auth.login, @atlas:contract auth.login
│   ├── service.go          ← @atlas:aggregate identity.auth
│   └── handler_test.go     ← @atlas:feature auth.login + #real (test belongs to feature)
└── billing/
    ├── handler.go          ← @atlas:feature billing.subscribe
    └── service.go
```

After `atlas init` this materialises into roughly:

```
features:      2 (auth.login, billing.subscribe)
aggregates:    1 (identity.auth)
contracts:     1 (auth.login)
symbols:       6 (3 handlers, 2 services, 1 type)
edges:         3 (handler -> service call chain)
```

## Worked queries

### Where is this handler?

```
# Run from: a Go-only project root, after `atlas init`
$ atlas codebase find AuthHandler.Login
AuthHandler.Login  auth/handler.go:14  [func]
```

### What does this feature touch?

```
# Run from: project root
$ atlas trace auth.login
trace feature auth.login (3 nodes)
AuthHandler.Login  [func] auth/handler.go:14
  AuthService.Authenticate  [func] auth/service.go:26
  AuthService.IssueToken  [func] auth/service.go:30
```

### Which aggregate roots are declared?

```
# Run from: project root
$ atlas codebase agg identity.auth
aggregate identity.auth
  decl: auth/service.go:23  identity.auth
  service: (none)
```

When a function in the same file carries
`// @atlas:aggregate-service identity.auth`, the `service: (none)` line
becomes `service: <file>:<line>` instead.

### Where do I emit this event?

```
# Run from: project root
$ atlas codebase emit user.signed_up
event user.signed_up (2 sites)
  auth/service.go:48   [event-emit]
  auth/outbox.go:12    [outbox-publish]
```

The split between `event-emit` and `outbox-publish` is intentional —
emit annotates the domain decision; outbox annotates the persistence
side that ensures at-least-once delivery.

## Common gotchas

### 1. Cross-package call edges may be missing

The Go AST scanner resolves call edges through atlas's `resolver/` package,
which knows about Wire and Fx but **not** about dynamic dispatch or
reflection-based DI containers. If your service calls
`container.Resolve("AuthService").(*AuthService).Login(...)`, atlas can't
trace through it — the edge will silently be absent from
`atlas trace auth.login`. Workaround: annotate the explicit call site with
`@atlas:contract auth.login` so the audit picks it up even if the trace
chain doesn't reach it.

### 2. Generated code is dropped silently

Any file under a `generated/` subdirectory is skipped — atlas considers
it derived from `.sql` or `.proto` sources. If you keep your
`oapi-codegen` / `sqlc` / `protoc` output somewhere atlas doesn't
recognise (e.g. a top-level `gen/` directory), it'll be indexed normally.
Either rename the directory to include `generated/` in the path, or
configure `codeindex/go.Options.IgnorePackages` programmatically via the
library API.

### 3. Receivers vs free functions in qualified names

A method `func (h *AuthHandler) Login(...)` has the qualified name
`AuthHandler.Login` — the receiver type wins; the package path is
implicit. A free function `func Login(...)` in the same package gets the
qualified name `Login` (no receiver). This means a free function named
the same as a method shadows the method in suffix-match queries; always
disambiguate with `symbol:<pkg>.<name>` when both exist.

### 4. `init()` and `main()` are indexed but rarely useful in traces

`init()` functions don't link cleanly into the call graph — they fire
implicitly. They're stored as symbols so `atlas codebase find init`
works, but `atlas trace` won't follow into them. Same for `main()`:
it's the entry point, but most call-graphs of interest start one level
deeper (handler / service).

## Related

- Annotation grammar (every `@atlas:<kind>` is parsed by the same engine
  across languages): [`docs/annotations.md`](../annotations.md).
- TypeScript scanner: [`docs/languages/ts.md`](./ts.md).
- Python scanner: [`docs/languages/py.md`](./py.md).
- Per-command reference: [`docs/commands/`](../commands/).
