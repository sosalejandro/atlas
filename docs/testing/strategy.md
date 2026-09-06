# Test strategy

Atlas sells one claim: *the numbers are true*. The first repository that claim
has to survive is this one.

This document describes the layers the suite is built from, what each is for,
and — the part that matters — which layer catches which class of bug. It is
the companion to [determinism.md](determinism.md), which covers one property in
depth.

## The layers

| Layer | Answers | Where it lives |
| --- | --- | --- |
| Unit | does this function do what its name says | `packages/*/{name}_test.go` |
| Golden | does the scanner still produce the same symbols and edges for a known repo | `packages/codeindex/go/testdata/goldencorpus` |
| **Property** | are the invariants true for *any* input | `packages/*/property_test.go` |
| Integration | does scan → store → audit hold together | `packages/store/ingest_*_test.go` |
| **Acceptance** | does the output match an external ground truth | `test/acceptance/pipeline_test.go` |
| **CLI / e2e** | does the command surface behave, `--json` shape included | `test/acceptance/cli_test.go` |
| Determinism | same input, same bytes | `packages/codeindex/go/determinism_test.go`, `packages/store/determinism_test.go` |
| **Dogfood** | does atlas hold up when run against atlas | `test/acceptance/dogfood_test.go` (tag `dogfood`) |

The four bold rows are what issue #122 added. The generators and invariant
checks they share live in [`packages/testing`](../../packages/testing) as
package `atlastest`.

## Why a property layer

Every bug this project has shipped and then written a regression test for was
an **invariant violation**, not a wrong answer to one particular question:

- **#85** — a colliding short name made a whole file's symbols vanish, so its
  statements were charged to nobody. On a 39-module workspace that silently
  dropped 210 production files. The example test that would have caught it is
  "two packages both declare `Chat.MarkLoaded`", and nobody writes that example
  until after the incident.
- **The duplicate coverprofile blocks** — `-coverpkg=./...` repeats every block
  once per tested package, so raw summation inflated every total.
- **#97 / PR #99** — map iteration order changed the output. No example test
  fails for this, because the example passed on the run you wrote it on.

An example test finds a bug only if you already guessed it. A property finds
the class. The rule this suite follows: **every property names the bug class it
guards, and every property has been shown to fail when that invariant is
deliberately broken.** A property that has never been seen to fail is a
decoration.

### The properties, and what each guards

| Property | Package | Guards |
| --- | --- | --- |
| `TestProperty_Scan_EveryDeclarationIsIndexedOrReported` | `codeindex/go` | #85. A declaration is either indexed at its own position under a unique id, or named in a warning. Never silently collapsed. |
| `TestProperty_Scan_SpansDoNotStraddle` | `codeindex/go` | Mis-attribution. Spans in a file must be disjoint or strictly nested, or "the symbol containing this line" is not well defined. |
| `TestProperty_Scan_GraphIsReferentiallyClosed` | `codeindex/go` | #97. Every edge endpoint names a node the graph holds. |
| `TestProperty_Scan_IsReproducibleAndRootRelative` | `codeindex/go` | PR #99. Two scans of one tree render identically; two checkouts at different paths do too. |
| `TestProperty_Attribution_ConservesStatements` | `coverage` | #85. attributed + unattributed == the statements the profile holds. |
| `TestProperty_Attribution_ChargesEachStatementOnce` | `coverage` | Double counting. Per-symbol totals sum to exactly the attributed figure, and per file never exceed what the profile holds for that file. |
| `TestProperty_Attribution_IsMonotoneInTheIndex` | `coverage` | Indexing more symbols never charges fewer statements. Scoped — see the caveat below. |
| `TestProperty_Attribution_IsDeterministic` | `coverage` | A percentage that wobbles between CI runs with no source change. |
| `TestProperty_Attribution_MergeIsIdempotent` | `coverage` | The per-test fold. Union, not summation — a 1,122-test run must not report a blind spot 1,122× too large. |
| `TestProperty_Attribution_ReIngestIsStable` | `coverage` | The same claim through SQLite, keyed on qualified names rather than surrogates. |
| `TestProperty_FindCycles_IsStableUnderRenaming` | `graph` | A cycle report that changes because somebody renamed a file. |
| `TestProperty_FindCycles_ReportsOnlyRealComponents` | `graph` | Checked against the definition of a strongly-connected component, not against a previous run. |
| `TestProperty_MergeNode_KeepsTheGraphReferentiallyClosed` | `graph` | #97 in memory: a forgotten edge retarget leaves an edge pointing at a deleted node. |
| `TestProperty_Adjacency_AgreesWithTheEdgeSlice` | `graph` | A missed cache invalidation, whose stale answer is a previously-correct answer. |
| `TestProperty_Symbols_RoundTrip` | `store` | A field that does not survive persistence. Everything atlas prints is a join over these rows. |
| `TestProperty_Symbols_InsertIsIdempotent` | `store` | #97 at the port. A re-scan must not renumber a symbol. |
| `TestProperty_Edges_EndpointsSurvivePersistence` | `store` | The same, for edges, compared through qualified names. |
| `TestProperty_Ingest_ReScanNeverRenumbersAnExistingSymbol` | `store` | The incremental path, which runs on every commit. |
| `TestProperty_Ingest_EdgesInsertedCountsOnlyNewRows` | `store` | #97 on the edge path. `INSERT OR IGNORE` leaves `last_insert_rowid` untouched, so deciding "inserted" from `LastInsertId` counted every already-known edge as new. |

### Reproducing a failure

Generators are pure functions of an explicit seed, and every case runs as a
named subtest:

```
go test ./packages/coverage -run 'TestProperty_Attribution_ConservesStatements/seed=41'
```

The same generators drive `Fuzz*` entry points (`FuzzAttribution_ConservesStatements`,
`FuzzSymbols_RoundTrip`) for deeper search:

```
go test ./packages/coverage -fuzz=FuzzAttribution -fuzztime=5m
```

Under a plain `go test` a fuzz target runs only its seed corpus — microseconds.
The everyday coverage comes from the `Test*` form, which sweeps 64 seeds
(12–24 where a case opens a SQLite file).

No new dependencies were added. `gopter` and `rapid` were considered and not
used: the generators here need to emit *Go source* and *coverprofile text*, and
neither library helps with that, while both would put a third-party package in
the path of the suite that certifies the tool.

### Two claims that had to be scoped, and why

**Attribution is not monotone when `end_line` is NULL.** With no end line,
`indexSymbolsByFile` guesses the span, and the guess for the last symbol in a
file is end-of-file (`1<<30`). That symbol absorbs every trailing statement,
including statements belonging to a declaration atlas has not indexed. Restore
the declaration and it takes its statements back — a *decrease*. Nothing is
broken; the wider index is the more truthful one. So the property is stated for
indexes where every symbol carries a real end line, and
`TestAttribution_EOFFallbackCanOverAttribute` pins the exception (3 of 64 seeds
exhibit it) so the caveat stays executable rather than becoming a comment
nobody rechecks.

**`atlas scan --json` under-reports.** `features_materialized`,
`feature_symbols_linked` **and** `orphan_annotations_skipped` are always `0`,
because `internal/cli/scan.go` builds its result field by field from
`store.IngestStats` and never copies those three. `atlas init` copies all
three from the same struct, which is what makes the divergence provable rather
than merely suspected.
`TestAcceptance_CLI_ScanOmitsTheIngestFeatureCounts` characterises the defect
over a purpose-built tree (the shared fixture produces no orphan annotation, so
the third field could not be told apart there) and fails the moment any of the
three is wired up, at which point the test should be deleted.

## Why an acceptance layer

The unit and property layers cannot answer the question a buyer asks: do the
layers *compose*? Nothing in `test/acceptance` is mocked.

`testdata/shopfixture` is a real Go module with real tests.
`cover.coverprofile` is the untouched output of running them. The per-symbol
fractions atlas produces are compared against **`go tool cover -func` invoked
live on that same profile** — live rather than a checked-in expectation,
because the claim is not "these seven numbers" but "our arithmetic agrees with
the compiler's". When a toolchain upgrade changes how statements are counted,
the honest outcome is this test failing.

The fixture is small enough to hold in your head and every shape in it is
there for a reason:

| Shape | What it exercises |
| --- | --- |
| `billing.Order.Total` and `shipping.Order.Total` | #85: both must survive, one under a package-qualified id |
| `normalize`, `rate` — package-private | the compiler instruments them, so they need symbols of their own |
| `Pay` / `Paid`, never called | a feature that reports coverage its tests did not produce |
| `Surcharge`, a function *value* | the compiler instruments it, the scanner cannot index it — the known blind spot, reported as a gap and never absorbed |

That last row is the one with teeth. `Surcharge` is the last thing in its file,
so if anything stopped trusting `rate`'s `end_line`, the span fallback would
extend `rate` to end-of-file and quietly hand it three statements it does not
contain. Verified: disabling the `end_line` branch turns 11 attributed / 3
unattributed into 14 / 0, and `TestAcceptance_TheBlindSpotIsReportedNotAbsorbed`
fails.

Measured on the committed fixture profile: 14 statements, 11 attributed across
7 declarations, 3 reported as one `outside-symbol-spans` gap. `go tool cover
-func` puts the whole fixture at 63.6%.

## Atlas gating atlas

The headline. `test/acceptance/dogfood_test.go` runs the shipped commands
against this repository and checks the answers.

```
./test/acceptance/run.sh
```

In CI it is the `atlas gates atlas (blocking)` job in
[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml). Three details of
that job are load-bearing:

- **It blocks.** Issue #122's criterion is that CI goes red when the dogfood
  numbers regress. A `continue-on-error` job would satisfy the letter and
  nothing else.
- **It reuses the test job's coverprofile.** `build-and-test` on Linux runs
  `go test ./... -race -coverprofile=cover.coverprofile
  -coverpkg=./packages/...,./internal/...` and uploads the result as the
  `repo-coverprofile` artifact; the dogfood job downloads it and passes it in
  through `ATLAS_DOGFOOD_PROFILE`. Without that, `run.sh` regenerates one,
  which means running the whole suite a second time to measure the same code.
- **It checks out with `fetch-depth: 0`.** `atlas cov diff` needs a base ref.
  On the default single-commit checkout there is neither `origin/main` nor
  `HEAD~1`, and `run.sh` then hands the suite an empty base — which the suite
  reports as not-applicable rather than failing. The gate would stay green
  having quietly made one assertion fewer.

Before this job existed the dogfood suite ran in no pipeline at all: it is
behind the `dogfood` build tag, which `go test ./...` does not set.

### What it asserts, and what it only records

| Command | Assertion |
| --- | --- |
| `atlas scan` | the index is not nearly empty — floors of 2,000 symbols and 2,000 edges |
| `atlas cov sync` | the attributed share of executed statements is at or above a committed floor |
| `atlas doctor` | no check fails, and none reports `n/a` when its inputs were prepared |
| `atlas sql scan` | the resolved fraction is at or above a committed floor |
| `atlas cov diff` | it reaches an *answer*: a percentage over a real denominator, or an explicit "no measurable change" |

`cov diff` is deliberately not gated on a threshold. Patch coverage is a
property of the branch, so a target would fail on a legitimate docs-only commit
and teach everyone to ignore the gate. What can be asserted is that the command
finds its base, joins the diff onto a fresh index, and reports a number with a
denominator behind it.

### The baselines, and how to move them

Measured on branch `fix/test-strategy` (base `189e713`), Go 1.26.4,
linux/amd64, with a profile from
`go test ./packages/... ./internal/... -coverprofile=… -coverpkg=./packages/...,./internal/...`:

| Metric | Observed | Committed floor | Margin |
| --- | --- | --- | --- |
| statements charged to a symbol | 24,118 / 24,934 = 0.9673 | 0.95 | ~430 statements |
| SQL operations resolved | 134 / 138 = 0.9710 | 0.97 | **one operation** |
| symbols indexed | 4,895 | 2,000 | large |
| edges recorded | 9,281 | 2,000 | large |

The attribution floor sits below the observation because the figure moves with
the code — the residual ~3.3% is dominated by `packages/store/sqlc`, which is
generated and excluded from the index by design, so a new generated file lowers
it without anything being wrong.

**The SQL floor has almost no margin left.** It was set at 0.97 when the
observation was 0.9922; the observation is now 0.9710, so one more unresolvable
operation (134/138 → 133/138 = 0.9638) turns the gate red. That is the floor
behaving as designed — an unresolvable query is one atlas cannot advise on, and
adding one should be a deliberate act — but it is a gate a contributor will
trip without knowing why, so it is recorded here rather than discovered in CI.
Deciding whether to fix the four unresolved operations or restate the floor is
its own change; this document does not pre-empt it.

The edge count is lower than the 10,433 an earlier revision of this document
recorded, and the difference is not a regression. `upsertEdgeTx` decided
"inserted" from `LastInsertId` after an `INSERT OR IGNORE`, which SQLite does
not update when it skips a row, so duplicate edges were counted as insertions.
Measured on this tree: a first scan into an empty database reported **11,185**
edges inserted before the fix and **9,281** after, and the `edges` table holds
9,281 rows in both cases. The old number over-reported by 1,904.

**Ratchet upward in a PR that says why. Never downward without one.** Re-measure
with `ATLAS_DOGFOOD_KEEP=1 ./test/acceptance/run.sh`, which logs every observed
value beside its floor.

## Cost

Measured on branch `fix/test-strategy`, `-count=1`, warm build cache,
same machine as the baselines above:

| Layer | Wall clock |
| --- | --- |
| `-run TestProperty_` in `codeindex/go` | 0.26 s |
| `-run TestProperty_` in `coverage` | 0.26 s |
| `-run TestProperty_` in `graph` | 0.01 s |
| `-run TestProperty_` in `store` | 1.27 s (each case opens a SQLite file) |
| `test/acceptance` (fixture + CLI, includes one `go build`) | 0.68 s |
| `test/acceptance` with `-tags=dogfood` | 11.8 s, plus whatever produced the coverprofile |

Everything except the dogfood layer runs on every `go test ./...`. The dogfood
layer is gated separately only because its input is a coverprofile of this
repository, and producing one means running the suite that would be running it
— not because it is slow. Given CI's existing profile it costs twelve seconds.

## Issue #122's acceptance criteria, one by one

Issue #122 lists five. This is where each one actually stands, so the next
reader is not misled about the coverage of the coverage tool. "Not started" is
recorded as not started; nothing here is described as done because part of it
is.

### 1. Property tests exist for the six invariants, and run in CI — **met**

The issue names six invariants. Each has a property, and all of them run under
the plain `go test ./...` that `build-and-test` executes:

| Invariant from the issue | Property |
| --- | --- |
| Σ per-symbol `total_stmts` over a file never exceeds the profile's count for that file | `TestProperty_Attribution_ChargesEachStatementOnce` (per-file half) |
| Every executed block is attributed to at most one symbol | `TestProperty_Attribution_ChargesEachStatementOnce` (global half) + `TestProperty_Attribution_ConservesStatements` |
| Attribution is monotone in the index | `TestProperty_Attribution_IsMonotoneInTheIndex` (scoped — see the caveat above) |
| A symbol's span never overlaps another's in the same file | `TestProperty_Scan_SpansDoNotStraddle` |
| Re-ingesting the same profile twice produces identical per-symbol counts | `TestProperty_Attribution_ReIngestIsStable` |
| Scan → ingest → scan again is idempotent | `TestProperty_Ingest_ReScanNeverRenumbersAnExistingSymbol` |

### 2. Every evidence source pinned to an external ground truth — **one of four**

| Evidence source | Ground truth | Status |
| --- | --- | --- |
| `go-cover` | `go tool cover -func`, invoked live on the same profile | done — `TestAcceptance_PerSymbolCoverageMatchesGoToolCover` |
| Istanbul / vitest | `nyc report` or vitest's own summary | **not done** |
| Gherkin binding (#117) | the framework's own step-definition resolution | **not done** |
| Diagram verify (#111) | a hand-checked fixture with one finding of each kind | **not done** |

The reason all three outstanding rows are outstanding is the same one, and it
is a real constraint rather than a preference: each needs a second toolchain in
the test environment. `nyc`/vitest and a Gherkin runner are Node and would put
`npm install` on the critical path of the suite that certifies atlas; the whole
Go suite currently runs on a container with no Node at all, and
`packages/coverage`'s Istanbul tests are hermetic for that reason. The concrete
next step is not "write the test" but "decide whether CI grows a Node job":
until that is decided, an Istanbul acceptance test would either be skipped in
CI (a green check measuring nothing) or would compare atlas against a
reimplementation of `nyc` living in this repo, which is pinning atlas to
itself under another name. Diagram verify (#111) has no external tool to pin
against at all — the honest ground truth there is a hand-checked fixture, which
is example-based, and it belongs in #111's own change rather than here.

### 3. `.atlas/features/` declares atlas's own capabilities, CI fails on regression — **partly, and the weaker part**

CI does now fail on regression against a committed threshold: the dogfood job
blocks, and `atlas cov sync`'s attributed share is gated at 0.95 with `atlas
sql scan` gated at 0.97. What does *not* exist is `.atlas/features/`, so the
gate is repo-wide rather than per capability — exactly the single global
percentage this tool exists to replace.

This is issue #106's deliverable, not something the test layer can supply on
its own: writing feature declarations is a claim about what atlas's
capabilities *are*, and inventing that taxonomy inside a testing change would
produce a feature map nobody reviewed and a per-capability floor derived from
whatever it happened to measure on the day. Once `.atlas/features/` exists,
`dogfood_test.go` gains a per-capability assertion beside the repo-wide one;
the harness for it (`runAtlas` + `atlas audit --json`) is already in that file.

### 4. The README shows atlas's own feature matrix, regenerated on release — **not started**

The dogfood run prints the raw material — attributed share, per-feature audit
scores, SQL resolution — and nothing publishes it. Two things are missing: a
renderer (`atlas audit --json` → a Markdown table) and a release-time step to
regenerate it. Neither is a test, which is why neither is here; the blocker for
the *matrix* specifically is criterion 3, since a feature matrix with no
declared features is a table of one row.

### 5. Nightly mutation run over `packages/audit` and `packages/coverage` — **not started**

Nothing in this repo runs `gremlins` or `go-mutesting`, and adding it to
`ci.yml` would be wrong twice over: `ci.yml` runs on push and pull_request, and
the issue asks for a nightly schedule precisely because a mutation run is far
too slow for per-PR. It needs its own scheduled workflow. The property layer is
what makes it worth doing — mutation testing is only informative when the suite
has invariants to violate — so this is the natural next change, and it was not
attempted here because a mutation score is a number, and committing a number
nobody has measured is the failure this whole document is against.
