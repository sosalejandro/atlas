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
| `@atlas:*` annotation comments                              | (annotation)     | Parsed by the **Go-side** annotation parser, not `scanner.py` — same grammar.  |

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

### 3. Annotations on a class don't auto-propagate to methods

A class-level `@atlas:feature` annotation attaches to the class symbol
only, not to every method defined inside it. If you want each method to
participate in the audit, annotate each one explicitly:

```python
# @atlas:feature billing.subscribe
class BillingHandler:
    # @atlas:feature billing.subscribe
    def subscribe(self, user_id, plan):
        ...
```

This is intentional — atlas treats the class as the API surface and
treats methods as implementation. If your "API surface" is the method,
annotate the method, not the class.

### 4. The scanner is not annotation-aware (Go side handles that)

`scanner.py` extracts the AST shape — symbols, decorators, file
positions — but does **not** parse `@atlas:feature` annotations. That
parsing happens in the Go orchestrator
([`packages/codeindex/annotations/`](../../packages/codeindex/annotations/)),
which reads the raw source text and matches against the shared
annotation grammar.

The practical consequence: if you customise the annotation grammar
(e.g. by registering a new `@atlas:<kind>`), no Python-side change is
needed — the Go-side parser handles it for all three languages.

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
