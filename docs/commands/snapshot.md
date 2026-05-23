# atlas snapshot

`atlas snapshot` runs a fresh scan, optionally an audit pass, and persists
the combined view as one row in the `snapshots` table. That row is the
input to [`atlas diff`](./diff.md) — capture a snapshot on `main` as your
CI baseline, then diff PR heads against it to detect regressions.

The git ref defaults to `HEAD` (resolved via `git rev-parse HEAD`); pass
`--ref` to override. `--note` is optional free-form metadata.

With `--include-audit` (the default) the snapshot also carries the
audit-score slice in its `audit_json` column so later diffs can detect
score regressions.

## Usage

```
atlas snapshot [flags]
```

## Flags

| Flag                          | Default               | Description                                                                                            |
| ----------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------ |
| `--ref`                       | current HEAD          | Git ref to record on the snapshot row. Useful when CI knows the canonical ref but `HEAD` is a merge.   |
| `--note`                      | (empty)               | Free-form note recorded alongside the snapshot. Surfaces in `atlas diff` headers.                      |
| `--include-audit`             | `true`                | Compute + persist the audit slice alongside the index.                                                 |
| `--root`                      | repo root / cwd       | Project root for the fresh scan.                                                                       |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                                  |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                        |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                          |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                         |

## Examples

### Capture a snapshot

```
# Run from: /tmp/atlas-fixture
$ atlas snapshot --note "fixture v1"
snapshot captured  id=1 git_ref=df106e98ebce8be115380459375cc247644cf572  note=fixture v1
```

The header line reports the new row's primary key, the resolved git ref
(40-char SHA from `git rev-parse HEAD`), and the note verbatim. Snapshot
ids are monotonically increasing per-database — successive snapshots get
`id=2`, `id=3`, ...

### Capture another snapshot, then diff

```
# Run from: /tmp/atlas-fixture
$ atlas snapshot --note "fixture v2"
snapshot captured  id=2 git_ref=df106e98ebce8be115380459375cc247644cf572  note=fixture v2

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

Both snapshots captured at the same HEAD with no intervening code change,
so the diff is empty.

### Skip the audit slice

The audit slice adds a few hundred milliseconds on large repos. When the
caller knows it doesn't need score regressions detected, opt out:

```
# Run from: any repo
$ atlas snapshot --include-audit=false --note "structural-only"
snapshot captured  id=3 git_ref=a1b2c3d  note=structural-only
```

A snapshot without the audit slice can still be diff'd — `atlas diff` will
report `audit: +0 / -0 / ~0 (missing_on_a=N missing_on_b=M)` where the
`missing_on_*` counters surface the absence rather than a regression.

### CI usage

The typical CI shape:

```bash
# Run from: repo root, on the `main` branch in scheduled CI
$ atlas snapshot --ref main --note "nightly-baseline"

# Run from: repo root, on a PR head
$ atlas snapshot --ref HEAD --note "pr-${PR_NUMBER}"
$ atlas diff main HEAD   # baseline by git-ref lookup
```

`atlas diff` accepts the git ref directly — no need to capture the
snapshot id from the snapshot step's stdout.

## How it works

1. Run a fresh code-index scan at `--root` (`init`-equivalent walk, plus
   schema migrations if the DB needs them).
2. If `--include-audit`, evaluate every registered audit component and
   collect the per-feature scores.
3. Serialise the resulting slices — features, symbols, edges, annotations,
   contracts, pattern matches, audit (if requested), coverage — into the
   `snapshots` table as JSON payloads.
4. Stamp the row with `git_ref` (`--ref` or `git rev-parse HEAD`),
   `note` (`--note`), and `created_at`.

A snapshot is a *frozen* view: subsequent `atlas scan` / `atlas cov sync`
calls do not alter past snapshot rows. That immutability is what makes
the diff a stable regression boundary.
