# atlas cov diff

```
atlas cov diff --base <ref> [--fail-under N] [--json]
```

`cov diff` scores the lines **this branch changed** instead of the whole
repository, and turns the answer into an exit code.

Whole-repo coverage is a lagging, nearly immovable number: one pull request
cannot move it, so it cannot fail one. Patch coverage — what fraction of the
lines this diff added or modified is covered — is the number that *can* gate,
and it is the primitive every coverage product ships (Codecov's `patch`
status, SonarQube's "Clean as You Code", diff-cover, Coveralls).

## What it measures

1. The changed line ranges come from `git diff --unified=0 <base>...HEAD`.
2. Those lines are intersected with the indexed symbols whose
   `[line, end_line]` span contains them.
3. Each touched symbol is scored against the **current coverage frontier** —
   the same runs [`atlas cov status`](./cov.md) and `atlas audit` read, so a
   polyglot build that tagged its syncs with `--run-group` is scored as one
   measurement rather than by whichever framework synced last.

The known fraction is **line-weighted**: a symbol contributes its changed
lines at its own covered/total statement ratio, so a one-line tweak to a large
covered function cannot outvote a whole new uncovered one.

## The three buckets

Every changed line lands in exactly one bucket, and the third one is the
reason this command is subtle:

| Bucket        | Meaning                                                                       |
| ------------- | ----------------------------------------------------------------------------- |
| **covered**   | Inside a symbol the frontier measured, weighted by that symbol's ratio.        |
| **uncovered** | Inside a measured symbol with zero covered statements. Provably a gap.         |
| **unknown**   | Atlas cannot score it. Its own state — never folded into either of the above.  |

A changed line atlas has no symbol for is **not** 0% covered. Scoring it as
uncovered makes the gate fire on files atlas simply cannot see (the blind spot
`cov status --gaps` reports), which teaches teams to switch the gate off.
Scoring it as covered hides real gaps. So it is reported as a third state:
`--fail-under` decides on the **known** fraction only, and the unknown one is
printed next to it, loudly.

Unknown lines carry a reason, because each one has a different remedy:

| Reason                 | What it means                                                                      | Fix                                                     |
| ---------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------- |
| `file-not-indexed`     | Atlas holds no symbol for the file (a doc, a config, an unscanned language).        | Nothing, usually — or extend the scanner.               |
| `outside-symbol-spans` | The file is indexed, but the lines sit outside every symbol's span.                 | Re-run `atlas scan` so the index matches HEAD.          |
| `no-coverage-data`     | A symbol owns the lines, but no run carried statement counts for it.                | `atlas cov sync --framework go-cover` / `istanbul`.     |

Two more rules follow from the same honesty:

- **Deleted lines are not patch coverage.** Only the post-image of each hunk
  counts; a file that only lost lines never appears.
- **No denominator is not zero percent.** A diff that changed no indexed,
  measured code reports `no measurable change` and exits 0 under *any*
  `--fail-under`. Reporting 0% would fail every docs-only and config-only pull
  request, which is how a gate gets deleted.

## Flags

| Flag                | Default | Purpose                                                                     |
| ------------------- | ------- | ---------------------------------------------------------------------------- |
| `--base <ref>`      | —       | **Required.** The ref this branch forked from: `origin/main`, a SHA, a tag.  |
| `--fail-under <N>`  | unset   | Exit non-zero when the known patch fraction is below `N` percent.            |
| `--json`            | off     | The full accounting, every bucket, for a PR-comment bot.                     |

`<base>...HEAD` is the **merge-base** (three-dot) form: it reports what this
branch did, not how it differs from the current tip of `base`. Without it, a
long-lived branch is gated on commits other people landed after it forked.

Only **committed** work is visible. Uncommitted edits in the working tree do
not appear — which is what a CI gate wants, and what a local caller has to
know.

## Example

```
# Run from: a repo root, after `atlas scan` and `atlas cov sync`
$ atlas cov diff --base origin/main --fail-under 80
patch coverage  origin/main...HEAD
changed files: 12   changed lines: 418

  known:     380 lines   covered 301 (79.2%)
  unknown:    38 lines   26 outside-symbol-spans, 12 file-not-indexed  (not scored, not gated)

  features touched (by changed lines):
    billing.checkout                          142 lines    91.2% covered
    measurements.log-entry                     96 lines     0.0% covered

  uncovered changed lines:
    src/contexts/measurements/application/services/log_service.go:88-131  measurements.LogService.Log  (0/24 stmts)

  partially covered - atlas knows the symbol's ratio, not which of its lines ran:
    src/contexts/billing/checkout.go:44-51  billing.Checkout  (18/22 stmts, 81.8%)

  unknown changed lines:
    src/generated/queries.sql.go:1-26  outside-symbol-spans
    docs/billing.md:1-12  file-not-indexed

FAIL  patch coverage 79.2% is below --fail-under 80
$ echo $?
1
```

The uncovered list names lines, not just a number, because a percentage with
no lines is not actionable. Note what is *not* in it: partially covered
symbols. Atlas knows a symbol's covered/total ratio, not which of its
individual lines ran, so those are reported separately — printing lines it
cannot vouch for would be worse than printing none.

## In CI

```yaml
- run: go test -coverprofile=cover.out ./...
- run: atlas scan
- run: atlas cov sync --framework go-cover --input cover.out
- run: atlas cov diff --base "origin/${{ github.base_ref }}" --fail-under 80
```

`atlas scan` before `cov diff` matters: the intersection is only as good as
the symbol index. An index that predates the branch reports the new code as
`outside-symbol-spans` — honest, but unmeasured. Make sure the checkout has
enough history for the merge base to resolve (`fetch-depth: 0` on
`actions/checkout`).

`--json` carries every bucket for a bot that posts the numbers on the PR:

```json
{
  "schema_version": "v1",
  "command": "cov.diff",
  "result": {
    "base": "origin/main",
    "head": "HEAD",
    "fail_under": 80,
    "passed": false,
    "changed_files": 12,
    "changed_lines": 418,
    "known_lines": 380,
    "covered_lines": 301.0,
    "uncovered_lines": 79.0,
    "unknown_lines": 38,
    "measurable": true,
    "percent": 79.2,
    "uncovered": [{ "path": "...", "symbol": "...", "ranges": [{ "start": 88, "end": 131 }] }],
    "partial": [],
    "covered": [],
    "unknown": [{ "path": "docs/billing.md", "reason": "file-not-indexed", "lines": 12 }],
    "files": [],
    "features": []
  }
}
```

`covered_lines` is a line-**equivalent** and is deliberately fractional: a
changed line in a 3-of-4 symbol contributes 0.75. The text output rounds it
for display; `percent` is always computed from the exact value.

`measurable: false` (with `percent: null`) is the "no denominator" case — a
consumer must treat it as "nothing to say", never as zero.

## Exit codes

| Code | Meaning                                                                     |
| ---- | ---------------------------------------------------------------------------- |
| `0`  | No `--fail-under`, or the known fraction met it, or the diff is unmeasurable. |
| `1`  | The known fraction is below `--fail-under`, or the command itself failed.     |

`cov diff` refuses to run against a store with no coverage runs at all. With
no frontier every line would be unknown and the gate would pass silently —
green for the wrong reason, which is the worst outcome for a command whose job
is to fail builds.
