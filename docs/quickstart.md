# Grunnr Quickstart — one command, zero annotations

Grunnr indexes Go, TypeScript and Python into a per-project SQLite store and
answers questions about capabilities, coverage, drift and impact.

Most of what it can tell you is keyed to a **capability registry** — the set
of `@atlas:feature` annotations somebody wrote in the source. On a codebase
where nobody has written any yet, that is a chicken-and-egg problem, and it
is the reason a tool like this usually dies in evaluation: you are asked to
annotate before you have any evidence that annotating is worth it.

`grunnr onboard` is the answer to that. It runs on a repository with zero
annotations and ends on a **provisional capability map** plus a list of
things the map made visible — endpoints nothing tests, tables written from
two places, code changing with nothing verifying it — followed by an honest
statement of what grunnr could not see.

Nothing it infers goes into the registry. See
[Inferred is not declared](#inferred-is-not-declared) for why that matters
and what the one path in is.

---

## 1. Install

```bash
go install github.com/sosalejandro/grunnr/cmd/grunnr@latest
```

For a specific tagged release, swap `@latest` for the version
(e.g. `@v0.13.0`). Verify with `grunnr --version`; a version that reports
`dev` means you installed from a non-tag ref.

### Optional runtime dependencies

The TypeScript and Python sub-scanners shell out to native runtimes. Each is
**optional** — grunnr continues with the languages it can handle and emits one
warning per missing runtime.

| Language    | Runtime    | Floor    | If missing                                 |
| ----------- | ---------- | -------- | ------------------------------------------ |
| Go          | (none)     | —        | Always indexed; never disabled.            |
| TypeScript  | `node`     | 18+      | TS sources skipped; one warning emitted.   |
| Python      | `python3`  | 3.8+     | Python sources skipped; one warning.       |

---

## 2. The first run — `grunnr onboard`

```bash
cd your-project
grunnr onboard
```

One command. It scans, ingests, builds the SQL inventory, reads the HTTP
route registrations, mines git history, and derives the map from all of it.

Output below is recorded from running `grunnr onboard` on the grunnr
repository itself (which does carry annotations — the `declared` line is
those, adopted as they are), against a state DB that did not exist yet. The
only edit is the path, shortened to `/home/…/grunnr`; the `…` lines are
printed by the command itself. Every count moves with the codebase, so read
this as a snapshot of one run rather than as numbers to expect.

Two of the entries are **groupings grunnr refused to name** — see
[`grunnr onboard`](./commands/onboard.md#groupings-grunnr-will-not-name). It
reports their size and their file breakdown rather than labelling 174 symbols
with a word it cannot point at in the code.

```
$ grunnr onboard --top 2

grunnr onboard — /home/…/grunnr

  scanned      2779 production symbols, 2552 test symbols      1.4s
  sql          145 operations, 10 unresolved                0.1s
  routes       28 registrations
  declared     64 features from annotations, adopted as they are
  PROVISIONAL  105 named proposals across 35 domains + 2 unnamed groupings (174 symbols), over 2778 undeclared symbols
  total           1.9s

WHAT GRUNNR FOUND

  1. [high] 2 tables are written from more than one capability  [over provisional groupings]
     A shared writer is coupling that no import graph shows: a schema or invariant change in one capability lands in the other's rows.
       · coverage_results is written by 2 capabilities: provisional:packages.store, provisional:store.coverage
       · symbols is written by 2 capabilities: provisional:packages.store, provisional:store.symbols
     → grunnr sql capabilities

  2. [info] 24 tables have exactly one writing capability  [over provisional groupings]
     Single-writer tables are an ownership boundary the code already enforces. They are the cheapest capabilities to declare first.
       · annotations is written only by provisional:packages.store
       · audit_snapshot_runs is written only by provisional:packages.store
       · cfg_blocks is written only by provisional:packages.store
       · cfg_decision_coverage is written only by provisional:packages.store
       · cfg_edges is written only by provisional:packages.store

  3. [medium] 67 SQL advisories across 3 checks
     Unbounded reads, unstable pagination, filters no index serves.
       · sql.unbounded-list x53 -- SELECT over annotations has no LIMIT and no keyset predicate; the result set grows with the table  (packages/doctor/probe.go:184)
       · sql.missing-index x12 -- filters annotations on kind, and no index on that table leads with any of those columns  (packages/doctor/probe.go:184)
       · sql.possible-injection x2 -- query text is built by string formatting from a value the caller supplies  (packages/redact/inventory.go:231)
     → grunnr sql advise

  4. [info] 258 symbols have no incoming reference grunnr can see
     A candidate list, not a verdict: dynamic dispatch, entry points and plugin registries all look like this to a static graph.
       · assets.python.grunnr  (assets/python/grunnr.py:1)
       · assets.python.grunnr.T  (assets/python/grunnr.py:36)
       · assets.python.grunnr._identity  (assets/python/grunnr.py:39)
       · assets.python.grunnr._identity.wrap  (assets/python/grunnr.py:44)
       · cmd/grunnr-serve.main  (cmd/grunnr-serve/main.go:64)
     → grunnr codebase dead

WHAT GRUNNR CANNOT SEE

  · No coverage run has been ingested, so grunnr cannot say what your tests actually execute. Test evidence in this report means "a test file sits in the same directory", which is a weaker claim than it looks.
    → go test ./... -coverprofile=cover.out -covermode=atomic && grunnr cov sync --framework go-cover --input cover.out

  · 10 of 145 queries (7%) were assembled where grunnr could not read them. Every table set above is a lower bound: a table only those queries touch is missing from it.
    → grunnr sql list --unresolved

  · 1 of 28 route registrations point at a handler grunnr could not resolve to an indexed symbol, so no capability was proposed for them.
    → grunnr contract list

  · The scan deliberately excluded 28 files (generated code and ignored packages); their symbols are absent from every capability above. The scan raised 24 scanner warnings. A warning is a diagnostic, not a count of files grunnr could not read: some cost it a symbol and some do not, and it does not tell them apart -- so read them rather than the number.
    → grunnr doctor

  · Grunnr could not name 174 of 2778 undeclared symbols (6%). They are in 2 groupings it refused to name rather than label with a word scraped off a test name or a directory that says nothing -- listed as "groupings grunnr would not name" in the map below, with their file breakdown. Naming one is the single judgement this tool will not make for you.
    → grunnr onboard promote --id unnamed:1 --as <your.feature.id>

  · All 107 capabilities below are inferred. Grunnr did not write any of them to the registry; 64 declared features already in the registry were adopted as they are.
    → grunnr onboard promote --id <id> --apply

PROVISIONAL CAPABILITY MAP (107 entries)

  What this is: a first guess at the capabilities in this repository, derived from
  HTTP routes, from test names the production code corroborates, and from the
  directory tree — in that order of strength. Nothing here is in the registry, and
  nothing here is a name grunnr is asking you to keep.

  from HTTP routes (2 of 19)

  provisional:sprint.handle                   3 symbols  tests:colocated-tests
      route ANY /sprint registered here  (internal/server/server.go:94)
  provisional:contract.handle                 2 symbols  tests:colocated-tests
      route ANY /contract registered here  (internal/server/server.go:92)

  … 17 more

  from code structure and test names (2 of 86)

  provisional:cli.trend                      21 symbols  tests:colocated-tests
      24 tests in internal/cli/ lead with "Trend"; the name is the head of runTrend, compareTrend, emitTrend  (internal/cli/trend_issue92_test.go:24)
  provisional:store.symbols                  20 symbols  tests:colocated-tests writes symbols  (+2 unreadable queries — lower bound)
      10 tests in packages/store/ lead with "Symbols"; the name is the head of CarriedBySymbol, writeSymbols, repositionMovedSymbols and 5 more  (packages/store/symbols_test.go:17)

  … 84 more

  groupings grunnr would not name (2 of 2)

  provisional:unnamed:1                      95 symbols  tests:colocated-tests
      95 undeclared symbols in internal/app/ that no route, no directory name and no corroborated test name covers. Grunnr has no honest name for this one.  (internal/app/audit_feature.go:30)
        29  internal/app/audit_feature.go
        18  internal/app/scan_tests.go
        13  internal/app/contract_feature.go
        10  internal/app/trace_feature.go
         6  internal/app/get_status.go
      … +5 more files
      → grunnr onboard promote --id unnamed:1 --as <your.feature.id>
  provisional:unnamed:2                      79 symbols  tests:none
      79 undeclared symbols in the repository root that no route, no directory name and no corroborated test name covers. Grunnr has no honest name for this one.  (external:py:1)
        79  external:py
      → grunnr onboard promote --id unnamed:2 --as <your.feature.id>

  Full map written to /home/…/grunnr/.grunnr/provisional/capabilities.json.
  Nothing above was added to the registry.
```

The run ends with the CI snippet and the three commands worth running next.

### How long it takes

Timings are wall clock from `time`, on one developer laptop, recorded from
an **earlier build** than the transcript above — that transcript's own header
line is the measurement for its own run, and it is smaller. Both are one
machine on one day. Every `onboard` run below is cold: a
state DB that does not exist yet, so nothing is served from the incremental
cache. **Your machine is not this machine** — these are one person's numbers
on one afternoon, not a benchmark, and the point they make is the order of
magnitude.

| Repository                                                      | `grunnr onboard`, cold      |
| --------------------------------------------------------------- | -------------------------- |
| The grunnr repo — 619 indexed files, 4,674 symbols, Go + TS + Py  | **6.5 s** (3 runs: 7.1, 6.6, 6.5) |
| A synthetic 3,000-file Go tree (60 packages × 50 files, 12,000 symbols) | **1.2 s** (2 runs, both 1.2) |

The synthetic tree holds about two and a half times the symbols and takes
roughly a fifth of the time, because it is pure Go: the TypeScript and
Python sub-scanners are
subprocesses, and on a polyglot repo they dominate the scan. Both are far
inside the five-minute budget this command was designed against.

`onboard` also prints its own per-phase timings on the header line, and
reports them in `--json` under `timings_ms`, so you can check the claim on
your own repository rather than take this table's word for it.

Adding execution evidence is the expensive step and is still small: on the
same machine and the same checkout,
`go test ./... -coverprofile=cover.out -covermode=atomic` took **11.6 s**
and ingesting the profile with `grunnr cov sync` took **0.2 s**.

---

## 3. Inferred is not declared

Grunnr's registry is worth something only because a human wrote every row in
it. A tool that quietly filled it with guesses to make its own demo look
good would be trading the product for the screenshot.

So the map `onboard` produces is kept apart from the registry by four
mechanisms, not by a convention:

1. **A separate namespace.** Every inferred capability is addressed as
   `provisional:<id>`. A colon cannot appear in a feature id, so a
   provisional reference can never be mistaken for a declared one by any
   consumer, human or machine.
2. **A separate file.** The map is written to
   `.grunnr/provisional/capabilities.json`, not into the SQLite store. It
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

When a directory's derived id turns out to belong to a capability an earlier
signal already proposed, its symbols are merged into that capability and the
merge is written into the evidence as a `merged in: …` line. The alternative
— attaching them silently — leaves the reader looking at route or test-name
evidence above a symbol list a directory sweep partly filled in.

The SQL inventory, git churn and any ingested coverage are not grouping
sources — they are attached to whatever grouping was made, which is where
the data footprint, the churn score and the test evidence on each line come
from.

---

## 4. Accepting a proposal — `grunnr onboard promote`

Promotion is the only path from a proposal into the registry, and it does
**not** write the database. It writes the `@atlas:feature` annotation into
your source, above the anchor declaration the proposal cites; the annotation
then reaches the features table through the ordinary scan, exactly as a
hand-written one would.

The default is a dry run:

```
$ grunnr onboard promote --id provisional:cli.cov

grunnr onboard promote (dry-run) — 1 capability

  would write internal/cli/cov.go:27
              // @atlas:feature cli.cov

  Nothing was written. Re-run with --apply to accept these.
```

```bash
grunnr onboard promote --id provisional:cli.cov --apply   # accept one
grunnr onboard promote --all                              # preview all of them
grunnr onboard promote --all --apply                      # accept all of them
grunnr scan                                               # materialise them
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
yourself — `grunnr chain <feature-id>` shows what the claim currently covers.

---

## 5. Turning it into a gate

`grunnr onboard` prints these steps at the end of every run:

```yaml
      - run: grunnr init
      - run: go test ./... -coverprofile=cover.out -covermode=atomic
      - run: grunnr cov sync --framework go-cover --input cover.out
      - run: grunnr cov diff --base origin/main --fail-under 70
      - run: grunnr health --worst 10
```

`grunnr cov diff` is the one that can actually fail a pull request: it scores
the lines the branch added or modified, rather than the whole repository,
which no single PR can move.

The snippet ingests coverage the plain way — a coverprofile plus
`cov sync` — rather than through `grunnr cov run`. `cov run` is the richer
per-test path, but it needs the shim armed first (`grunnr cov shim init`) and
without it exits 0 having ingested nothing, which is the worst thing a
starter snippet can do.

Re-run `grunnr onboard` after that and the map's test evidence stops being a
guess. `colocated-tests` (a test file sits nearby) is replaced by one of two
measurements: `execution`, meaning a test actually ran this code, or
`measured-not-executed`, meaning the run reported on these symbols and
recorded none of them running. The second is the more useful finding of the
two, and it is reported as itself rather than collapsed back into "a test
exists nearby".

On the run recorded above, ingesting the profile moved 101 of the 107 map
entries to `execution`. Four went from `none` to `measured-not-executed`
(`packages.envelope`, `cmd.grunnr-serve`, `internal.calibrate`, `cmd.grunnr`) —
the same word's worth of output carrying a great deal more information,
because a measured negative is a thing you can act on and an absence of
signal is not.

The remaining 2 stayed on `none`, and that is the point of keeping the two
apart: they are `assets.python` and the unnamed grouping `unnamed:2`, whose
files are Python. A Go coverprofile said nothing at all about them, so they
are not reported as measured and dead.

---

## 6. The daily verbs

Once there is a registry — promoted, hand-written, or both:

```bash
grunnr scan                      # incremental re-scan; hash-driven, warm scans are milliseconds
grunnr codebase find Login       # where is this symbol?
grunnr chain auth.login          # what does this feature call?
grunnr health --worst 10          # what needs attention?
grunnr diagnose "Authenticate"   # where would this error have come from?
grunnr hotspots                  # what is changing fastest with the least coverage?
grunnr sql advise                # unbounded reads, unstable pagination, missing indexes
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

The state DB lives at `.grunnr/grunnr.db` and the provisional map at
`.grunnr/provisional/capabilities.json`. The DB is per-checkout state — add
it to `.gitignore`. The provisional map is a proposal set; commit it if you
want the team to review the proposals, delete it if you don't.
