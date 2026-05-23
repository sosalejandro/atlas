# atlas diff

`atlas diff` loads two snapshot rows from the store and emits the structured
delta produced by [`packages/diff/`](../../packages/diff/): added / removed /
changed counts across features, symbols, edges, annotations, contracts,
pattern matches, audit scores, and coverage runs.

It's the regression detector for CI — capture a baseline snapshot on `main`
with [`atlas snapshot`](./snapshot.md), then diff the PR head against it to
surface "what changed shape-wise".

## Usage

```
atlas diff <ref-a> <ref-b> [flags]
```

Each `<ref-a>` / `<ref-b>` argument can be either:

- An integer snapshot id (e.g. `12`) — looked up by primary key.
- A git ref string (e.g. `main`, `f3a2b1c`) — the latest snapshot row with
  that `git_ref` wins.

Use `atlas snapshot --ref <ref>` to capture a snapshot before running diff
if your CI hasn't already.

## Flags

| Flag                          | Default               | Description                                                                                           |
| ----------------------------- | --------------------- | ----------------------------------------------------------------------------------------------------- |
| `--audit-noise-floor`         | `5`                   | Audit score delta below which `Changed` entries are suppressed. Filters out one-point score wiggle.   |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                 |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                       |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                         |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                        |

## Examples

### Diff two snapshot ids

```
# Run from: /tmp/atlas-fixture (after `atlas snapshot` ×2 with no code change)
$ atlas diff 1 2
diff df106e98ebce8be115380459375cc247644cf572 -> df106e98ebce8be115380459375cc247644cf572
  features:    +0 / -0 / ~0
  symbols:     +0 / -0 / ~0
  edges:       +0 / -0
  annotations: +0 / -0 / ~0
  contracts:   +0 / -0 / ~0
  patterns:    +0 / -0
  audit:       +0 / -0 / ~0  (missing_on_a=0 missing_on_b=0)
  coverage:    +0 / -0 / ~0
  (no differences)
```

The header line carries the git refs from each snapshot row (identical
here because both snapshots were captured against the same HEAD). The
counts read as `+added / -removed / ~changed` — for `edges` and
`patterns`, atlas tracks only adds and removes (no notion of a "changed"
edge). `(no differences)` is printed when every slice is zero.

### Diff two git refs

```
# Hypothetical: CI baseline snapshot on `main`, current HEAD on a feature branch
$ atlas diff main HEAD
diff a1b2c3d4 -> e5f6a7b8
  features:    +2 / -0 / ~1
  symbols:     +7 / -3 / ~12
  edges:       +18 / -4
  annotations: +5 / -1 / ~0
  contracts:   +2 / -0 / ~3
  patterns:    +0 / -0
  audit:       +2 / -0 / ~6  (missing_on_a=0 missing_on_b=0)
  coverage:    +0 / -0 / ~14
```

Reading top-down: two new features were declared, three symbols were
removed (deleted methods), twelve symbols changed in shape (e.g. method
signature). The `audit` line says two features are new in the audit
slice; six features had their score change by more than
`--audit-noise-floor` (default 5).

### Suppress small audit deltas

When CI noise is high, raise the floor:

```
# Run from: any repo with snapshot rows in atlas.db
$ atlas diff main HEAD --audit-noise-floor 15
```

This hides any feature whose audit score moved by < 15 points between the
two snapshots, useful when a flaky test run wobbles `coverage_pass_rate`.

## How it works

1. Resolve `<ref-a>` and `<ref-b>` to snapshot rows in the `snapshots`
   table — by id if the arg parses as an int, by git ref otherwise.
2. Hydrate each snapshot's serialised slices (features, symbols, edges,
   annotations, contracts, pattern matches, audit scores, coverage runs).
3. Pairwise-diff the slices per the `packages/diff/` algorithms:
   - Features / symbols / annotations / contracts / coverage: keyed by
     stable id; produces `added` (only in B), `removed` (only in A),
     `changed` (in both but with a different shape).
   - Edges / pattern matches: keyed by (from, to, kind) tuples; tracks
     only adds and removes.
   - Audit: per-feature score delta filtered through `--audit-noise-floor`.
4. Render the summary table (default) or emit the full delta as JSON.

For the full delta payload — with per-row added/removed/changed lists —
use `--json`. The text output is intentionally summary-only so it fits in
a CI log header.
