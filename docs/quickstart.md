# Atlas Quickstart — one command, zero annotations

Atlas indexes Go, TypeScript and Python into a per-project SQLite store and
answers questions about capabilities, coverage, drift and impact.

Most of what it can tell you is keyed to a **capability registry** — the set
of `@atlas:feature` annotations somebody wrote in the source. On a codebase
where nobody has written any yet, that is a chicken-and-egg problem, and it
is the reason a tool like this usually dies in evaluation: you are asked to
annotate before you have any evidence that annotating is worth it.

`atlas onboard` is the answer to that. It runs on a repository with zero
annotations and ends on a **provisional capability map** plus a list of
things the map made visible — endpoints nothing tests, tables written from
two places, code changing with nothing verifying it — followed by an honest
statement of what atlas could not see.

Nothing it infers goes into the registry. See
[Inferred is not declared](#inferred-is-not-declared) for why that matters
and what the one path in is.

---

## 1. Install

```bash
go install github.com/sosalejandro/atlas/cmd/atlas@latest
```

For a specific tagged release, swap `@latest` for the version
(e.g. `@v0.13.0`). Verify with `atlas --version`; a version that reports
`dev` means you installed from a non-tag ref.

### Optional runtime dependencies

The TypeScript and Python sub-scanners shell out to native runtimes. Each is
**optional** — atlas continues with the languages it can handle and emits one
warning per missing runtime.

| Language    | Runtime    | Floor    | If missing                                 |
| ----------- | ---------- | -------- | ------------------------------------------ |
| Go          | (none)     | —        | Always indexed; never disabled.            |
| TypeScript  | `node`     | 18+      | TS sources skipped; one warning emitted.   |
| Python      | `python3`  | 3.8+     | Python sources skipped; one warning.       |

---

## 2. The first run — `atlas onboard`

```bash
cd your-project
atlas onboard
```

One command. It scans, ingests, builds the SQL inventory, reads the HTTP
route registrations, mines git history, and derives the map from all of it.

Output below is from running `atlas onboard` on the atlas repository itself
(which does carry annotations — the `declared` line is those, adopted as
they are). Long sections are elided with `…`; nothing else is edited.

```
$ atlas onboard --top 2

atlas onboard — /home/…/atlas

  scanned      2422 production symbols, 1963 test symbols      5.5s
  sql          129 operations, 1 unresolved                0.1s
  routes       23 registrations
  declared     60 features from annotations, adopted as they are
  PROVISIONAL  232 capabilities across 45 domains, over 2421 undeclared symbols
  total           5.9s

WHAT ATLAS FOUND

  1. [high] 1 table is written from more than one capability  [over provisional groupings]
     A shared writer is coupling that no import graph shows: a schema or invariant change in one capability lands in the other's rows.
       · coverage_results is written by 2 capabilities: provisional:packages.store, provisional:store.coverage
     → atlas sql capabilities

  2. [info] 25 tables have exactly one writing capability  [over provisional groupings]
     Single-writer tables are an ownership boundary the code already enforces. They are the cheapest capabilities to declare first.
       · annotations is written only by provisional:packages.store
       …

  3. [medium] 59 SQL advisories across 2 checks
     Unbounded reads, unstable pagination, filters no index serves.
       · sql.unbounded-list x48 -- SELECT over annotations has no LIMIT and no keyset predicate; the result set grows with the table  (packages/doctor/probe.go:179)
       · sql.missing-index x11 -- filters annotations on kind, and no index on that table leads with any of those columns  (packages/doctor/probe.go:179)
     → atlas sql advise

  4. [info] 428 symbols have no incoming reference atlas can see
     A candidate list, not a verdict: dynamic dispatch, entry points and plugin registries all look like this to a static graph.
       …
     → atlas codebase dead

WHAT ATLAS CANNOT SEE

  · No coverage run has been ingested, so atlas cannot say what your tests actually execute. Test evidence in this report means "a test file sits in the same directory", which is a weaker claim than it looks.
    → go test ./... -coverprofile=cover.out -covermode=atomic && atlas cov sync --framework go-cover --input cover.out

  · 1 of 129 queries (1%) were assembled where atlas could not read them. Every table set above is a lower bound: a table only those queries touch is missing from it.
    → atlas sql list --unresolved

  · 1 of 23 route registrations point at a handler atlas could not resolve to an indexed symbol, so no capability was proposed for them.
    → atlas contract list

  · The scan excluded 26 files (generated code and ignored packages) and raised 24 warnings. Symbols atlas could not read are absent from every capability above.
    → atlas doctor

  · All 232 capabilities above are inferred. Atlas did not write any of them to the registry; 60 declared features already in the registry were adopted as they are.
    → atlas onboard promote --id <id> --apply

PROVISIONAL CAPABILITY MAP (232 proposals)

  from HTTP routes (2 of 14)

  provisional:sprint.handle                   3 symbols  tests:colocated-tests
      route ANY /sprint registered here  (internal/server/server.go:94)
  provisional:contract.handle                 2 symbols  tests:colocated-tests
      route ANY /contract registered here  (internal/server/server.go:92)
  …

  from code structure and test names (2 of 218)

  provisional:cli.cov                        39 symbols  tests:colocated-tests
      26 tests in internal/cli lead with "Cov"  (internal/cli/cov_diff_test.go:227)
  provisional:cli.trend                      18 symbols  tests:colocated-tests
      24 tests in internal/cli lead with "Trend"  (internal/cli/trend_issue92_test.go:24)
  …

  Full map written to /home/…/atlas/.atlas/provisional/capabilities.json.
  Nothing above was added to the registry.
```

The run ends with the CI snippet and the three commands worth running next.

### How long it takes

Two measurements, both on one developer laptop:

| Repository                                                    | `atlas onboard`, cold |
| ------------------------------------------------------------- | --------------------- |
| The atlas repo — 581 indexed files, 4,385 symbols, Go + TS + Py | **5.9 s**             |
| A synthetic 3,000-file Go tree (60 packages × 50 files)        | **2.2 s**             |

The synthetic tree is larger and faster because it is pure Go: the
TypeScript and Python sub-scanners are subprocesses, and on a polyglot repo
they dominate the scan. Both are far inside the five-minute budget this
command was designed against.

Adding execution evidence is the expensive step and is still small: on the
atlas repo, `go test ./... -coverprofile=cover.out -covermode=atomic` took
**17 s** and ingesting the profile with `atlas cov sync` took **under a
second**.

---

## 3. Inferred is not declared

Atlas's registry is worth something only because a human wrote every row in
it. A tool that quietly filled it with guesses to make its own demo look
good would be trading the product for the screenshot.

So the map `onboard` produces is kept apart from the registry by four
mechanisms, not by a convention:

1. **A separate namespace.** Every inferred capability is addressed as
   `provisional:<id>`. A colon cannot appear in a feature id, so a
   provisional reference can never be mistaken for a declared one by any
   consumer, human or machine.
2. **A separate file.** The map is written to
   `.atlas/provisional/capabilities.json`, not into the SQLite store. It
   cannot be joined against by a verb that forgot the distinction, and it is
   reviewable in a diff.
3. **A separate code path.** The inference (`packages/onboard`) is a pure
   function over values. It holds no store handle, so no path through it can
   write the features table even by mistake.
4. **A label on every record.** Both the document and each capability inside
   it carry `"provisional": true`, so a consumer that renders only part of
   the JSON still cannot present it as declared state.

Existing annotations are adopted exactly as they are. A symbol that already
belongs to a declared feature is invisible to every grouping stage, so no
proposal duplicates work somebody already did.

### Where the proposals come from

Each proposal cites the evidence it came from, and anything that resolves to
no symbol is dropped before it is displayed. Grouping runs
strongest-signal-first, and each stage claims only what earlier stages left:

| Source            | Proposal                                                                   |
| ----------------- | -------------------------------------------------------------------------- |
| HTTP routes       | one capability per registration — `POST /measurements` → `measurements.create` |
| Test names        | two or more tests leading with the same word — `TestCheckout*` in `billing` → `billing.checkout` |
| Directory tree    | everything left, named `<parent>.<dir>` — the weakest proposal, and the one that is a fact about the tree rather than a guess |
| Existing annotations | adopted as-is; their symbols are never re-proposed                      |

The SQL inventory, git churn and any ingested coverage are not grouping
sources — they are attached to whatever grouping was made, which is where
the data footprint, the churn score and the test evidence on each line come
from.

---

## 4. Accepting a proposal — `atlas onboard promote`

Promotion is the only path from a proposal into the registry, and it does
**not** write the database. It writes the `@atlas:feature` annotation into
your source, above the anchor declaration the proposal cites; the annotation
then reaches the features table through the ordinary scan, exactly as a
hand-written one would.

The default is a dry run:

```
$ atlas onboard promote --id provisional:cli.cov

atlas onboard promote (dry-run) — 1 capability

  would write internal/cli/cov.go:27
              // @atlas:feature cli.cov

  Nothing was written. Re-run with --apply to accept these.
```

```bash
atlas onboard promote --id provisional:cli.cov --apply   # accept one
atlas onboard promote --all                              # preview all of them
atlas onboard promote --all --apply                      # accept all of them
atlas scan                                               # materialise them
```

`--id` takes the namespaced form the report printed or the bare id; it is
repeatable. An id that is not in the map is an error rather than a silent
no-op, so a typo in a script fails instead of passing green.

A declaration that already carries an `@atlas:feature`, `@atlas:contract` or
`@testreg` annotation is skipped with a reason. Promotion never overwrites a
human's annotation.

Promotion seeds membership at **one** symbol per capability. That is
deliberate: an annotation is a claim a person is making, and a hundred of
them written at once is not. Broaden a feature by annotating more symbols
yourself — `atlas trace <feature-id>` shows what the claim currently covers.

---

## 5. Turning it into a gate

`atlas onboard` prints these steps at the end of every run:

```yaml
      - run: atlas init
      - run: go test ./... -coverprofile=cover.out -covermode=atomic
      - run: atlas cov sync --framework go-cover --input cover.out
      - run: atlas cov diff --base origin/main --fail-under 70
      - run: atlas audit --worst 10
```

`atlas cov diff` is the one that can actually fail a pull request: it scores
the lines the branch added or modified, rather than the whole repository,
which no single PR can move.

The snippet ingests coverage the plain way — a coverprofile plus
`cov sync` — rather than through `atlas cov run`. `cov run` is the richer
per-test path, but it needs the shim armed first (`atlas cov shim init`) and
without it exits 0 having ingested nothing, which is the worst thing a
starter snippet can do.

Re-run `atlas onboard` after that and the map's test evidence changes from
`colocated-tests` (a test file sits nearby) to `execution` (a test actually
ran this code) — which is the difference between a guess and a measurement,
and the reason the two are printed as different words.

---

## 6. The daily verbs

Once there is a registry — promoted, hand-written, or both:

```bash
atlas scan                      # incremental re-scan; hash-driven, warm scans are milliseconds
atlas codebase find Login       # where is this symbol?
atlas trace auth.login          # what does this feature call?
atlas audit --worst 10          # what needs attention?
atlas diagnose "Authenticate"   # where would this error have come from?
atlas hotspots                  # what is changing fastest with the least coverage?
atlas sql advise                # unbounded reads, unstable pagination, missing indexes
```

Every verb takes `--json` for a stable envelope. See
[`docs/commands/`](./commands/) for the per-verb reference and
[`docs/json-output.md`](./json-output.md) for the envelope schema.

---

## Where to go next

- **Annotation grammar** — the `@atlas:<kind> <id>` syntax promotion writes
  and the scanner reads: [`docs/annotations.md`](./annotations.md).
- **Per-language guides** — prerequisites, what gets indexed, gotchas:
  [Go](./languages/go.md) / [TypeScript](./languages/ts.md) /
  [Python](./languages/py.md).
- **Per-command reference** — every verb, every flag:
  [`docs/commands/`](./commands/).
- **Architecture** — package boundaries and dependency direction:
  [`docs/architecture.md`](./architecture.md).
- **Coming from testreg?** —
  [`docs/migration-from-testreg.md`](./migration-from-testreg.md).

The state DB lives at `.atlas/atlas.db` and the provisional map at
`.atlas/provisional/capabilities.json`. The DB is per-checkout state — add
it to `.gitignore`. The provisional map is a proposal set; commit it if you
want the team to review the proposals, delete it if you don't.
