# atlas onboard

`atlas onboard` is the first run on a repository nobody has annotated.

Every other verb in this tool is gated behind somebody having written
`@atlas:feature` annotations first, which makes the very first run a symbol
count and a shrug. `onboard` runs the whole chain — scan, ingest, SQL
inventory, HTTP route extraction, git churn — and ends on a **provisional
capability map** plus the findings that map made visible, followed by an
explicit account of what atlas could *not* see.

**Inferred is not declared.** Nothing `onboard` proposes is written to the
features table. See [Inferred is not
declared](../quickstart.md#3-inferred-is-not-declared) for the four
mechanisms that keep it apart, and
[`packages/onboard/doc.go`](../../packages/onboard/doc.go) for why.

The inference lives in [`packages/onboard/`](../../packages/onboard/) and is
a pure function over values: it holds no store handle, so no path through it
can write the registry even by mistake.

## Usage

```
atlas onboard [flags]
atlas onboard promote [flags]
```

## Flags

| Flag                   | Default           | Description                                                                                     |
| ---------------------- | ----------------- | ----------------------------------------------------------------------------------------------- |
| `--root`               | repo root or cwd  | Project root to scan.                                                                            |
| `--skip-sql`           | off               | Skip the SQL inventory pass. Capabilities then carry no data footprint, and the header says `skipped` rather than printing a zero. |
| `--top`                | `15`              | How many provisional capabilities to print per section (`0` = all). The full map is always written to disk. |
| `--skip-churn`         | off               | Skip git history mining. Drops the "changing and untested" finding entirely rather than degrading it. |
| `--config` *(global)*  | `.atlas.yaml`     | Explicit config path.                                                                            |
| `--db-path` *(global)* | `.atlas/atlas.db` | Override the SQLite state path.                                                                  |
| `--json` *(global)*    | off               | Emit the stable JSON envelope instead of human-friendly text.                                    |

## What it reports

The run has five sections, in this order.

**The header** — production and test symbol counts, the SQL inventory, the
route count, how many features were already declared, and how many
provisional capabilities were proposed over how many undeclared symbols, each
with the wall time it cost. With `--skip-sql` the SQL line reads `skipped`;
it does not print `0 operations, 0 unresolved 0.0s`, which would be an
unknown wearing a measurement's clothes.

**WHAT ATLAS FOUND** — the findings, ordered by how unlikely they are to be
known already: endpoints with no test reaching the handler, tables written
from more than one capability, tables with exactly one writer, capabilities
under active change with nothing verifying them, SQL advisories, dead-code
candidates. Findings computed over inferred groupings are tagged
`[over provisional groupings]`.

**WHAT ATLAS CANNOT SEE** — the limits. This section is not a disclaimer: a
report that lists only its findings reads as complete, and this one is a
lower bound in several directions at once (queries it could not parse,
routers it does not recognise, execution nobody gave it, history git would
not hand over, files the scan excluded). Every limit that can be removed
carries the command that removes it.

**PROVISIONAL CAPABILITY MAP** — the proposals, split into those derived from
HTTP routes and those derived from code structure and test names, each with
one line of the evidence it came from.

**NEXT / CI** — the promote commands, the command that gives atlas execution
evidence, and a GitHub Actions snippet.

## Why this is a separate verb from `atlas init`

The obvious shape is for [`atlas init`](./init.md) to do this by itself: run
it on an unannotated repository and get the map for free. It is a separate
verb on purpose, and the reason is not tidiness.

`init` is the bootstrap every other verb depends on, and it is the command
that runs in CI. Its output today is entirely derived from the source: a
scan, the migrations, and whatever annotations a human wrote. `onboard`
writes something else — a proposal set — into `.atlas/provisional/`, and
proposals are the one kind of state in this tool that nobody asked for. A
bootstrap that produced them by surprise would mean:

- CI writes a provisional map on every run, in a directory nobody committed
  and nothing reads, on repositories that finished onboarding a year ago.
- The verb whose entire job is "make the store match the source" acquires a
  second job — "guess at things the source does not say" — and the reader of
  `atlas init` output has to tell the two halves apart every time.
- The 6-second inference is charged to every `init`, including the warm
  incremental ones that otherwise finish in milliseconds.

So `init` bootstraps, `onboard` proposes, and the split is what lets each of
them say exactly one thing. The cost is that the first run on a fresh
repository is two commands rather than one; `atlas onboard` performs the
scan and ingest itself, so in practice it is `atlas onboard` first and
`atlas init` never, until CI needs it.

## Where the proposals come from

Grouping runs strongest-signal-first, and each stage claims only the symbols
earlier stages left, so a symbol lands in exactly one proposal:

| Source               | Proposal                                                                                    |
| -------------------- | ------------------------------------------------------------------------------------------- |
| Existing annotations | Adopted as they are. Their symbols are invisible to every grouping stage and never re-proposed. |
| HTTP routes          | One capability per registration — `POST /measurements` → `measurements.create`.               |
| Test names           | Two or more tests in one directory leading with the same word — `TestCheckout*` in `billing` → `billing.checkout`. |
| Directory tree       | Everything left, named `<parent>.<dir>`. The weakest proposal, and the only one that is a fact about the tree rather than a guess. |

The SQL inventory, git churn and any ingested coverage are **not** grouping
sources. They are attached to whatever grouping was made, which is where each
capability's data footprint, churn score and test evidence come from.

When a directory-derived id collides with a capability an earlier signal
already proposed, the directory's symbols are merged into it and the merge is
recorded in that capability's evidence (`merged in: …`), so the reader can
see that a symbol list under route or test-name evidence was partly filled in
by a directory sweep.

## Test evidence

The `tests:` column on each capability is a scale of confidence in the
*claim*, not in the code, and the four values are printed rather than
collapsed because they are different facts:

| Value                   | Means                                                                                        |
| ----------------------- | -------------------------------------------------------------------------------------------- |
| `execution`             | An ingested coverage run recorded one of the capability's symbols executing. A measurement.    |
| `measured-not-executed` | An ingested coverage run reported on these symbols and recorded none of them executing. Also a measurement — the negative one — and it outranks colocation. |
| `colocated-tests`       | Test files sit alongside the code. A test exists near it; nothing says a test reaches it.      |
| `none`                  | Neither. No test file, no execution record.                                                    |

`measured-not-executed` is only used for symbols the run actually reported
on. A Go coverprofile ingested into a Go + TypeScript repository measures
half the tree, and the other half falls back to `colocated-tests` or `none`
rather than being reported as measured and dead.

## `atlas onboard promote`

Promotion is the only path from a proposal into the registry, and it does
**not** write the database. It writes an `@atlas:feature <id>` annotation
into your source, above the anchor declaration the proposal cites; the
annotation reaches the features table through the ordinary scan, exactly as a
hand-written one would.

| Flag      | Default          | Description                                                                    |
| --------- | ---------------- | ------------------------------------------------------------------------------ |
| `--root`  | repo root or cwd | Project root.                                                                   |
| `--id`    | —                | Provisional capability id to promote (repeatable; the `provisional:` prefix is optional). |
| `--all`   | off              | Promote every capability in the provisional map.                                |
| `--apply` | off              | Write the annotations. The default is a dry run that prints the exact lines.    |

Notes:

- The default is a dry run. A verb that edits source because somebody typed
  it once is a verb people stop typing.
- A declaration that already carries `@atlas:feature`, `@atlas:contract` or
  `@testreg` is skipped with a reason. Existing annotations are adopted,
  never overwritten.
- An id that is not in the map is an error, not a silent skip, so a typo in a
  script fails instead of passing green.
- `--all` regularly lands several annotations in one file. Those writes are
  ordered per file, bottom-up, so an earlier insertion cannot shift the line
  a later one is aimed at.
- Promotion seeds membership at **one** symbol per capability. Broaden a
  feature by annotating more symbols yourself; `atlas trace <feature-id>`
  shows what the claim currently covers.
- Re-run `atlas scan` afterwards to materialise the annotations.

## Output artefacts

The full map — every proposal, its evidence, its anchor and its symbol ids —
is written to `.atlas/provisional/capabilities.json` on every run, whatever
`--top` prints. It is JSON in a directory named `provisional`, deliberately
not a table in the state DB: a proposals table would eventually be joined
against by a verb that forgot the distinction. Delete it with `rm -r` when
the proposals turn out to be wrong.

## See also

- [Quickstart](../quickstart.md) — the whole first run with recorded output
- [`atlas init`](./init.md) — the bootstrap for a repository that already has
  annotations
- [`atlas scan`](./scan.md) — materialise promoted annotations
- [`atlas sql`](./sql.md), [`atlas hotspots`](./hotspots.md),
  [`atlas cov`](./cov.md) — the verbs the findings point at
