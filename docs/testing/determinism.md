# Determinism

Atlas's pitch is "the numbers are true". A number that changes when nothing
in the source changed is not true, it is a coin flip with a decimal point.
This document describes the property, the tests that enforce it, the corpus
they run against, and the places where the property does not hold yet.

## The property

> For a fixed source tree and fixed options, every scan produces the same
> symbols, the same edges, the same positions, the same annotations and the
> same persisted rows — independent of iteration order, scheduling, and the
> absolute path of the checkout.

Ordering of the *containers* is not part of the contract: `Result.Symbols`
is materialised by ranging over `Graph.Nodes`, which is a map, so the slice
order genuinely varies run to run. Content is part of the contract. The
tests therefore compare canonicalised (sorted) renderings, which keeps the
comparison ordering-independent without making it content-insensitive —
duplicates survive as duplicate lines, so counts stay pinned too.

## Why this is a test and not a code review

Two determinism bugs reached `main` this year and both were found by hand:

- map-iteration order in the fuzzy resolvers (PR #99);
- `LastInsertId` returning a neighbouring row's id (#97), which wired edges
  to the wrong symbols while `symbols: N  edges: M` stayed reassuringly
  stable.

Neither would have been caught by any test in the repo. Both are
disqualifying for the regulated tier (#119), where "the tool produces the
same answer twice" is evidence you have to be able to produce. And the
moment #109 parallelises the scan, the whole class comes back silently.

## The suite

| Test | Package | What it pins |
| --- | --- | --- |
| `TestDeterminism_GoScanner_ScanIsReproducible` | `packages/codeindex/go` | Repeated scans of the corpus under varying `GOMAXPROCS`, across four option sets (plain, pre-resolved routes, `SkipTests`, `EntryPoints`), render byte-identical. |
| `TestDeterminism_GoScanner_IsRootRelative` | `packages/codeindex/go` | The corpus copied into two differently-named temp directories scans identically, and no absolute path leaks into the output. |
| `TestGoldenCorpus_SymbolsAndEdgesMatchSnapshot` | `packages/codeindex/go` | The corpus scan matches the snapshot checked in beside it. |
| `TestGoldenCorpus_LineEndingsDoNotMoveTheSnapshot` | `packages/codeindex/go` | Scanning the corpus from CRLF sources produces the same canonical document as scanning it from LF sources. |
| `TestDeterminism_Ingest_TwoStoresAgree` | `packages/store` | Two independent scan-and-ingest runs of the same tree produce identical `symbols`, `edges`, `annotations`, `features` and `feature_symbols` rows. |
| `TestDeterminism_Ingest_SurrogateIDsFollowQualifiedNames` | `packages/store` | Every persisted edge resolves, through surrogate ids, back to the same pair of qualified names the scanned graph recorded. |

CI runs `go test ./... -race`, so all of them run on every push. There is no
build tag and no opt-in.

### What actually shakes the scanner

`GOMAXPROCS` buys nothing today — `goscan.Scan` is single-threaded. It is
varied anyway as a forward guard for #109, and it costs milliseconds.

The thing with teeth right now is Go's per-`range` map-order randomisation.
`scanContext.funcLookup`, `scanContext.structFields` and `Graph.Nodes` are
all maps that every `Scan` call ranges over afresh, so repeated scans in one
process really do walk them in different orders. This was verified by
deleting the `sort.Strings` from the canonicaliser: the "identical" runs
then differ on most lines.

### The store side is not a repeat of the scanner side

Ingest turns qualified names into surrogate row ids, and a surrogate id is
exactly where a determinism bug hides without moving a single count. So the
store tests read every row back **through qualified names**, never through
ids: a mis-assigned surrogate surfaces as an edge between the wrong two
names. That is the query that would have caught #97.

The two runs go into two independent stores rather than one store ingested
twice. A second ingest into the same store takes the `INSERT OR IGNORE`
paths and would pass even if the first ingest had written wrong rows.

## The golden corpus

`packages/codeindex/go/testdata/goldencorpus/` is a layered Go service in 23
source files — `cmd` -> `handlers` -> `services` -> `persistence`, plus a
platform package and an outward-facing client. It is small enough to read
in one sitting and shaped like real code, which matters: the private
monorepo behind `NUTRITION_ROOT` is a bus factor and a CI hole, and every
claim verified only there is a claim nobody else can check.

It deliberately carries the hazards we have been bitten by:

| Hazard | Where |
| --- | --- |
| SymbolID collision across packages | `internal/platform/config` and `internal/persistence` both declare `Config.Validate` |
| Unexported plain function (dropped) vs unexported method (kept) | `config.isBlank` vs `StdLogger.write` |
| Interface with two implementations | `persistence.OrderRepository`, implemented by the memory and postgres repositories |
| Two handler types sharing a method name | `OrderHandler.Get` and `AdminHandler.Get` |
| Generated code, both shapes | `internal/persistence/queries_gen.go` (scanned) and `internal/persistence/generated/` (skipped) |
| Feature annotations on tests | three `_test.go` files carrying `@atlas:feature` |

Nothing in it compiles, and it is not supposed to. The scanner is AST-only
(no `go/types`, no module resolution), so the import paths are fictional.

### Reading the snapshot

`.golden/symbols_edges.txt` holds one record per line, sorted:

```
# symbols=49 edges=41 warnings=0
EDGE	<from>	<to>	<kind>	<line>	cycle=<bool>	ambiguous=<bool>	meta=<meta>
SYM	<id>	<kind>	<path>	<line>	<col>	<package>	<signature>	<doc>
```

Signature and doc are Go-quoted so a newline inside a doc comment cannot
forge a record boundary.

The snapshot is **not a correctness oracle**. Several lines record
behaviour we would like to change — `Config.Validate` resolving to whichever
package the directory walk reached first, the interface-typed
`OrderRepository` calls being dropped rather than recorded. It is a change
detector: a scanner edit that moves a symbol, drops an edge or reclassifies
a kind shows up as a reviewable diff in the PR that causes it, and the
author either explains the improvement or discovers a regression.

### Regenerating it

```
go test ./packages/codeindex/go -run TestGoldenCorpus -update
```

Commit the regenerated file in the same commit as the change that moved it,
and say in the commit body why each group of lines moved.

### The snapshot on Windows

Issue #143 asserted that the golden snapshot "bakes in path separators", and
a normaliser was written into `symbolLine` to fix it. That normaliser was a
no-op and has been dropped. This section records what was actually measured,
so nobody writes it again.

**Separators are not the problem.** Every `Position.Path` the Go scanner
emits goes through `filepath.ToSlash` at construction — see the `relOrSelf`
call sites in `scanner.go` — as do the package directory in a qualified
`SymbolID` and the two file paths in the collision warning. A second
normalisation at the rendering layer cannot change a byte of any real scan
on any platform. `TestDeterminism_GoScanner_IsRootRelative` already pins the
"no host path leaks into the document" half of this.

**Line endings are.** Git for Windows installs with `core.autocrlf=true`, so
without intervention a Windows checkout rewrites every text file to CRLF.
Two things could break, and only one of them does:

- *The snapshot file.* `canonicalize` renders `\n`; `os.ReadFile` returns
  whatever is on disk. On a CRLF checkout all 109 lines differ, and the diff
  blames the scanner for the checkout. This is real, and it is pinned two
  ways: `.gitattributes` at the repo root holds the corpus at `eol=lf` in the
  working tree, and `TestGoldenCorpus_SymbolsAndEdgesMatchSnapshot` checks
  for CRLF before comparing so the failure names the cause instead of
  printing a phantom diff.
- *The corpus sources.* Measured, and they do **not** move the snapshot:
  `TestGoldenCorpus_LineEndingsDoNotMoveTheSnapshot` scans the same corpus
  from LF and from CRLF and asserts the two documents are byte-identical.
  Go's tokeniser strips CR from comment literals, signatures are rendered
  from the AST rather than from source bytes, and the generated-header probe
  reads through a `bufio.Scanner` (whose `ScanLines` drops the CR). That last
  one is the fragile link — rewriting the probe to `strings.Split(s, "\n")`
  makes `generatedHeaderRe`'s `$` anchor miss, `queries_gen.go` stops being
  recognised as generated, and two symbols and two edges appear. That mutant
  passes the rest of the determinism suite and fails only this test, which is
  the reason the test exists.

Neither of these was the whole reason the Windows CI leg is red; see
`.github/workflows/ci.yml` for what is still outstanding.

### The third thing that can move the snapshot: a path lookup that misses

The snapshot records a `resolution_tier` on every edge, and `typed` is
produced by `packages/resolver`. That package indexes type-checked files by
absolute path, and the two sides of the index come from different places:
the entries are the paths `go list` reported, the queries are the paths the
scanner's `filepath.WalkDir` produced. On Linux those two strings are always
identical and the question never comes up.

On Windows one file has several equally correct spellings — `C:\` versus
`c:/`, `Users\RUNNER~1` versus `Users\runneradmin` (GitHub's runners hand out
the first as `%TEMP%`, the go tool reports the second) — and a mismatch
**fails silently**. Nothing errors; the file is simply not type-checked,
every call in it falls back to name matching, and the only visible effect is
that its edges leave the `typed` tier. That is a snapshot diff on one
platform, produced by a lookup, with no error message anywhere in between.

Closed in three layers, all in `packages/resolver`:

- *Textual*, `pathkey.go`. One `pathKey` function canonicalises separators,
  cleans the path, and folds case where the filesystem does, and BOTH the
  insert and the query go through it. The platform is a parameter
  (`pathKeyOn(p, windows bool)`) so the Windows rules are asserted from a
  Linux run; a table that read `runtime.GOOS` would exercise the Windows
  branch only on the platform CI cannot gate on, which is how this survived
  as long as it did.
- *Filesystem*, `Program.lookup`. `RUNNER~1` and `runneradmin` are one
  directory and no string rule says so, so on a miss the lookup asks
  `filepath.EvalSymlinks`. `TestSyntax_ResolvesAThirdSpellingThroughTheFilesystem`
  reproduces exactly that relationship on a host with no 8.3 names, using a
  symlink — the POSIX instance of "two real paths, one file".
- *End to end*, `TestTypeChecked_AgreesWithWhatTheScannerWalks`. Every
  non-test `.go` file in the golden corpus, addressed the way a walk
  addresses it, must be answerable. This is the assertion that fails on
  Windows if the keying is ever unwired; on Linux it is true either way,
  which is the point — it is there to gate the platform that cannot check
  itself.

What has *not* been done is observing any of it run on Windows. The layers
above close the failure classes that are known and reproducible from here;
they are not a substitute for a green run.

### Adding to the corpus

Two footguns, both discovered while building it:

1. **The `@api` proximity rule.** The scanner attaches an `@api` annotation
   to the next function declared within ten lines of it. Putting a small
   unexported helper just after an annotated handler silently creates an
   endpoint -> helper edge. `internal/handlers/decode.go` exists to keep
   `OrderHandler.decode` outside that window.
2. **`fuzzyResolve` picks the first map hit.** Any call site whose variable
   name is a substring of two receiver type names that share the called
   method is genuinely non-deterministic today (see below). Do not add one
   unless you are also fixing the resolver.

## Known gaps

### `fuzzyResolve` is still order-dependent

`(*scanContext).fuzzyResolve` in `packages/codeindex/go/scanner.go` returns
the first matching entry from a `range` over `funcLookup`. When two receiver
types both contain the variable name and both declare the method, the callee
is chosen by map order. This is the same class as PR #99, in a resolver that
PR did not reach.

Minimal reproduction — scanning this single file 50 times resolved
`handler.Get` to `AdminHandler.Get` 45 times and to `OrderHandler.Get` 5
times:

```go
package handlers

type AdminHandler struct{}

func (h *AdminHandler) Get(id string) string { return id }

type OrderHandler struct{}

func (h *OrderHandler) Get(id string) string { return id }

func Dispatch(id string) string {
	handler := &OrderHandler{}
	return handler.Get(id)
}
```

The golden corpus deliberately does **not** contain such a call site, because
the suite has to be green while the resolver is not fixed. The fix belongs
with `scanner.go`: either return the sole candidate or nothing (the shape
`fuzzyResolveMethod` already uses), or sort the candidates and take a stable
winner. Once it lands, add the snippet above to the corpus and the coverage
follows for free.

### Cycle flags depend on edge insertion order

`Graph.AddEdgeKindLineMeta` sets `Edge.Cycle` from `hasPath(to, from)`
against the graph *as built so far*, and `extractCalls` ranges over
`funcLookup`. In a source tree with a genuine call cycle, which edge of the
cycle gets flagged therefore depends on map order. The corpus has no call
cycle, so the suite does not currently exercise this; adding one would make
the suite red until the flag is computed as a post-pass over the finished
graph.

### Coverage of the other scanners

The suite covers the Go scanner and the store. The TS and Python
sub-scanners run out of process and are not yet in the corpus — issue #120
asks for one corpus per language plus a polyglot one, and this is the Go
slice of that. `NUTRITION_ROOT` remains an optional extra
(`packages/store/ingest_nutrition_integration_test.go`), not the only real
test bed.

## Rules of thumb for new code

- Never let a `range` over a map decide an output value. Collect, sort,
  then pick — or require exactly one candidate.
- Never let a wall clock into a derived value. `GeneratedAt`,
  `last_scanned` and `created_at` are recorded state; nothing downstream may
  branch on them.
- Prefer a stable key over a surrogate id in anything that crosses a
  process or file boundary.
- When you add a resolution heuristic, add the ambiguous case to the corpus
  in the same PR.
- When a comparison's correct answer depends on the host — path separator,
  filename case, environment-variable name case — take the platform as a
  **parameter** and assert both answers in one table, then pin the
  host-bound wrapper to it in a one-line test. Reading `runtime.GOOS` or
  `filepath.Separator` inside the function under test means the branch that
  matters runs only on the platform whose CI leg is advisory, which is the
  same as not testing it. `packages/resolver/pathkey.go`,
  `packages/codeindex/{py,ts}/scanner.go` (`buildScannerArgsSep`) and
  `packages/codeindex/{py,ts}/hostenv.go` are the worked examples.
