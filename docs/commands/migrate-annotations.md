# atlas migrate-annotations

`atlas migrate-annotations` walks every source file under `--root` and
rewrites each `// @testreg <id> [#tag ...]` comment in place to the
canonical `// @atlas:feature <id> [tag ...]` form, preserving trailing tags
verbatim (the leading `#` is dropped per the new grammar's tag convention).

It's the one-shot bulk-rewrite verb used during the testreg → atlas
cutover. After running it, every annotation is the canonical
`@atlas:feature` form and the legacy reader path goes idle.

The verb refuses to touch:

- Files inside `vendor/` or `node_modules/`.
- Files containing the magic suppressor comment
  `// nolint:atlas-migrate` — useful for fixtures that intentionally
  keep the legacy spelling for parser tests.

You **must** pass exactly one of `--dry-run` or `--apply`. The default is
a no-op so a CI script that forgot to pick a mode fails loud.

## Usage

```
atlas migrate-annotations [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                  |
| ----------------------------- | --------------------- | -------------------------------------------------------------------------------------------- |
| `--dry-run`                   | (required if no `--apply`) | Report rewrite candidates without modifying any file.                                  |
| `--apply`                     | (required if no `--dry-run`) | Rewrite candidates in place.                                                          |
| `--root`                      | repo root / cwd       | Project root to walk.                                                                        |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                        |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path. (Unused by `migrate-annotations` but accepted globally.)     |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                               |

## Examples

### Dry-run first

```
# Run from: /tmp/atlas-fixture (with a file containing `// @testreg auth.legacy #real`)
$ atlas migrate-annotations --dry-run
migrate-annotations: dry-run mode
  files_scanned=5  files_touched=0  candidates=1
  legacy.go:3
    - // @testreg auth.legacy #real
    + // @atlas:feature auth.legacy real
```

Header summary line shows files walked, files that *would* be touched
(zero — dry-run), and the candidate rewrite count. Each candidate prints
a unified diff hunk so the operator can eyeball the change before
applying.

Note that the legacy `#real` tag becomes the bare token `real` — the
leading `#` was a testreg convention; the new grammar uses bare tokens
after the ids.

### Apply

```
# Run from: /tmp/atlas-fixture
$ atlas migrate-annotations --apply
migrate-annotations: apply mode
  files_scanned=5  files_touched=1  candidates=1
  legacy.go:3
    - // @testreg auth.legacy #real
    + // @atlas:feature auth.legacy real
```

Same diff, `files_touched=1`. The rewrite is atomic — atlas writes to a
sibling temp file and renames over the original, so a crash mid-write
can't corrupt the source.

### Idempotency

Running the same `--apply` twice is a no-op the second time:

```
# Run from: /tmp/atlas-fixture (immediately after the apply above)
$ atlas migrate-annotations --dry-run
migrate-annotations: dry-run mode
  files_scanned=5  files_touched=0  candidates=0
```

`candidates=0` means no `@testreg ` comment was found — the regex matches
only `@testreg ` (with the trailing space), never `@atlas:`, so
subsequent runs find nothing.

### Suppressing per-file rewrites

To exclude a single file from the rewrite — typically a parser test that
needs the legacy comment verbatim — add the suppressor:

```go
// nolint:atlas-migrate
package parser_test

// @testreg legacy_parser_test  // <-- preserved through migrate-annotations
func TestLegacyAnnotation(t *testing.T) { /* ... */ }
```

The suppressor must appear on its own line; atlas matches the comment
text exactly.

## How it works

1. Walk `--root` collecting every source file the language scanners
   recognise (`.go`, `.ts`, `.tsx`, `.js`, `.jsx`, `.py`, `.md`).
2. Skip files inside `vendor/`, `node_modules/`, or carrying the
   suppressor comment.
3. For each remaining file:
   - Find every `// @testreg ` comment via regex.
   - Construct the rewritten form: drop the leading `#` from each tag,
     prepend `@atlas:feature`, preserve the rest verbatim.
   - In `--dry-run` mode, print the diff hunk. In `--apply` mode, write
     a temp file with the rewrites applied, then atomically rename over
     the original.
4. Emit the summary footer with `files_scanned` / `files_touched` /
   `candidates`.

The rewrite is per-comment, not per-file: a single file with three
`@testreg` comments produces three diff hunks and writes three rewrites.

For the full annotation grammar — what the rewrite is targeting and what
the new form supports — see [`docs/annotations.md`](../annotations.md).
For the broader cutover playbook, see
[`docs/migration-from-testreg.md`](../migration-from-testreg.md).
