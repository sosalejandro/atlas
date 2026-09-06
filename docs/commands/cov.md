# atlas cov

`atlas cov` groups the coverage-ingest verb (`sync`) and the coverage-status
view (`status`). Coverage is what feeds the `coverage_pass_rate` audit
component, so a project that never runs `cov sync` will see the audit
fall back to other signals (annotation freshness, aggregate linkage, etc.).

| Subcommand                | Purpose                                                                        |
| ------------------------- | ------------------------------------------------------------------------------ |
| [`sync`](#sync)           | Ingest a test framework's report into the atlas store.                         |
| [`status`](#status)       | Per-feature coverage view, and the attribution gap, from the latest run.       |

## Subcommand reference

### `sync`

```
atlas cov sync [flags]
```

`cov sync` parses a test-framework report and writes the resulting run +
per-test rows through the Coverage port.

Supported frameworks (`--framework`):

| Framework      | Typical report shape                                                          |
| -------------- | ----------------------------------------------------------------------------- |
| `go-test`      | `go test -json ./...` line-delimited JSON.                                    |
| `go-cover`     | `go test -coverprofile=cover.out ./...` profile (statement coverage).         |
| `playwright`   | `playwright test --reporter=json` JSON document.                              |
| `vitest`       | `vitest run --reporter=json` JSON document.                                   |
| `jest`         | `jest --json` JSON document.                                                  |
| `maestro`      | `maestro test --format=json` JSON document.                                   |
| `istanbul`     | `coverage-final.json` from the vitest/jest v8 reporter (statement coverage).  |

`go-cover` and `istanbul` are the **statement-coverage** tracks: instead of
recording one pass/fail row per TEST, they attribute each executed statement
to the production symbol whose source span contains it, which is what makes
per-feature coverage a real line fraction rather than a test tally.

When `--framework` is omitted, `cov sync` attempts to auto-detect from the
filename and the file's top-level shape. Failed detection is fatal — pass
`--framework` explicitly.

Input source: `--input <path>` (a file) or `-` / unset for stdin.

#### Flags

| Flag                          | Default               | Description                                                                                       |
| ----------------------------- | --------------------- | ------------------------------------------------------------------------------------------------- |
| `--framework`                 | (auto-detect)         | Framework tag — one of `go-test`, `playwright`, `vitest`, `jest`, `maestro`.                      |
| `--input`                     | `-` (stdin)           | Report file path, or `-` for stdin.                                                                |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                                                              |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                                                                    |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope instead of human-friendly text.                                      |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                                                                     |

#### Example: ingest a go-test report

```bash
# Run from: /tmp/atlas-fixture
$ cat > cov.json <<'EOF'
{"Time":"2026-05-22T00:00:00Z","Action":"run","Package":"example.com/fixture/auth","Test":"TestLogin"}
{"Time":"2026-05-22T00:00:01Z","Action":"pass","Package":"example.com/fixture/auth","Test":"TestLogin","Elapsed":0.05}
{"Time":"2026-05-22T00:00:01Z","Action":"run","Package":"example.com/fixture/auth","Test":"TestIssueToken"}
{"Time":"2026-05-22T00:00:01Z","Action":"fail","Package":"example.com/fixture/auth","Test":"TestIssueToken","Elapsed":0.02}
EOF

$ atlas cov sync --framework go-test --input cov.json
coverage ingest complete  run_id=1 framework=go-test
```

The `run_id` is the primary key of the newly inserted row in the
`coverage_runs` table. Successive ingests get successive `run_id` values;
`cov status` always reads the highest.

#### Example: stream from stdin

```bash
# Run from: a Go project root
$ go test -json ./... | atlas cov sync --framework go-test
coverage ingest complete  run_id=12 framework=go-test
```

Atlas does not buffer the entire input — it streams the line-delimited
JSON one record at a time, so very large test runs don't blow memory.

#### Example: ingest a Go coverprofile, and read the attribution gap

```bash
# Run from: a Go project root, after `atlas scan`
$ go test ./... -coverprofile=cover.out
$ atlas cov sync --framework go-cover --input cover.out
coverprofile ingest complete  run_id=4 blocks=980247 files=852/852 unmatched=0 symbols_executed=6912 stmts=1204331/1204331
```

The trailing counters are the **attribution accounting** — the answer to
"how much of the code that actually ran did atlas manage to charge to a
symbol?":

| Counter             | Meaning                                                                                       |
| ------------------- | --------------------------------------------------------------------------------------------- |
| `files=M/N`         | Profile files that reconciled to an indexed atlas file, out of all files in the profile.       |
| `unmatched=U`       | Profile files atlas has **no symbols for** — their execution cannot be attributed at all.      |
| `stmts=A/T`         | Statements charged to a symbol, out of all statements in the profile.                          |

When anything is unattributed, `cov sync` prints the size of the blind spot
and can enumerate it:

```bash
$ atlas cov sync --framework go-cover --input cover.out --verbose
coverprofile ingest complete  run_id=5 blocks=980247 files=641/852 unmatched=211 symbols_executed=4706 stmts=812004/1204331
attribution gap: 392327/1204331 statements (32.6%) in 254 file(s) could not be charged to a symbol
    5312 stmts  no-indexed-symbol      github.com/org/repo/src/infrastructure/persistence/generated/scheduling.sql.go
     871 stmts  outside-symbol-spans   github.com/org/repo/src/contexts/billing/service.go
   ...
```

Two reasons are reported:

| Reason                 | What it means                                                                                                     |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `no-indexed-symbol`    | Atlas has zero symbols for the file: it was never scanned, it is generated code (`**/generated/**` is skipped), or the profile path could not be reconciled to a repo-relative path. |
| `outside-symbol-spans` | The file IS indexed, but those statements fall outside every symbol's `[line, end_line]` span — declarations the scanner did not index. |

`--json` carries the same accounting under `result.attribution`, with the
full `gaps` list (the terminal view caps at 25 rows). Treat a large
`no-indexed-symbol` bucket as a **scan** problem, not a test problem: the
tests ran, atlas just doesn't know what they touched.

All of it is also **persisted with the run** — the counters as columns on
`coverage_runs`, the per-file list as `coverage_run_gaps` rows that cascade
with it. So the blind spot stays inspectable long after the ingest that
measured it, via [`cov status --gaps`](#example-the-attribution-gap-of-the-latest-run),
without re-running the profile. The stored gap list is capped at 500 files
per run; when it is cut, the number of files dropped is recorded on the run
and reported, and the statement totals stay exact regardless — a truncated
list never passes itself off as a complete one.

#### Example: per-test evidence (what each test actually ran)

```bash
# One profile per test, named <qualified test symbol>.out
$ ls .atlas/per-test/
billing.TestCheckout_Idempotent.out   measurements.TestLogEntry.out   ...

$ atlas cov sync --framework go-cover --per-test .atlas/per-test
per-test ingest complete  run_id=6 tests=1122 rows=214883 symbols_executed=6912
```

This writes the same union run as a whole-run ingest **plus** a row per
(test, symbol executed) pair. That evidence changes how a feature's
implementation surface is derived: instead of walking `call` edges out of the
annotated test and hoping the scanner resolved them, atlas takes the union of
what the feature's own tests actually executed, minus the symbols nearly every
test executes (the logger, the DI container, the middleware chain).

That derivation is correct through interface dispatch, DI containers,
reflection and string-routed handlers — none of which a static walk can follow.
`atlas audit --json` reports which derivation produced each score:

```json
{ "feature_id": "measurements.log-entry", "score": 78.4, "surface_source": "dynamic" }
```

| `surface_source` | Meaning |
| ---------------- | -------- |
| `dynamic`        | union of what this feature's tests executed (strongest) |
| `static`         | call-edge walk from the annotated test symbols |
| `package-anchor` | production symbols co-located with the feature's test package |
| `direct-links`   | the annotated symbols themselves, with the gotest pass/fail model |

**Collecting per-test profiles (Go).** Go writes coverage counters at process
exit, so per-test granularity needs the counters cleared and dumped around each
test — `runtime/coverage.ClearCounters()` and `WriteCountersDir()` (Go 1.20+)
from a `TestMain` shim, or a `-run` pass per test for small suites. Python's
`coverage.py` has this natively (`dynamic_context = test_function`); for
JS/TS the granularity is per test file.

### `status`

```
atlas cov status [flags]
```

`cov status` pulls the most recent coverage run from the store and
summarises pass/fail/skip counts grouped by `feature_id`. With `--feature`
the output is filtered to a single feature.

With `--gaps` it also reports that run's **attribution accounting** — read
back from the store, not recomputed — so "how much of what ran can atlas
actually see?" is answerable by anything that did not run the ingest itself.

#### Flags

| Flag                          | Default               | Description                                              |
| ----------------------------- | --------------------- | -------------------------------------------------------- |
| `--feature`                   | (all features)        | Restrict output to one feature id.                       |
| `--gaps`                      | off                   | Also report the run's attribution accounting and the files whose execution could not be attributed. |
| `--config` *(global)*         | `.atlas.yaml` lookup  | Explicit config path.                                    |
| `--db-path` *(global)*        | `.atlas/atlas.db`     | Override the SQLite state path.                          |
| `--json` *(global)*           | off                   | Emit the stable JSON envelope.                           |
| `-v`, `--verbose` *(global)*  | off                   | Verbose human-readable output.                           |

#### Example: latest run summary

```
# Run from: /tmp/atlas-fixture (after the go-test ingest above)
$ atlas cov status
Coverage run 1 (go-test, finished 2026-05-22 00:00:01)
  <unassigned>                              pass=1 fail=1 skip=0  (50%)
```

#### Example: the attribution gap of the latest run

```
# Run from: a Go project root, after `atlas cov sync --framework go-cover`
$ atlas cov status --gaps
Coverage run 5 (go-test, finished 2026-09-05 11:20:14)
  billing.checkout                          pass=412 fail=0 skip=0  (100%)
attribution: 392327/1204331 statements (32.6%) unattributed, files 641/852 matched
    5312 stmts  no-indexed-symbol      github.com/org/repo/src/infrastructure/persistence/generated/scheduling.sql.go
     871 stmts  outside-symbol-spans   github.com/org/repo/src/contexts/billing/service.go
  ... +229 more
```

The counters and the per-file list come from the `coverage_runs` row and the
`coverage_run_gaps` table respectively; nothing is recomputed, so this is
cheap enough to run in CI on every build. `--json` carries the whole thing
under `result.attribution`, which is what a gate like "fail if
`stmts_unattributed` exceeds 10% of the total" reads.

A run that recorded no accounting — one ingested before schema `0011`, or by
a framework with no statement coverage (`playwright`, `maestro`, the
`go-test` pass/fail model) — says so rather than reporting a flawless
zero-of-zero:

```
$ atlas cov status --gaps
Coverage run 1 (playwright, finished 2026-05-22 00:00:01)
  <unassigned>                              pass=1 fail=1 skip=0  (50%)
run 1 carries no attribution metadata (ingested before schema 0011, or by a framework without statement coverage)
```

`<unassigned>` is the bucket for tests that didn't link to a feature —
the fixture's `TestLogin` and `TestIssueToken` carry no
`@atlas:feature` annotation, so they fall here. On a real codebase tests
annotated with `// @atlas:feature auth.login` would group under
`auth.login` instead of `<unassigned>`.

## How it works

1. `cov sync` opens the input (file or stdin), routes through the
   framework's parser, and writes one row into `coverage_runs` plus one
   row per test into `coverage_tests`.
2. The test → feature linkage is harvested from the persisted
   `annotations` table — atlas joins the test's source file against the
   `@atlas:feature` annotations declared there.
3. `cov status` pulls the highest `run_id` from `coverage_runs`, joins
   `coverage_tests` against `feature_symbols`, and emits the pass / fail /
   skip rollup per feature.
4. The statement-coverage ingests additionally stamp their attribution
   accounting onto the `coverage_runs` row and write one `coverage_run_gaps`
   row per file they could not attribute. Both cascade with the run, so the
   accounting cannot outlive the run it describes — and `cov status --gaps`
   reads them straight back.

There is no "merge with previous run" mode — each `cov sync` is a
standalone run. To see history across runs, query the `coverage_runs`
table directly via sqlite3 or use [`atlas diff`](./diff.md)'s `coverage:`
slice.
