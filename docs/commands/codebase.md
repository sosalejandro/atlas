# grunnr codebase

`grunnr codebase` groups the read-only structural-lookup verbs against the
indexed codebase. Every subcommand reads from the SQLite state DB; nothing
re-walks the source on disk. Run `grunnr init` or `grunnr scan` first to
populate the store.

The subcommands are the daily-driver "where is X?" / "what's in X?" /
"who fires X?" queries:

| Subcommand                          | Answers                                                       |
| ----------------------------------- | ------------------------------------------------------------- |
| [`find <symbol>`](#find)            | Where is this qualified symbol declared?                      |
| [`pattern <name>`](#pattern)        | Which symbols match this Phase 6f pattern recogniser?         |
| [`emit <event-name>`](#emit)        | Which sites fire (or outbox-publish) this event?              |
| [`agg <id>`](#agg)                  | What's the canonical declaration + service for this aggregate? |
| [`bc <bc-name>`](#bc)               | What's inside this bounded context?                           |
| [`consumer [<stream>]`](#consumer)  | Which subscribers consume this Redis stream?                  |
| [`cycles`](#cycles)                 | Which files form circular imports (SCC over the import graph)? |

## Subcommand reference

### `find`

```
grunnr codebase find <symbol> [flags]
```

Resolves a fully-qualified symbol name (e.g. `auth.AuthHandler.Login`) to
its position in the codebase via the persisted `symbols` table. If no
exact match exists, `find` performs a case-sensitive suffix match
(`Login` resolves `auth.AuthHandler.Login`) and returns the first hit.

```
# Run from: /tmp/grunnr-fixture
$ grunnr codebase find Login
AuthHandler.Login  go/auth.go:14  [func]
```

```
# Run from: /tmp/grunnr-fixture
$ grunnr codebase find AuthService
AuthService.Authenticate  go/auth.go:26  [func]
```

The second example shows the suffix-match fallback: there is no symbol
literally named `AuthService` (it's the receiver type), so grunnr returns
the first method that shares the suffix.

```
# Run from: /tmp/grunnr-fixture
$ grunnr codebase find py.billing.BillingService
py.billing.BillingService  py/billing.py:13  [type]
```

Python symbols are prefixed with the module path (`py.billing.*`) so
qualified-name lookups work across Go, TS, and Python in one namespace.

### `pattern`

```
grunnr codebase pattern <name> [flags]
```

Lists every symbol whose `pattern_matches` column carries a hit for the
named Phase 6f pattern recogniser. Common recogniser names:

- `canonical-service` — function tagged as the canonical service for an
  aggregate.
- `event-recorder-embed` — struct embedding the
  `events.RecorderImpl` mixin.
- `outbox-append` — call site for `outbox.Append(...)`.

```
# Run from: /tmp/grunnr-fixture (no patterns registered for this fixture)
$ grunnr codebase pattern canonical-service
pattern canonical-service: 0 symbols
```

On a real codebase with the EDA patterns wired:

```
# Run from: a nutrition-v2-go-shaped repo
$ grunnr codebase pattern outbox-append
pattern outbox-append: 12 symbols
  src/contexts/identity/internal/application/services/auth_service.go:284  outbox-append
  src/contexts/messaging/internal/application/services/conversation_service.go:91  outbox-append
  ...
```

### `emit`

```
grunnr codebase emit <event-name> [flags]
```

Groups every `@grunnr:event-emit` and `@grunnr:outbox-publish` annotation
for the given event name. Useful for "where does this event fire from"
and "is it published to the bus, or staged in the outbox":

```
# Run from: a nutrition-v2-go-shaped repo
$ grunnr codebase emit conversation.message_sent
event conversation.message_sent (3 sites)
  src/contexts/messaging/.../conversation_service.go:91  [event-emit]
  src/contexts/messaging/.../outbox_publisher.go:42       [outbox-publish]
  src/contexts/messaging/.../outbox_publisher.go:58       [outbox-publish]
```

### `agg`

```
grunnr codebase agg <id> [flags]
```

Returns the `@grunnr:aggregate` declaration for an aggregate id plus its
linked canonical-service site (when one exists).

```
# Run from: /tmp/grunnr-fixture
$ grunnr codebase agg identity.auth
aggregate identity.auth
  decl: go/auth.go:23  identity.auth
  service: (none)
```

`service: (none)` means no `@grunnr:aggregate-service` annotation is
linked. That's not an error — many aggregates carry only the declaration.

### `bc`

```
grunnr codebase bc <bc-name> [flags]
```

Returns every annotation row inside files that declare `@grunnr:bc <name>`.
Useful for "what's in this BC" inventories.

```
# Run from: /tmp/grunnr-fixture (one bc declaration in go/bc.go)
$ grunnr codebase bc identity
bc identity: 1 annotations
  go/bc.go:1  [bc] identity
```

On a real codebase the row count climbs into the hundreds — every
`@grunnr:feature`, `@grunnr:contract`, `@grunnr:aggregate` inside the BC's
files surfaces here.

### `consumer`

```
grunnr codebase consumer [<stream>] [flags]
```

Lists `@grunnr:consumer` subscriptions, optionally filtered by stream
name. With no argument, every consumer in the store is listed.

```
# Run from: /tmp/grunnr-fixture (no consumers)
$ grunnr codebase consumer
consumers: 0
```

```
# Run from: a nutrition-v2-go-shaped repo
$ grunnr codebase consumer batch_session_events
consumers (stream=batch_session_events): 2 sites
  src/contexts/meal_prep/.../consumer.go:18  [consumer] stream=batch_session_events
  src/contexts/meal_prep/.../audit_consumer.go:24  [consumer] stream=batch_session_events
```

### `cycles`

```
grunnr codebase cycles [--scope <prefix>] [--scope-filter module|function|conditional|type_checking|try_guard|all]
```

Detects circular imports by running Tarjan's strongly-connected-components
algorithm over the `kind='import'` edge subgraph (joined to `symbols` on
both endpoints for the file path). Single-node SCCs are filtered — only
cycles with two or more distinct files are reported. Cycles are grouped
by length, smallest first (2-node cycles are the highest-fix-value
target, so they lead the output).

Default `--scope-filter` is `module`. Module-level imports are the only
ones that cause a real load-time `ImportError`; the other scope tags
(`function`, `conditional`, `type_checking`, `try_guard`) are deferred
or intentional and stay hidden unless you opt in. Pass
`--scope-filter all` to surface every cycle regardless of scope — the
output flags non-module edges inline so you can tell deferred-import
workarounds from real cycles at a glance.

```
$ grunnr codebase cycles
grunnr codebase cycles
  2-node cycles: 1
    a.py
    <-> b.py
```

```
$ grunnr codebase cycles --scope-filter all
grunnr codebase cycles
  2-node cycles: 1
    a.py
    <-> b.py
    (function-import edge at b.py:42 — flagged as function scope)
```

`--scope <prefix>` narrows the analysis to symbols whose qualified
name starts with the prefix — useful for cycle hunting inside one BC
of a monorepo (e.g. `--scope services.preprocessor`).

## How it works

All `codebase` verbs are pure SQL lookups against the persisted store:

- `find` indexes by `qualified_name` with a fallback suffix-match query.
- `pattern` joins `symbols` against `pattern_matches`.
- `emit` joins `annotations` filtered to `kind IN ('event-emit', 'outbox-publish')`
  and groups by `event_name`.
- `agg` joins `annotations` (kind=`aggregate`) with `annotations` (kind=
  `aggregate-service`) on the aggregate id.
- `bc` finds files containing an `@grunnr:bc <name>` row and returns every
  annotation in those files.
- `consumer` filters `annotations` by `kind='consumer'` and (optionally)
  `stream_name`.
- `cycles` projects `edges` JOINed against `symbols` for both endpoints
  to a file-to-file shape, then runs Tarjan's O(V+E) SCC algorithm in
  Go (`packages/graph.FindCycles`). The scope filter is applied at the
  SQL layer (`WHERE edge_meta IN (...)`) so the Go layer never sees
  filtered-out edges.

There is no live re-walk here — if a query returns "not found" but you
know the symbol exists, run `grunnr scan` first to refresh the store.
