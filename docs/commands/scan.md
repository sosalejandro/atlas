# atlas scan

`atlas scan` re-walks the project root, re-indexes every source file, and
writes the resulting symbols / edges / annotations / pattern matches to the
SQLite state DB. Files whose SHA-256 matches the cached hash are skipped to
avoid pointless re-writes — the cache makes warm scans on a multi-thousand-
file repo cheap enough to put in a pre-commit hook.

Run `atlas init` first to create the state DB; `scan` errors out if the DB
doesn't exist.

## Usage

```
atlas scan [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                                          |
| ----------------------------- | --------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `--root`                      | repo root / cwd       | Project root to scan.                                                                                                |
| `--hash-files`                | `true`                | Compute SHA-256 of every scanned file. Pin to `false` only if hashing dominates wall time on a giant repo.           |
| `--node-modules-path`         | auto-detected         | Absolute path to a `node_modules/` directory the TS scanner can borrow `typescript` from. Repeatable.                |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                                |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                                      |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                                        |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                                       |

## Examples

### Warm re-scan (incremental)

```
# Run from: /tmp/atlas-fixture, immediately after `atlas init`
$ atlas scan
Atlas scan complete (root: /tmp/atlas-fixture, db: /tmp/atlas-fixture/.atlas/atlas.db)
  symbols=0 edges=0 annotations=0 file_hashes=3 pattern_matches=0
  files_scanned=3 files_skipped=3 duration=0ms
```

`files_skipped=3` is the cache doing its job — every Go/Python source had
the same SHA-256 as the previous scan, so the indexer short-circuited. The
counts on the first line (`symbols=0`, `edges=0`, ...) are *deltas*: how
many rows were re-written this scan. The first scan after `init` typically
reports zero because `init` already populated the slices.

### After editing one file

When you edit a single source file, scan re-indexes only that file:

```
# After editing /tmp/atlas-fixture/go/auth.go to add a method
# Run from: /tmp/atlas-fixture
$ atlas scan
Atlas scan complete (root: /tmp/atlas-fixture, db: /tmp/atlas-fixture/.atlas/atlas.db)
  symbols=10 edges=3 annotations=7 file_hashes=4 pattern_matches=0
  files_scanned=4 files_skipped=3 duration=1ms
```

Three files were cache-hits; one was re-indexed. The symbol count climbed
by 1 (the new method).

### Forcing a full re-walk

There is no `--force` flag — re-indexing is hash-driven on purpose. To
force a full re-walk, either:

1. Delete the file-hash rows (`sqlite3 .atlas/atlas.db 'DELETE FROM file_hashes'`)
   and re-run `atlas scan`, or
2. Run `atlas scan --hash-files=false`, which disables the cache check
   altogether.

Use sparingly. The intended escape hatch for "the cached graph looks wrong"
is `atlas trace --fresh`, which re-walks live without touching the store.

## How it works

`scan` is the same code path as `atlas init` minus the schema-migration step:

1. Open the existing DB at `--db-path` (errors if missing).
2. For each candidate source file under `--root`:
   - Compute SHA-256.
   - If the hash matches the row in `file_hashes`, skip the file.
   - Otherwise, re-parse it, diff the resulting symbols/edges/annotations
     against the cached set, and write the delta.
3. Re-materialise the `features` and `feature_symbols` join tables.

This means `scan` is safe to run from a git pre-commit hook on monorepos:
warm scans finish in single-digit milliseconds because the AST walker only
fires on changed files.
