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
| `TestProperty_Attribution_ChargesEachStatementOnce` | `coverage` | Double counting. Per-symbol totals sum to exactly the attributed figure. |
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

**`atlas scan --json` under-reports.** `features_materialized` and
`feature_symbols_linked` are always `0`, because `internal/cli/scan.go` builds
its result field by field from `store.IngestStats` and never copies those two.
`TestAcceptance_CLI_ScanOmitsTheFeatureCounts` characterises the defect and
fails the moment it is fixed, at which point the test should be deleted.

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

CI wiring is one step, placed **after** the existing `go test` step so the
suite is not run twice:

```yaml
- name: atlas gates atlas
  env:
    ATLAS_DOGFOOD_PROFILE: coverage.out   # the profile the test step wrote
    ATLAS_DOGFOOD_BASE: origin/main
  run: ./test/acceptance/run.sh
```

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

Measured on this worktree (Go 1.26.4, linux/amd64) with a profile from
`go test ./packages/... ./internal/... -coverprofile=… -coverpkg=./packages/...,./internal/...`:

| Metric | Observed | Committed floor |
| --- | --- | --- |
| statements charged to a symbol | 22,396 / 23,165 = 0.9668 | 0.95 |
| SQL operations resolved | 128 / 129 = 0.9922 | 0.97 |
| symbols indexed | 4,565 | 2,000 |
| edges recorded | 10,433 | 2,000 |

The attribution floor sits below the observation because the figure moves with
the code — the residual ~4% is dominated by `packages/store/sqlc`, which is
generated and excluded from the index by design, so a new generated file lowers
it without anything being wrong. The SQL floor sits close to the observation
because an unresolvable operation is a query atlas cannot advise on, and adding
one should be a deliberate act.

**Ratchet upward in a PR that says why. Never downward without one.** Re-measure
with `ATLAS_DOGFOOD_KEEP=1 ./test/acceptance/run.sh`, which logs every observed
value beside its floor.

## Cost

Measured on this worktree, `-count=1`, warm build cache:

| Layer | Wall clock |
| --- | --- |
| `-run TestProperty_` in `codeindex/go` | 0.15 s |
| `-run TestProperty_` in `coverage` | 0.18 s |
| `-run TestProperty_` in `graph` | 0.01 s |
| `-run TestProperty_` in `store` | 1.04 s (each case opens a SQLite file) |
| `test/acceptance` (fixture + CLI, includes one `go build`) | 0.65 s |
| `test/acceptance` with `-tags=dogfood` | 6.2 s, plus whatever produced the coverprofile |

Everything except the dogfood layer runs on every `go test ./...`. The dogfood
layer is gated separately only because its input is a coverprofile of this
repository, and producing one means running the suite that would be running it
— not because it is slow. Given CI's existing profile it costs six seconds.

## What is still missing

The acceptance criteria on issue #122 that this work does **not** close, so the
next reader is not misled about coverage of the coverage tool:

- **Evidence sources other than go-cover are not yet pinned to external ground
  truth.** `go-cover` is pinned to `go tool cover -func`. Istanbul has ingest
  tests but nothing that compares against `nyc report` or vitest's own summary;
  Gherkin binding (#117) and diagram verify (#111) likewise.
- **`.atlas/features/` does not declare atlas's own capabilities** (#106), so
  the dogfood gate cannot fail on a per-capability coverage regression — only
  on the repo-wide attribution floor. That is a weaker gate than the issue asks
  for, and it is the next thing to build here.
- **The README does not yet show atlas's own feature matrix.** The numbers the
  dogfood run prints are the raw material; nothing publishes them.
- **No mutation testing.** `gremlins` over `packages/audit` and
  `packages/coverage` on a nightly schedule remains the right next move, and
  the property layer is what makes it affordable: mutation testing is only
  informative when the suite has invariants to violate.
