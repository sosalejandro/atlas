# atlas sql

`atlas sql` opens up the data access layer. Atlas could already tell you that
a repository function is covered by a test. What it could not tell you — and
what actually breaks in production — is that the query inside that function
has no `LIMIT`, takes a caller-supplied `OFFSET`, and filters on a column no
index leads with.

The command extracts the SQL your code issues, records what each statement
does, and turns that shape into advisories with a confidence attached.

## The honesty contract

Read this first, because it is the thing the rest of the command is built
around.

Atlas reads SQL **only where it is statically visible**: a string literal
passed to a `database/sql` method, a constant or single-assignment local
holding query text, and the `.sql` files sqlc generates from. A query built by
a query builder, assembled across functions, or read from configuration is
**recorded as `UNRESOLVED` with the reason** — never dropped, and never given
a guessed shape.

That matters because a zero-valued statement shape is indistinguishable from
"a `SELECT` with no `LIMIT`". Silently analysing the half of a codebase it can
read and reporting a clean bill of health is the failure mode this feature
exists to avoid, so:

- every verb prints the **resolved fraction** ("42 operations: 38 resolved,
  4 unresolved (90% of the data layer analysed)");
- `atlas sql list --unresolved` enumerates exactly what was not analysed and
  why;
- every check that could not run reports itself under **Checks that did not
  run**, rather than passing by default.

### One query, counted once

A sqlc query exists twice in the sources: as the `-- name: X :many` block in
the `.sql` file, and as the generated Go call that hands that same text —
header comment and all — to `database/sql`. Counting both would count the
whole data layer twice, which is not cosmetic: it doubles the operation count
and it moves the resolved fraction, the number a CI gate reads.

So `scan` reconciles them. The **`.sql` definition is the row that survives**:
it is where the query is written, its `:one`/`:many` annotation is better
evidence of the row shape than anything inferable from generated code, and its
`file:line` is where a developer goes to change it. Suppression directives
written at the generated call site are merged onto it, so a directive in
either place still silences the advisory. The count is reported —
"merged 12 generated call site(s) onto the .sql queries that define them" —
because a gap between call sites read and operations reported would otherwise
be unexplained.

Only cross-source pairs merge. Two Go call sites issuing identical SQL are two
places that can be slow, and collapsing them would hide one.

The index checks obey the same rule. Where the target schema is unknown —
no DDL was read, or the query names a table with no `CREATE TABLE` in the
files that were read — the check says it did not run. It never assumes an
index is absent.

## Usage

```
atlas sql scan [path] [flags]     # extract into the Atlas store
atlas sql list [flags]            # what was recorded
atlas sql advise [flags]          # what is wrong with it
atlas sql capabilities [flags]    # which tables each capability touches
```

`scan` reads the working tree and writes the inventory. `list` and `advise`
read the inventory back out of the store, so CI and a developer's terminal
reason over the same evidence, and a stale answer is visibly stale rather
than quietly recomputed against a tree that has moved on.

## What is recorded per operation

| Field | Meaning |
| --- | --- |
| `kind` | `select` / `insert` / `update` / `delete` / `other`, or `unknown` when unresolved |
| `tables` | every table touched, tagged `read` or `write` |
| `predicates` | the columns compared in `WHERE` and `JOIN`, with the operator, and whether the right-hand side is a bind parameter |
| `param_count` | distinct bind parameters (`$1` repeated twice is one parameter; two bare `?` are two) |
| `interpolation` | `concat` or `sprintf` when the query text was built rather than written |
| `caller_data` | the interpolated value traces to a parameter of the enclosing function |
| `has_limit` / `has_offset` / `has_order_by` | the bounding trio, and only for **this** statement — a `LIMIT` inside a subquery, a CTE body or an `IN (...)` list bounds that inner result set and is not credited to the outer one |
| `keyset` | cursor pagination: a `LIMIT`, plus a caller-bound range predicate on the column the statement orders by **first** |
| `offset_bound` | `none` / `parameter` / `literal` / `expression` — where the OFFSET's value comes from |
| `row_scan` | `slice` / `single` / `exec` / `unknown` — what the call site does with the rows |
| `symbol_name` | the enclosing symbol, so the operation joins the rest of the graph |

`keyset` and `offset_bound` exist because conflating pagination styles gives
bad advice. `WHERE (created_at, id) < ($1, $2) ORDER BY created_at DESC
LIMIT 50` is pagination even though it has no `OFFSET`, and advising it to
"add a LIMIT" would be nonsense.

All three parts of `keyset` are required, because all three are what make a
cursor walk bounded:

- **a `LIMIT`.** The cursor says where the page *starts*, not how big it is:
  `WHERE created_at < $1 ORDER BY created_at DESC` returns every row before
  the cursor, which on the first page is the whole table.
- **a caller-bound range predicate.** The cursor value comes from the previous
  page, so it is a parameter. `WHERE created_at < '2024-01-01'` is a constant
  filter.
- **on the leading `ORDER BY` column.** An index walk advances along the
  leading sort column; a range predicate on any other ordered column does not
  move the cursor.

A time window (`WHERE created_at > $1 ORDER BY id LIMIT 100`) and a recursion
depth guard (`WHERE depth < $1`) meet the first two conditions and are not
pagination. Anything short of the bar falls through to `sql.unbounded-list`,
which is suppressible per site for the case where the caller really does apply
the page size itself.

## Advisories

Every advisory carries a **code**, a **`file:line`**, a **confidence**, and a
suggested fix.

| Code | Fires when | Confidence |
| --- | --- | --- |
| `sql.unbounded-list` | a `SELECT` with no `LIMIT` and no keyset predicate whose rows are collected | `high` when the call site builds a slice, `medium` when the row shape is unknown |
| `sql.unstable-pagination` | `LIMIT`/`OFFSET` with no `ORDER BY` — rows can repeat or vanish between pages, silently | `high` |
| `sql.offset-depth` | `OFFSET` takes a caller-supplied value, so cost grows linearly with depth | `high` |
| `sql.possible-injection` | a value that traces to a parameter of the enclosing function is concatenated or formatted into query text | `high` |
| `sql.missing-index` | a filtered table where no index leads with any of the filtered columns | `medium` |
| `sql.select-star` | `SELECT *`, which couples the result shape to the table | `low` |
| `sql.orphan-table` | a table in the schema no query reads or writes | `medium` |
| `sql.unused-index` | a non-unique index whose leading column no query filters or orders on | `low` |

### Why `sql.possible-injection` is so quiet

A false positive here costs the credibility of every other advisory in the
report, so the bar is set where a finding is nearly always worth acting on:
the spliced expression must be a *reference* rooted at a parameter of the
enclosing function.

Deliberately **not** reported:

- `fmt.Sprintf(q, strings.Repeat("?,", n))` — the placeholder-widening idiom.
  The operand is a call, so it is a computed value, not caller data.
- `%d`, `%f` and other non-string verbs. An integer spliced into SQL cannot
  carry a quote or a semicolon.
- a table name formatted in from a package constant.

Which argument a verb reads is worked out the way `fmt` does it, not by
position: `%*s` takes its width from an argument of its own and shifts every
later verb along, and `%[1]s` names its argument outright and moves the cursor
for what follows. Zipping them positionally checks the wrong expression, which
on this check means both missed findings and false ones. Where a format string
contains a directive Atlas cannot account for at all, it stops claiming to
know which argument lands in the text and weighs **every** argument — on a
security check a silent miss is the expensive failure.

Those queries are still recorded, as unresolved with their reason, where a
reader can weigh them. The cost of this choice is real: a caller value that
passes through a helper before reaching the query is missed. That is the
trade, made in the direction of an advisory list people keep reading.

### Why `sql.missing-index` only looks at leading columns

An index on `(tenant_id, created_at)` cannot serve a lookup that filters
`created_at` alone. Counting any listed column as covered would bless the
single most common real-world index mistake, so a table is considered served
only when some index's **first** column is among the filtered ones. `LIKE`
and `IS NULL` predicates are excluded — neither is evidence that an index is
missing.

### What "the schema" means here

`sql_tables` and `sql_indexes` hold what Atlas **read**, not a mirror of a
live database, and they are the schema as of the last migration rather than
the union of everything ever declared:

- **`*.down.sql` files are not read.** A rollback undoes its `up` sibling;
  reading both leaves a table that was created once and dropped once sitting
  in the inventory, and the "26 tables, 65 indexes" line overstates the schema
  by exactly the migrations that have a rollback.
- **`DROP TABLE`, `DROP INDEX` and `ALTER TABLE … RENAME TO` are applied**, in
  file order then statement order. A dropped table leaves the inventory and
  takes its indexes with it; a renamed one carries its indexes across.
- **Columns are still additive.** `ALTER TABLE … DROP COLUMN` and
  `RENAME COLUMN` are not applied: the inventory records *index* columns, and
  rewriting an index definition Atlas never re-read would be a guess dressed
  as a fact.
- **`CREATE INDEX CONCURRENTLY`** and `IF NOT EXISTS` are understood. The
  table is anchored on the `ON` keyword rather than on position, so an index
  spelling Atlas has not met records nothing rather than recording an index
  against a table that does not exist.

### Why the schema-wide checks abstain so readily

`sql.orphan-table` and `sql.unused-index` both assert a negative: *nothing*
queries this table, *nothing* uses this index. A negative asserted over a
partial inventory is simply false, so both run only when every operation
resolved and DDL was actually read. Otherwise they report themselves under
"Checks that did not run" — a table declared orphan on incomplete evidence is
a table someone deletes.

## Suppression

Every advisory has a legitimate exception, and a check with no way to say
"yes, I know" gets turned off wholesale.

**Per site**, with a comment. In Go, on the call's line, in the contiguous
comment block above it, or in the enclosing function's doc comment:

```go
// AllTenants is knowingly unbounded; there are nine rows.
// atlas:sql-ignore sql.unbounded-list
func (r *Repo) AllTenants(ctx context.Context) ([]Tenant, error) {
```

In a `.sql` file, in the query's comment header:

```sql
-- name: ListUsers :many
-- atlas:sql-ignore sql.unbounded-list,sql.select-star
SELECT id, email FROM users WHERE tenant_id = $1;
```

Multiple codes are comma-separated; `all` silences every code at that site.
Trailing prose is ignored, so
`// atlas:sql-ignore sql.select-star -- payload is versioned` reads well.

**Globally**, per invocation: `atlas sql advise --suppress sql.select-star`.

## Flags — `atlas sql scan`

| Flag | Default | Description |
| --- | --- | --- |
| `--schema-dir` | the sqlc `schema` path, plus `db/migrations`, `migrations`, `sql/migrations`, `db/schema`, `schema` where they exist | directory holding DDL; repeatable |
| `--query-dir` | the sqlc `queries` path | directory holding sqlc `.sql` query files; repeatable |
| `--include-tests` | `false` | index queries in `_test.go` files too |

The positional `[path]` defaults to the repository root. `vendor`,
`node_modules`, `dist`, `build` and `testdata` subdirectories are pruned;
`testdata` for the same reason `_test.go` is, since a query in a fixture is
not a production data path.

## Flags — `atlas sql list`

| Flag | Default | Description |
| --- | --- | --- |
| `--unresolved` | `false` | list only what Atlas could not statically resolve |
| `--table` | — | list only operations touching this table |

## Flags — `atlas sql advise`

| Flag | Default | Description |
| --- | --- | --- |
| `--suppress` | — | advisory codes to silence globally; repeatable |
| `--min-confidence` | `low` | drop advisories below this confidence (`low`\|`medium`\|`high`) |

An unknown `--min-confidence` is an error rather than a silent default: a CI
job filtering on a typo'd level would report a clean run forever.

## Flags — `atlas sql capabilities`

| Flag | Default | Description |
| --- | --- | --- |
| `--feature` | — | show only this capability |

`capabilities` rolls the recorded operations up to the capability that owns
them: for each feature, the tables its queries read and the tables they write.
It is the data behind "what does this capability touch" — the answer a privacy
review, a migration blast radius, or a per-capability ERD is drawn from.

The join is `feature_symbols → sql_operations → sql_operation_tables`, so a
query reaches a capability only where the symbol it lives in is linked to one
(`atlas scan` indexes those annotations). The output leads with how many of
the recorded operations belong to any capability at all, because a rollup over
a fifth of the inventory should not read like a rollup over all of it.

**The footprint is a lower bound wherever a capability has unresolved
queries.** Those queries touch tables Atlas could not see, so the row keeps its
count and the line reads `PARTIAL`. A capability whose queries *all* failed to
resolve still gets a row with an empty table set rather than being dropped:
"nothing recorded" and "touches no data" are different answers, and only one
of them is true.

```
$ atlas sql capabilities

  42 operations: 38 resolved, 4 unresolved (90% of the data layer analysed)
  17 of 42 operations belong to a capability

  billing.invoice
    reads:  customers, invoices, line_items
    writes: invoices, outbox
  search.query
    reads:  documents
    writes: (none)
    PARTIAL: 2 of 5 queries could not be resolved; tables only they touch are missing
```

## Example

```
$ atlas sql scan

  indexed 42 operations from /src/api
  42 operations: 38 resolved, 4 unresolved (90% of the data layer analysed)
  schema: 17 tables, 31 indexes from db/migrations

  Unresolved (recorded, not analysed):
      3  query text is an expression atlas cannot statically resolve
      1  query text is assembled with a formatting call

  next: atlas sql advise

$ atlas sql advise --min-confidence high

  42 operations: 38 resolved, 4 unresolved (90% of the data layer analysed)

  sql.possible-injection  internal/repo/search.go:61  [high]
    in SearchRepo.ByColumn
    query text is built by string concatenation from a value the caller supplies (column)
    fix: bind the value as a parameter; if it must be an identifier, validate it against a fixed allowlist
  sql.unbounded-list  internal/repo/audit.go:88  [high]
    in AuditRepo.All
    SELECT over audit_log has no LIMIT and no keyset predicate; the result set grows with the table
    fix: add a LIMIT, or paginate by keyset (WHERE ordered_column < $cursor ORDER BY ordered_column)

  Checks that did not run:
    sql.orphan-table (all): 4 of 42 operations could not be resolved; a table or index cannot be called unused on a partial inventory
    sql.missing-index (table:legacy_jobs): no CREATE TABLE for this table was found in the schema files that were read
```

## `--json`

All four verbs emit the standard v1 envelope. `sql.scan` carries the counters
plus `merged`; `sql.list` carries `operations`, `resolved`, `unresolved`,
`resolved_fraction`; `sql.advise` carries `advisories`, `skipped_checks` and
the same resolution counters; `sql.capabilities` carries `capabilities` (each
with `reads`, `writes`, `operations` and `unresolved`) plus
`linked_operations`.

`resolved_fraction` and `skipped_checks` are part of the contract, not
decoration: a consumer that gates a build on this output needs to know how
much of the data layer produced it and which checks abstained.

## Limits

- Extraction covers Go (`database/sql` call sites) and sqlc `.sql` files.
  Other languages and ORMs are not read; their queries do not appear at all,
  which is a *silence*, not a pass.
- Where the query argument cannot be resolved to any text, Atlas records the
  call only when the receiver looks like a database handle (`db`, `conn`,
  `tx`, `q`, `queries`, …). Proper type resolution needs a fully buildable
  target repository, which Atlas cannot assume. A handle under an unusual name
  is therefore invisible rather than mis-recorded.
- Atlas is not a query planner and not an APM. It says what a query *is* and
  whether an index *could* serve it; it does not estimate cost or read
  `EXPLAIN` output.
- `capabilities` gives the table set behind a feature, not a drawing of it.
  Rendering tables, the queries that touch them and their coverage as an ERD
  is a UI surface this repository does not have; the rollup is the data it
  would be drawn from.
