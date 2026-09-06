# atlas doctor

`atlas doctor` answers the one question no other atlas command can: **is
atlas's picture of this repo still true?**

Every number atlas prints — a coverage percentage, an audit score, a
sprint ranking — is downstream of a scan and an ingest, and both go stale
silently. A stale index does not error. It answers confidently about a
repo that no longer exists, and nothing in the output distinguishes that
from a correct answer. `doctor` is the cheap command whose whole job is
to tell you your inputs are wrong (issue #88), in the same family as
`go vet`, `terraform validate` and sonar-scanner's analysis warnings.

Three rules shape the output:

1. **Every check is reported, passing ones included.** A doctor that
   prints nothing when healthy teaches you nothing about what it looked
   at, and leaves you unable to tell "checked and fine" from "never
   looked".
2. **A check that cannot run says `n/a` with the reason — never `ok`.**
   No coverage ingested yet is a real and normal state; reporting healthy
   coverage hygiene for a store with no coverage in it would be exactly
   the confident-but-empty answer `doctor` exists to catch. The same rule
   applies to *half* of a check: a file that could not be read, or a
   sweep whose input never opened, is reported as unexamined rather than
   folded into a clean count.
3. **The command never changes what it is inspecting.** `store.Open`
   applies the embedded migrations to whatever it is handed, so doctor
   verifies from a read-only handle that the file at the state path is
   already an atlas store before opening it read-write. A 0-byte
   placeholder, a database an interrupted `atlas init` left half-made, or
   an unrelated SQLite file is reported, never migrated: a diagnostic
   must not conjure the state it was asked to inspect.

## Usage

```
atlas doctor [flags]
```

## Flags

| Flag                         | Default              | Description                                                                       |
| ---------------------------- | -------------------- | --------------------------------------------------------------------------------- |
| `--fail-on`                  | `fail`               | Exit non-zero when any check reaches this severity. `warn` or `fail`.             |
| `--root`                     | repo root or cwd     | Working tree the index is compared against.                                       |
| `--config` *(global)*        | `.atlas.yaml` lookup | Explicit config path.                                                             |
| `--db-path` *(global)*       | `.atlas/atlas.db`    | Override the SQLite state path.                                                   |
| `--json` *(global)*          | off                  | Emit the stable JSON envelope instead of human-friendly text.                     |
| `-v`, `--verbose` *(global)* | off                  | Print the numbers behind each finding (the `details` map).                        |

## Exit code

`0` unless some check reached the `--fail-on` severity, so the command
drops into CI unchanged:

```yaml
- run: atlas doctor            # fails the build on any fail-severity check
- run: atlas doctor --fail-on warn   # a tighter gate, same checks
```

`--fail-on` accepts only `warn` and `fail`. `ok` and `n/a` are rejected:
a gate at either would fail every healthy repo, which is the fastest way
to get the gate switched off.

The report is always printed **before** the non-zero exit. A CI job that
sees only the exit code learns nothing; one that sees the report can act.

## The checks

| Name                   | Question it answers                                                                    | Fails when |
| ---------------------- | -------------------------------------------------------------------------------------- | ---------- |
| `index.freshness`      | Do the `file_hashes` rows still describe the files on disk?                             | any indexed file changed or disappeared |
| `coverage.freshness`   | How old is the coverage frontier, and did the index move under it?                      | never (warn only) |
| `coverage.attribution` | What share of executed statements could not be charged to a symbol?                     | that share ≥ 33% |
| `feature.linkage`      | Are there features with no symbols, or *anchored* annotations naming a feature that does not exist? | never (warn only) |
| `store.schema`         | Applied migration version vs. this binary's, plus golang-migrate's `dirty` flag         | dirty, mismatched, or unreadable |

### `index.freshness`

The load-bearing one. Re-hashes every file in `file_hashes` and compares
against disk, reporting four counts:

- **changed** — the file is still there with different bytes. Everything
  atlas says about it is about content it has not read.
- **missing** — the file is gone. Its symbols are still in the store,
  still scored, still ranked.
- **unreadable** — the file is still there and its bytes could not be
  read (a permission bit, an I/O error, a path that is no longer a
  regular file). Nothing was established about it in either direction.
- **unindexed** — a **Go** file on disk with no `file_hashes` row *and*
  no indexed symbols.

`changed` and `missing` invalidate answers atlas has already given, so
they **fail**. `unindexed` only bounds them, so it **warns**.
`unreadable` also **warns**, and is named in the finding: doctor did not
prove anything is wrong with those files, only that it could not look —
but folding them into "all N indexed files match the working tree" would
report an unknown as a check that passed, which is exactly what this
command exists to stop other commands doing.

Two deliberate limits on `unindexed`, both of which the finding states in
so many words:

- It counts `.go` files only — the clean sentence says so too, so a
  reader is never left assuming a `.ts` or `.py` file was examined.
  `codeindex` hashes every Go file it walks but hashes a `.ts` / `.py`
  file only when that file carries an annotation, so a perfectly
  well-indexed TypeScript file legitimately has no hash row. Counting
  those would be a wall of non-problems.
- It re-applies the scanner's exclusion rules (a
  `// Code generated ... DO NOT EDIT.` header, a `generated` path
  segment, the `scan.generated` globs from `.atlas.yaml`, `scan.skip_dirs`,
  `vendor/`, `node_modules/`, dot-directories) so files the scanner
  declined by policy are not reported as gaps. That reimplementation can
  drift from the scanner; the drift is bounded on purpose, because
  `unindexed` is warn-only and never fails a build.

Special cases, both reported honestly rather than as `ok`:

- Symbols in the store but **no hash rows at all** (the last scan ran
  `--hash-files=false`): `n/a` — there is nothing to compare against.
- **Nothing at all** in the store: `fail`, remediation `atlas init`.

### `coverage.freshness`

Reads `store.Coverage().LatestFrontier()` — the same runs `atlas audit`
scores — and warns on either of two independent complaints, reporting
both when both hold:

- the frontier is older than 7 days;
- the frontier was measured **before** the newest `file_hashes.mtime`,
  meaning the index holds file content the coverage run never executed,
  so the coverage half and the index half of the picture are about
  different repos.

The second signal is deliberately **not** `max(file_hashes.last_scanned)`.
`store.Ingest` refreshes `last_scanned` for every file on every scan,
unchanged ones included, to keep the cache TTL warm — so keying
staleness off it made the complaint fire after *any* scan whether or not
a byte of code had moved. A warning that is almost always wrong is a
warning people switch off. `mtime` is the file's own modification time as
recorded at scan, so it moves only when the indexed content moved.

The bound is one-sided: a file whose bytes changed while its mtime did
not (a restore from an archive, a deliberate backwards `touch`) is
invisible to this check. `index.freshness` catches that by re-hashing,
and it *fails* rather than warns, so nothing is lost.

With no `file_hashes` rows at all there is no signal to date the indexed
content from. That half of the check reports `predates_index: "unknown"`,
and if the age half is clean the whole check is `n/a` with the reason —
never a bare `ok` for a question it did not get to ask.

A frontier spanning several runs of one polyglot build is dated by its
*youngest* member — an old Playwright run inside an otherwise-fresh group
should not nag.

### `coverage.attribution`

The blind spot, pooled across the whole frontier: `stmts_unattributed /
(stmts_attributed + stmts_unattributed)`. Unattributed statements are
dropped from the numerator *and* the denominator of every per-feature
figure, so a repo whose coverprofile paths do not reconcile shows
plausible-looking percentages computed over a fraction of what actually
ran (issues #85 / #100).

Warns at ≥ 10%, fails at ≥ 33%. Remediation is `atlas cov status --gaps`,
which enumerates the files behind the number.

Zero attributed **and** zero unattributed is `n/a`, not `ok`: pass/fail
frameworks carry no statement counts, and every run written before schema
0011 left those columns at zero. Reporting 0-of-0 as perfect attribution
would advertise a blind spot as coverage.

### `feature.linkage`

Two silent drifts between the annotation layer and the symbol layer:

- **features with no linked symbols.** `feature_symbols` cascades when a
  symbol is deleted; the `features` row does not. A renamed function
  leaves an empty shell that still ranks in `atlas sprint` and still
  scores in `atlas audit` — about nothing.
- **anchored annotations naming a feature the store does not have.** An
  annotation that resolves to an indexed symbol is exactly the shape the
  ingest materializes a feature for, so a missing `features` row means
  the annotation layer and the feature layer were written by different
  passes and no longer agree.

**What this check does *not* report, and why.** An annotation that
resolves to *no* symbol within `LookupAtPosition`'s 30-line window is an
**intentional orphan**, documented as such in `packages/store/ingest.go`:
markdown files, package-doc comments and end-of-file markers legitimately
carry annotations that cannot be anchored, and the ingest would "rather
have no link than a phantom one". Nothing in the store distinguishes
those from a real break after the fact, so the check re-asks the ingest's
own question through the same port with the same window, reports only
annotations that *did* anchor, and counts the rest as
`unanchored_annotations` — a number, not a complaint. Before this
narrowing the check reported the everyday state of any annotated repo as
a problem.

The narrowing under-reports in one known place: the ingest also
materializes a test-file annotation by falling back to the corresponding
impl file, and that fallback lives behind unexported store helpers. Such
an annotation counts as unanchored here. Under-reporting is the direction
this check errs in on purpose — a false alarm costs more than a missed
one, because it is what teaches people to ignore the output.

Ids are extracted with the same rule the ingest uses
(`annotations.IsDottedFeatureID`), so `#mocked`, `integration` and other
tags/tiers are not mistaken for missing features. A token the rule cannot
classify is not reported.

Warn, not fail — both findings make atlas's answers incomplete without
making the answers it does give wrong.

The annotation half needs doctor's read-only handle on the state
database. When that handle does not open the sweep does not run, and the
check says so: `annotation_sweep: "skipped"`, **no** `dangling_annotations`
key at all (a `0` beside an empty list would read exactly like a sweep
that ran and found nothing), and — if the features half is clean — a
verdict of `n/a` rather than `ok`, pointing at the `store.schema` finding
that carries the reason.

### `store.schema`

Compares the applied migration version to the one this binary carries and
reports golang-migrate's `dirty` flag.

The expectation is **measured, not declared**: doctor opens a throwaway
store in a temp dir, lets `store.Open` apply the embedded migrations, and
reads the version off it. A `const expectedSchema = 12` in the source
would be one more thing that goes stale the next time someone adds a
migration — the exact bug class doctor exists to close.

This is the one check written to work with **no open store**, because the
states it reports are the states in which `store.Open` refuses.
golang-migrate will not advance a dirty schema, so the moment the flag
matters is the moment every other atlas command dies in a wall of migrate
output. When the store will not open, `doctor` still runs: the schema
check fails with the actual reason and a way out, and every other check
reports `n/a` naming that failure rather than repeating it.

Failure modes:

- **dirty** — a migration died part-way through. Nothing will advance
  until it is cleared; the state DB is a rebuildable cache, so the
  remediation is to delete it and re-init.
- **applied > expected** — the store was migrated by a *newer* atlas.
  This is the quiet direction: `migrate.Up` is a no-op against a schema
  newer than the binary's, so the store opens cleanly and this binary
  then reads tables it was never built against.
- **applied < expected** — pending migrations have not been applied.
- **applied = 0** — the file carries no `schema_migrations` table, or an
  empty one; it is not an atlas store. This is also the verdict for the
  0-byte placeholder and the unrelated SQLite file the read-write open is
  guarded against: the file is *reported* as not-a-store rather than
  turned into one.

## Examples

### A typical run

```
$ atlas doctor
atlas doctor — /home/me/repo (db: /home/me/repo/.atlas/atlas.db)

  [ok]   index.freshness
        examines: the recorded file index against the files on disk, plus Go files on disk with no index entry
        436 indexed files all match the working tree, and no unindexed Go file was found under /home/me/repo (the unindexed sweep reads .go only -- see the docs for why)

  [ok]   coverage.freshness
        examines: how old the current coverage frontier is, and whether the index moved under it
        the coverage frontier (1 run(s)) is 2h0m0s old and postdates the newest file content in the index

  [warn] coverage.attribution
        examines: the share of executed statements the ingest could not charge to a symbol
        10.7% of executed statements (455 of 4250) could not be charged to a symbol, so every coverage figure atlas reports is understated by that much
        fix: atlas cov status --gaps

  [warn] feature.linkage
        examines: features with no linked symbols, and annotations that resolve to an indexed symbol yet name a feature the store does not have
        13 annotations resolve to an indexed symbol but name a feature the store does not have
        fix: atlas scan

  [ok]   store.schema
        examines: the applied migration version against the one this binary carries, and the dirty flag
        the store is at schema 12, the version this binary carries, and is not dirty

  3 ok, 2 warn, 0 fail, 0 n/a — worst: warn
```

Exit 0: nothing reached `fail`, so the default gate stays green even
though two checks have something to say. Under `--fail-on warn` the same
run exits 1.

### A stale index

Edit one indexed file without re-scanning:

```
$ atlas doctor
  [fail] index.freshness
        examines: the recorded file index against the files on disk, plus Go files on disk with no index entry
        the index is stale: of 436 indexed files, 1 changed on disk and 0 no longer exist
        fix: atlas scan
...
  2 ok, 2 warn, 1 fail, 0 n/a — worst: fail
$ echo $?
1
```

### Nothing ingested yet

```
$ atlas doctor
  [n/a]  coverage.freshness
        examines: how old the current coverage frontier is, and whether the index moved under it
        no coverage run has been ingested, so there is nothing to assess
        fix: atlas cov sync --framework go-cover --input coverage.out

  [n/a]  feature.linkage
        examines: features with no linked symbols, and annotations naming a feature the store does not have
        no features are declared in the store, so there is no linkage to check (nothing in this repo carries an @atlas:feature annotation yet)
        fix: annotate a symbol with @atlas:feature <id>, then: atlas scan
```

`n/a` never trips a gate — a repo mid-adoption is not a broken repo.

### Verbose

`-v` appends each check's `details` map, sorted by key so two runs over
the same state print identically:

```
$ atlas doctor -v
  [fail] index.freshness
        examines: the recorded file index against the files on disk, plus Go files on disk with no index entry
        the index is stale: of 436 indexed files, 1 changed on disk and 0 no longer exist
        fix: atlas scan
        changed: 1
        changed_files: packages/doctor/coverage.go
        indexed_files: 436
        missing: 0
        unindexed: 0
        unreadable: 0
```

Sample path lists are capped at 10 entries; the counts beside them stay
exact. One `atlas scan` fixes all of them at once, so a wall of paths
would only bury the number that matters.

## JSON

`--json` emits the standard v1 envelope with the whole report as
`result` — every check, not just the failures.

```json
{
  "schema_version": "v1",
  "command": "doctor",
  "args": {
    "db_path": "/home/me/repo/.atlas/atlas.db",
    "fail_on": "fail",
    "root": "/home/me/repo"
  },
  "result": {
    "checks": [
      {
        "name": "index.freshness",
        "examines": "the recorded file index against the files on disk, plus Go files on disk with no index entry",
        "severity": "fail",
        "finding": "the index is stale: of 436 indexed files, 1 changed on disk and 0 no longer exist",
        "remediation": "atlas scan",
        "details": {
          "changed": 1,
          "changed_files": ["packages/doctor/coverage.go"],
          "indexed_files": 436,
          "missing": 0,
          "missing_files": [],
          "unindexed": 0,
          "unindexed_files": [],
          "unreadable": 0
        }
      },
      {
        "name": "coverage.attribution",
        "examines": "the share of executed statements the ingest could not charge to a symbol",
        "severity": "warn",
        "finding": "10.7% of executed statements (455 of 4250) could not be charged to a symbol, so every coverage figure atlas reports is understated by that much",
        "remediation": "atlas cov status --gaps",
        "details": {
          "fail_above": 0.33,
          "files_unmatched": 13,
          "recorded": true,
          "run_ids": [1],
          "stmts_attributed": 3795,
          "stmts_unattributed": 455,
          "unattributed_fraction": 0.10705882352941176,
          "warn_above": 0.1
        }
      }
    ],
    "counts": { "ok": 2, "warn": 2, "fail": 1, "n/a": 0 },
    "worst": "fail"
  },
  "generated_at": "2026-09-06T04:18:37Z"
}
```

Field notes for consumers:

- `severity` is one of `ok`, `warn`, `fail`, `n/a`.
- `name` and `examines` are stable; `finding` is prose and may be
  reworded, so gate on `severity` and `details`, never on the sentence.
- `remediation` is a concrete command, absent only when there is nothing
  to do (i.e. on `ok`).
- `details` keys are per-check. Sample path lists are always arrays,
  never `null`, and are capped at 10 entries; the count keys beside them
  (`changed`, `missing`, `unreadable`, `unindexed`, …) are exact.
- **An absent key means "not examined", and is never substituted with a
  zero.** `unreadable_files` appears only when something was unreadable;
  `feature.linkage` omits `dangling_annotations` / `dangling_refs` /
  `unanchored_annotations` entirely when `annotation_sweep` is
  `"skipped"`; `coverage.freshness` omits `newest_indexed_content` when
  the store records no file hashes. Consumers should treat a missing key
  as unknown rather than defaulting it.
- `coverage.freshness.predates_index` is the string `"yes"`, `"no"` or
  `"unknown"`, not a boolean — the third state has to be representable,
  or a question doctor never asked reads as one it answered "no".
- `counts` sums to `len(checks)`; `worst` is the highest severity
  present, ranking `n/a` below `ok`.

## Cost

`index.freshness` re-hashes every indexed file and walks the tree, which
is roughly what one `atlas scan` pays to decide what to skip. An mtime
comparison would be cheaper and would miss a restored-then-edited file —
doctor being as expensive as a scan is the price of an answer that is not
itself a guess. `store.schema` additionally migrates one throwaway
database in a temp dir.

## Related

- [`atlas cov status --gaps`](cov.md) — the per-file enumeration behind
  `coverage.attribution`.
- [`atlas scan`](scan.md) — the fix for a stale index.
- [`atlas init`](init.md) — the fix for an empty or unreadable store.
