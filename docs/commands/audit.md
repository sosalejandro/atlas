# atlas audit

`atlas audit` computes the per-feature health score from the SQLite store and
prints the results ordered worst-first. Each feature's score is a weighted
roll-up across the audit components implemented in
[`packages/audit/`](../../packages/audit/) — coverage signals, annotation
freshness, aggregate linkage, contract presence, and so on.

Without `--feature`, every feature in the store is scored. With `--feature`,
only that single feature is returned (or an error if it isn't in the store).

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
2. For each feature, evaluate every registered audit component
   (`packages/audit/components/*`):
   - `annotation_freshness` — when was the most recent `@atlas:feature`
     site last touched?
   - `coverage_pass_rate` — pass/fail/skip ratio across recent
     [`atlas cov sync`](./cov.md) runs.
   - `aggregate_linkage` — does the feature have a canonical
     `@atlas:aggregate-service` declaration?
   - `contract_presence` — at least one `@atlas:contract` annotation?
3. Combine components into a 0–100 score (per-component weights live in
   the audit package).
4. Sort ascending by score (worst first), cap to `--worst N` when set.

When the component set for a feature is empty — no coverage signal, no
aggregate, no contract, no annotation source — atlas emits the
"no audit signals available" line instead of a numerical zero so the
operator can tell *unscoreable* apart from *poorly scored*.
