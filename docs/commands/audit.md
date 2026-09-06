# atlas audit

`atlas audit` computes the per-feature health score from the SQLite store and
prints the results ordered worst-first. Each feature's score is a weighted
roll-up across the audit signals implemented in
[`packages/audit/`](../../packages/audit/) — statement coverage, decision
coverage, annotation freshness, pattern compliance, contract drift.

Without `--feature`, every feature in the store is scored. With `--feature`,
only that single feature is returned (or an error if it isn't in the store).

Every signal reports whether it is AVAILABLE, and the weighted average
re-normalises over the ones that are. That is not a detail: it is what lets a
feature with no contracts be scored fairly on coverage alone instead of being
dragged toward zero by a signal that has nothing to say about it. It is also
why a signal with no data must report *unavailable* rather than *0* — see
[Decision coverage](#decision-coverage-a-separate-signal-not-a-blend).

## Usage

```
atlas audit [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                            |
| ----------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------ |
| `--feature`                   | (all)                 | Score only this feature id. Errors if the id isn't in the store.                                       |
| `--worst`                     | `0` (no cap)          | Cap output to the worst-scoring N features. `--worst 10` is the standard "what needs attention" call. |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                  |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                        |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                          |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                         |

## Examples

### Full audit (worst first)

```
# Run from: /tmp/atlas-fixture
$ atlas audit
billing.subscribe                                   score=  0.00
    - no audit signals available (no coverage, no aggregate, no contract, no annotation source)
auth.login                                          score=100.00
    annotation_freshness   100.00
```

Two features in the fixture; `billing.subscribe` has no annotation on a
function (only on a `BillingHandler` class) so the audit component set is
empty, and the catch-all "no audit signals available" message tells the
operator the feature exists but has nothing to score.

### Cap to the worst N

```
# Run from: /tmp/atlas-fixture
$ atlas audit --worst 2
billing.subscribe                                   score=  0.00
    - no audit signals available (no coverage, no aggregate, no contract, no annotation source)
auth.login                                          score=100.00
    annotation_freshness   100.00
```

Same shape, capped at 2 rows. On a real-world codebase with hundreds of
features, `atlas audit --worst 10` is the daily-driver flag.

### Single feature

```
# Run from: /tmp/atlas-fixture
$ atlas audit --feature auth.login
auth.login                                          score=100.00
    annotation_freshness   100.00
```

### JSON envelope

```
# Run from: /tmp/atlas-fixture
$ atlas audit --feature auth.login --json
{
  "schema_version": "v1",
  "command": "audit",
  "args": {"feature": "auth.login", "worst": 0},
  "result": {
    "features": [
      {
        "feature_id": "auth.login",
        "score": 100,
        "components": {"annotation_freshness": 100},
        "sampled_at": "2026-05-23T03:46:19.357657363Z"
      }
    ]
  },
  "generated_at": "2026-05-23T03:46:19Z"
}
```

`result.features` is always an array, even with `--feature` set, so a
caller can normalise the response shape across single-feature and full
runs.

## How it works

1. Read every row from the `features` view.
2. Resolve the feature's **implementation surface** — the symbols its score is
   computed over. In descending order of evidential strength: the symbols the
   feature's own tests actually executed (`dynamic`), a call-edge walk from its
   annotated symbols (`static`), the production symbols co-located with its
   test package (`package-anchor`), or the annotated symbols themselves
   (`direct-links`). Which one was used is reported as `surface_source`.
3. Evaluate each signal over that surface. Every signal independently reports
   whether it is available:
   - `coverage` — statement coverage from the current
     [`atlas cov`](./cov.md) frontier. Line-weighted (executed statements over
     total statements) when the run carries statement counts; otherwise the
     fraction of surface symbols with a passing result.
   - `decision_coverage` — branch outcomes taken over branch outcomes a
     profile could judge, from [`atlas flow`](./flow.md). See below.
   - `annotation_freshness` — how many `@atlas:feature` / `@atlas:contract`
     sites were last touched inside the freshness window (git blame).
   - `pattern_compliance` — how many linked `@atlas:aggregate-service`
     declarations match the canonical-service pattern.
   - `contract_drift` — how many referenced contracts were updated inside the
     drift window.
4. Blend the available signals as a weighted average, re-normalised over the
   available subset.
5. Sort ascending by score (worst first), cap to `--worst N` when set.

### Weights

| Signal                  | Weight | Notes                                                                                                                                             |
| ----------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `coverage`              | 0.40   | Shared with `decision_coverage` when that signal is available — see below.                                                                        |
| `pattern_compliance`    | 0.25   |                                                                                                                                                   |
| `contract_drift`        | 0.20   |                                                                                                                                                   |
| `annotation_freshness`  | 0.15   | Unavailable when no git blame source is wired.                                                                                                    |
| `annotation_presence`   | 0.10   | A *floor*, not a blended signal: it applies only when nothing else is available, so an annotated-but-unverified feature scores 10 rather than 0.   |

When a feature has no signal at all — no coverage, no aggregate, no contract,
no annotation source — atlas emits the "no audit signals available" line
instead of a numerical zero, so the operator can tell *unscoreable* apart from
*poorly scored*.

## Decision coverage: a separate signal, not a blend

`atlas cov` answers *did the line run*. [`atlas flow`](./flow.md) answers *was
the branch taken both ways*. A function with an untested error path can show
90% statement coverage while every failure mode in it is unexercised, so the
audit carries both numbers and never folds one into the other. `--json` reports
them as two components with two availability flags; a single composite would
satisfy the letter of "reports both" and destroy the reason for it.

Decision coverage appears once you have run `atlas flow build --profile
<coverprofile>`. Until then it is absent, and absent means absent.

### Availability, never zero

There are four distinct states, and the audit keeps them apart because an
operator acts differently on each:

| State                                                                       | `components.decision_coverage` | `decision_coverage` object | Effect on the score                                                         |
| --------------------------------------------------------------------------- | ------------------------------ | -------------------------- | --------------------------------------------------------------------------- |
| No symbol on the surface was ever analysed                                  | absent                         | absent                     | **none** — the feature scores exactly what it scored before `atlas flow`    |
| Analysed; some symbols measured, some not                                   | present                        | `symbols_unmeasured > 0`   | scored over the measured symbols only                                       |
| Analysed; nothing judgeable (every outcome is a short-circuit operand)      | absent                         | `available: false`         | **none** — but the reading is still reported                                |
| Analysed and judgeable                                                      | present                        | `available: true`          | blended                                                                     |

Reporting 0 for an unmeasured feature would mean that adopting `atlas flow`
drops the score of every feature it has not yet measured, which is how a signal
gets switched off in its first week. So the rule is availability, not zero.

The same rule applies one level down, to the outcomes themselves.
`outcomes_undetermined` — outcomes that exist in the source and that no
statement-coverage profile can judge — move **neither** the numerator nor the
denominator. Decision coverage is `taken / decidable`, never `taken / total`.

### Why the two coverage signals share one weight

Statement and decision coverage answer the same question at two resolutions,
and atlas derives them from the same artefact: `atlas flow` decides a branch
outcome by asking whether the statements on either side of it ran, using the
counters `atlas cov` already ingested. They are not two independent witnesses.
So when both are available they **split** the 0.40 coverage budget rather than
each drawing their own — otherwise one measurement, counted twice, would take
roughly 57% of a score that also has to carry pattern compliance and contract
drift.

The split favours decision coverage, 0.6 to 0.4 — **0.24 and 0.16** of the
total — because decision coverage subsumes the statement verdict over the
branches it can judge: an outcome cannot be taken if the statements behind it
never ran, while a statement can run with its branch only ever taken one way.
It stops at 1.5:1 rather than going further because decision coverage is judged
only over the *decidable* outcomes, and everything outside that — short-circuit
operands, symbols with no CFG row — is exactly what the statement half still
sees.

When `atlas flow` has measured a feature that `atlas cov` has not, decision
coverage holds the whole 0.40 budget: it is the only reading of the question,
and leaving the budget unspent would let freshness and drift decide a
coverage-shaped score.

The split is tunable — `audit.Options.DecisionCoverageShare`, default 0.6.
Values outside `(0, 1)` fall back to the default, because 0 would silence the
new signal through the weights instead of through availability, and 1 would
silence the old one.

## Worked example: before and after `atlas flow`

The fixture is a four-package Go module. `auth.Login` has two `if`s and a test
that only walks the happy path; `billing.Charge` guards on `amount > 0 && live`;
`gate.Allow` is a bare `return a && b`; `report.Summarise` is outside the test
run entirely. The coverprofile comes from `go test -covermode=count` and is
ingested with `atlas cov sync --framework go-cover`.

Statement coverage alone:

```
$ atlas audit
report.summary                                      score=  0.00
    coverage                 0.00
    - coverage: 0/1 symbols passing in latest run
auth.login                                          score= 37.50
    coverage                37.50
    - coverage: 3/8 statements executed (38%)
billing.charge                                      score= 66.67
    coverage                66.67
    - coverage: 2/3 statements executed (67%)
gate.allow                                          score=100.00
    coverage               100.00
```

Then the branch verdicts land:

```
$ atlas flow build --profile cover.out
flow build: 8 symbol(s) across 7 file(s)
  most complex: auth.Login (cyclomatic 3)
  conditions: 9 (9 independently exercisable in principle)
  decision coverage: 37.5% (3 of 8 decidable outcomes taken; 4 outcome(s) UNDETERMINED)
  findings: flow.untested-branch x5
  [the MC/DC and "not statement coverage" notes flow always prints are elided here]

$ atlas audit
report.summary                                      score=  0.00
    coverage                 0.00
    - coverage: 0/1 symbols passing in latest run
auth.login                                          score= 35.00
    coverage                37.50
    decision_coverage       33.33
    decision: 2/6 decidable outcomes taken, 0 undetermined, 2/2 symbols measured
    - decision coverage: 2/6 decidable branch outcomes taken (33%)
    - coverage: 3/8 statements executed (38%)
billing.charge                                      score= 56.67
    coverage                66.67
    decision_coverage       50.00
    decision: 1/2 decidable outcomes taken, 2 undetermined, 1/1 symbols measured
    - decision coverage: 1/2 decidable branch outcomes taken (50%); 2 undetermined (not counted either way)
    - coverage: 2/3 statements executed (67%)
gate.allow                                          score=100.00
    coverage               100.00
    decision: not scored - no decidable branch outcome (2 undetermined, 1/1 symbols measured)
```

Read the four rows:

- **`report.summary` did not move.** Nothing on its surface was analysed, so
  the signal is unavailable and the score is what it was. This is the property
  that makes the signal adoptable at all.
- **`auth.login` fell 37.50 → 35.00.** Its tests execute 3 of 8 statements but
  take only 2 of 6 branch outcomes: each error return is a line that never ran
  *and* a branch never taken. `0.16×37.50 + 0.24×33.33`, over a 0.40 budget.
- **`billing.charge` fell 66.67 → 56.67**, and its reason names the two
  outcomes nothing could judge. `amount > 0 && live` has four outcomes; the two
  belonging to the operands share one profile counter, so they leave the ratio
  entirely instead of being scored as gaps. Decision coverage is 1/2, not 1/4.
- **`gate.allow` did not move either**, at 100.00. It *was* analysed — the
  `decision:` line says so — and every outcome it has is undetermined. That is
  reported, and not scored. A build patched to score the unjudgeable case as 0%
  instead of excluding it puts this same feature at `score= 40.00`.

### The same two features in JSON

`billing.charge`, where the signal is available:

```
$ atlas audit --feature billing.charge --json
{
  "schema_version": "v1",
  "command": "audit",
  "args": {"feature": "billing.charge", "worst": 0},
  "result": {
    "features": [
      {
        "feature_id": "billing.charge",
        "score": 56.66666666666668,
        "components": {
          "coverage": 66.66666666666667,
          "decision_coverage": 50
        },
        "reasons": [
          "decision coverage: 1/2 decidable branch outcomes taken (50%); 2 undetermined (not counted either way)",
          "coverage: 2/3 statements executed (67%)"
        ],
        "sampled_at": "2026-09-06T19:17:48.718579783Z",
        "surface_source": "static",
        "decision_coverage": {
          "available": true,
          "percent": 50,
          "outcomes_taken": 1,
          "outcomes_decidable": 2,
          "outcomes_undetermined": 2,
          "outcomes_total": 4,
          "symbols_measured": 1,
          "symbols_unmeasured": 0
        }
      }
    ]
  },
  "generated_at": "2026-09-06T19:17:48Z"
}
```

`gate.allow`, where it is not. `components` carries no `decision_coverage` key
at all, while the object still reports what happened:

```
$ atlas audit --feature gate.allow --json
{
  "schema_version": "v1",
  "command": "audit",
  "args": {"feature": "gate.allow", "worst": 0},
  "result": {
    "features": [
      {
        "feature_id": "gate.allow",
        "score": 100,
        "components": {"coverage": 100},
        "sampled_at": "2026-09-06T19:17:48.710254187Z",
        "surface_source": "static",
        "decision_coverage": {
          "available": false,
          "percent": 0,
          "outcomes_taken": 0,
          "outcomes_decidable": 0,
          "outcomes_undetermined": 2,
          "outcomes_total": 2,
          "symbols_measured": 1,
          "symbols_unmeasured": 0
        }
      }
    ]
  },
  "generated_at": "2026-09-06T19:17:48Z"
}
```

`percent` is 0 and meaningless when `available` is false. Read the flag, not
the number — that is what the flag is for.
