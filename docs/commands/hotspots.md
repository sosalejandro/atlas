# atlas hotspots

`atlas hotspots` ranks the backlog by **change frequency x health gap**
instead of by the gap alone.

`atlas health` and `atlas sprint` both score a feature on how bad its
numbers are. That puts a gap in code nobody has touched in two years next
to a gap in the file three people edited last week, at the same rank. Only
the second is worth a sprint. The multiplier that separates them is how
often the code actually changes, and it is free: it is already in git.

The idea is not new — CodeScene's *hotspots* are change frequency x
complexity, and Code Climate and SonarQube both surface churn-weighted
views — but the useful part is that atlas already knows the gap, so the
only missing half is the history.

Churn mining lives in [`packages/churn/`](../../packages/churn/); the
ranking lives in [`packages/sprintplan/`](../../packages/sprintplan/).

## Usage

```
atlas hotspots [flags]
```

## Flags

| Flag                            | Default            | Description                                                                                 |
| ------------------------------- | ------------------ | ------------------------------------------------------------------------------------------- |
| `--top`                         | `0` (all)          | Cap output to the top-N hotspots.                                                            |
| `--window-days`                 | `365`              | How far back to mine commit history. Must be >= 1; `0` is rejected, not defaulted.            |
| `--half-life-days`              | `90`               | Age at which a commit counts half as much (recency decay). Must be >= 1.                      |
| `--max-files-per-commit`        | `50`               | Drop commits touching more files than this. Negative disables the rule; `0` is rejected.      |
| `--exclude-message`             | *(see below)*      | Extra regexp matched against commit subjects; matching commits are not churn. Repeatable.    |
| `--no-default-exclusions`       | off                | Do not apply the built-in chore/style/formatter subject exclusions.                          |
| `--no-author-diversity`         | off                | Score on commit frequency alone, ignoring how many people touch the file.                    |
| `--config` *(global)*           | `.atlas.yaml`      | Explicit config path.                                                                        |
| `--db-path` *(global)*          | `.atlas/atlas.db`  | Override the SQLite state path.                                                              |
| `--json` *(global)*             | off                | Emit the stable JSON envelope instead of human-friendly text.                                |

## Example

> **Illustrative, not recorded.** The transcript below is *constructed* to
> show the shape of the output and the argument the ranking makes.
> `billing.refunds`, `auth.session` and `legacy.importer` are invented
> feature ids and the file paths under them do not exist in this
> repository — do not read the numbers as a measurement of anything. The
> same applies to the JSON and `atlas sprint` samples further down. Run
> the command against your own repository for real numbers.

```
$ atlas hotspots --top 3
hotspots: churn x gap over 365d of history, half-life 90d (141 commits counted, 2 skipped as bulk, 22 by subject)
 1. billing.refunds                                hotspot= 41.23  gap= 68.00  churn= 60.63  cost=M
    churn: 12 commits by 3 author(s), last 2026-05-28, hottest packages/billing/refund.go
    gap:   coverage: 4/17 impl symbols executed in the latest run
 2. auth.session                                   hotspot= 17.53  gap=100.00  churn= 17.53  cost=S
    churn: 3 commits by 1 author(s), last 2026-06-01, hottest packages/auth/session.go
    gap:   annotation freshness: 0/1 sites within 720h0m0s
 3. legacy.importer                                hotspot=  0.00  gap= 94.00  churn=  0.00  cost=L
    churn: 0 commits in the window - this code is not moving
    gap:   no audit signals available
```

The invented `legacy.importer` row is there because it is the case the
whole command exists for. A feature like it sits near the top of `atlas
sprint`: a 94-point gap on a large surface. Under `hotspots` it falls to
the bottom, because nothing about it has changed in a year. That is the
correct answer — it is not where this quarter's risk lives.

Every row prints all three numbers. `hotspot` is the product; `gap` and
`churn` are the factors, and the line beneath each names the file the
churn came from. A composite score nobody can decompose is a score nobody
trusts, so the decomposition is not optional output.

## What counts as churn, and what does not

A naive commit count ranks noise at the top. Four rules keep it honest.

### File moves are not changes

Renames are detected with `git log --name-status -M` (not `--follow`,
which costs one subprocess per file and does not survive a large
repository). A pure move — git reports `R100`, meaning byte-identical
content — contributes **no** churn, but the rename is remembered, so edits
made before the move still count toward the file's current path. A rename
that also edited the file (`R085` and friends) is real churn and is kept.

### Sweeps are not changes

A commit touching more than `--max-files-per-commit` files is dropped. A
reformat, a licence-header sweep, a codegen refresh and a dependency bump
all add the same `+1` to every file in the repository, which is exactly
zero discrimination in a ranking whose only job is to discriminate.

The default of 50 sits above any hand-written change — a refactor really
spanning fifty files is genuinely fifty files' worth of risk — and below
every sweep, which touch hundreds.

Commit subjects are filtered too. The defaults are deliberately narrow:

```
(?i)^\s*(chore|build|style|revert)(\([^)]*\))?!?:
(?i)\b(gofmt|goimports|prettier|clang-format|re-?format|licen[cs]e header)\b
```

The first names the conventional-commit types that are *defined* as not a
behaviour change. The second names formatter sweeps that arrive under any
subject at all. `--exclude-message` **adds** to this set (it does not
replace it); `--no-default-exclusions` is the explicit way to drop it. A
filter that eats real work is worse than no filter, because the ranking
then omits the very files the sweep was hiding.

The header line reports how many commits each rule removed, so you can see
whether a filter is doing what you expected.

### Recency outweighs volume

Twenty commits last month means more than twenty commits three years ago,
so each commit contributes `0.5 ^ (age / half-life)` rather than `1`.

The default half-life is 90 days — one quarter, the unit teams plan in, so
"counts half as much as this quarter's work" is a claim you can check
against your own calendar. It also produces the curve you want: last
month's commits stay near full weight, last year's land near 1/16, and
neither is erased. The decay is exponential rather than linear because a
linear ramp has a cliff at the window edge, where widening the window by a
day would reorder the backlog.

Author diversity adjusts the result by up to 25%: shared, contended code
is riskier code. It is a multiplier rather than a term because "three
people touched it" is a weaker claim than "it changed last week". Turn it
off with `--no-author-diversity`.

### Generated files are not measured at all

Generated code churns constantly and means nothing. atlas does **not**
re-classify it here: the scanner already made that determination (issue
#96 — a `// Code generated ... DO NOT EDIT.` header, a `scan.generated`
glob, or a `generated/` path segment), and a second copy of those rules
would drift from the first.

Instead, churn is rolled up only over the file paths atlas has **indexed**.
A generated file is absent from the symbol table precisely because the
scanner declined it, so its churn can never reach a score.

Test-role files are dropped from the roll-up for the same kind of reason: a
feature whose tests churn weekly while its implementation is frozen is a
test being stabilised, not a hotspot. A feature with *only* test links
falls back to those files, because otherwise there would be nothing to
measure.

## Unknown churn is not zero churn

A file git has no history for is **unknown**, not quiet. There are two
common cases and both matter:

- **A brand-new file**, not yet tracked. Scoring it zero would delete the
  newest code in the repository from the backlog — often exactly the code
  that needs attention.
- **A shallow clone.** `actions/checkout` clones with `fetch-depth: 1` by
  default, so this is the *normal* CI checkout. An unguarded implementation
  reports every file as barely-changed, and every hotspot ranking taken in
  CI is silently wrong.

Both are reported as `status: "unknown"` and scored a neutral **50** —
the midpoint, because zero would drop them and 100 would put them all at
the top. Shallow clones additionally emit a warning on **stdout**,
alongside the ranking — every warning `atlas` prints goes to stdout, so
one command's warnings are not on a different stream from the next's —
and in the JSON envelope's `warnings` array:

```
WARN: shallow clone: git history is truncated, so every churn score is a
lower bound and all rankings are reported as unknown. Re-run with a full
clone (actions/checkout fetch-depth: 0) for a usable hotspot ranking.
```

A file that git *does* track but that has no qualifying commit in the
window scores a real **0**. That is the "bad and dead" case, and pushing it
down is the point.

## Two path namespaces, reconciled rather than assumed

The roll-up joins two lists of file paths that are not relative to the
same directory by construction:

- **Churn paths** come from `git`, so they are relative to the repository
  top level.
- **Index paths** come from the symbol table, so they are relative to the
  root `atlas scan` was pointed at — which `atlas scan --root <subdir>`
  makes a different directory.

Joining those by string equality when they differ misses *every* file, and
the failure does not look like a failure: it looks like a repository where
nothing has ever changed. So the two are reconciled explicitly.

- If any indexed path is tracked at the top level, the namespaces already
  agree and nothing is done.
- Otherwise, if exactly one sub-directory maps the indexed paths onto
  tracked files, the churn report is **rebased** onto it and a warning
  says so. Files outside that sub-directory are then not part of the
  ranking.
- If two sub-directories fit equally well, or none does, the roll-up is
  reported as **not computable** — a warning naming how many indexed paths
  git could not place, and every churn factor left UNKNOWN. Reporting
  "cannot determine" is always available and is always better than a
  confident wrong ranking.

Non-ASCII paths join too: `git log` renders them C-quoted
(`"caf\303\251.go"`) while `git ls-files -z` renders them raw, so mining
turns quoting off and decodes whatever git quotes anyway.

## Zero is not "unset"

The mining flags reject `0`. `--window-days 0` cannot mean "mine no
history" *and* be distinguished from "the flag was not passed", and the
header line above exists precisely so the score can be interpreted against
the window it was taken under. Rather than silently substitute the default
and then report it, the command fails:

```
$ atlas hotspots --window-days 0
Error: hotspots: --window-days must be at least 1 (got 0): a zero-length history window has no churn to mine
```

`--max-files-per-commit` is the same: pass a positive limit, or a
*negative* value to disable the bulk-commit rule entirely.

## JSON

```
$ atlas hotspots --json --top 1
{
  "schema_version": "v1",
  "command": "hotspots",
  "args": { "top": 1, "window_days": 365, "half_life_days": 90 },
  "result": {
    "items": [
      {
        "feature_id": "billing.refunds",
        "score": 41.23,
        "health": 32,
        "gap": 68,
        "churn": {
          "score": 60.63,
          "status": "known",
          "hot_file": "packages/billing/refund.go",
          "commits": 12,
          "authors": 3,
          "last_commit": "2026-05-28T09:14:02Z",
          "files_known": 4,
          "files_unknown": 0
        },
        "cost": "M",
        "reasons": ["coverage: 4/17 impl symbols executed in the latest run"]
      }
    ],
    "churn": {
      "window_days": 365,
      "half_life_days": 90,
      "max_files_per_commit": 50,
      "shallow": false,
      "commits_scanned": 141,
      "commits_skipped_bulk": 2,
      "commits_skipped_message": 22,
      "files_with_churn": 213,
      "unknown_score": 50
    }
  },
  "generated_at": "2026-06-01T12:00:00Z"
}
```

`result.churn` describes the mining pass itself. A churn score is not
interpretable without the window and half-life it was taken under, and the
skip counters are how you find out that a filter removed more than you
meant it to.

## The same ranking inside `atlas sprint`

`atlas sprint --rank churn` applies the identical weighting to the sprint
backlog, mined with the identical flags: `--window-days`,
`--half-life-days`, `--max-files-per-commit`, `--exclude-message`,
`--no-default-exclusions` and `--no-author-diversity` all mean the same
thing under `sprint` as they do here, because both verbs share one
mining path. Passing one without `--rank churn` is an error rather than a
silent no-op — a flag that changes nothing is worse than a missing flag,
because you believe it did something.

The same illustrative caveat as above applies to this transcript:

```
$ atlas sprint --rank churn --top 2
 1. billing.refunds                                     priority= 60.00 cost=M churn= 60.63 weighted= 36.38
    - Score 32 (low), 17 linked symbols, cost=M
    - churn 61: 12 commits by 3 author(s), hottest packages/billing/refund.go
 2. auth.session                                        priority= 60.00 cost=S churn= 17.53 weighted= 10.52
    - Score 0 (critical), 1 linked symbols, cost=S
    - churn 18: 3 commits by 1 author(s), hottest packages/auth/session.go
```

`--rank gap` is the default and stays the default. The flag changes the
**order**, not the meaning of `priority`: that number is the same
gap-weighted score it always was, and `weighted_priority` (the one the
list is sorted by) is emitted next to it. A ranking model should not change
under a team without them asking for it.

The difference between the two commands is which signals participate.
`sprint` folds in bug signal and annotation recency alongside the gap;
`hotspots` is gap x churn and nothing else, which is the cleaner view when
you want to argue about where the risk is rather than what to schedule.

## How it works

1. Mine the history window in three subprocesses, whatever the repository
   size: one to ask whether history is truncated, one for the tracked-file
   set, one for `git log --no-merges -M --name-status`.
2. Drop bulk commits, subject-excluded commits, and pure moves; fold
   renames onto each file's current path.
3. Score each file: decayed commit volume through a saturating curve
   (`w / (w + 5)`, so the interesting part of the range is 2–8 recent
   commits rather than 80 vs 200), times the author-diversity factor.
4. Reconcile the churn paths with the indexed ones — rebase onto the scan
   root when one unambiguously maps them, or report the roll-up as not
   computable when none does.
5. Roll up per feature by taking the **hottest** file. Averaging would let
   frozen helpers dilute the one module being rewritten weekly; summing
   would make a feature hot merely for being large. The max also keeps the
   result explainable — `hot_file` names where the number came from.
6. Multiply by the audit gap (`100 - health`) and sort, breaking ties by
   feature id so repeated runs over unchanged data are byte-identical.

## See also

- [`atlas sprint`](./sprint.md) — the gap-weighted backlog this reranks.
- [`atlas health`](./health.md) — where the health score, and so the gap,
  comes from.
