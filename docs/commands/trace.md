# atlas trace

`atlas trace` walks atlas's call graph starting from the supplied id and
emits the chain as human-readable text (default) or JSON (`--json`). It
reads from the cached SQLite store by default — orders of magnitude faster
than the pre-#29 live walk, but stale if `atlas scan` hasn't picked up the
last code change. Pass `--fresh` to re-walk the codebase on disk when the
cached graph looks wrong.

## Usage

```
atlas trace <id> [flags]
```

`<id>` can be:

| Form                  | Example                                | Meaning                                                                              |
| --------------------- | -------------------------------------- | ------------------------------------------------------------------------------------ |
| feature-id            | `plans-patient.export-pdf`             | Resolves linked symbols, then walks each chain.                                       |
| SymbolID              | `auth.AuthHandler.Login`               | Direct symbol match.                                                                  |
| feature suffix        | `AuthHandler.Login`                    | Fuzzy suffix match against persisted symbols.                                         |
| `saga:<id>`           | `saga:checkout-flow`                   | Uses the store's saga step ordering view.                                             |
| `feature:<id>`        | `feature:plans-patient.export`         | Explicit feature lookup — bypasses fuzzy resolution.                                  |
| `symbol:<qn>`         | `symbol:auth.Login`                    | Explicit symbol lookup.                                                               |

For an unprefixed input, `atlas trace` first tries a feature lookup (the
strict-regex shape that wins most real-world inputs); a hit dispatches
through `traceByFeature`. On no-feature, it falls back to symbol resolution.
When the same id matches BOTH a feature and a symbol's qualified-name
suffix, `trace` errors and asks the caller to disambiguate with the
explicit prefix.

## Flags

| Flag                          | Default               | Description                                                                                                       |
| ----------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `--fresh`                     | off                   | Re-walk the codebase from disk instead of reading the cached store. Slow; use only when the cache looks wrong.    |
| `--max-depth`                 | `10`                  | Maximum trace depth. Useful for shallow "what does this call directly" views.                                     |
| `--root`                      | repo root / cwd       | Project root for the `--fresh` re-walk.                                                                           |
| `--node-modules-path`         | auto-detected         | Absolute path to a `node_modules/` directory the TS scanner can borrow `typescript` from. Repeatable.             |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                             |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                                   |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                                     |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                                    |

## Examples

### Trace by feature id

```
# Run from: /tmp/atlas-fixture
$ atlas trace auth.login
trace feature auth.login (3 nodes)
AuthHandler.Login  [func] go/auth.go:14
  AuthService.Authenticate  [func] go/auth.go:26
  AuthService.IssueToken  [func] go/auth.go:30
```

The header line names what was resolved (`feature auth.login`) and the
total node count. The tree shows the call chain depth-first; each line
carries the symbol kind in `[brackets]` and a repo-relative `file:line`
ref.

### Trace by symbol id

```
# Run from: /tmp/atlas-fixture
$ atlas trace AuthHandler.Login
trace AuthHandler.Login (confidence 0.00, 3 nodes)
AuthHandler.Login  [func] go/auth.go:14
  AuthService.Authenticate  [func] go/auth.go:26
  AuthService.IssueToken  [func] go/auth.go:30
```

When the input is interpreted as a symbol, the header line carries a
`confidence` score reflecting how unambiguous the resolution was —
`1.00` for an exact qualified-name hit, lower for fuzzy suffix matches.

### Limit depth

```
# Run from: /tmp/atlas-fixture
$ atlas trace auth.login --max-depth 1
trace feature auth.login (3 nodes)
AuthHandler.Login  [func] go/auth.go:14
  AuthService.Authenticate  [func] go/auth.go:26
  AuthService.IssueToken  [func] go/auth.go:30
```

In this fixture the call graph is only one level deep, so `--max-depth 1`
matches the unlimited output. On real codebases (handler → service →
repository → query) `--max-depth 2` is a useful "what does this thin
wrapper do directly" lens.

### Disambiguation error

When an input could mean either a feature id OR a symbol suffix, `trace`
refuses to guess:

```
# Hypothetical: codebase has both feature `auth.login` and symbol `pkg.auth.login`
$ atlas trace auth.login
error: ambiguous id "auth.login" — matches feature "auth.login"
       AND symbol suffix "pkg.auth.login". Re-run with feature:<id> or symbol:<qn>.
```

Always prefer the explicit prefix in scripts.

## How it works

1. Parse the input. Recognised prefixes (`feature:`, `symbol:`, `saga:`)
   short-circuit dispatch.
2. For an unprefixed input:
   - Try the `features` view first (strict regex match).
   - On miss, query `symbols` for exact qualified-name match, then for
     suffix matches.
   - If both lookups succeed, error with the disambiguation hint above.
3. Walk the store's `edges` table depth-first up to `--max-depth`, dropping
   nodes already visited (cycle break).
4. Render either as the indented text tree (default) or as the JSON
   envelope (`--json`).

The cached walk costs single-digit milliseconds even on large codebases
because the adjacency list lives entirely in SQLite. `--fresh` falls back
to the pre-cache codepath, which re-parses every file under `--root` — use
only when you suspect the cache is wrong AND `atlas scan` hasn't caught
the drift.
