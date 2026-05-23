# atlas cov

`atlas cov` groups the coverage-ingest verb (`sync`) and the coverage-status
view (`status`). Coverage is what feeds the `coverage_pass_rate` audit
component, so a project that never runs `cov sync` will see the audit
fall back to other signals (annotation freshness, aggregate linkage, etc.).

| Subcommand                | Purpose                                                                        |
| ------------------------- | ------------------------------------------------------------------------------ |
| [`sync`](#sync)           | Ingest a test framework's report into the atlas store.                         |
| [`status`](#status)       | Per-feature coverage view from the latest coverage run.                        |

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
| `playwright`   | `playwright test --reporter=json` JSON document.                              |
| `vitest`       | `vitest run --reporter=json` JSON document.                                   |
| `jest`         | `jest --json` JSON document.                                                  |
| `maestro`      | `maestro test --format=json` JSON document.                                   |

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

### `status`

```
atlas cov status [flags]
```

`cov status` pulls the most recent coverage run from the store and
summarises pass/fail/skip counts grouped by `feature_id`. With `--feature`
the output is filtered to a single feature.

#### Flags

| Flag                          | Default               | Description                                              |
| ----------------------------- | --------------------- | -------------------------------------------------------- |
| `--feature`                   | (all features)        | Restrict output to one feature id.                       |
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

There is no "merge with previous run" mode — each `cov sync` is a
standalone run. To see history across runs, query the `coverage_runs`
table directly via sqlite3 or use [`atlas diff`](./diff.md)'s `coverage:`
slice.
