# Performance

Where atlas actually spends its time, measured rather than assumed, and what
issue #109 changed.

Every number on this page came out of a benchmark in this repository. The
command that produced it is printed next to it, so it can be re-run and
disagreed with. Nothing here is an estimate, a projection, or a figure
carried over from an earlier branch — if a measurement is missing, the row
says so instead of guessing.

## How to reproduce

```sh
# Scan-side: the orchestrator, the Go sub-scanner, and the two per-file
# passes. Defaults to scanning this repository.
go test ./packages/codeindex -run NONE -bench . -benchtime 3x -count 2

# Sweep the worker count without recompiling.
ATLAS_BENCH_JOBS=1 go test ./packages/codeindex -run NONE \
  -bench 'Benchmark(PatternRecognizers|AnnotationWalk|IndexProject)' \
  -benchtime 10x -count 5

# Point the same benchmarks at a bigger tree.
ATLAS_BENCH_ROOT=/path/to/other/repo go test ./packages/codeindex -run NONE -bench .

# Ingest-side: a synthetic corpus shaped like this repository.
go test ./packages/store -run NONE -bench BenchmarkIngest -benchtime 5x -count 3

# A profile of either.
go test ./packages/codeindex -run NONE -bench BenchmarkGoScan_Typed \
  -benchtime 3x -cpuprofile /tmp/scan.prof -o /tmp/scan.test
go tool pprof -top -cum /tmp/scan.test /tmp/scan.prof
```

## The machine and the corpus

All figures below were taken on:

- Intel Core i7-10750H @ 2.60GHz, 12 logical CPUs, `GOMAXPROCS=12`
- linux/amd64, Go 1.26.4
- **The machine was not quiet.** Other work ran alongside these
  benchmarks, and the scan-side timings vary by up to 35% run to run
  because of it. Every scan-side figure is the median of the runs shown,
  and where the spread matters it is printed. The ingest-side figures were
  stable to within 2%.

The corpus is this repository, scanned at `f4d5299` plus this branch:

| | |
|---|---|
| `.go` files (whole tree) | 653 |
| `.go` files the pattern pass parses (non-test, outside hidden/vendor dirs) | 386 |
| files the annotation pass reads and hashes | 661 |
| symbols the Go scanner emits | 4,999 |
| edges the Go scanner emits | 12,728 |

## 1. Where the scan's time goes

`IndexProject` on this repository, with the TS and Python sub-scanners
disabled and file hashing on:

| phase | measured | share |
|---|---|---|
| whole `IndexProject` | 7.28 s | 100% |
| **phase A — `goscan.Scan`** | **7.13 s** | **98%** |
| phase A.5 — EDA pattern recognisers (serial) | 92 ms | 1.3% |
| phase B — annotations + SHA-256 (serial) | 115 ms | 1.6% |

`BenchmarkIndexProject`, `BenchmarkGoScan_Typed`,
`BenchmarkPatternRecognizers`, `BenchmarkAnnotationWalk` — the last two at
`ATLAS_BENCH_JOBS=1`, medians of 5×10 iterations.

**The orchestrator's own per-file parsing is 2.9% of a scan.** That is the
first thing the profile said, and it is the opposite of what issue #109
assumed. Parallelising it perfectly could not buy 3% of a scan.

### The actual hot spot: `graph.AddEdge`

A CPU profile of `goscan.Scan` over this repository:

```
go test ./packages/codeindex -run NONE -bench 'BenchmarkGoScan' \
  -benchtime 3x -cpuprofile /tmp/scan.prof -o /tmp/scan.test
go tool pprof -top -cum -nodecount=35 /tmp/scan.test /tmp/scan.prof
```

```
Duration: 61.49s, Total samples = 142.36s (231.51%)

  flat  flat%      cum   cum%
 4.33s  3.04%   52.36s 36.78%  graph.(*Graph).hasPath
 1.07s  0.75%   76.19s 53.52%  runtime.gcDrain
 5.62s  3.95%   28.89s 20.29%  runtime.mapassign_faststr
21.62s 15.19%   39.17s 27.51%  runtime.tryDeferToSpanScan
17.67s 12.41%   47.71s 33.51%  runtime.scanObjectsSmall
```

`hasPath` has exactly one caller, `graph.AddEdgeKindLineMetaTier`, and 55%
of its own time is `mapassign_faststr`. The reason is in
`packages/graph/graph.go`: every `AddEdge` runs a cycle check, and that
check rebuilds the entire adjacency map from `g.Edges` from scratch —

```go
func (g *Graph) hasPath(src, dst shared.SymbolID) bool {
	if src == dst { return true }
	adj := make(map[shared.SymbolID][]shared.SymbolID)
	for _, e := range g.Edges {          // <- every edge, every time
		adj[e.From] = append(adj[e.From], e.To)
	}
	...
}
```

so building an E-edge graph costs O(E²) map inserts and throws away E
adjacency maps. That is 36.8% of scan CPU directly, and it is also what
drives the garbage collector, which accounts for most of the rest.

Isolated at scan scale — 4,999 nodes, 12,728 edges — with the cycle check
and without it:

```
go test ./packages/codeindex -run NONE -bench BenchmarkGraph -benchtime 3x
```

| | time | allocated | allocations |
|---|---|---|---|
| `BenchmarkGraphAddEdge_ScanSized` (with the cycle check) | 19.96 s | 13.3 GB | 81,838,453 |
| `BenchmarkGraphAppendEdge_ScanSized` (append only) | 6.17 ms | 5.6 MB | 20 |

The synthetic graph is more densely connected than a real call graph, so
19.96 s is an upper bound rather than this repository's actual hasPath
cost; the profile's 36.8% is the figure to quote for a real scan. Either
way the shape is the same, and it is the largest single win available in
the scan path by a wide margin.

**This was not fixed here.** `packages/graph/graph.go` is outside the
ownership boundary this change was given, and the fix is not a one-liner:
the cycle flag has to keep meaning what it means, so the adjacency map has
to be maintained incrementally (or cached and invalidated) rather than
simply dropped. The two benchmarks above exist to hand that work a
before-and-after on day one. See issue #109 for the follow-up.

### go/packages load mode (issue #109, candidate 3)

Already addressed, and re-checked here. `packages/resolver` loads with
`NeedName | NeedFiles | NeedCompiledGoFiles | NeedImports | NeedTypes |
NeedSyntax | NeedTypesInfo` and deliberately **without** `NeedDeps`
(`packages/resolver/doc.go` records the 0.5 s → 3.6 s difference that
decision was based on). A direct `resolver.Load` of this repository with a
warm build cache measured **355 ms**, which is 5% of a scan.

Type checking is also not the expensive half of the scan. Compare the
default with `--skip-typed-resolution`, which replaces it with the AST
name-matching ladder:

| | run 1 | run 2 |
|---|---|---|
| `BenchmarkGoScan_Typed` | 7.13 s | 9.80 s |
| `BenchmarkGoScan_ASTOnly` | 6.89 s | 8.32 s |

The two are within this machine's noise of each other, and an earlier pass
measured the AST-only path as the *slower* of the two (8.87 s vs 7.52 s) —
the name ladder does more work per unresolved call than go/types does per
resolved one. There is nothing to win by dropping type checking, and
`atlas edges` would lose the typed tier for 96% of the graph if it did.

## 2. Parallel per-file passes — what it bought, and what it did not

`Options.Jobs` (`atlas scan --jobs`) bounds the worker count for the two
passes the orchestrator runs itself. Default is `GOMAXPROCS`; `--jobs=1` is
strictly serial and is what the determinism suite runs.

```
ATLAS_BENCH_JOBS=<n> go test ./packages/codeindex -run NONE \
  -bench 'Benchmark(PatternRecognizers|AnnotationWalk)' -benchtime 10x -count 3
```

| jobs | pattern recognisers | annotations + SHA-256 | sum |
|---|---|---|---|
| 1 | 92.2 ms | 114.8 ms | 207 ms |
| 2 | 49.6 ms | 62.9 ms | 113 ms |
| 4 | 41.5 ms | 46.2 ms | 88 ms |
| 8 | 33.4 ms | 38.0 ms | 71 ms |
| 12 | 31.9 ms | 39.3 ms | 71 ms |

The passes themselves get **2.8x and 3.0x faster** at `--jobs=8`, and stop
scaling at about 8 workers on a 12-thread machine — both are I/O plus
allocation bound, not purely CPU bound.

End to end, that is worth nothing you can measure:

```
ATLAS_BENCH_JOBS=<n> go test ./packages/codeindex -run NONE \
  -bench BenchmarkIndexProject -benchtime 3x -count 2
```

| jobs | `IndexProject` |
|---|---|
| 1 | 7.28 s, 7.29 s |
| 12 | 7.22 s, 7.22 s |

**0.9%, which is the edge of this machine's noise.** The predicted saving
from the table above is 136 ms of 7,280 ms — 1.9% — and the measurement is
consistent with that. It is not the 2x the issue's acceptance criteria
asked for, and it never could have been: that criterion was written on the
assumption that per-file parsing dominates a scan, and it does not.

The change is kept anyway, for two reasons that are also measured:

**Peak parser memory is now bounded by `--jobs` rather than by repository
size.** The previous pattern pass collected one `*ast.File` per non-test Go
file into a slice and only then ran the recognisers over all of them. Live
heap with every AST retained, measured with `runtime.GC()` +
`ReadMemStats`, is **21.7 MB for 386 files** — and it grows linearly with
the tree, so a repository five times this size would hold roughly five
times that (issue #109's reference tree is 1,985 files). The pass now parses and matches one file per worker and drops
the AST immediately, so the ceiling is `jobs` trees; a single large file's
AST measured **0.30 MB**. This is issue #109's stated "parser memory" risk,
and it is the half of the change that pays for itself.

**`--jobs=1` is a real code path.** With one worker `mapOrdered` calls the
function inline and starts no goroutines at all, so the serial reference
the determinism suite compares against is the code a single-threaded build
would run, not a pool that happens to have one member.

### Determinism

`--jobs=N` output is byte-identical to `--jobs=1`, and that is enforced
rather than hoped for:

- `TestParallel_MatchesSerialOutput` scans the golden corpus at jobs 1, 2,
  4, 8 and `NumCPU+3` and diffs the full rendered index — symbols, edges,
  annotations in walk order, file hashes, pattern matches, the skipped
  ledger and the warning list.
- `TestParallel_ConcurrentScansAreIdentical` runs eight scans at once and
  diffs them all against the serial reference, under `-race` in CI. A race
  that shows up one run in fifty is worse than no parallelism.

The structural reason it holds: the file list comes from a single-threaded
`filepath.WalkDir`, so order is fixed before any worker starts; and each
worker writes `out[i]` for the item it claimed and nothing else, so
completion order cannot reach the output. See `packages/codeindex/parallel.go`.

## 3. Batched ingest

`store.Ingest` wrote one row per SQL statement: an `INSERT` per symbol, plus
an `UPDATE` and a `SELECT` for every symbol that already existed, plus an
`INSERT` per edge, all inside one transaction. A profile said the cost was
not the writing:

```
go test ./packages/store -run NONE -bench BenchmarkIngest_RescanChanged \
  -benchtime 5x -cpuprofile /tmp/ingest.prof -o /tmp/store.test
```

| | before | after |
|---|---|---|
| `sqlite3_prepare_v2` (cum) | **39.29%** | 18.00% |
| `upsertEdgeTx` / `insertEdgesTx` (cum) | 47.29% | 53.82% |
| `conn.bind` (cum) | below the cut | 25.27% |

39% of the ingest was SQLite re-parsing the same `INSERT` text, thousands of
times, because a new statement was compiled per call. Every additive write
now batches into multi-row `VALUES` statements chunked against a
conservative 999 bound-parameter ceiling (`rowsPerChunk`), and the two
per-row reads — the unchanged-file check and the symbol id lookup — became
chunked `IN` queries. Preparation drops to 18% of a much smaller total, and
parameter binding takes its place as the top cost, which is what a batched
write is supposed to look like.

Measured on a synthetic corpus shaped like this repository (661 files,
5,288 symbols, 10,576 edges, 200 annotations). Baseline taken by restoring
`HEAD`'s `ingest.go` in place and re-running the identical harness; each
figure is the median of 3 runs × 5 iterations:

| benchmark | before | after | speedup |
|---|---|---|---|
| `BenchmarkIngest_Fresh` — empty DB, everything new | 546.9 ms | **260.1 ms** | **2.10x** |
| `BenchmarkIngest_RescanChanged` — every row exists, every file edited | 759.1 ms | **174.4 ms** | **4.35x** |
| `BenchmarkIngest_RescanUnchanged` — every hash matches | 83.2 ms | **45.5 ms** | **1.83x** |

Allocations, same runs:

| benchmark | before | after |
|---|---|---|
| Fresh | 348,861 allocs / 15.7 MB | 290,362 allocs / 24.9 MB |
| RescanChanged | 546,827 allocs / 23.6 MB | 268,091 allocs / 19.8 MB |
| RescanUnchanged | 164,144 allocs / 6.6 MB | 127,518 allocs / 7.3 MB |

The fresh case allocates *more* bytes than it did: a batch builds an
`[]any` of up to 999 bound parameters per statement, and on a cold database
there are no lookups to save. That is a deliberate trade — 9 MB of
short-lived argument slices for half the wall time — and it is recorded
here rather than left for someone to discover.

`RescanChanged` gains most because it lost the most work. Pre-change every
already-known symbol cost an `INSERT OR IGNORE`, an unconditional `UPDATE`
of its position and a `SELECT` for its id; now one chunked `SELECT` reads
the stored rows up front, the `UPDATE` runs only for declarations that
actually moved, and the `INSERT` batch covers the rest. `symbols` has no
`updated_at`, so a skipped no-op `UPDATE` is invisible to every reader.

### What batching did not change

Row order, and therefore surrogate ids. Symbols are inserted in index
order, edges in graph order, and both go out in chunks of that same
sequence — a reordered batch would silently renumber the graph, which is
the failure mode #97 already cost this project once. `file_hashes` is now
written in sorted path order rather than Go's map order, so which new path
gets which rowid stopped being a coin flip.

### What is still row-at-a-time

Two loops were left alone, deliberately:

- **`SetSymbolPatternMatches`** — one `UPDATE` per symbol with EDA matches.
  This repository produces zero, so there is nothing here to measure and
  nothing to justify batching against. A repository where it matters should
  be profiled first.
- **Feature materialisation** — one `LookupSymbolAtOrAfterLine` per
  feature/contract annotation plus an `EnsureFeature` and a
  `LinkFeatureSymbol` per id. 184 annotations on this repository; the
  lookup is a per-annotation question with a per-annotation fallback path,
  and collapsing it into a batch would trade real complexity for a cost
  nobody has shown to be significant.

## 4. Summary — what to do next

Ordered by measured value:

1. **Cache or incrementally maintain `graph.Graph`'s adjacency map so
   `AddEdge` stops rebuilding it.** 36.8% of scan CPU, plus most of the GC
   pressure. Benchmarks are already written
   (`BenchmarkGraphAddEdge_ScanSized` vs `BenchmarkGraphAppendEdge_ScanSized`).
   Not done here: `packages/graph` was outside this change's ownership.
2. **Profile `goscan`'s phase 2 and phase 3 separately.** With graph
   construction removed, what remains of the 98% becomes visible for the
   first time. Parallelising phase 2 is worth reconsidering *after* that
   measurement, not before.
3. Batched ingest and the parallel orchestrator passes: done, above.
4. Do not spend time on the go/packages load mode. It is already minimal,
   it costs 355 ms of a 7.3 s scan, and dropping type checking measures no
   faster than keeping it.
