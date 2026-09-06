# atlas mcp

```
atlas mcp [--max-features N] [--max-symbols N] [--max-edges N] [--max-tests N]
atlas mcp --json          # describe the server; do NOT serve
```

`atlas mcp` runs a [Model Context Protocol](https://modelcontextprotocol.io)
server on stdio, exposing the atlas index to a coding agent as tools it calls
directly — instead of shelling out to the CLI and inventing a parser for
`--json`.

A coding agent re-derives, badly and every session, what atlas already knows
precisely: which symbols implement a capability, what a change would affect,
which tests cover it. That lives in SQLite already. MCP is the protocol the
agent already speaks.

## The one thing to understand first

**Every answer says how far to trust it, and this is the point of the whole
surface.**

An index can be wrong in three ways an agent cannot detect on its own, and each
one has a field:

| The risk | The field | What it means |
| --- | --- | --- |
| The list is a guess, not evidence | `surface_source` | Which derivation produced it (see below) |
| The list is incomplete | `truncated` | Rows were withheld. There are more. |
| Atlas was never given the data | `no_data` | Not "there is none" — "nobody ran the command" |
| The spans predate the working tree | `index_freshness` | The file moved since the scan; the line numbers are wrong |

An agent that ignores these will confidently delete a symbol with 400 callers
because a capped `callers()` showed it five.

## Read-only, by construction

No tool writes to the store, runs a scan, or executes anything.

That is enforced by the type system, not by review: the tool handlers hold
`GraphIndex` / `CoverageIndex` / `Scorer`, which declare only reads. The
writable store ports (`Upsert`, `Link`, `Insert`, `Ingest`, `DeleteByFile`) are
not reachable from any value the `packages/mcp` tree holds. An agent-facing
surface that *can* write is a surface that *will* corrupt the index on a
hallucinated call, and the index is what every other atlas answer derives from.

Every tool also carries `annotations.readOnlyHint: true`, so a client knows
this without having to call anything.

## Wiring it into a client

```json
{
  "mcpServers": {
    "atlas": {
      "command": "atlas",
      "args": ["mcp"]
    }
  }
}
```

Run it with the repository as the working directory: `atlas mcp` resolves
`.atlas/atlas.db` the same way every other subcommand does, and honours
`--db-path` and `--config`.

Starting the server in a repo that has never been scanned is fine and
deliberate — a client launches it when the editor session starts, which is
routinely before anybody has run `atlas scan`. Every tool then answers with a
structured `no_data` naming the command to run, which is the useful thing to
say.

`stdout` carries the protocol and nothing else, as the stdio transport
requires. Diagnostics go to `stderr`.

## Tools

| Tool | Answers |
| --- | --- |
| `find_feature(query, limit?)` | Which features match a name or title |
| `feature_surface(feature_id, limit?)` | Which symbols implement it — **with the derivation's provenance** |
| `symbol_info(qualified_name)` | Kind, file, line span, package, owning features, **distinct** caller/callee counts |
| `callers(qualified_name, limit?)` | Who calls it, and from which file:line |
| `callees(qualified_name, limit?)` | What it calls, and from which file:line |
| `tests_covering(qualified_name, limit?)` | Which tests actually executed it, and how many statements |
| `coverage_for(feature_id)` | The feature's health on the current coverage frontier |

Every tool declares a full JSON Schema for its arguments. Print the catalog
with schemas:

```
$ atlas mcp --json | jq '.result.tools[] | {name, input_schema}'
```

### `caller_count` / `callee_count` count symbols, not call sites

The `edges` table is keyed on `(from, to, kind, file, line)`, so it holds one
row per **call site**. `symbol_info` counts the **distinct symbols** at the far
end of those edges instead: a function that calls `Pay` five times is one
caller, not five.

That is the number the question "is this safe to change" is actually about.
`callers` / `callees` still return one row per site, because an agent citing a
call needs its `file:line` — so their row counts are usually larger than the
counts on `symbol_info`, and `symbol_info`'s `notes` say so.

## `surface_source`: how a feature's implementation was derived

`feature_surface` never returns a symbol list without saying where it came
from. The four tiers, strongest first:

| `surface_source` | Derivation | How much to trust it |
| --- | --- | --- |
| `dynamic` | The union of what this feature's own tests executed, minus symbols nearly the whole suite runs | Execution evidence. Correct through interface dispatch, DI and reflection. |
| `static` | A call-edge walk (depth 3) from the annotated symbols | Only what the scanner resolved. **A lower bound.** |
| `package-anchor` | Every production symbol in the annotated symbols' package | Whole-package granularity. May include unrelated code. |
| `direct-links` | Only the symbols a human annotated | No evidence behind it at all. The real implementation is almost certainly larger. |

`dynamic` requires a per-test ingest (`atlas cov sync --per-test`). Without
one, the best available answer is `static`, and the tool says so.

The same string appears on `coverage_for`, because a coverage number whose
denominator's provenance is invisible is how issue #84 survived three releases.

## Bounds and truncation

Every result set is capped. An unbounded `callers()` on a hot utility returns
thousands of rows and evicts the agent's working context.

A **silent** truncation would be worse: the agent then reasons from a partial
graph believing it is complete. So whenever rows are withheld, the response
carries:

```json
"truncated": {
  "returned": 100,
  "total": 1284,
  "limit": 100,
  "note": "TRUNCATED: showing 100 of 1284 call edges. The remaining 1184 are NOT in this response — do not conclude they do not exist. There is NO cursor and no offset: calling this tool again cannot retrieve them, and `limit` may only narrow the server cap, never exceed it. Ask a narrower question, or read the complete set outside MCP with the atlas CLI (e.g. `atlas trace --json` for call edges). The server cap itself is set by the operator with `atlas mcp --max-features/--max-symbols/--max-edges/--max-tests`."
}
```

The absence of the block is a positive statement that the list is complete.

**Truncation is not pagination, and the note says so.** These results are capped
but *not* paginated: every `inputSchema` is `additionalProperties: false` with
no `cursor`, `offset` or page token, so there is no call an agent can make to
fetch the withheld rows. A note telling a model to "raise `limit`" when it has
already hit the cap would be an instruction the protocol cannot satisfy, and the
model would burn a turn discovering that. The rows past the cap are reachable
only by narrowing the question, or from the CLI, which is not capped.

The per-call `limit` argument may only *narrow* the server's cap — a client
asking for 100000 rows gets the cap, plus the `truncated` block telling it so.
Raise the cap itself with `--max-edges` and friends on the command line, which
is where an operator can see it.

Defaults: 50 features, 200 symbols, 100 call edges, 100 tests.

## `no_data`: the difference between "none" and "not measured"

An empty list reads to a model as a confident *there is nothing*. When the real
situation is *nothing has been scanned*, that is a lie the agent will act on.

So a tool that cannot answer returns a `no_data` envelope **instead of** the
list — the empty array is not merely `null`, it is absent, so there is nothing
to misread:

```json
{
  "tool": "tests_covering",
  "no_data": {
    "reason": "no-per-test-evidence",
    "detail": "the current coverage frontier records THAT symbols ran, not WHICH test ran them; per-test attribution needs a per-test ingest",
    "run": "atlas cov sync --framework go-cover --per-test <dir of per-test coverprofiles>"
  }
}
```

| `reason` | What is missing | Fix |
| --- | --- | --- |
| `index-empty` | No symbols at all | `atlas init` / `atlas scan` |
| `no-features-annotated` | Code is indexed; nothing says what it is for | Add `@atlas:feature <id>`, re-scan |
| `feature-has-no-linked-symbols` | The feature exists; nothing is annotated for it | Annotate the implementation, re-scan |
| `no-coverage-frontier` | No coverage run ingested | `atlas cov sync` |
| `no-per-test-evidence` | Coverage exists, but only as a union | `atlas cov sync --per-test` |

A genuinely empty *match* — `find_feature("zzz")` against a populated store —
returns an empty list and no `no_data`, because that IS the answer.

## `index_freshness`

An agent handed a symbol at `pay.go:20` will open `pay.go:20`. If the file has
changed since the scan, that line is now something else — not approximately
wrong, arbitrarily wrong, because one inserted line at the top shifts every
span below it.

Results that cite spans therefore carry the block below. That is
`feature_surface`, `symbol_info`, `callers`, `callees` and `tests_covering` —
every tool whose rows carry a `file` and a `line`. (`coverage_for` returns
scores rather than spans, and `find_feature` returns feature ids, so neither
carries one.)

```json
"index_freshness": {
  "files_checked": 3,
  "untrustworthy_files": ["pkg/checkout/pay.go (stale)"],
  "note": "the working tree has moved since the scan: spans in these files no longer point where they did. Re-run `atlas scan` before citing line numbers in them."
}
```

States come from `packages/indexfresh`: `current`, `stale`, `absent`,
`deleted`, `unreadable`. Only `current` is safe to cite.

## Protocol details

- **Transport**: stdio. Messages are newline-delimited, per the specification.
  LSP-style `Content-Length` framing is also accepted on input, and answered in
  kind — several editor-embedded hosts inherited that framing from their
  language-server plumbing, and a server that refuses it simply hangs at
  `initialize` with no diagnostic anywhere.
- **Methods**: `initialize`, `notifications/initialized`, `ping`, `tools/list`,
  `tools/call`. Anything else is `-32601`.
- **Protocol versions**: `2025-11-25` (preferred) and `2025-06-18`. A client
  asking for either gets that one back; a client asking for anything else gets
  the preferred version, per the spec's negotiation rule. The `2026-07-28`
  revision replaced the `initialize` handshake with per-request version
  declaration and a `server/discover` RPC, and is not yet supported.
- **Capabilities**: `tools` only. Advertising `resources` or `prompts` we do not
  implement would make a conforming client issue requests that can only fail.
- **Errors** follow the specification's two-channel split:

  | Situation | Channel |
  | --- | --- |
  | Unknown method | `-32601` |
  | Unknown tool, missing/mistyped argument, malformed params | `-32602` |
  | Malformed JSON frame | `-32700`, `id: null` (the connection survives) |
  | Request before `initialize` | `-32600` |
  | No such feature / no such symbol | Result with `isError: true` |
  | Database failure | `-32603` |

  The last two are the important pair. A protocol error says *the call was
  wrong* and the model must fix its arguments; an `isError` result says *the
  question was wrong* and the model must ask a different one — which it can
  only do if it can read the explanation.

- Every result is returned **twice**: as `structuredContent`, and serialised
  into a `text` content block. The duplication is the spec's own
  recommendation, and load-bearing in practice — most clients shipping today
  render only `content`.

## Shutdown

Close the server's stdin. `Serve` reads EOF as the spec's shutdown signal and
exits cleanly. `SIGINT` / `SIGTERM` also stop it.

## `--json` describes; it does not serve

`atlas mcp` cannot honour the global `--json` envelope while serving: stdout
*is* the protocol stream, and an envelope written onto it is precisely the
"anything that is not a valid MCP message" the stdio transport forbids.

`--json` therefore prints the catalog and exits without serving — the server's
name and version, the transport, the protocol versions, the effective limits,
and every tool with its full input schema. That is what an operator wiring the
server into a client config actually wants to see.

## Known limitations

- **No `doc` or `signature` on `symbol_info`.** Atlas indexes declarations, not
  source text: neither is in the schema. `symbol_info` says so in its `notes`
  rather than omitting the fields silently, because an absent `doc` reads as
  "this symbol has no doc comment". Read the declaration at the reported
  `file:line`.
- **`coverage_for` omits the `annotation_freshness` signal.** That signal shells
  out to `git blame` once per annotation site, which is too slow to run inside a
  request an agent is blocking on. The audit re-normalises over the remaining
  signals; run `atlas audit --feature <id>` for a score that includes it. The
  result says so in its `notes`.
- **One request at a time.** The store is a single SQLite connection and an
  agent's calls are serialised by its own turn structure, so concurrency would
  buy nothing while costing a write lock on the output stream.
- **No pagination cursor on `tools/list`.** Seven tools fit in one page.
- **No pagination on tool results either.** Results are capped, and a cut one
  says so, but the withheld rows cannot be fetched through this server: no tool
  takes a `cursor`, `offset` or page token. Narrow the question, or use the CLI.

## Related

- [scan.md](scan.md) — building the index the server reads
- [cov-per-test.md](cov-per-test.md) — the ingest that unlocks the `dynamic`
  surface and `tests_covering`
- [audit.md](audit.md) — the score `coverage_for` reports
- [trace.md](trace.md) — the same call-graph walk, for humans
