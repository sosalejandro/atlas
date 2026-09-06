# Go language guide

The Go scanner is atlas's reference implementation. It's always on (no
runtime dependency), reads from
[`packages/codeindex/go/`](../../packages/codeindex/go/), and produces
the canonical `shared.Symbol` + `graph.Edge` shape every other scanner
mirrors.

## Prerequisites

- Atlas itself (`go install github.com/sosalejandro/atlas/cmd/atlas@latest`).
- The Go toolchain is **recommended but not required**. With `go` on
  PATH and a module that compiles, atlas resolves calls with
  `go/packages` + `go/types` and every call edge is exact. Without it —
  no toolchain, no `go.mod`, or a build that is red — atlas falls back to
  parsing `.go` files with `go/ast` alone and resolving calls by name.
  The scan succeeds either way; what changes is how much of it atlas can
  vouch for. See [How calls are resolved](#how-calls-are-resolved).

There is no hard `go.mod` requirement; atlas indexes loose `.go` files in
any layout, at the fallback fidelity.

## What gets indexed

The Go scanner walks every `.go` file under the project root (including
`_test.go`) and surfaces:

| What                                                  | Symbol kind     | Notes                                                                        |
| ----------------------------------------------------- | --------------- | ---------------------------------------------------------------------------- |
| Package-level functions                               | `function`      | `func DoThing(...) {...}` — exported **and** package-private (`func helper()`), because the compiler instruments both for coverage. |
| Methods on a receiver                                 | `function`      | `func (h *Handler) Login(...) {...}` — qualified name includes receiver.    |
| Struct type declarations                              | `type`          | Carry `@atlas:aggregate` annotations when applicable.                        |
| Interface declarations                                | `type`          |                                                                              |
| `@atlas:*` annotation comments                        | (annotation)    | Bound to the next declared symbol on the same/following line.                |
| Call-graph edges from one function to another         | `call`          | Type-resolved when the package compiles, including interface dispatch and generics; name-matched otherwise. Each edge records which. |
| Wire / Fx DI bindings                                 | `dep_inject`    | Wire `wire.Build` sets + Fx `fx.Provide` calls.                              |
| SQLC method ↔ SQL file mappings                       | `sql_query`     | Joins generated `*.sql.go` methods to their `*.sql` files.                   |
| HTTP route declarations (Chi, Echo, stdlib, Huma)     | (extracted by `atlas contract list`) | Surfaces as `route` contracts, not raw symbols. |

The scanner skips by default:

- Directories named `vendor/`, `node_modules/`, or starting with `.`
  (`.git/`, `.atlas/`, etc.).
- Generated code — see [Generated code](#generated-code) below.

`_test.go` files are **included** by default — they're where
`@atlas:feature` lives most often. Pass
`codeindex/go.Options.SkipTests = true` via the library API if a caller
needs production-only indexing.

Package-private functions are **included** by default too. They carry no
annotations and rarely matter to a trace, but they do carry statements: a
helper atlas hasn't indexed has no source span, so `atlas cov sync
--framework go-cover` cannot charge its executed statements to anything.
Pass `codeindex/go.Options.SkipUnexportedFuncs = true` for a graph-only
audit where they are noise — accepting that coverage attribution then
under-reports.

## How calls are resolved

Everything atlas claims — impact of a change, a feature's implementation
surface, which test covers which handler — rests on one question: when
this function calls something, WHAT does it call? Until issue #87 the Go
scanner answered it by matching names. It rendered the call site as a
string (`"OrderService.Create"`), looked that string up in a table of
declarations, and, when that missed, fell through a ladder of guesses
ending in a case-insensitive substring match.

That answer is wrong in ways a name cannot detect. Two bounded contexts
that each declare a `Chat` share one short id, so a call binds to
whichever was walked first. A call through an interface-typed field names
no receiver type at all, so it resolved to nothing. A method on a generic
type is declared once and instantiated N times, so it fragmented.

Atlas now loads the tree with
[`golang.org/x/tools/go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages)
and resolves each call site through `go/types`, with class-hierarchy
analysis (`callgraph/cha`) for interface dispatch. The implementation is
[`packages/resolver/`](../../packages/resolver/); the scanner consumes it
in [`packages/codeindex/go/typed.go`](../../packages/codeindex/go/typed.go).

### Resolution tiers

Every edge records the mechanism that produced it (issue #146). The
vocabulary is closed:

| Tier            | What it means                                                                 |
| --------------- | ----------------------------------------------------------------------------- |
| `typed`         | The type checker resolved it. Exact across packages, through embedding, through generic instantiation. Interface dispatch is typed too, and marked ambiguous when class-hierarchy analysis named more than one implementation — counted over every candidate CHA found, including the ones atlas did not index, because an alternative it cannot see is still an alternative. |
| `name_resolved` | A name was bound to a declaration atlas indexed, using scope rules. No types were consulted. Produced only by the AST fallback. |
| `syntactic`     | The shape of the source suggested it and nothing was bound: a substring match, a DI-binding guess, an `@api` comment sitting above a declaration, a sqlc query node picked by bare method name. The target may not exist. |
| `imported`      | Someone else's indexer said so (SCIP; not yet produced).                       |

`atlas resolve` prints the histogram, and `--ast` re-runs the same scan
with type checking off so the two are side by side:

```
# Run from: this repository's root
$ atlas resolve --root . --ast
Go call resolution for .
  packages: 100 type-checked, 0 degraded (of 100)
  files:    543 of 613 indexed files resolved with types
  dispatch: class-hierarchy analysis ran, 1338 interface call sites resolved
  cost:     load 444ms, call graph 237ms, whole scan 7158ms

  4935 symbols, 12522 edges
    tier              --ast    typed    delta
    typed                 0    12361   +12361
    name_resolved      7854       92    -7762
    syntactic          3611       69    -3542
    imported              0        0       +0
    scan             5840ms   7158ms
```

Read that as: of the 11,465 call edges the name resolver produced over
this repository, 3,611 were guesses. 12,361 edges are now type-checked
and 69 guesses remain — the edges atlas cannot vouch for fell by 98%,
against a 9% growth in the edge count (11,465 → 12,522). The extra 1,057
are edges the name resolver could not see at all: interface dispatch,
methods called on the result of a call, calls through a generic field.

The 92 `name_resolved` and 69 `syntactic` edges that survive come from
the files no package covers (see the next section) plus the `@api`
comment edges, which are syntactic by construction. Where the type
checker ran, it is the only resolver that spoke: a typed file cannot
produce a name-matched call edge.

### It degrades, per package, and says so

`go/packages` needs a build that succeeds. Atlas runs mid-edit, so a red
build must not fail a scan: type checking is attempted per package, and
any package the type checker rejects has its files resolved by the AST
ladder instead, with the tier saying so. Nothing is silently upgraded.

`atlas resolve` names every degraded package with its first type error.
The fixture at `packages/codeindex/go/testdata/brokencorpus` is a module
with one package that compiles and one that does not, and it exists to
pin exactly this:

```
# Run from: this repository's root
$ atlas resolve --root packages/codeindex/go/testdata/brokencorpus
Go call resolution for packages/codeindex/go/testdata/brokencorpus
  packages: 1 type-checked, 1 degraded (of 2)
  files:    1 of 2 indexed files resolved with types
  dispatch: class-hierarchy analysis ran, 0 interface call sites resolved
  cost:     load 10ms, call graph 0ms, whole scan 11ms

  4 symbols, 2 edges
    typed               1
    name_resolved       1
    syntactic           0
    imported            0

  degraded packages (first type error each):
    example.com/broken/broken
      broken/broken.go:19:9: cannot use key (variable of type string) as error value in return statement: string does not implement error (missing method Error)
```

Both packages were scanned; both produced their call edge. One edge is
`typed` and one is `name_resolved`, and the tiers are the only thing
distinguishing them.

Partial degradation also reaches the scan warnings, which is what `atlas
scan` prints to stderr — so a reader who never opens the report still
learns why syntactic edges are in the histogram:

```
  warning: typed resolution degraded for 1 of 2 Go packages; calls in them are
  name-resolved or syntactic, not typed: example.com/broken/broken
  (broken/broken.go:19:9: cannot use key (variable of type string) as error value ...)
```

At most three packages are named there; `Result.Resolution` carries the
rest.

Three distinct outcomes, which the report keeps apart because the fix for
each is different:

| What you see                                    | What happened                                                                  |
| ----------------------------------------------- | ------------------------------------------------------------------------------ |
| `packages: N type-checked, 0 degraded`          | Everything resolved with types.                                                 |
| `packages: N type-checked, M degraded` + errors | The tree loaded; M packages do not compile. Fix the code.                       |
| `type checking unavailable: ...`                | `go list` could not enumerate anything — no module, no toolchain. Fix the environment. |

A file can also be indexed without being type-checked while every package
type-checks, which is why `files:` is reported separately from
`packages:`. On this repository 70 of 613 indexed files are in that
state. Walking every `.go` file under the repo and asking the loaded
program about each one accounts for 77 such files: 68 live under
`testdata/` (fixture trees that are separate modules or no module at all,
which `go list ./...` does not enumerate) and 9 are excluded by build
constraints — `cmd/debug_scan.go` (`//go:build ignore`),
`internal/adapters/filelock_windows.go` (`//go:build windows`), and seven
`*_test.go` files behind `//go:build integration` or `//go:build
dogfood`. The difference between 77 and 70 is the exclusion ledger:
several of those `testdata/` files are generated code the scanner does
not index either way.

### What it costs

The load mode is `NeedName | NeedFiles | NeedCompiledGoFiles |
NeedImports | NeedTypes | NeedSyntax | NeedTypesInfo`. `NeedDeps` is
deliberately absent: with it, every dependency's source is parsed and
type-checked; without it, dependencies come from compiled export data and
the packages you asked for are still fully type-checked. Nothing atlas
asks needs dependency syntax.

Measured on this repository — `packages.Load(./..., Tests: true)`, 145
packages, warm module and build cache, one developer laptop, three runs
of each mode:

| Load mode                            | `packages.Load`  |
| ------------------------------------ | ---------------- |
| without `NeedDeps` (what atlas uses) | 0.39 – 0.42 s    |
| with `NeedDeps`                      | 2.70 – 2.80 s    |

SSA construction plus class-hierarchy analysis costs a further 0.21 –
0.24 s on the same machine. End to end, repeated `atlas resolve --ast`
runs on this repository measured **5.6 – 5.9 s** for a whole scan without
type checking and **6.9 – 7.4 s** with it. The wall-clock
difference is smaller than the load duration alone would suggest, because
the typed path also skips a second parse of every file it adopts.

These are one machine's numbers under variable load. Run `atlas resolve
--root . --ast` to get yours; the tier columns are exact and
reproducible, the millisecond columns are not.

Issue #109 tracks parallelising the scan, which is where this cost is
best absorbed.

### Turning it off

```
atlas scan --skip-typed-resolution
atlas init --skip-typed-resolution
```

Both scan Go with the name resolver alone. Reach for the flag when the
LOAD itself is the problem — no toolchain, a build that needs credentials
to resolve modules, or a latency budget that cannot absorb the load. The
tiers then report `name_resolved` and `syntactic`, honestly, and nothing
claims to be typed.

It only turns type checking off, never back on, for the same reason
`--include-generated` is one-way: it is the escape hatch for a one-off
"go/packages will not run here", not a second place to configure the
default. Library callers set `codeindex/go.Options.SkipTypedResolution =
true` directly.

Two things change in the output besides the tiers, and both are visible
to a caller that queries the graph:

- **`ambiguous` means something different.** With types off there is no
  CHA, so an interface call site never enumerates its implementations;
  the flag then marks the name ladder's own guesses — a fuzzy match, a
  rendered `Type.Method` that bound to nothing — rather than a genuine
  choice between known candidates.
- **`external` stub nodes come back.** See below.

### `external` stubs exist only on the AST path

When the name ladder renders a callee it cannot find a declaration for
and the name starts with a known standard-library prefix
(`sync.Mutex.Lock`, rendered from `s.mu.Lock()`), the AST path synthesises
an `external` node for it and points a `syntactic` edge at it. The typed
path never does: it offers only callees it has already matched to an
indexed declaration, so a call into the standard library or into a
dependency produces no node and no edge.

That is the right answer — the stub was a guess about a symbol atlas had
never scanned, with no span and no owner — but it is a real difference in
what a caller can query. Turning typed resolution on removes those nodes
from `Result.Symbols`; turning it off brings them back. Every node for a
declaration atlas actually indexed is identical either way, which
`TestTypedResolution_DropsOnlyExternalStubsFromTheSymbolSet` pins.

### What is still not typed

- **`@api` comment → handler edges** and **route-table → handler edges**
  are `syntactic` and always will be: the first is "this comment sits
  within ten lines above that declaration", the second is a handler name
  supplied as a string. Neither is call resolution.
- **Calls through a function VALUE** (`f := s.Do; f()`, a
  `http.HandlerFunc` field) resolve to nothing. The type checker reports
  a variable, not a function, and CHA would answer them by matching
  signatures across the whole program — which for a common signature is a
  fan-out, not a resolution.
- **Calls into dependencies and into skipped generated files** produce no
  edge. The type checker names the callee exactly; atlas has no symbol,
  no span and no owner for it, so there is nothing to point an edge at.
- **`init()` and package-level variable initialisers** are not walked as
  callers, same as before.

## Generated code

Machine-written files are excluded from the index by default, and
therefore from the coverage denominator: they execute constantly, nobody
writes tests for them, and counting them flatters every number they touch.

A file is treated as generated when **any** of these hold:

| Rule              | What it matches                                                              |
| ----------------- | ---------------------------------------------------------------------------- |
| `generated-header`| A line matching `^// Code generated .* DO NOT EDIT\.$` **before** the package clause — [Go's own convention](https://go.dev/s/generatedcode). Emitted by sqlc, protoc-gen-go, mockgen, stringer, wire. |
| `generated-glob`  | A hit on `codeindex/go.Options.GeneratedGlobs`.                              |
| `generated-dir`   | Any path segment named `generated`.                                          |

The header rule is the one that travels: it holds wherever the tool put
its output. Reach for globs only when your generator omits the header —
some `protoc-gen-*` plugins and ORMs do. Pattern shapes:

```
*.pb.go            filename convention — matches at any depth
gen/               a whole subtree, rooted or nested
internal/db/*.go   anchored path glob — `*` does not cross a `/`
**/*.sql.go        leading `**/` is stripped, then matched as above
```

A malformed pattern is reported on the scan warnings and ignored, so one
typo can't silently widen or narrow the denominator.

Every exclusion is recorded — path plus reason, in walk order — on
`Result.SkippedFiles` (surfaced as `Index.SkippedFiles`), alongside files
dropped by `Options.IgnorePackages` (reason `ignored-package`). That's what
lets a report say *"12% of executed statements are in generated code,
excluded by policy"* instead of *"12% unattributable"*.

To measure the generated layer instead — a hand-written repository wrapper
living next to its sqlc output is arguably production code — set
`codeindex/go.Options.IncludeGenerated = true`. Detection still runs, but
nothing is skipped and `SkippedFiles` stays empty of generated entries.

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

### 1. Reflection-based dispatch is still invisible

Cross-package calls, interface dispatch and generic instantiation all
resolve exactly now — see
[How calls are resolved](#how-calls-are-resolved). What the type checker
cannot follow is dispatch that is not written in the types at all. If your
service calls `container.Resolve("AuthService").(*AuthService).Login(...)`,
the receiver is produced by a runtime string lookup, and atlas sees a type
assertion on an `any`. The edge will be absent from
`atlas trace auth.login`.

The same holds for a call through a function VALUE (`f := s.Do; f()`, a
`http.HandlerFunc` struct field): the type checker reports a variable, not
a function, and atlas emits nothing rather than guessing.

Workaround unchanged: annotate the explicit call site with
`@atlas:contract auth.login` so the audit picks it up even if the trace
chain doesn't reach it.

The one thing that has changed is how you find out. `atlas trace` showing
fewer hops than you expected used to be indistinguishable from a resolver
that guessed wrong; run `atlas resolve --root .` and, if the packages
involved type-checked, a missing edge is a genuine gap in what the types
say rather than a name that failed to match.

### 2. Generated code without a header needs a glob

Codegen that writes the standard `// Code generated ... DO NOT EDIT.`
header is excluded wherever it lands — no configuration, no directory
naming convention. Codegen that omits it is indexed like hand-written
code, and its statements land in the coverage denominator with no test
that could plausibly cover them.

If a scan reports symbols from `oapi-codegen` / `protoc` / ORM output,
check the first line of one of those files. No header means you need a
`codeindex/go.Options.GeneratedGlobs` entry — see
[Generated code](#generated-code). Every exclusion shows up on
`Result.SkippedFiles` with its reason, so you can verify the rule fired
rather than inferring it from a symbol count.

### 3. Duplicated type names across packages get package-qualified ids

Symbol ids are short by design (`Chat.MarkLoaded`, not the full import
path) because that is what annotations and `atlas trace` arguments use. In a
monorepo where several bounded contexts each declare a `Chat`, only one
declaration can own the short id: the first in lexical walk order. The others
are indexed under `<packageDir>.<Type>.<Method>`, e.g.

```
Chat.MarkLoaded                                    src/contexts/messaging/domain/aggregates/chat.go
src/contexts/ai-chat/domain/aggregates.Chat.MarkLoaded   src/contexts/ai-chat/domain/aggregates/chat.go
```

`atlas scan` prints a warning for every collision. If a feature annotation
resolves to the wrong context's symbol, that warning is why — reference the
package-qualified id explicitly, or rename the type.

**Call edges are no longer affected by this.** The collision is a
RENDERING problem — two declarations, one short name — and it used to be
a RESOLUTION problem as well, because a call to `Chat.MarkLoaded` was
bound by looking that string up. Type-checked resolution binds the call to
the declaration the compiler binds it to and then asks which id that
declaration was filed under, so a call inside the ai-chat context reaches
the ai-chat symbol even though messaging owns the short id. The golden
corpus pins exactly this: `config.Load` used to produce an edge into
`internal/persistence`'s `Config.Validate`, a package it does not import.

What is still affected is anything you type by hand: annotations, `atlas
trace <id>` arguments, `atlas codebase find`. Those still take the id, and
the id is still short.

### 4. Receivers vs free functions in qualified names

A method `func (h *AuthHandler) Login(...)` has the qualified name
`AuthHandler.Login` — the receiver type wins; the package path is
implicit. A free function `func Login(...)` in the same package gets the
qualified name `Login` (no receiver). This means a free function named
the same as a method shadows the method in suffix-match queries; always
disambiguate with `symbol:<pkg>.<name>` when both exist.

### 5. `init()` and `main()` are indexed but rarely useful in traces

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
- `atlas resolve` — the tier histogram and per-package type-check report
  described in [How calls are resolved](#how-calls-are-resolved). It has no
  page under `docs/commands/` yet; `atlas resolve --help` carries the flag
  reference.
