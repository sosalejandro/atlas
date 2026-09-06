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

1. An up-to-date symbol index — `atlas init` / `atlas scan`.
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
2. **Changed lines** come from `git diff --unified=0 <ref>...HEAD`, and are
   mapped to symbols by declaration span (`symbols.line` … `symbols.end_line`).
3. **Tests** are the union of `test_coverage.TestsExecuting(run, symbol)` over
   every changed symbol and every run in the current coverage frontier.
4. **Tests the diff itself edited** are selected unconditionally, without
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
| `unrunnable-test`  | A changed symbol in a test file that `go test` will not dispatch on its own — a helper, a fixture builder, `TestMain`. |

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
so a reader can see why the reduction is smaller than the diff suggests.

A path that `--name-only` reports but `--unified=0` produces no hunks for (a
rename, a mode change) widens to the whole file for the same reason.

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

## Trusting the answer

The coverage evidence and the diff come from different commits. `affected`
reports the frontier's run ids, its run group (typically the git SHA or CI run
id `cov sync --run-group` was given), and how long ago it finished, on every
invocation including the bail-out path. A selection computed from week-old
evidence is worth less than one computed from this morning's, and the output
should never let you forget which you have.

The reduction (`reduction`, the share of the indexed suite skipped) is reported
for the same reason: a team needs to see when test selection stops paying off.

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
elif [ "$status" -ne 0 ]; then
  exit "$status"   # a real failure, not a bail-out
else
  pattern=$(jq -r '.result.run_pattern' affected.json)
  mapfile -t pkgs < <(jq -r '.result.packages[] | "./" + . + "/..."' affected.json)
  go test -run "$pattern" "${pkgs[@]}"
fi
```

Note the ordering: an exit code that is neither `0` nor your chosen fallback is
a genuine error (a bad ref, an unreadable store) and must not be treated as
"run everything".

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
mean to `go test`. Branch on `outcome`.

## Limitations

- Go only, for now. The `-run` pattern and the `./pkg/...` arguments are Go
  toolchain shapes, and the per-test coverage ingest that feeds this is
  `go-cover`-based.
- Selection is only as good as the per-test evidence behind it. A frontier that
  is stale relative to a large refactor will still be *safe* (uncertainty
  widens, never narrows) but the reduction will fall.
- Sub-tests are not addressed individually: the pattern selects the top-level
  `TestXxx`, and `go test` runs all of its sub-tests.
