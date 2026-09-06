# atlas flow

`atlas flow` opens the box. Every other verb in atlas describes symbols and
the edges *between* them; this one describes what happens *inside* one — the
branches, the loops, the cyclomatic complexity, and, where the execution data
can honestly support it, which way each branch actually went.

The reason it exists, in one sentence: **statement coverage says a line ran,
not that the branch was taken both ways.** A function with an untested error
path can show 90% statement coverage while every failure mode in it is
unexercised — and the error paths are what production hits.

## What it computes

| Output | From | Needs a coverprofile |
| --- | --- | --- |
| Control-flow graph per Go function (blocks, edges, branch conditions) | the AST | no |
| Cyclomatic complexity per symbol | the CFG | no |
| Condition enumeration (what MC/DC would need) | the AST | no |
| `flow.unreachable` — code no path can reach | the CFG | no |
| `flow.query-in-loop` — the N+1 candidate | the CFG | no |
| **Decision coverage** — which branch outcomes were taken | CFG + profile | yes |
| `flow.untested-branch` | CFG + profile | yes |

## The honest limits — read this before you act on a number

### 1. Decision coverage is not statement coverage, and the two are never blended

`atlas cov` answers *did the line run*. `atlas flow` answers *was the branch
taken both ways*. They are different questions with different denominators,
and neither is reported as the other. `flow` never prints a statement
percentage of its own; `cov` never prints a decision percentage.

### 2. The denominator is the *decidable* outcomes, never the total

Go's `-coverprofile` records that a basic block executed and how often. It
never records which way a condition went. That is enough to judge some branch
outcomes and not others, so `flow` counts three separate numbers:

- **outcomes total** — every branch outcome the source has.
- **outcomes decidable** — how many of those a statement-coverage profile can
  judge *at all*.
- **outcomes taken** — how many of the decidable ones were taken.

Decision coverage is `taken / decidable`. Dividing by the total instead would
charge a symbol for outcomes no instrumentation could have observed, which
punishes code for the tool's blind spot. When *nothing* is decidable, the
output says `UNAVAILABLE` — never `0%`, because "no branch was covered" and
"no branch could be judged" are different facts and you would act differently
on them.

What is and is not decidable:

| Shape | Decidable? | Why |
| --- | --- | --- |
| `if … { } else { }` | both arms | each arm has its own counter |
| `if … { }`, then-arm returns | both arms | the statement after the `if` is reached only on the false path, so any count there proves it |
| `if … { }`, then-arm falls through | both arms | the successor's count is both paths summed, so the false path was taken exactly when it *exceeds* the then-arm's count |
| the same, **inside a loop** | true arm only | counts accumulate across iterations; differencing them conflates "the false path ran once" with "the loop ran twice", so `flow` **refuses to answer** rather than guess |
| `if … { }` as the last statement | true arm only | the false path increments no counter anywhere |
| `switch`/`select` clause bodies | yes | each clause body has its own counter |
| a `switch` with no `default` | the implicit "nothing matched" arm is decidable only when every clause returns/breaks | otherwise a matched clause reaches the same successor and the two are indistinguishable |
| loop "body entered" | yes | the body has a counter |
| loop "exited by the condition" | only when the body has no `break` | a `break` reaches the statement after the loop without the condition ever going false |
| **`&&` / `\|\|` operand outcomes** | **never** | both operands are instrumented as one block |

### 3. MC/DC is not derivable, at all

MC/DC — modified condition/decision coverage, the DO-178C DAL A metric —
requires showing that each condition *independently* affected the decision's
outcome. Go's instrumentation gives one counter for `a && b`. Which operand
decided the result is not recorded anywhere, and no amount of arithmetic over
statement counts recovers it.

So `atlas flow` will never print an MC/DC number. What it does print is the
enumeration MC/DC would need:

- how many atomic **conditions** exist (the leaves of the `&&`/`||` trees,
  with parentheses and `!` stripped), and
- how many of those are **independently exercisable in principle** — a
  condition repeated inside one decision (`a && (b || a)`) cannot be varied on
  its own, so MC/DC for it is unreachable *by construction*, which is a fact
  about the code worth knowing before anyone tries.

Both are properties of the **source**, not of your tests. Every surface that
prints them prints the caveat next to them. If you need a real MC/DC verdict,
you need condition-level instrumentation that atlas does not have; do not
report atlas's numbers to an auditor as if it did.

### 4. `flow.query-in-loop` is a smell, not a proof

A query reached from inside a loop body is the N+1 shape. It is also
sometimes completely fine: the collection may hold two elements, the call may
be served by a cache, the "loop" may run once. Every finding therefore carries
a **confidence**, and the grading is explicit:

- start at **medium** when the call matched a repository/query naming
  convention (a `Get…`/`List…`/`Query…`-shaped method on a `db`/`tx`/`repo`
  receiver), or **high** when atlas followed a real graph edge from the call
  site into a query symbol;
- **minus one notch** when the loop is not over a collection (a counted
  `for i := 0; i < 3; i++` retry loop is not a per-row loop);
- **minus one notch** when the loop has a path that *skips* the query — which
  is exactly what a correct cache-miss branch looks like.

Two structural facts keep the noise down. A loop with no back edge (a `for`
whose body always `break`s) executes once and yields nothing. And "inside the
loop" is computed on the CFG's natural loop, not by line ranges.

The analysis is **intra-procedural**: a query reached through a helper called
from the loop is a real N+1 and this will not find it. Finding those needs the
call graph, and mixing a guess about them into the same list as the proven
direct case would make the whole list untrustworthy.

## Usage

```
atlas flow build [--root <dir>] [--profile <cover.out>]
atlas flow show <symbol>
atlas flow findings [--kind <kind>]
```

Run `atlas scan` first: `flow` analyses the functions the store already knows
about, and re-parses the files those symbols name.

### `build`

Builds and persists a CFG for every indexed Go symbol.

| Flag | Default | Description |
| --- | --- | --- |
| `--root <dir>` | repo root | The directory the indexed file paths are relative to. |
| `--profile <path>` | (none) | A `go test -coverprofile` file to derive decision coverage from. |
| `--json` *(global)* | off | Emit the stable JSON envelope. |

Use **`-covermode=count`** when producing the profile. The inference that
recovers the false outcome of an `if` with no `else` needs execution *counts*;
the default `set` mode throws them away and every such outcome becomes
undecidable.

```bash
# Run from: a Go project root, after `atlas scan`
go test -covermode=count -coverprofile=cover.out ./...
atlas flow build --profile cover.out
```

```
flow build: 412 symbol(s) across 87 file(s)
  most complex: store.(*Store).Ingest (cyclomatic 24)
  conditions: 908 (871 independently exercisable in principle)
  decision coverage: 61.4% (498 of 811 decidable outcomes taken; 197 outcome(s) not decidable)
  findings: flow.query-in-loop x3
  findings: flow.untested-branch x313
  MC/DC: MC/DC is NOT derivable from Go's statement coverage: …
  note: decision coverage is NOT statement coverage: …
```

Without `--profile`, decision coverage is **absent** from the output and from
the JSON payload — not zero.

### `show`

Prints one symbol's stored graph: blocks, edges with the source condition on
each branch, complexity, decision coverage, and findings. The argument is an
exact qualified name or a unique substring of one; an ambiguous substring is
an error rather than a silent first match.

```bash
atlas flow show svc.Handle
```

```
svc.Handle  svc/handler.go:3
  complexity 3   decisions 2   branch arms 4   defers 0
  conditions 2 (2 independently exercisable in principle)
  decision coverage: 50.0% (1 of 2 decidable outcomes taken; 2 not decidable)
  (statement coverage is a different question and is reported by `atlas cov`; the two are never blended)
  MC/DC: …
  blocks:
      0  entry   lines 3-3
      1  exit    lines 8-8
      2  branch  lines 3-4
      …
  edges:
      2 -> 3   true         [n < 0]
      2 -> 5   false        [n < 0]
      …
```

Block 0 is always the synthetic entry and block 1 the synthetic exit, so a
renderer can anchor a flowchart without loading the whole function first.

### `findings`

Lists what `build` recorded, highest confidence first.

| Flag | Default | Description |
| --- | --- | --- |
| `--kind <kind>` | (all) | `flow.query-in-loop`, `flow.unreachable`, or `flow.untested-branch`. |

The three kinds are deliberately distinct and must not be merged:

- **`flow.unreachable`** is a *structural* fact — no path from the entry
  reaches this block. It is true with no tests at all.
- **`flow.untested-branch`** is a fact about the *test run* — a decidable
  outcome that was never taken. Outcomes nothing could observe never become
  findings, because burying the real ones under unobservable ones is how a
  diagnostic gets muted.
- **`flow.query-in-loop`** is a *suspicion*, with a confidence and a caveat.

## How complexity is counted

Cyclomatic complexity is `1 + Σ (arms − 1)` over every decision, which is
exactly `E − N + 2` on the graph atlas builds. A test asserts the two agree on
the golden fixture; if they ever diverge, the builder has dropped an edge and
*both* numbers are untrustworthy.

Two consequences worth knowing:

- `&&` and `||` count, wherever they appear — including outside a condition
  (`x := a() && b()` still branches at runtime). Hand-rolled complexity
  counters routinely miss these.
- A `select` with N clauses contributes N−1, because the graph has N−1 extra
  edges. `gocyclo` charges N. The difference is the definition, not a bug.

Not modelled, deliberately:

- **`defer`** is recorded (`defers` in the output) but never wired as an edge.
  A deferred call runs at *every* exit; edging it in would put its body on
  every path and count it as a branch arm it is not.
- **Function literals** are separate flows with their own coverage counters.
  Their branches are not folded into the enclosing function — that would
  attribute them to a symbol that does not contain them — and a closure with
  control flow of its own is reported as a warning rather than silently
  dropped.
- **`goto`** edges are not drawn; the graph carries a warning saying so.

## Schema

`flow build` writes `cfg_blocks`, `cfg_edges`, `cfg_symbols`,
`cfg_decision_coverage` and `cfg_findings` (migration 0015). Every row is
keyed by `symbol_id`, so every result joins the graph. See
[docs/schema-v1.md](../schema-v1.md) §5.15.

## Language support

Go only, today. The same shape comes from tree-sitter for TypeScript and
Python once the generic tier exists (#105); the store schema is
language-neutral and does not need to change for it.
