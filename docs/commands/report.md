# atlas report

`atlas report` renders the findings atlas already has into the three formats a
pull request actually displays. Without it, atlas's output is a terminal report
or a JSON envelope, and every team that wants atlas as a gate has to write the
same glue: parse the envelope, decide what fails, map findings back to lines,
render a comment.

| Subcommand           | Output                                            | What consumes it                                          |
| -------------------- | ------------------------------------------------- | --------------------------------------------------------- |
| `atlas report sarif` | SARIF 2.1.0                                       | `github/codeql-action/upload-sarif`, Azure DevOps, VS Code |
| `atlas report github`| `::warning file=...,line=...::` workflow commands | The Actions runner, straight off stdout                    |
| `atlas report pr`    | Sticky-comment markdown + its HTML marker         | `gh pr comment --body-file -`                              |

**atlas never calls the GitHub API.** `report pr` emits the comment body and the
marker a workflow greps for to find the comment it should update; posting and
editing stay with `gh`. That keeps the renderers pure functions over a finding
list — which is why they are covered by golden files
(`packages/report/testdata/golden/`) rather than by a mock of GitHub.

## Usage

```
atlas report sarif  [flags]
atlas report github [flags]
atlas report pr     [--base <ref>] [--head <ref>] [flags]
```

## Flags

Shared by every subcommand:

| Flag                     | Default              | Description                                                                                |
| ------------------------ | -------------------- | ------------------------------------------------------------------------------------------ |
| `--include`              | `audit,coverage`     | Producers to collect: `audit`, `coverage`, `dead`, or `all`. Unknown values are an error.   |
| `--warn-below`           | `70`                 | Feature health scores below this are warnings. `0` disables the band.                       |
| `--error-below`          | `30`                 | Feature health scores below this are errors. `0` disables the band.                         |
| `--min-gap-stmts`        | `10`                 | Ignore coverage gaps smaller than this many unattributed statements.                        |
| `--out`                  | *(stdout)*           | Write the rendering to this file instead of stdout.                                         |
| `--json` *(global)*      | off                  | Wrap the rendering in the v1 envelope; the rendering itself is `result.body`.                |
| `--db-path` *(global)*   | `.atlas/atlas.db`    | Override the SQLite state path.                                                             |
| `--config` *(global)*    | `.atlas.yaml` lookup | Explicit config path.                                                                       |

`atlas report pr` adds:

| Flag         | Default         | Description                                                                          |
| ------------ | --------------- | ------------------------------------------------------------------------------------ |
| `--base`     | *(none)*        | Git ref or snapshot id to compare against. Omit for a comment with no delta section.  |
| `--head`     | newest snapshot | Git ref or snapshot id for the current side.                                          |
| `--title`    | `Atlas report`  | The comment's heading.                                                                |
| `--max-rows` | `10`            | Findings listed per rule before the section collapses into a count.                   |

`dead` is deliberately not in the default `--include` set: `atlas codebase dead`
documents its own output as a *candidate* list with known false positives
(dynamic dispatch, plugin entry points, re-export chains), and putting candidates
on a PR as if they were findings is how a team learns to ignore the bot.

## Ready-to-copy workflow

```yaml
name: atlas
on: pull_request

permissions:
  contents: read
  security-events: write   # required by upload-sarif
  pull-requests: write     # required by the sticky comment

jobs:
  atlas:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # atlas snapshot resolves --base by git ref

      - uses: actions/setup-go@v5
        with: { go-version: stable }

      - run: go install github.com/sosalejandro/atlas/cmd/atlas@latest

      - run: atlas init
      - run: go test ./... -coverprofile=cover.out
      - run: atlas cov sync --framework go-test --input cover.out

      # 1. Inline annotations on the Files view, deduped across pushes.
      - run: atlas report sarif --out atlas.sarif
      - uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: atlas.sarif

      # 2. The cheap path — annotations without any upload step.
      - run: atlas report github

      # 3. One comment, updated in place on every push.
      - run: atlas report pr --base ${{ github.event.pull_request.base.ref }} > comment.md
      - run: gh pr comment ${{ github.event.pull_request.number }} --body-file comment.md --edit-last --create-if-none
        env:
          GH_TOKEN: ${{ github.token }}
```

`--edit-last --create-if-none` is what makes the comment sticky when `gh` posted
the previous one. If your workflow posts through the API instead, find the
comment whose body contains the marker (see below) and `PATCH` it.

## The sticky marker

Every `atlas report pr` body starts with:

```
<!-- atlas-report:sticky -->
```

The marker is invisible in rendered markdown, appears exactly once, and never
changes. Changing it would orphan every existing comment, so the next push would
post a second one that then sticks alongside the first. In `--json` mode it is
also returned as `result.marker`, so a workflow does not have to hard-code it.

## SARIF details that matter

These are the four things a hand-rolled SARIF writer gets wrong, each of which
fails *silently* — the upload succeeds, the check goes green, and the finding is
simply not there:

1. **Every result's `ruleId` is declared in `tool.driver.rules`.** GitHub drops a
   result whose rule is undeclared without logging anything. `atlas report sarif`
   returns an error rather than emitting such a result, so the failure is a red
   build instead of a quiet omission.
2. **Paths are repo-relative, forward-slashed, with no leading `./`.** An
   absolute uri makes the finding vanish from the Files view. Findings whose path
   cannot be made repo-relative are dropped and counted in the command's
   warnings, rather than shipped as a report that says nothing.
3. **`partialFingerprints` is derived from rule id + path + subject, never from
   the line number.** That is what lets GitHub recognise a finding across pushes.
   Fingerprinting the line would mark every finding in a file as new after any
   edit above it — which is how a code-scanning integration turns into noise.
4. **Severity is encoded twice.** `level` (`error`/`warning`/`note`) drives the
   annotation; `properties.security-severity` drives the alert list's ordering,
   and an alert without one sinks below every scored alert regardless of level.

The tool's `semanticVersion` is atlas's version with the `v` stripped — a leading
`v` is not semver and some ingests reject the whole document over it.

## Rules

Rule ids are a wire contract: GitHub keys an alert's history off the id, so
renaming one closes every open alert and reopens it as new. Ids are added, never
renamed.

### `atlas/contract-drift`

**error**, `security-severity: 5.0`

A declared contract no longer matches its implementation. Drift in a published
interface is the one atlas rule whose blast radius reaches outside the repo,
which is why it is the one that carries a security-severity.

### `atlas/feature-uncovered`

**warning** (error below `--error-below`)

A feature's health score is below the configured floor. The message names the
weakest component signal, so the annotation says where to start rather than just
reporting a number.

Features with no linked symbol produce no finding — there is no line to hang the
annotation on — and are reported instead as a warning naming how many were
skipped. Fix by adding an `@atlas:feature` annotation to the implementation.

### `atlas/coverage-unattributed`

**note**

Statements the coverage report says ran, but which atlas could charge to no
indexed symbol, so they contribute to no feature's score. This is a blind spot in
the *measurement*, not a defect in the code.

These paths frequently cannot be made repo-relative — a Go coverprofile names
files by import path, which is often exactly why atlas could not map them — so
they survive in the PR comment (which needs no line anchor) and not in SARIF
(which does).

### `atlas/dead-code`

**note**, opt in with `--include dead`

A symbol with no qualifying incoming edges, using the same defaults as
`atlas codebase dead` (import edges; `module` + `conditional` scopes). A triage
candidate, not a verdict — see that command's caveats block.

### `atlas/diagnosis`

**note**

A symbol atlas ranks as a likely source of a reported symptom. Produced by the
`packages/report` adapter over `atlas diagnose` results; not collected by the
`atlas report` subcommands, which have no symptom string to work from.

## Examples

### SARIF to a file

```
$ atlas report sarif --out atlas.sarif
atlas report sarif: wrote 7 findings to atlas.sarif
```

The status line goes to **stderr**. Stdout stays clean so `atlas report github`
can be piped straight into the runner's log without a stray line being echoed as
build output.

### Workflow annotations

```
$ atlas report github
::error file=internal/billing/invoice.go,line=42,endLine=87,title=atlas/feature-uncovered::feature billing.invoice scores 0.0/100 (weakest signal: coverage 0.0)
::notice file=internal/worker/queue.go,line=1,title=atlas/coverage-unattributed::118 statements executed but charged to no indexed symbol (reason: no-indexed-symbol)
```

Message payloads escape `%`, CR and LF; property values additionally escape `:`
and `,`. An unescaped comma in a path would truncate the annotation's location
and hang the finding on the wrong file.

### Sticky PR comment with a delta

```
$ atlas report pr --base origin/main
<!-- atlas-report:sticky -->
## Atlas report

| Metric | Value |
| --- | --- |
| Features scored | 18 |
| Features below the floor | 2 |
| Statements attributed | 4812 / 4930 (97.6%) |

### Change since `origin/main`

| Feature | Before | After | Delta |
| --- | ---: | ---: | ---: |
| `billing.invoice` | 58 | 31 | -27 |

### Findings

**2 findings** — 1 error, 1 notice.
...
```

The delta needs a snapshot for the base ref (`atlas snapshot --audit` on the
default branch). When there is none, the comment still renders — without the
delta section, and with a warning on stderr. A first push on a new branch has
nothing to compare against, and failing there would break the workflow on exactly
the commit that introduces it.

## JSON envelope

```
$ atlas report sarif --json
{
  "schema_version": "v1",
  "command": "report.sarif",
  "args": { "include": "audit,coverage", "warn_below": 70, "error_below": 30, "min_gap_stmts": 10, "out": "" },
  "result": {
    "format": "sarif",
    "body": "{\n  \"$schema\": ...",
    "finding_count": 7,
    "rules": ["atlas/coverage-unattributed", "atlas/feature-uncovered"]
  },
  "warnings": ["..."],
  "generated_at": "2026-05-24T01:31:52Z"
}
```

`result.body` is the rendering verbatim, not a re-modelled finding list: the
point of the command is the exact bytes CI consumes, and a consumer that
re-serialised a structured form would not be uploading what atlas rendered.
`report.pr` additionally returns `result.marker`.

Warnings are the honest half of the output. They report what did *not* make it
into the rendering — findings whose path could not be relativised, low-scoring
features with nothing to anchor to, a capped gap list, a missing base snapshot —
so that "no findings" and "nowhere to put them" stay distinguishable.

## See also

- [`atlas audit`](audit.md) — the scores `atlas/feature-uncovered` reports on.
- [`atlas cov`](cov.md) — `cov status --gaps` is the interactive view of
  `atlas/coverage-unattributed`.
- [`atlas codebase`](codebase.md) — `codebase dead` and its false-positive
  caveats.
- [`atlas snapshot`](snapshot.md) / [`atlas diff`](diff.md) — how `--base` gets
  something to compare against.
