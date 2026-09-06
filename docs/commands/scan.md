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
| `--include-generated`         | off                   | Index machine-written files instead of excluding them. See "Generated code" below.                                   |
| `--skipped`                   | off                   | Do not scan. Print the exclusion ledger the last scan wrote. See "Why is this file not indexed?" below.              |
| `--skipped-path`              | none                  | Print the ledger entry for one file. Implies `--skipped`.                                                            |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                                |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                                      |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                                        |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                                       |


## Generated code

Machine-written files are EXCLUDED from the index by default. Generated
statements execute constantly -- a protobuf accessor runs under every test
that touches the message -- so indexing them lets them dominate any coverage
or complexity reading taken over hand-written code.

Two rules apply without any configuration:

1. Go's conventional `// Code generated ... DO NOT EDIT.` header, checked
   before anything else because it is the signal the file itself declares.
2. A `generated` path segment, which catches legacy trees that carry no
   header.

Which OTHER files a codebase generates is a property of the codebase, not of
the invocation, so extra patterns belong in `atlas.yaml`:

```yaml
scan:
  generated:
    - "**/*.pb.go"
    - "**/*_gen.go"
    - "mocks/**"
```

`--include-generated` turns exclusion off for one run -- the escape hatch for
"why did my symbol disappear?". `scan.include_generated: true` makes that the
default for the project. The flag can only turn exclusion off; it is not a
second place to configure the default, so a config that asks to index
generated code cannot be switched back from the command line.

`atlas scan --json` reports what was skipped and under which rule, so an
over-broad glob is visible rather than silent.

## Why is this file not indexed?

Exclusion is silent by design, and that is its danger. A symbol that was
never indexed does not show up as uncovered, unlinked or missing -- it shows
up as nothing at all. An over-broad glob (`**/*_gen.go` catching a
hand-written `token_gen.go`) quietly removes real code from every coverage
and audit number, and the only trace is the scan output that scrolled away.

So every scan persists what it excluded, and `--skipped` reads it back --
from the store, without re-walking the tree:

```
$ atlas scan --skipped
Excluded from the index by the last scan (db: /repo/.atlas/atlas.db): 4 file(s)
  api/schema.pb.go          generated-glob    **/*.pb.go
  db/queries.sql.go         generated-header
  generated/legacy.go       generated-dir     generated
  generated/with_header.go  generated-header
```

The third column is the rule's parameter, and it is the actionable half:
`**/*.pb.go` is the line of `.atlas.yaml` to narrow. The rule names the
specific check that claimed the file, never a category -- "generated" would
say what happened without saying what to change.

A file matched by more than one rule is listed under the ONE that claimed
it. `db/queries.sql.go` above matches `**/*.sql.go` too, but the header rule
runs first (strongest signal first: a `// Code generated ... DO NOT EDIT.`
line travels with the file, a glob only describes where it landed), so the
header is what the ledger records.

For a single file:

```
$ atlas scan --skipped-path api/schema.pb.go
Excluded from the index by the last scan (db: /repo/.atlas/atlas.db): 1 file(s)
  api/schema.pb.go  generated-glob  **/*.pb.go

$ atlas scan --skipped-path internal/auth/login.go
internal/auth/login.go is not on the exclusion ledger: the last scan either
indexed it or never walked it (db: /repo/.atlas/atlas.db)
```

Not being on the ledger is an answer, not an error -- the exit code stays 0.

The ledger is REPLACED by every scan, never appended to: a file that stops
matching a rule leaves it. It therefore describes the current index and not
the history of every rule ever tried, and it is written in the same
transaction as the symbols, so it can never describe a scan that did not
finish. `atlas scan` prints `files_excluded=N` when a scan excluded
anything; `--skipped` is the detail behind that number.

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
4. Replace `skipped_files` with the files this walk declined to index and
   the rule that claimed each one (docs/schema-v1.md §5.15).

This means `scan` is safe to run from a git pre-commit hook on monorepos:
warm scans finish in single-digit milliseconds because the AST walker only
fires on changed files.
