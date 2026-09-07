# Security and data handling

Atlas reads an entire proprietary codebase and writes what it learns to a
local database. This document says what it reads, what it stores, what leaves
the machine, and what never does.

It is derived from the code rather than asserted about it. The enumerations
below are the ones `packages/redact` holds, and `packages/redact/schema_test.go`
compares that list against a freshly migrated store on every test run: a
migration that adds a table or a column fails the build until somebody says
what it holds. Anywhere this document and the code disagree, the code is what
is right — and the way to find out is to run the command:

```
atlas security
```

which prints the same inventory, taken from the database in front of you.

---

## 1. The short answer

**Nothing leaves your machine.** Atlas makes no outbound network calls: no
upload, no telemetry, no update check, no crash reporting. There is no
account, no server, and no cloud tier today.

That is enforced, not promised, and it is enforced twice because atlas ships
**two binaries** with different capabilities:

| binary | can it open a socket? | enforced by |
| --- | --- | --- |
| `atlas` | **No — at all.** It cannot import `net`, `net/http`, `net/rpc` or `net/smtp`. | `TestAtlasBinary_ImportsNoNetworkPackage` walks the import graph from `cmd/atlas` and fails the build. |
| `atlas-serve` | It **listens**, on loopback only. It never **dials**. | `TestServeBinary_NeverDialsOut` walks from `cmd/atlas-serve` looking for the calls — `net.Dial*`, `http.Get/Post/NewRequest`, `http.Client`, `net.Dialer` — and `httpapi.RequireLoopback` refuses any routable address. |

Everything that reads your source lives in `atlas`, which cannot reach the
network under any circumstances. `atlas-serve` exists only to hand an
already-built index to a local UI, and it is a separate binary precisely so
that the first row of that table stays absolute: a GUI is not a reason to
weaken the guarantee for every user who does not want one.

The distinction the second row draws is the one that matters for
exfiltration. An inbound listener bound to loopback cannot send your code
anywhere; an outbound dial is the thing that could. The dialing check is also
strictly more precise than an import check, which would pass a package that
imported `os/exec` and shelled out to `curl`.

`atlas-serve` has **no authentication** and serves a complete map of the
indexed tree — symbol names, file paths, which code nothing tests. That is
why the loopback refusal is in the server rather than in a warning: on a
routable address it would be a disclosure, not a convenience.
The walk covers the first-party packages `go list -deps ./cmd/atlas` reports.
No count is quoted here on purpose: the number moves with every package
split, and a stale figure in a security document is worse than none. To see
it for your checkout, run `go list -deps ./cmd/atlas | grep sosalejandro`.

**The bound on that check:** it covers first-party code. It does not audit
third-party dependencies, and it does not prove that a dependency could not
open a socket. Supply-chain provenance for the released binary is a separate
concern tracked in #121; this check is about atlas's own code.

**The uncomfortable half.** The state database is *not* metadata about your
code. Alongside file paths and symbol names it stores text copied verbatim
out of your repository — SQL query text, the source text of every branch
condition, and, inside a snapshot, every symbol's doc comment and signature.
Anyone who receives `atlas.db` receives all of that. Section 3 lists exactly
which columns.

---

## 2. Every file atlas writes

| Path | Written by | Contents | Mode |
| --- | --- | --- | --- |
| `.atlas/atlas.db` (+ `-wal`, `-shm`) | `init`, `scan`, `snapshot`, `cov`, `sql`, `flow`, `trend record`, and `mcp` on first open | The state database. Section 3. | default |
| the file named by `report --out` | `report sarif`, `report github`, `report pr` | The rendering, byte for byte, that would otherwise go to stdout. | `0600` |
| `<test symbol>.out` in the directory named by `cov run --out` | `cov run` | One Go coverprofile per test: file paths, line ranges, execution counts. The test's qualified symbol name is the filename. | `0644` |
| the directory named by `cov run --work` | `cov run` | Go coverage meta and counter files plus atlas's own plan and report JSON. A temporary directory by default, removed after the run unless `--keep` is passed. | `0755` dirs |
| your own source files | `migrate-annotations --apply` | Rewritten annotation comments, with the original file mode preserved. | unchanged |
| your own source files | `onboard promote --apply` | One `@atlas:feature <id>` comment above the anchor declaration. The id is inferred from the index. | unchanged |
| `atlas_shim_test.go` in each selected package | `cov shim init` | A generated `TestMain` — atlas's own template plus your package clause, no indexed content. Inert unless `ATLAS_COV_DIR` is set. A package that already declares a `TestMain` is left alone and reported. | `0644` |
| `.atlas/provisional/capabilities.json` | `onboard` | The provisional capability map: package and directory names, symbol names, route paths, table names. No verbatim source text. | default |
| `$TMPDIR/atlas-pyscan-*/scanner.py` | the Python scanner | Atlas's own embedded scanner script. None of your code. | `0600` |
| `$TMPDIR/atlas-tsscan-*/scanner.ts` (+ a `node_modules` bridge beside it) | the TypeScript scanner | Atlas's own embedded scanner script, and a symlink (or copy, where symlinks are unavailable) of the TypeScript compiler already installed in your project. | `0600` |

The database path comes from `--db-path`, then `db_path` in `.atlas.yaml`,
then the default `.atlas/atlas.db` relative to the repository root.

**Three commands write into your working tree:**

- `migrate-annotations --apply` rewrites your annotation comments into another
  grammar, preserving the file mode. It writes back what was already there.
- `cov shim init` writes a generated `TestMain` into each selected package —
  atlas's own template plus your package clause.
- `onboard promote --apply` writes one `@atlas:feature <id>` comment above an
  anchor declaration. This is the only repository write that puts something
  atlas *derived* into your source, and what it puts there is an identifier.
  Without `--apply` it prints the line and touches nothing.

Everything else atlas produces goes to stdout, to `.atlas/`, or to a path you
named on the command line.

### Programs atlas launches

Atlas chooses to launch these, and nothing else:

| Program | Used for |
| --- | --- |
| `git` | history, blame, diff, `rev-parse` |
| `go` | `go test`, `go tool covdata`, `go list` |
| `node` | the TypeScript scanner |
| `python` | the Python scanner |

**Plus whatever you tell it to run.** `atlas cov run -- <command>` executes
the argv you supply — anything at all, with your environment and your
permissions — and, for a `go test` command, adds the coverage flags per-test
collection needs. With no `--` argument it runs `go test ./...`. Atlas wraps
that process to collect coverage; it does not restrict it. So the accurate
statement is: atlas launches the four programs above on its own initiative,
and exactly one verb runs a command of your choosing.

Every invocation is argv-style — `exec.Command(name, args...)`. No command
string is ever handed to a shell, so nothing atlas passes through (a branch
name, a file path, your `cov run` argv) is subject to shell expansion.

---

## 3. What is in the state database

Run `atlas security` for the live version of this, with row counts.

Every stored TEXT column falls into one of six classes:

| Class | Meaning |
| --- | --- |
| `path` | A filesystem path. Repo-relative everywhere except `coverage_runs.raw_path`, which records where a coverage report was read from and can be absolute. |
| `identifier` | A name declared in your source: symbol, package, table, column, feature, owner. Not a secret, but it is the shape of your system. |
| `source-text` | **Text copied verbatim out of your repository.** |
| `user-text` | Free text a person typed on the command line or in config. |
| `enum` | A value from a closed vocabulary atlas assigns itself. |
| `digest` | A SHA-256 of file content. One-way. |

Columns not listed anywhere below hold integers, floats and timestamps —
counts, scores, line numbers, run times. None of them can carry text out of
your repository.

### 3.1 The columns that hold verbatim source text

This is the list that matters. Anything here is your code, not a description
of it:

| Column | What it holds |
| --- | --- |
| `sql_operations.sql_text` | **The query text**, verbatim, including any inline literals. A hardcoded connection string in a query lands here. |
| `sql_operations.interpolation` | The interpolated fragment, verbatim, when a query is assembled by concatenation. |
| `sql_operations.unresolved_reason` | Why a query could not be read; quotes the construct. |
| `sql_operations.suppressions` | The atlas directives found on the enclosing declaration. |
| `cfg_edges.condition` | **The source text of every branch condition**, verbatim. |
| `cfg_findings.detail` | A flow finding's explanation, which quotes source constructs. |
| `annotations.value` | The argument text of each `@atlas` annotation, verbatim from the comment. |
| `snapshots.index_json` | **The whole serialised index**, including every symbol's **doc comment** and **signature**. Nothing else in the database holds doc comments. |
| `snapshots.audit_json` | The audit slice at that git ref. |
| `audit_snapshot_runs.score_json` | Serialised audit scores; carries feature ids and titles. |
| `coverage_runs.summary_json` | The ingest's own summary of a coverage report. |
| `coverage_results.message` | A test framework's message. Failure output routinely quotes data. |
| `features.title` | A feature's human title, taken from the source. |
| `skipped_files.detail` | The evidence for an exclusion rule — the matched glob, or the generated-code header. |
| `sql_indexes.predicate` | A partial index's `WHERE` clause, verbatim from your DDL. |
| `symbols.pattern_matches` | Serialised EDA recogniser hits, which quote source constructs. |

Two of these deserve to be called out separately, because they are the ones
people are surprised by:

- **`snapshots.index_json`** is written by `atlas snapshot`, and only by
  `atlas snapshot` — `atlas diff` reads snapshot rows, it does not capture
  them. It is a JSON serialisation of the entire in-memory index, and
  `shared.Symbol` carries `Doc` and `Signature`. So: if you have ever run
  `atlas snapshot`, your doc comments are in the database. If you have not,
  they are not — the `symbols` table has no doc column at all.
- **`sql_operations.sql_text`** exists so `atlas sql` can tell you a query
  has no `LIMIT`. It holds the query as written, so a DSN embedded in a
  migration or a `dblink` call is stored as query text. That is what §5 is
  for.

### 3.2 Paths and identifiers

| Column | What it holds |
| --- | --- |
| `symbols.qualified_name`, `symbols.package` | Symbol and package names. |
| `symbols.file_path`, `symbols.domain` | Repo-relative path, and its product-area (bounded-context) prefix. |
| `edges.file_path` | Where a relation was observed. |
| `annotations.file_path`, `file_hashes.file_path`, `skipped_files.file_path` | Repo-relative paths. |
| `sql_operations.file_path`, `sql_operations.symbol_name`, `sql_operations.name`, `sql_operations.ref` | Where a query lives, and what encloses it. |
| `sql_tables.name`, `sql_tables.file_path`, `sql_indexes.*`, `sql_operation_tables.table_name`, `sql_operation_predicates.table_name`, `sql_operation_predicates.column_name` | Your database schema, as read out of your DDL. |
| `features.id`, `features.owner`, `features.deprecated_since`, `features.introduced_in`, `feature_symbols.feature_id`, `coverage_results.feature_id`, `coverage_history_features.feature_id` | Feature ids, owner handles and version strings from annotations. |
| `coverage_runs.raw_path`, `coverage_run_gaps.path` | Where a coverage report was read from — possibly an absolute, machine-specific path — and the files in it. |
| `coverage_history.commit_sha`, `snapshots.git_ref`, `coverage_runs.run_group` | Git refs and correlation keys, as supplied. |
| `config.key` | Config keys atlas set. |

`file_hashes.content_hash` is a SHA-256 digest of file content. It is
one-way: it reveals only whether two files are identical.

`config.value`, `snapshots.notes` and `coverage_history.note` hold free text
someone typed (`--note`, config values).

### 3.3 What is not stored

- Source file *contents*. Atlas parses files and stores what the parse
  found; it never copies a file into the database.
- String literals from your code, except where §3.1 says otherwise (SQL
  text, branch conditions, doc comments in a snapshot, annotation values).
- Environment variables, credentials from your shell, or anything about the
  machine beyond paths that appear in the columns above.
- Anything about *you*: no user id, no email, no machine id, no timing
  telemetry. The only person-shaped value anywhere is `features.owner`,
  which is the handle you wrote in an `@atlas:owner` annotation.

---

## 4. What leaves the database

Atlas transmits nothing. Everything below is a local file or a local stream,
and what happens to it afterwards is your decision. Run
`atlas security --export <verb>` to ask about one command.

| Surface | Destination | Carries |
| --- | --- | --- |
| the state database | a local file | everything in §3 |
| `--json` on any verb | stdout | whatever that verb reads — `atlas sql --json` includes query text, `atlas flow --json` includes branch conditions |
| `atlas report sarif` | stdout or `--out` | paths, line spans, rule ids, and messages naming features, symbols and scores. The audit, coverage and dead-code producers quote no source text, so no query text or doc comment reaches this file today |
| `atlas report github` | stdout, copied by the runner into the job log | the same findings. Job logs are visible to anyone who can see the repository's Actions tab, which on some plans is a wider audience than the repository |
| `atlas report pr` | stdout; you pipe it to `gh pr comment` | the same findings. Atlas never calls the GitHub API itself; the comment becomes public the moment it is posted on a public repository |
| `atlas cov run --out` | one local coverprofile per test | file paths, line ranges, execution counts; the filenames are test symbol names |
| `atlas cov run --work` | a local directory, temporary unless `--keep` | coverage meta and counter files, and atlas's plan and report JSON |
| `atlas mcp` | stdout, as JSON-RPC to the client that launched it | paths, identifiers, coverage figures, **and `features.title`** — which is source text, lifted out of your annotation comments. Capped per tool and read-only by construction |
| `atlas snapshot` | the `snapshots` table | the whole index, doc comments and signatures included |
| `atlas cov shim init` | `atlas_shim_test.go` in your packages | nothing from the index: atlas's own generated `TestMain` |
| `atlas migrate-annotations --apply` | your source files, in place | nothing from the index: it rewrites your annotation comments into another grammar |
| `atlas onboard promote --apply` | your source files, in place | one inferred feature id, as an `@atlas:feature` comment |

**`atlas mcp` is the one to think hardest about.** It discloses nothing over
a network by itself — it speaks JSON-RPC on stdin/stdout to the process that
spawned it. But that process is usually an editor talking to a model
provider, so it is the surface where indexed content routinely reaches a
third party. Through your client, never through atlas.

The source text it carries is `features.title` and nothing else. A title is
taken from your annotation comment rather than typed by an operator, which is
why §3 classifies it as source text and why the catalogue declares it here.
No MCP tool returns a doc comment, a signature, query text or a branch
condition.

---

## 5. Secrets in your source, and in the index

Source contains credentials more often than anyone admits, and atlas will
faithfully store them: a hardcoded connection string inside a query becomes
`sql_operations.sql_text`, and a key quoted in a doc comment becomes part of
`snapshots.index_json`.

Atlas attacks this from both ends.

**At ingest.** The write paths run every value bound for a redactable column
through the same detector before it is stored, so a hardcoded connection
string inside a query never reaches `sql_operations.sql_text` in the first
place — it is stored as `[redacted:connection-string:<digest>]`, with the
scheme, user and host left intact so `atlas sql` can still analyse the query.
Each replacement is logged with the file and line the credential is still
sitting in, because rotating it is the part atlas cannot do for you. The
columns covered today are `sql_operations.sql_text`, `.interpolation`,
`.unresolved_reason` and `.suppressions`, `sql_indexes.predicate`,
`annotations.value`, `symbols.pattern_matches` and `skipped_files.detail`.
The other redactable columns in §3.1 — snapshot blobs, coverage messages,
branch conditions, audit score blobs — are not yet redacted at ingest and are
cleaned by the sweep below.

**After the fact.** `atlas security` scans the stored TEXT columns for four
high-signal shapes:

| Rule | What it matches |
| --- | --- |
| `private-key` | A PEM private key block, including an unterminated one. |
| `aws-access-key` | An AWS access key id: a documented four-character prefix plus exactly sixteen more characters. |
| `connection-string` | The password in a `scheme://user:password@host` URL. Only the password is covered — the scheme, user and host stay, so the row remains analysable. |
| `assigned-secret` | A quoted literal assigned to an identifier whose *name* says it is a credential (`password`, `secret`, `token`, `api_key`, …), that is at least 16 bytes long and has at least 3.5 bits of entropy per byte, and is not an obvious placeholder or template. |

`atlas security redact` replaces each one with
`[redacted:<rule>:<digest>]`, where the digest is the first 12 hex
characters of SHA-256 over what was removed. The same credential in nine
rows redacts to nine identical placeholders, so it reads as one secret to
rotate rather than nine unrelated holes.

**The detector is tuned to under-report, on purpose.** The two errors do not
cost the same. A missed weak password is a credential you could have found by
reading the file. A false positive silently rewrites a legitimate query that
`atlas sql` then analyses and reports on as though it were your code — a
wrong answer that looks authoritative.

So `password = "supersecretvalue"` is left alone — 16 bytes, but only 3.125
bits of entropy per byte, measured — and so is anything containing `${`,
`{{`, a printf verb, whitespace, or a placeholder word. What *is* redacted is
always reported: the column, the row, the rule and the digest.

Three more limits, stated rather than hidden:

- **The sweep reads the columns in the registry, not every TEXT column in the
  file.** It iterates the enumeration in `packages/redact/schema.go`, so a
  column that exists in the database and not in that list is never read — and
  a clean result says nothing about it. In a released build that set is empty,
  because `packages/redact/schema_test.go` compares the registry against a
  freshly migrated store and fails the build on drift. That is a *build-time*
  guarantee, and it does not hold when an older binary is pointed at a newer
  database, so both `atlas security` and `atlas security redact` print a
  `NOT SWEPT` line naming exactly which columns went unread (`columns_not_swept`
  in `--json`). "No secrets found" and "nothing was looked at" are different
  answers and the report distinguishes them.
- **Identity columns are reported, never rewritten.** A credential that ended
  up in a symbol name or a file path is shown to you and left in place.
  Rewriting it would change what the index *means* rather than what it
  discloses, and the credential is still in your source file either way.
- **Redaction does not touch your repository.** It cleans the database.
  Rotate the credential.

---

## 6. Using `atlas security`

The per-verb reference, with flags and worked output, is
[docs/commands/security.md](commands/security.md). The short version:

```
atlas security                     # the whole report: store, egress, secrets
atlas security --json              # the same, as a stable envelope
atlas security --export report     # what one verb would disclose
atlas security redact --dry-run    # what redaction would change
atlas security redact              # replace the credentials it can
```

`atlas security` and `atlas security redact --dry-run` open the state
database **read-only** — SQLite's own `mode=ro`, with `query_only` on top of
it — and do not run migrations. Inspecting a store cannot alter it, and it
cannot bring one into existence either: pointed at a path with no database,
the command fails rather than creating and migrating an empty one. The
schema version in the report is the version the file actually carries, so a
store captured from a machine running an older atlas reads back as that older
store.

`atlas security redact` (without `--dry-run`) is the only one that opens for
writing, and it writes in one transaction: either every replacement lands or
none does, so you are never left with a database that is neither the one you
inspected nor a clean one. It still does not migrate.

Neither command prints a credential. Findings carry the rule, the column, the
row, the byte length and a 12-hex-character digest, so the output is safe to
paste into a ticket.

A note on `--export <verb>`: a verb that exists but has no artifact of its own
gets the cross-cutting surfaces back, while a verb that does not exist is an
error. A typo must not read as "this command discloses nothing".

---

## 7. Reporting a vulnerability

See [SECURITY.md](../SECURITY.md).

---

## 8. What this document deliberately does not claim

- There is no encryption at rest. The state database is a plain SQLite file
  with the permissions your umask gives it. If the machine is not trusted,
  the database is not protected.
- There is no authorization model, because there is no multi-user surface.
  Anyone who can read `.atlas/atlas.db` can read everything in §3.
- There is no sync, no telemetry and no account, so there is nothing to opt
  out of. If a future release adds any of them, the egress test in
  `packages/redact/egress_test.go` fails first, and §1 has to be rewritten
  before it can ship.
