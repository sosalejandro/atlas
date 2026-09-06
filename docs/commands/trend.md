# atlas trend

`atlas trend` reads the per-commit measurement series that `atlas trend
record` writes, and answers the question a snapshot cannot: **is this
getting better or worse?**

With `--compare-to <ref-or-sha>` it reports the delta against that commit
and **exits non-zero** when the score fell by more than `--max-regression`.
That gate is the point of the command. It is adoptable on a legacy codebase
precisely because it never asks a team to agree on an absolute threshold
they would never hit — only that *this change did not make things worse*.

## The three ways a naive trend line lies

Everything odd-looking in the output below exists to avoid one of these.

**1. A denominator change is not a coverage change.** Deleting a thousand
untested lines raises the number without a single new test. Every point
therefore records the *surface* it was measured over (the count of symbols
linked to features in `feature_symbols`) alongside the score. When two
compared points differ by more than `--denominator-tolerance`, the report
says so in the `surface` line and in a warning; the delta is still shown,
because suppressing it would hide real news, but never on its own.

**2. Absent evidence is not a zero.** A commit nobody ran the suite on has
*no* score. It appears in the series as `-  no evidence`, serialises to
`null` in `--json`, and a comparison involving it returns `no_evidence`
rather than fabricating a regression. Failing a build because the baseline
was never measured punishes the wrong PR.

**3. A gate with no tolerance fails builds on noise.** `--max-regression`
defaults to `0.5` points. Float accumulation contributes ~1e-12 and is
irrelevant; what actually moves the number between two runs of the *same*
code is measurement jitter — a timing-sensitive test skipping a branch, a
flaky integration test excluded on a retry. 0.5 is the smallest round number
above that. It is a floor on sensitivity, not a licence to lose half a point
per PR: the **per-feature** comparison runs at the same tolerance, so one
capability falling 80 → 40 fails the gate no matter how flat the project
average stays. `--max-regression 0` gives an exact zero-tolerance gate.

## Usage

```
atlas trend [flags]
atlas trend record [flags]
```

## Flags — `atlas trend`

| Flag                            | Default              | Description                                                                                     |
| ------------------------------- | -------------------- | ----------------------------------------------------------------------------------------------- |
| `--feature <id>`                | (whole project)      | Read the series for one feature instead of the project headline.                                |
| `--since <duration>`            | (no bound)           | Only include points measured within this window, e.g. `720h`.                                   |
| `--limit <n>`                   | `0` (no cap)         | Cap the series to the N most **recent** points.                                                 |
| `--compare-to <ref-or-sha>`     | (off)                | Compare the newest recorded point against this commit. Exits non-zero on regression.            |
| `--max-regression <points>`     | `0.5`                | Score drop tolerated before `--compare-to` fails. `0` = exact gate.                             |
| `--denominator-tolerance <frac>`| `0.02`               | Fractional surface change before a comparison is flagged as not like-for-like.                  |
| `--json` *(global)*             | off                  | Emit the stable JSON envelope.                                                                  |
| `--db-path` *(global)*          | `.atlas/atlas.db`    | Override the SQLite state path.                                                                 |

`--since`, `--limit` and `--feature` shape what a **human reads**. They never
narrow the gate: `--compare-to` always resolves its baseline against the full
history and always evaluates the whole project plus *every* feature. A gate
that could be quietly narrowed by a display flag is worse than no gate.

`--compare-to` accepts, in order: an exact recorded commit sha; anything
`git rev-parse` resolves (so `--compare-to main` works) that has a recorded
point; or an unambiguous prefix of a recorded sha. An ambiguous prefix is an
error, never a guess.

## Flags — `atlas trend record`

| Flag                  | Default        | Description                                                                    |
| --------------------- | -------------- | ------------------------------------------------------------------------------ |
| `--commit <sha>`      | current `HEAD` | Commit to record the point under.                                              |
| `--note <text>`       | (empty)        | Free-form note stored with the point (a CI run id, a branch name).             |
| `--retain <duration>` | `0` (keep all) | After recording, delete points older than this window.                         |

Recording the same commit twice **replaces** its point rather than appending
a second one, so a CI retry leaves the series intact.

## Examples

### Record a point, then read the series

```
$ atlas cov sync --framework go-cover --input coverage.out
$ atlas trend record --note "ci-run-4821"
trend point recorded  id=4 commit=4444444444 score=63.10 surface=1002 features=3

$ atlas trend
trend  scope=project  points=4

COMMIT          MEASURED             SCORE   SURFACE         DELTA
111111111111    2026-09-02 04:17     64.20       980             -
222222222222    2026-09-03 04:17         -         0   no evidence
333333333333    2026-09-04 04:17     66.90       995         +2.70
444444444444    2026-09-05 04:17     63.10      1002         -3.80
```

The `222222…` row is a commit measured with no coverage ingested. It is a
gap, not a zero, and the `+2.70` on the next row is computed against the last
**measured** point.

### Gate a PR against its merge base

```
$ atlas trend --compare-to origin/main
...
compare 3333333333333333333333333333333333333333 -> 4444444444444444444444444444444444444444
  score    66.90    -> 63.10          -3.80  REGRESSED
  surface  995 -> 1002 (+0.7%)

features (worst first):
  search.index                             58.00    -> 38.00         -20.00  REGRESSED

gate: FAIL  (max regression 0.50 points)

trend: regression gate failed: score fell by more than 0.50 points against 3333...
$ echo $?
1
```

Only features that actually moved are listed — an unchanged feature in a
hundred-feature repo is noise that buries the one that regressed.

### Read one feature's series

```
$ atlas trend --feature search.index --since 720h
```

A commit whose breakdown has no row for the feature still appears, as a gap.
Dropping it would silently close the hole and make a feature that *stopped
being measured* look continuous.

### JSON

```
$ atlas trend --json --compare-to main
```

`result.series.points[]` carries `commit_sha`, `measured_at`, `score`
(**`null` when unmeasured**) and `denominator`. `result.comparison` carries
`project`, `features[]` (worst-delta first), `regressed`, `warnings[]`, and
the `max_regression` / `denominator_tolerance` the verdicts were reached
under, so a CI log explains itself.

Per-scope `verdict` is one of `improved`, `unchanged`, `regressed`,
`no_evidence`, `new`, `removed`. Only `regressed` fails the gate.

## Retention

`--retain` is a hard prune of points older than the window; the per-feature
breakdown goes with them. Atlas does **not** roll old points up into daily
aggregates — a rollup would have to pick a representative score per day, and
every choice (first / last / mean) makes the retained series disagree with
the raw one it replaced. Pruning is lossy in a way a reader can see; a rollup
is lossy in a way they cannot.

## Why `--compare-to` lives on `trend` and not on `audit`

Issue #92 sketched the gate as `atlas audit --compare-to`. It landed on
`atlas trend` instead, for one substantive reason and one practical one.

The substantive one: `audit` scores whatever the store holds *right now*,
blending coverage with pattern compliance, contract drift and annotation
freshness, re-normalising over whichever signals happen to be available. A
gate over that blend fires when a contract goes stale, which is a different
event from "this PR made coverage worse" and needs a different threshold. The
series deliberately records only the coverage-backed component (see *Where
the numbers come from*), and the flag belongs on the command that owns that
distinction.

The practical one: `atlas audit`'s flag surface was owned by a concurrent
change. If a future release wants `audit --compare-to` as an alias, it can
delegate to `runTrend` — the comparison logic is entirely in
`packages/trend`, which takes two `store.HistoryPoint`s and no CLI state.

## Relationship to `snapshots` / `atlas diff`

[`atlas diff`](./diff.md) compares two *structural* snapshots: which symbols,
edges and features changed. `atlas trend` compares *measurements* over many
commits. They answer different questions and share no tables.

## Where the numbers come from

`atlas trend record` scores every feature via the same path as
[`atlas audit`](./audit.md), then keeps only what rests on real coverage
evidence:

- A feature is **measured** only when the audit produced a `coverage`
  component for it. The audit deliberately re-normalises its weighted blend
  over whatever signals are available, so a feature with no coverage run
  still scores respectably on pattern compliance and contract freshness.
  Recording *that* on a coverage trend would make the line jump the day
  coverage first arrives, with no change to the code.
- The project score is the **denominator-weighted** mean of the measured
  features. An unweighted mean lets a one-symbol feature outvote a
  two-hundred-symbol one, so a repo could hold its headline steady while its
  bulk rotted.
- The per-feature denominator is the count of linked symbols in
  `feature_symbols` — *not* the audit's derived impl surface, which can
  change between releases of Atlas itself. A denominator that moved on a tool
  upgrade would flag every comparison across that upgrade as incomparable.

Storage: migration `0013`, tables `coverage_history` and
`coverage_history_features`. See [docs/schema-v1.md](../schema-v1.md) §5.14.
