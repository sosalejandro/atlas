# atlas security

`atlas security` answers the question every security review actually asks and
no other verb answers: **if I hand you this database, what am I handing you?**

Atlas reads an entire proprietary codebase. The state database it writes is
not metadata about that code — it holds SQL query text verbatim, the source
text of every branch condition, and, inside a snapshot, every symbol's doc
comment and signature. Until this verb existed the only way to establish that
was to read the migrations.

The written statement is [docs/security.md](../security.md). This command is
the same statement taken from the database in front of you, which is the only
version that cannot go stale.

## The honesty contract

Read this first, because the rest of the command is built around it.

**Everything is derived, not asserted.** The table and column enumeration
lives in `packages/redact/schema.go`, and `packages/redact/schema_test.go`
compares it against a freshly migrated store on every test run: a migration
that adds a table or a TEXT column **fails the build** until somebody says
what it holds. The export catalogue lives in `packages/redact/exports.go`, and
`internal/cli/security_test.go` checks it against the real cobra command tree:
a verb added without a decision about what it discloses fails the build too.

**It reports what it did not look at.** The secrets sweep iterates the
compiled-in registry, so a TEXT column this binary does not know about is
never read. "No secrets found" and "nothing was looked at" would otherwise
print identically, so the report names the unread columns under `NOT SWEPT`
(`columns_not_swept` in `--json`) and says "none detected **in what was
swept**". On a matched binary and store that list is empty; a non-empty one
means the binary is older than the database.

**It never modifies the database it describes.** `atlas security` and
`atlas security redact --dry-run` open the store **read-only** — SQLite's
`mode=ro` with `query_only` on top — and run **no migrations**. Pointed at a
path with no database, the command fails rather than creating one. Inspecting
an artifact must not change it, and for a store copied off a machine as
evidence that is the whole point.

**It never prints a credential.** Every finding carries the rule, the column,
the row, the byte length and a 12-hex-character digest — never the secret. The
`context` line elides the matched span. Output is safe to paste into a ticket.

## Usage

```
atlas security                     # the whole report: store, egress, secrets
atlas security --json              # the same, as a stable envelope
atlas security --export report     # what one verb would disclose
atlas security redact --dry-run    # what redaction would change
atlas security redact              # replace the credentials it can
```

## The three sections

### STATE DATABASE — what is stored

Path, size (with `-wal`/`-shm` named separately, because a copy of `atlas.db`
taken without its `-wal` can be a stale database), schema version, and every
table with its row count and content classes.

A table or column the registry does not describe is listed under
`NOT DESCRIBED BY ATLAS`, and the inventory says it is incomplete rather than
quietly omitting it.

### VERBATIM SOURCE TEXT — the uncomfortable half

The columns holding text copied out of your repository, each with what it
holds. This is the list that refutes "atlas stores only structure".

### EGRESS — what leaves

The no-network statement plus its enforcement, then every surface through
which indexed content leaves the database: the verb, the destination, the
content classes it can carry, and why.

Two things deserve attention. `atlas mcp` carries `features.title`, which is
source text lifted out of your annotation comments, and its destination is
usually an editor talking to a model provider. And three verbs write into your
working tree: `migrate-annotations --apply`, `cov shim init` and
`onboard promote --apply` — the last being the only one that puts something
atlas derived (an inferred feature id) into your source.

### SECRETS — what is exposed

What was swept, then what was found.

## Redaction, at ingest and after the fact

Source contains credentials more often than anyone admits, and a hardcoded
connection string inside a query is stored as query text.

**At ingest**, the write paths run values bound for redactable columns through
the detector before they are stored, so the credential does not land in the
first place. **After the fact**, `atlas security redact` sweeps a store that
already holds one. [docs/security.md §5](../security.md) lists which columns
are covered where, and the four detection rules.

Both replace the secret with `[redacted:<rule>:<digest>]`. The same credential
in nine rows produces nine identical placeholders, so it reads as one secret
to rotate rather than nine unrelated holes.

Only free-text columns are rewritten. A credential that ended up in a symbol
name or a file path is **reported and left alone**: rewriting it would change
what the index *means* rather than what it discloses, and the credential is
still in your source file either way.

**Neither touches your repository.** Rotate the credential.

## `--export <verb>`

`atlas security --export 'report sarif'` narrows the egress section to one
command. A verb that exists but has no artifact of its own gets the
cross-cutting surfaces back (the `--json` envelope and the state database); a
verb that does not exist is an **error**. A typo must never read as "this
command discloses nothing".

## Flags

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--export <verb>` | `security` | restrict the egress section to one command path, e.g. `--export 'report sarif'` |
| `--dry-run` | `security redact` | report what would be replaced and write nothing; the database is opened read-only |
| `--json` | both | emit the stable envelope instead of the rendering |
| `--db-path` | both | the state database to inspect |

## Example

Transcript from a throwaway fixture: one Go file whose query embeds a DSN
password, indexed with `atlas scan` and `atlas sql scan`. The figures below
are that fixture's, not yours — `swept N ... over M value(s)` moves with the
size of your store.

The credential is redacted on the way in, so the report comes back clean:

```
SECRETS
  swept 80 registered TEXT column(s) over 28 value(s)
  none detected in what was swept.
```

With the credential written straight into the store (as an older atlas would
have left it), the same command finds it and does not repeat it:

```
SECRETS
  swept 80 registered TEXT column(s) over 28 value(s)
  1 finding(s):

    sql_operations.sql_text  row 1
        connection-string  16 bytes  digest 1e9742d3294a
        context postgres://reporting:<redacted>@warehouse.internal

  Run `atlas security redact` to replace the redactable ones.
  Redaction does not remove anything from your repository. Rotate them.
```

The dry run reports what it would change, and writes nothing:

```
atlas security redact  .atlas/atlas.db
  swept 80 registered TEXT column(s) over 28 value(s)
  would redact sql_operations.sql_text row 1  connection-string digest 1e9742d3294a
  1 distinct value(s) would be replaced; 0 finding(s) in columns atlas will not rewrite.
  Nothing was written: --dry-run opens the database read-only.
  This did not touch your repository. Rotate the credentials.
```

## `--json`

The envelope's `result` carries `store` (the inventory), `egress` (the
statement plus its four booleans), `exports` (the catalogue),
`source_text_columns`, and `secrets` (the sweep). Two conditions surface in
the envelope's `warnings` rather than having to be inferred from counts: an
inventory that is incomplete because the schema drifted, and a store holding
credentials.

`atlas security redact --json` carries `path`, `dry_run` and `sweep`. Inside
the sweep, `values_rewritten` is the count of **distinct values** — computed
in both modes, so a dry run reports what it *would* replace — and each hit's
`applied` says whether the database was actually written.

## Limits

- **The no-network check covers first-party code.** It walks the import graph
  of the `atlas` binary and fails if any first-party package reaches `net`,
  `net/http`, `net/rpc` or `net/smtp`. It does not audit third-party
  dependencies and does not prove a dependency could not open a socket.
- **The detector is tuned to under-report.** A false positive silently
  rewrites a legitimate query that `atlas sql` then analyses as though it were
  your code — a wrong answer that looks authoritative, which costs more than a
  missed weak password you could have found by reading the file.
- **Not every redactable column is redacted at ingest yet.** Snapshot blobs,
  coverage messages, branch conditions and audit score blobs are cleaned by
  the sweep, not on the way in.
- **Redaction cleans the index, never the repository.** The credential is
  still in your source. Rotate it.

## Related

- [../security.md](../security.md) — the written data-handling statement this
  command is derived from
- [../../SECURITY.md](../../SECURITY.md) — reporting a vulnerability, and what
  is in and out of scope
- [sql.md](sql.md) — the verb that populates `sql_operations.sql_text`
- [snapshot.md](snapshot.md) — the largest single disclosure atlas creates
- [mcp.md](mcp.md) — the surface where indexed content reaches a third party
