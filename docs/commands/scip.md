# `atlas scip`

Read an index produced by somebody else's indexer.

SCIP is Sourcegraph's published interchange format. Indexers exist for Java,
Python, Ruby, C#, Rust, TypeScript and more — so ingesting SCIP is how atlas
covers a language it has no scanner for, without writing a scanner, a grammar,
or a name-binding rule for any of them.

This is Tier 3 of [#105](https://github.com/sosalejandro/atlas/issues/105).

## What it is not

**It is not a claim that atlas verified any of it.** Every edge from this path
is recorded at the `imported` tier, and that is deliberate: *"scip-java said
so"* and *"we type-checked it"* are different claims even when they usually
agree. The tier histogram in `atlas doctor` then tells you exactly how much of
your graph rests on someone else's work, and every consumer that gates on tiers
keeps working unchanged.

| tier | meaning |
| --- | --- |
| `typed` | atlas type-checked it (Go, via `go/types`) |
| `name_resolved` | atlas resolved the name across files |
| `syntactic` | the shape of the source said so |
| `imported` | **another indexer said so** — this path |

## `atlas scip inspect`

```bash
scip-go --output index.scip          # or scip-java, scip-python, scip-ruby…
atlas scip inspect --input index.scip
```

```
atlas scip inspect — scip-go 0.2.7

  project root  file:///home/you/atlas
  documents     662
  symbols       12380  (+33283 function-scoped, not addressable across files)
  edges         64996, all at the "imported" tier

  by language
    go             12380 definitions

  of 206322 reference(s):
     64996 became edges
     43057 named a symbol this index does not define (a call out of the indexed set)
     90513 were function-scoped
      7756 sat inside no definition, so there is no caller to attribute them to
```

Run it before wiring an indexer into CI. It answers *"is this index worth
ingesting"* in one command, which is a cheaper question than discovering the
answer after it is in the store.

### The counters are the point

An index whose references all lead outside it produces **no edges** — and
without the counts, that is indistinguishable from a clean run over code with
no calls. The four buckets always sum to the reference total; if they ever do
not, that is an atlas bug and the command says so rather than letting the
arithmetic pass quietly. Same invariant as `stmts_unattributed`
([#100](https://github.com/sosalejandro/atlas/issues/100)) one layer up.

The bucket worth reading first is **"sat inside no definition"**. An indexer
that emits no enclosing ranges puts *every* reference there, which means that
index can contribute symbols but no call graph — a useful thing to learn before
you depend on it.

## Exit codes

Follows the [contract](exit-codes.md):

| code | when |
| --- | --- |
| 0 | the index was read |
| 2 | `--input` missing |
| 3 | the file could not be opened, or is not a SCIP index |

`3` rather than `1` on an unreadable index is the distinction that matters in
CI: atlas could not look, which is a broken pipeline step, not a finding about
your code.

## Reading from stdin

```bash
scip-java index --output - | atlas scip inspect --input -
```

## What is not here yet

`inspect` reports; it does not write to the store. Persisting imported symbols
and edges is the next step of #105, and it is deliberately separate: the
mapping and the honesty counters are worth reviewing on their own before
anything they produce lands in a database people query.
