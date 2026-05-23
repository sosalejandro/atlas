# Python language guide

The Python scanner is a `python3` subprocess that walks each `.py` file
through the stdlib `ast` module and returns the discovered symbols and
decorator-derived edges to the atlas Go orchestrator. Source lives in
[`packages/codeindex/py/`](../../packages/codeindex/py/) — the embedded
`scanner.py` is what `python3` actually runs.

The scanner shipped in atlas v0.3.0 (issue #46 / PR #47). It mirrors the
TS scanner's wire-format contract but uses pure-stdlib AST parsing rather
than the TypeScript Compiler API, so there's no pip dependency to manage.

## Prerequisites

- `python3` on `PATH`, version **3.8 or newer** (the scanner uses
  `from __future__ import annotations` and dataclass shapes that
  predate 3.7 → 3.8 features atlas relies on).
- **No pip dependencies** — `scanner.py` is strict-stdlib by design
  (`ast`, `json`, `sys`, `os`, `argparse`). You don't need a virtualenv,
  Poetry, or pip-installed packages to run atlas against a Python
  project.

The Python scanner is **optional**: if `python3` isn't on PATH, atlas
emits a single warning and continues indexing Go + TypeScript.

## What gets indexed

The scanner walks the entire project root (skipping the directories
listed below) and surfaces:

| What                                                        | Symbol kind      | Notes                                                                          |
| ----------------------------------------------------------- | ---------------- | ------------------------------------------------------------------------------ |
| Module-level functions (`def foo(): ...`)                   | `function`       | Qualified name = `<module-path>.<func>` (e.g. `py.billing.create_subscription`). |
| Class declarations (`class Foo: ...`)                       | `type`           | Carry `@atlas:aggregate` annotations when applicable.                          |
| Methods on a class                                          | `method`         | Qualified name = `<module-path>.<class>.<method>`.                              |
| `__init__` constructors                                     | `method`         | Indexed; not specially marked.                                                  |
| Decorator names applied to a function/class                 | `call`           | Surfaced as edges with the decorator as `to`.                                  |
| `@atlas:*` annotation comments                              | (annotation)     | Comment-form `# @atlas:...` parsed by the shared Go-side annotations parser.    |
| `@atlas.feature("id")` decorator annotations                | (annotation)     | Decorator-form parsed by `scanner.py`; class-level decorators propagate to methods. |

The scanner skips by default:

- `.git/`, `.venv/`, `venv/`, `env/` — virtualenv conventions.
- `__pycache__/`, `.tox/`, `.mypy_cache/`, `.pytest_cache/`,
  `.ruff_cache/` — tool caches.
- `node_modules/`, `dist/`, `build/`, `.eggs/` — JS-monorepo + Python
  packager outputs.

The skip list is in `DEFAULT_SKIP_DIRS` inside `scanner.py`; it's not
configurable today (open an issue if a project needs it).

## Sample project layout

A minimal mixed-language layout the scanner happily indexes:

```
my-svc/
├── pyproject.toml
├── billing/
│   ├── __init__.py
│   ├── handler.py         ← @atlas:feature billing.subscribe
│   └── service.py         ← @atlas:aggregate billing.subscription
└── tests/
    └── test_billing.py    ← @atlas:feature billing.subscribe + #real
```

After `atlas init` this materialises into roughly:

```
features:        1 (billing.subscribe)
aggregates:      1 (billing.subscription)
symbols:         6 (3 functions, 2 classes, 1 method)
annotations:     3 (the @atlas:feature / @atlas:aggregate sites)
```

Symbols are stored with the `py.` namespace prefix so they don't collide
with Go symbols of the same short name in cross-language queries.

## Tagging Python code for feature-level grouping (optional)

Atlas associates symbols with features through `@atlas:<kind> <id>`
annotations — the same grammar used in Go and TypeScript. For Python,
two recognition modes are supported and may be mixed freely within a
project.

### Mode 1 — comment-style (no runtime dependency)

A `# @atlas:<kind> <id>` comment on the line immediately above a `def`
or `class` declaration attaches that annotation to the symbol below.
Mirrors Go's `// @atlas:feature ...` convention.

```python
# @atlas:feature ingest-csv-imports
def parse_csv_file(path: str) -> list[dict]:
    ...

# @atlas:contract ingest-csv-imports.parse-row
def parse_row(line: str) -> dict:
    ...
```

Tags after the id (`#real`, `step=1`, `stream=meal_prep_events`) follow
the canonical grammar in [`docs/annotations.md`](../annotations.md).

### Mode 2 — decorator-style (idiomatic Python)

A `@atlas.feature("id")` decorator (or `@feature("id")` when imported
as `from atlas import feature`) is functionally equivalent to the
comment form. Atlas reads the decorator name and its first string
argument statically; at runtime the decorator is a no-op.

```python
from atlas import feature, aggregate

@feature("ship-orders")
def ship_one(order_id: str) -> str:
    ...

@aggregate("ship-orders.batch")
class BatchShipper:
    def enqueue(self, order_id: str) -> None: ...
    def flush(self) -> int: ...
```

To use the decorator form, drop the 3-line helper module shipped at
[`assets/python/atlas.py`](../../assets/python/atlas.py) into your
project (e.g. as `atlas.py` at a package root, or anywhere on
`PYTHONPATH`). The helper has no pip dependencies — it provides
identity-decorators for the id-shaped kinds (`feature`, `contract`,
`bc`, `aggregate`, `aggregate_service`, `saga`, `consumer`,
`event_emit`, `outbox_publish`).

Python identifiers cannot contain `-` so the kebab-case wire kinds
(`aggregate-service`, `event-emit`, `outbox-publish`) are reached via
their `aggregate_service` / `event_emit` / `outbox_publish` Python
aliases. The annotation parser canonicalises back to the wire form
when materialising the record.

Free-form kinds (`owner`, `deprecated`, `since`) are intentionally
NOT decorator-addressable — a string argument is the wrong shape for
"alice@team.com" or a free-text deprecation reason. Use the comment
form for those.

### Class-level annotations propagate to methods

A `@atlas:feature` annotation on a `class` propagates to every method
defined inside the class body — one `feature_symbols` link per
method, anchored at the method's own source line. So a single
annotation at the class declaration covers the entire class surface:

```python
@atlas.feature("ship-orders.batch")
class BatchShipper:
    def enqueue(self, order_id: str) -> None:
        ...  # inherits ship-orders.batch

    def flush(self) -> int:
        ...  # inherits ship-orders.batch
```

`atlas trace ship-orders.batch` then returns the call chains of the
methods, not just the class declaration line. This works with both
the comment form (`# @atlas:feature ...` above the `class`) and the
decorator form.

### Verifying the linkage

```
# Run from the project root after `atlas init`:
$ atlas trace ship-orders.batch
ship-orders.batch
└─ annotated_decorator.BatchShipper.enqueue       annotated_decorator.py:40
└─ annotated_decorator.BatchShipper.flush         annotated_decorator.py:44
```

If `atlas trace` returns nothing, the most likely cause is that the
annotation's id failed the strict id-grammar (`^[a-z0-9_-]+(\.[a-z0-9_-]+)*$`)
— see [`docs/annotations.md`](../annotations.md) §id grammar.

## Worked queries

### Find a class

```
# Run from: /tmp/atlas-fixture (with a py/billing.py fixture file)
$ atlas codebase find BillingService
py.billing.BillingService  py/billing.py:13  [type]
```

### Find a method

```
# Run from: /tmp/atlas-fixture
$ atlas codebase find BillingHandler.subscribe
py.billing.BillingHandler.subscribe  py/billing.py:8  [method]
```

The qualified name carries the full module path. Suffix matching means
`BillingHandler.subscribe` resolves the right hit without needing the
`py.billing.` prefix; for unambiguous scripting always use the fully
qualified `py.<module>.<class>.<method>` form.

### Triage a symptom against Python bodies

```
# Run from: /tmp/atlas-fixture
$ atlas diagnose "create_subscription" --min-confidence 0.1
  0.450  py.billing.BillingService.create_subscription   py/billing.py:14  [feature=-]
    matched whole symptom 1x in body; matched 1 symptom tokens
  0.225  py.billing.BillingHandler.subscribe             py/billing.py:8  [feature=-]
    matched whole symptom 1x in body; matched 1 symptom tokens
```

The `[feature=-]` tag means the symbol isn't linked to a feature — the
fixture's `BillingHandler` carries `@atlas:feature billing.subscribe`
only at the class level, and atlas doesn't propagate that linkage to
nested methods (see Gotcha #1 below).

## Common gotchas

### 1. Duck-typing breaks call-graph resolution

Python's dynamic dispatch is fundamentally incompatible with static
call-graph extraction. Consider:

```python
def process(handler):
    handler.handle(event)
```

`scanner.py` records `process` and (via decorator edges) any decorators
applied to it — but it cannot know which `handle` method `handler` refers
to, because `handler` could be any object at runtime. The edge is
silently absent from `atlas trace`.

**Practical impact**: Python `atlas trace` chains are shallow compared to
Go traces. A Python function that dispatches through a `dict` of
handlers, a class registry, or `getattr(obj, name)()` will look like a
dead-end node in the trace.

**Workaround**: when you know the dispatch table, annotate each
destination with `@atlas:contract <feature>` so the audit picks up the
linkage even though the trace doesn't reach it.

### 2. Decorators surface as `call` edges, not as their target

`scanner.py` records decorators as edges with the decorator's name as
`to` — but it does **not** follow into the decorator's body to find
what it wraps. This means:

```python
@flask_app.route("/login", methods=["POST"])
def login():
    ...
```

Atlas emits an edge `login -> flask_app.route` but doesn't know `login`
is now an HTTP handler. Route extraction for Python is **not** wired in
v0.3.0 (the Go-side `atlas contract list` Route extractor is Chi / Echo /
Huma / stdlib only).

**Practical impact**: there's no `atlas contract list --kind route`
support for Python today. If you need HTTP-route extraction across
Python services, open an issue at
<https://github.com/sosalejandro/atlas/issues>.

### 3. Annotations on a class auto-propagate to methods (RESOLVED in v0.4.0)

**Resolved in v0.4.0 (issue #53).** A class-level `@atlas:feature` (or
any id-shaped kind) now propagates to every method defined inside the
class. The Python AST walker emits one `feature_symbols` link per
method, anchored at the method's own source line.

So this single annotation covers every method:

```python
# @atlas:feature billing.subscribe
class BillingHandler:
    def subscribe(self, user_id, plan):
        ...  # inherits billing.subscribe

    def cancel(self, sub_id):
        ...  # inherits billing.subscribe
```

If you need a specific method to NOT inherit the class-level link, drop
the class annotation and annotate the methods you want individually.
Propagation only applies to direct method definitions inside the class
body — nested functions or closures defined inside a method body are
NOT considered part of the class API surface and do not inherit.

### 4. Cross-module callee resolution (RESOLVED in v0.5.0)

**Resolved in v0.5.0 (issue #61).** scanner.py emits best-effort
unqualified callee names (`echo`, `Base`, `style`) because Python's
dynamic dispatch makes full name resolution at AST time infeasible.
Prior to v0.5.0 those bare names became `external:py:1` stubs at
ingest time, so `atlas trace` chains terminated at the first
cross-module hop.

The Go-side resolver
([`packages/codeindex/py/resolver.go`](../../packages/codeindex/py/resolver.go))
now promotes bare callee names to qualified ids through a 5-tier
lookup (first match wins):

1. Exact qualified-name match against the symbol table.
2. Same-module basename or dotted-head match (e.g. `helper` from
   `sample.compute` → `sample.helper`).
3. Caller's own `from X import Y` — `Y` resolves to `X.Y`.
4. Re-export from the caller's package `__init__.py` — `from .module
   import Y` in `pkg/__init__.py` lets callers in `pkg.anything`
   reference bare `Y` and resolve to `pkg.module.Y`.
5. Sibling-module top-level: `Y` not bound by an import edge but
   declared at top-level in a sibling module of the caller's package.
   Most-imported wins on tie-breaks.

If all five tiers miss, the edge keeps its bare callee target and the
ingestor synthesises an `external:py` stub so the edge still lands in
the graph (preserving the depth-1 reachability set).

**Out of scope** (acknowledged limitations):

- Type inference for dynamic dispatch (`x.method()` where `x` is an
  `Any`-typed parameter). Requires pyright-grade type analysis.
- `super().method` — MRO walking is not implemented. The call stays
  as an `external:py` stub.

### 5. Annotation parsing is split across two layers

As of v0.4.0 (issue #53) the Python scanner is annotation-aware:
`scanner.py` extracts both comment-form (`# @atlas:...`) and
decorator-form (`@atlas.feature("...")`) hits, including class-level
propagation to methods.

The Go-side comment parser
([`packages/codeindex/annotations/`](../../packages/codeindex/annotations/))
also walks every `.py` file and surfaces comment-form hits
independently. Both paths land in the same `feature_symbols` table;
the store's idempotent upsert collapses any duplicates so dual
emission is harmless.

The practical consequence: registering a new id-shaped `@atlas:<kind>`
in `annotations.Kinds` automatically lights up the comment-form path
in every language. To support the new kind via the **decorator-form**
in Python, also add it to the `decoratable` map in
[`packages/codeindex/py/scanner.py`](../../packages/codeindex/py/scanner.py)
and to the helper module at
[`assets/python/atlas.py`](../../assets/python/atlas.py).

## Related

- Annotation grammar (works identically across Go / TS / Python):
  [`docs/annotations.md`](../annotations.md).
- Go scanner: [`docs/languages/go.md`](./go.md).
- TypeScript scanner: [`docs/languages/ts.md`](./ts.md).
- Per-command reference: [`docs/commands/`](../commands/).
- The Python scanner internals:
  [`packages/codeindex/py/scanner.py`](../../packages/codeindex/py/scanner.py)
  is the embedded AST walker;
  [`packages/codeindex/py/scanner.go`](../../packages/codeindex/py/scanner.go)
  is the Go orchestrator that drives it.
