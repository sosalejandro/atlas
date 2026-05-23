# atlas init

`atlas init` is the one-time bootstrap step. It performs a fresh scan of the
project at `--root` (default: git toplevel, falling back to cwd), opens the
SQLite state DB at `.atlas/atlas.db` — creating it if it doesn't exist —
applies all pending migrations, and ingests the resulting `codeindex.Index`.

After `init`, every other read-only verb (`audit`, `trace`, `codebase find`,
`diagnose`, `sprint`) reads from the cached store. Re-scan with
[`atlas scan`](./scan.md) when source files change; the cache is file-hash
keyed and incremental.

Feature membership is materialised directly from `@atlas:feature`,
`@atlas:contract`, and legacy `@testreg` annotations during ingest. There is
no separate "import YAML" step — the code is the registry.

## Usage

```
atlas init [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                                          |
| ----------------------------- | --------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `--root`                      | repo root / cwd       | Project root to scan.                                                                                                |
| `--hash-files`                | `true`                | Compute SHA-256 of every scanned file. Pin to `false` only if hashing dominates wall time on a giant repo.           |
| `--node-modules-path`         | auto-detected         | Absolute path to a `node_modules/` directory the TS scanner can borrow `typescript` from. Repeatable.                |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path. Without it, atlas searches upward from `--root` for `.atlas.yaml`.                             |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path. Useful for parallel CI shards (e.g. `.atlas/ci-shard-3.db`).                         |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope (`{schema_version, command, args, result, generated_at}`) instead of human-friendly text. |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output. No effect with `--json`.                                                              |

## Examples

### First run on a fresh project

```
# Run from: /tmp/atlas-fixture (a mixed Go + TS + Python project)
$ atlas init
Atlas initialised /tmp/atlas-fixture/.atlas/atlas.db (root: /tmp/atlas-fixture)
  symbols=9 edges=2 annotations=7 file_hashes=4 pattern_matches=0
  features=3 feature_symbols=3 orphan_annotations=1
  files_scanned=4 files_skipped=0 duration=1ms
  warning: no router signal detected (react-router, tanstack, or expo)
```

The summary line covers the four index slices written to the store: symbols
(functions, methods, classes), edges (call-graph + DI bindings), annotations
(`@atlas:*` markers), and the file-hash table that powers incremental
re-scans. `features=3` is the count of distinct feature IDs harvested from
`@atlas:feature` annotations. `orphan_annotations=1` flags annotations
the parser couldn't attach to a symbol (often a stray `@atlas:bc` at file
top-level, which is the intended shape).

A `no router signal detected` warning is normal on backend-only or
fixture-style projects — the TS scanner only emits route/component/hook
symbols when it sees a router boot-call (`createBrowserRouter`,
`@tanstack/react-router`, or an `app/` directory for Expo). See
[`docs/languages/ts.md`](../languages/ts.md) for the full discovery rules.

### Pointing at an external node_modules

When atlas runs against a polyglot repo whose TS scanner needs the
`typescript` package but the scanned project has no `node_modules/`,
forward an external path:

```
# Run from: any repo without local node_modules
$ atlas init --node-modules-path /home/me/some-project/node_modules
```

The flag is repeatable; the first directory containing a resolvable
`typescript` module wins.

### JSON envelope

```
# Run from: /tmp/atlas-fixture
$ atlas init --json
{
  "schema_version": "v1",
  "command": "init",
  "args": {"root": "/tmp/atlas-fixture", "hash_files": true},
  "result": {
    "db_path": "/tmp/atlas-fixture/.atlas/atlas.db",
    "stats": {
      "symbols": 9, "edges": 2, "annotations": 7, "file_hashes": 4,
      "files_scanned": 4, "files_skipped": 0, "duration_ms": 1
    }
  },
  "generated_at": "2026-05-22T03:46:00Z"
}
```

Use `--json` for scripting and CI — the envelope shape is stable across
patch releases per the schema-version contract.

## How it works

1. Resolve `--root` (git toplevel → cwd fallback).
2. Open/create `.atlas/atlas.db`, apply pending schema migrations
   (see [`docs/schema-v1.md`](../schema-v1.md)).
3. Run the multi-language code-index walker:
   - **Go**: AST-walks every `.go` under root (skipping `vendor/`,
     `node_modules/`).
   - **TypeScript**: shells out to `node` with the embedded
     `scanner.ts` if `node` is on PATH.
   - **Python**: shells out to `python3` with the embedded
     `scanner.py` if `python3` is on PATH.
4. Persist symbols, edges, annotations, pattern matches, and file hashes
   into the store.
5. Materialise the `features` and `feature_symbols` tables from harvested
   annotations.

Re-running `atlas init` on an existing DB is safe — the schema migrations
are idempotent and the file-hash cache means unchanged files are skipped.
For routine re-scans, prefer [`atlas scan`](./scan.md), which is the same
walk minus the schema-migration step.
