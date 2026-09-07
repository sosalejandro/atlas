# Exit codes

Every atlas command returns one of four statuses. The contract is defined in
`internal/cli/exitcode.go` and applies to the binary as a whole.

| Code | Name | Meaning |
| --- | --- | --- |
| 0 | ok | The command ran, and whatever it gates on holds. |
| 1 | finding | The command checked, and the finding is real: coverage below the floor, a check tripped, drift exists. |
| 2 | usage | The invocation was wrong — a missing required flag, an unparseable value. Nothing was measured, and nothing about your codebase is implied. |
| 3 | undetermined | The command **could not reach a verdict**: the index is stale or absent, an input could not be read, a dependency was missing. |

## Why 3 exists

A CI job that treats any non-zero status as failure still fails closed, so
adopting this costs nothing. What it buys is the ability to tell **"atlas found
untested code"** from **"atlas could not tell"** — which arrive as the same red
X otherwise, and call for opposite responses. The first is a code review. The
second is a broken pipeline step, and the worst outcome is a team that learns
to re-run it until it goes green.

This distinction is not hypothetical here. Atlas has shipped the bug twice:

- `.gitleaks.toml`'s worktree allowlist was unanchored, so it matched the
  **absolute** path and any scan rooted inside `.claude/worktrees` allowlisted
  its own tree — exiting 0 having read nothing. Eight batches of "secrets: ok"
  were vacuous.
- A scan run with `--hash-files=false` writes no hash rows, so every path
  classifies as `absent` and the diff-joining commands degrade to their
  fallback. Correct behaviour, and indistinguishable from a clean run.

`.github/scripts/secret-scan.sh` already encoded the fix for the shell half:
*"a caller must be able to tell 'I found a secret' from 'I could not look'."*
The binary now does too.

## When a command returns 3

The rule: **a command returns 3 only when it was asked to decide something it
cannot decide.**

Reporting a caveat is not undetermined. `atlas cov diff` without `--fail-under`
prints the stale-index warning and exits 0, because nobody is deciding anything
with that number. Add `--fail-under` and the same staleness becomes a refusal,
because a percentage computed against spans that describe a version of the file
that no longer exists is not a low number or a high one — it is not a
measurement, and comparing it to a threshold would launder it into one.

| Command | Returns 3 when |
| --- | --- |
| `atlas doctor` | A check could not complete. ("The input does not exist yet" is a not-applicable result, not an error, and does not trigger this.) |
| `atlas cov diff` | `--fail-under` was given and one or more changed files have moved since the index was built. |
| `atlas affected` | Opt-in, via `--fallback-exit-code 3`. Bailing to "run everything" *is* the undetermined case; the default stays 0 so enabling it never breaks an existing pipeline. |

## Using it

```bash
atlas cov diff --base origin/main --fail-under 80
case $? in
  0) ;;                                   # patch coverage meets the floor
  1) echo "::error::patch coverage below floor"; exit 1 ;;
  2) echo "::error::atlas invoked incorrectly"; exit 1 ;;
  3) atlas scan && exec "$0" ;;           # stale index: re-scan and retry
esac
```

Retrying on 3 is safe in a way that retrying on 1 is not. That is the whole
point of separating them.
