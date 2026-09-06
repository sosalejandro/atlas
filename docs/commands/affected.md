# atlas affected

```
atlas affected --since <ref> [--kind test|package|feature] [--fallback-exit-code N] [--json]
```

`atlas affected` answers one question: **given this diff, what do I actually
need to run?** It maps the changes between `<ref>` and `HEAD` onto the symbols
atlas has indexed, then names the tests that recorded executing those symbols.

CI reruns the whole suite for a three-line change. Atlas already stores, per
test, exactly which symbols that test executed (`test_coverage`, migration
0010, issue #104). Read backwards that table is a precise inverse index: given
the symbols a diff touched, it names the tests that reach them.

## The one thing to understand first

**An empty selection and "run everything" are not the same answer, and this
command never confuses them.**

To a CI runner, a list of zero tests is indistinguishable from "nothing needs
testing" — so a tool like this ships a broken build by being silently
confident. Every uncertainty in `atlas affected` therefore resolves to an
explicit `run-all` outcome instead of a small list. The failure mode of being
too conservative is a slow pipeline; the failure mode of being too clever is a
shipped regression.

The outcome is always one of three values:

| `outcome`    | Meaning                                                                |
| ------------ | ---------------------------------------------------------------------- |
| `selected`   | The diff was narrowed. Run `selected_tests` / `run_pattern`.            |
| `run-all`    | Atlas could **not** narrow it. Run the whole suite. `fallbacks` says why. |
| `no-changes` | The diff is empty. Running nothing is correct — and this is not a bail-out. |

## Prerequisites

`affected` reads two things from the store, and says so plainly when either is
missing:

1. An up-to-date symbol index — `atlas init` / `atlas scan`. "Up to date" is
   checked, not assumed: a changed file that no longer matches the hash the
   scanner recorded cannot be mapped by line number, and widens instead. See
   [Index freshness](#index-freshness).
2. Per-test coverage evidence — `atlas cov sync --per-test` (see
   [cov-per-test.md](cov-per-test.md)). Whole-run coverage is **not** enough:
   it records that a symbol ran, not which test ran it.

With no per-test rows for the current coverage frontier, the outcome is
`run-all` with reason `no-test-evidence`.

## Example

```
$ atlas affected --since origin/main

  1 of 132 tests selected (99% of the suite skipped)
  since origin/main · 1 changed file · 1 changed symbol

  changed symbols:
    billing.Checkout                             billing/checkout.go:10

  selected tests:
    TestCheckout_Idempotent                      billing/checkout_test.go
      executes billing.Checkout

  go test -run '^(TestCheckout_Idempotent)$' ./billing/...

  evidence:
    frontier run 41 (group 9f2c1ab) — go-test, finished 3h0m0s ago
    132 tests contributed per-test rows
    NOTE: that coverage was measured at an earlier commit than this diff.
          A symbol whose callers changed since then may reach tests this
          selection does not name.
```

And when it cannot narrow:

```
$ atlas affected --since origin/main

  RUN EVERYTHING — atlas could not narrow this diff (2 changed files)

  why:
    [build-config] go.mod
      a dependency or build input changed; it can alter the behaviour of any
      package, and no symbol-level edit records that

  evidence:
    ...
```

## How selection works

1. **Changed files** come from `git diff --name-only <ref>...HEAD`. The
   three-dot form diffs against the **merge base**, not the tip of `<ref>` — on
   a branch that is behind `main`, two-dot would report everything that landed
   on `main` meanwhile as part of your change and destroy the reduction.
2. **Changed lines** come from `git diff --unified=0 <ref>...HEAD`.
3. **The index is checked against the working tree** before any line number is
   used — see [Index freshness](#index-freshness). Only a file that still
   hashes to what the scanner recorded may be mapped by span.
4. **Changed lines are mapped to symbols** by declaration span (`symbols.line`
   … `symbols.end_line`), for those files only.
5. **Tests** are the union of `test_coverage.TestsExecuting(run, symbol)` over
   every changed symbol and every run in the current coverage frontier.
6. **Tests the diff itself edited** are selected unconditionally, without
   consulting evidence — see [New tests](#new-tests).

Static call-edge closure is deliberately **not** the selection mechanism. Call
edges miss interface dispatch, DI containers, reflection and table-driven
registries; a subset built from them looks convincing and quietly omits the
test that would have caught the regression. Where execution evidence is
missing, `affected` runs everything rather than substituting a weaker signal
for it.

## What forces "run everything"

| `reason`           | Trigger                                                                    |
| ------------------ | -------------------------------------------------------------------------- |
| `unindexed-file`   | A changed file atlas holds no symbols for — an unscanned language, generated output, or a file newer than the last `atlas scan`. |
| `build-config`     | `go.mod`, `go.sum`, `Makefile`, `Dockerfile`, lockfiles, anything under `.github/`. A dependency bump changes what every package compiles against. |
| `test-infra`       | Shared test scaffolding: a `testdata/` fixture, a `testutil` package, `conftest.py`. Coverage records the *production* symbols a test ran, never which helpers it called, so a fixture's consumers are unknowable. |
| `no-test-evidence` | The coverage frontier is empty, or carries no per-test rows. |
| `no-tests-indexed` | Atlas has indexed no test symbols, so there is no suite to take a subset of. |
| `unrunnable-test`  | A changed symbol in a test file that `go test` will not dispatch by name from a `-run` pattern — a helper, a fixture builder, `TestMain`, **or a benchmark** (see [Benchmarks, fuzz targets and examples](#benchmarks-fuzz-targets-and-examples)). |
| `unverifiable-spans` | Atlas could not determine whether its stored spans still describe a changed file — it could not be hashed, or the path escaped the repo root. Every other freshness verdict widens; this one is the absence of a verdict. |

### Index freshness

The diff's line numbers describe the working tree **now**. `symbols.line` and
`symbols.end_line` describe the tree as of the last `atlas scan`. Those two are
only comparable if nothing moved in between, and when they disagree the failure
is silent and directional: insert twenty lines at the top of a file and every
symbol below shifts down by twenty on disk while the stored spans stay put, so
a changed line resolves to whichever symbol *used* to occupy it. The wrong
tests get selected and the right ones are omitted — with a confident-looking
reduction printed next to it.

So before any span is consulted, every changed source path is re-hashed and
compared with `file_hashes` (the shared check in `packages/indexfresh`). Only a
file whose content still matches may be mapped by line number. The rest take
the safe side:

| File state | What `affected` does |
| ---------- | -------------------- |
| unchanged since the scan | Maps changed lines to symbols normally. |
| changed since the scan | Widens to the file's package, `widenings[].reason = "stale-index"`. |
| deleted from the working tree, still indexed | Widens to the package, reason `deleted-file`. |
| indexed with no content hash (`scan --hash-files=false`) | Widens to the package, reason `unverifiable-spans`. |
| unreadable, or a path outside the repo root | **run-all**, fallback reason `unverifiable-spans`. |

Every one of those is reported by path, in `widenings` and in the human output,
with the remedy: re-run `atlas scan`. A stale index never silently narrows —
but it does cost you the reduction, which is the visible symptom to act on.

### Inert paths

Markdown, plain text, images, `LICENSE`, `.gitignore` and friends are ignored:
prose has no compiled representation, so no test outcome can depend on it. The
set is deliberately tiny and enumerated in `packages/affected/classify.go`
rather than expressed as a broad ignore glob — every entry is a promise, and it
should be auditable at a glance. Ignored paths are echoed back as
`inert_files`.

### Widening (not a bail-out)

An edited line that falls **outside every indexed symbol** — a package-level
`var`, an import block, a build tag — widens the selection to the whole
package rather than to the whole repo. That is reported as a `widenings` entry
(`path`, `scope`, `reason`, `detail`) so a reader can see why the reduction is
smaller than the diff suggests.

A path that `--name-only` reports but `--unified=0` produces no hunks for (a
rename, a mode change) widens to the whole file for the same reason, and a file
whose spans cannot be trusted widens to its package — see
[Index freshness](#index-freshness).

`reason` is one of `unmapped-line`, `no-hunks`, `stale-index`, `deleted-file`
or `unverifiable-spans`. The last three mean the *index* is at fault rather
than the diff, and `atlas scan` fixes them.

**What a widening claims, and what it does not.** Widening to a package pulls
in that package's production symbols and the changed file's own symbols. It
does **not** pull in symbols from other test files in the directory: the diff
did not touch them, and they are already selected on their own evidence when
they execute something that changed. A test that a widening did sweep in is
recorded with `why: ["pulled in by a widening of its package; this diff did not
edit it"]` rather than `"changed by this diff"` — the command never claims the
diff edited something it did not.

## New tests

A test added in the diff has **no rows in `test_coverage`, precisely because it
has never run**. Reading that emptiness as "reaches nothing, skip it" is
exactly backwards, so a changed test symbol is always selected and flagged
`no_history` in the output.

## Changed symbols nothing covers

A changed symbol with no rows in `test_coverage` is reported under
`uncovered_symbols` and as a `WARN` block in the human output — it is *not* a
fallback. The per-test ingest records every executing test, so zero rows is
evidence of absence ("nothing tests this") rather than missing evidence. It is
usually the most actionable line in the output.

When **every** changed symbol is uncovered, the outcome is `selected` with an
empty `selected_tests` and an empty `run_pattern`. That is the one result a CI
recipe must handle explicitly — see [Use in CI](#use-in-ci). The human output
leads with `WARN NOTHING SELECTED` and the JSON envelope carries a matching
warning, because running nothing here means the diff went untested.

## Benchmarks, fuzz targets and examples

`FuzzXxx` and `ExampleXxx` are dispatched by `go test -run` and are part of
what a plain `go test ./...` executes, so they are counted in `total_tests` and
selected like any other test when the diff touches them.

`BenchmarkXxx` is **not**. `go test -run '^(BenchmarkFoo)$'` matches no test and
runs nothing without `-bench`, so naming a benchmark in the pattern would
produce a green run that executed nothing. A changed benchmark therefore forces
`run-all` with reason `unrunnable-test` rather than being quietly selected.

## Trusting the answer

The coverage evidence and the diff come from different commits. `affected`
reports the frontier's run ids, its run group (typically the git SHA or CI run
id `cov sync --run-group` was given), and how long ago it finished, on every
invocation including the bail-out path. A selection computed from week-old
evidence is worth less than one computed from this morning's, and the output
should never let you forget which you have.

The reduction (`reduction`, the share of the indexed suite skipped) is reported
for the same reason: a team needs to see when test selection stops paying off.

Two different kinds of staleness matter here, and only one of them is checked:

- **The symbol index** (`atlas scan`) — checked per changed file. Out of date
  means the selection widens, loudly. See [Index freshness](#index-freshness).
- **The coverage evidence** (`atlas cov sync --per-test`) — *not* checkable.
  Nothing records the tests that would execute a symbol today, only the tests
  that did at measurement time, so evidence older than the code can omit a test
  without any signal. `evidence.age_seconds` is the only handle you have on it.

## Flags

| Flag                   | Default | Meaning                                                                 |
| ---------------------- | ------- | ----------------------------------------------------------------------- |
| `--since <ref>`        | —       | **Required.** Ref to diff against, as `<ref>...HEAD`. There is no safe default. |
| `--kind test\|package\|feature` | `test` | What the human output leads with. All three views are always present in `--json`. |
| `--fallback-exit-code N` | `0`   | Exit with status `N` when the outcome is `run-all`. `0` disables it, so adopting the flag is opt-in and never breaks an existing pipeline. |

`--kind package` prints `go test` path arguments (`./billing/...`);
`--kind feature` leads with the annotated features whose symbols the diff
touched.

## Use in CI

`--fallback-exit-code` is what lets a pipeline branch without parsing text:

```bash
set +e
atlas affected --since "origin/${BASE_BRANCH}" --json --fallback-exit-code 75 > affected.json
status=$?
set -e

if [ "$status" -eq 75 ]; then
  echo "atlas bailed out; running the full suite"
  go test ./...
  exit $?
fi
if [ "$status" -ne 0 ]; then
  exit "$status"   # a real failure, not a bail-out
fi

outcome=$(jq -r '.result.outcome' affected.json)
pattern=$(jq -r '.result.run_pattern' affected.json)

if [ "$outcome" = "no-changes" ]; then
  echo "nothing changed; no tests to run"
  exit 0
fi

# THE CASE THAT LOOKS LIKE SUCCESS AND IS NOT.
# outcome=selected with an empty pattern means the diff changed code that no
# test in the index reaches. Skipping it here is a green build that tested
# none of the change, so fall back to the full suite (and fix the gap).
if [ -z "$pattern" ]; then
  echo "atlas selected NO tests for a non-empty diff; nothing covers this change."
  jq -r '.result.uncovered_symbols[]?.qualified_name' affected.json
  go test ./...
  exit $?
fi

mapfile -t pkgs < <(jq -r '.result.packages[] | "./" + . + "/..."' affected.json)
go test -run "$pattern" "${pkgs[@]}"
```

Three things about the ordering:

- An exit code that is neither `0` nor your chosen fallback is a genuine error
  (a bad ref, an unreadable store) and must not be treated as "run everything".
- **Never pass an empty `run_pattern` to `go test -run`.** Branch on it. A
  pipeline that only branches on `run-all` runs zero tests and exits 0 the
  first time a diff touches something nothing covers.
- `no-changes` is the only outcome for which running nothing is correct.

## JSON

```
atlas affected --since origin/main --json
```

The envelope is the standard `schema_version: v1` shape (see
[architecture.md](../architecture.md) §6) with `command: "affected"`. The result
carries `outcome`, `since`, `changed_files`, `inert_files`, `changed_symbols`,
`uncovered_symbols`, `selected_tests`, `packages`, `features`, `total_tests`,
`fallbacks`, `widenings`, `evidence`, plus the derived `run_pattern`,
`reduction` and a standing `caveat` about the age of the evidence.

`run_pattern` is empty whenever nothing is selected. **Do not treat an empty
pattern as "match everything"** — that is what an unanchored empty regex would
mean to `go test` — and do not treat it as "nothing to do" either. Branch on
`outcome` **and** on the pattern being empty; the recipe in
[Use in CI](#use-in-ci) shows both branches.

On the `run-all` path `selected_tests` and `packages` are cleared and
`run_pattern` is empty. A partial subset next to `outcome: "run-all"` would be
read by a runner as the thing to run, so the answer is discarded rather than
left in place as a hint.

`widenings[]` entries carry `path`, `scope` (`file` or `package`), a
machine-readable `reason`, and a `detail` sentence. Alerting on
`reason == "stale-index"` is how a pipeline notices its index has drifted.

## Limitations

- Go only, for now. The `-run` pattern and the `./pkg/...` arguments are Go
  toolchain shapes, and the per-test coverage ingest that feeds this is
  `go-cover`-based.
- **Stale coverage evidence can narrow the selection incorrectly, and nothing
  detects it.** `TestsExecuting` returns the tests that executed a symbol *as
  measured at the commit the coverage run was taken from*. A test that started
  executing a changed symbol after that measurement — added since, or reaching
  it through a call path the diff itself introduces — has no row saying so, and
  is silently absent from the selection. This is a genuine gap, not a
  conservative one: the run-all rules cover missing evidence, never wrong
  evidence, and a merged frontier from an old commit looks exactly like a fresh
  one apart from `evidence.age_seconds`. Re-run `atlas cov sync --per-test`
  often enough that the age you see is one you would bet a release on.
- Index staleness, by contrast, *is* detected — a changed file whose spans no
  longer describe it widens rather than resolving. See
  [Index freshness](#index-freshness).
- Sub-tests are not addressed individually: the pattern selects the top-level
  `TestXxx`, and `go test` runs all of its sub-tests.
