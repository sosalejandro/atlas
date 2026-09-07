# Performance

Where atlas actually spends its time, measured rather than assumed, and what
issues #109 and #150 changed.

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

# Graph-side: building a scan-sized call graph, cycle check included.
go test ./packages/graph -run NONE -bench BenchmarkGraph -benchtime 3x -count 3

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

Two corpora appear below, and the difference matters when comparing rows.
The §3 ingest figures and the §2 worker sweep are #109's, taken at `f4d5299`
on a 653-file tree. Everything in §1 was re-taken for #150 at `ba00402` plus
that branch, which is a slightly larger tree:

| | #109 (`f4d5299`) | #150 (`ba00402`+) |
|---|---:|---:|
| `.go` files (whole tree) | 653 | 673 |
| `.go` files the pattern pass parses (non-test, outside hidden/vendor dirs) | 386 | 391 |
| files the annotation pass reads and hashes | 661 | 776 |
| symbols the Go scanner emits | 4,999 | 5,100 |
| edges the Go scanner emits | 12,728 | 12,949 |

The #150 counts come from `find` under the walkers' own skip rules (any
directory named `vendor`/`node_modules` or beginning with `.`), and the
symbol/edge counts from a `goscan.Scan` of the tree. **A before/after pair
is only meaningful within one corpus**, so #150's before-numbers were
re-measured on the larger tree rather than compared against #109's.

## 1. Where the scan's time goes

`IndexProject` on this repository, with the TS and Python sub-scanners
disabled and file hashing on, after #150:

| phase | measured | share |
|---|---|---|
| whole `IndexProject` (`--jobs=1`) | 1.26 s | 100% |
| **phase A — `goscan.Scan`** | **0.98 s** | **78%** |
| phase A.5 — EDA pattern recognisers (serial) | 107 ms | 8.5% |
| phase B — annotations + SHA-256 (serial) | 123 ms | 9.8% |

```
ATLAS_BENCH_JOBS=1 go test ./packages/codeindex -run NONE \
  -bench BenchmarkIndexProject -benchtime 3x -count 3
go test ./packages/codeindex -run NONE -bench BenchmarkGoScan_Typed \
  -benchtime 3x -count 3
ATLAS_BENCH_JOBS=1 go test ./packages/codeindex -run NONE \
  -bench BenchmarkPatternRecognizers -benchtime 10x -count 3
ATLAS_BENCH_JOBS=1 go test ./packages/codeindex -run NONE \
  -bench BenchmarkAnnotationWalk -benchtime 10x -count 3
```

Medians of 3 runs; the first two at 3 iterations each, the last two at 10.

**The orchestrator's own per-file parsing is 18% of a scan.** It was 2.9%
when #109 measured it — not because the passes got slower (107 ms and
123 ms against 92 ms and 115 ms on a 20-file-larger tree) but because the
scan around them got six times faster. That is the shape to keep in mind
when reading #109's conclusions below: they were correct about the
proportions of a scan that had this defect in it, and the proportions have
moved.

### The hot spot #109 found, and #150 fixed: `graph.AddEdge`

`Graph.hasPath` has exactly one caller, `graph.AddEdgeKindLineMetaTier`,
which runs it as a cycle check on every edge appended. It used to rebuild
the entire adjacency map from `g.Edges` from scratch on each call:

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

Building an E-edge graph therefore cost O(E²) map inserts and threw away E
adjacency maps for the collector. The fix is to maintain the map
incrementally: `AddEdge` appends one entry in each direction instead of
dropping both maps, and `hasPath` walks the shared map rather than a
private copy. The walk itself is untouched.

**The profile, before and after.** Same command; the before column is
#109's profile of the `f4d5299` tree and the after column is #150's of the
`ba00402`+ tree, so read the **shares**, not the durations — the two totals
are not a speedup measurement. The wall-clock before/after for this
benchmark, taken on one tree, is in the end-to-end table below.

```
go test ./packages/codeindex -run NONE -bench BenchmarkGoScan_Typed \
  -benchtime 3x -cpuprofile /tmp/scan.prof -o /tmp/scan.test
go tool pprof -top -cum /tmp/scan.test /tmp/scan.prof
```

| symbol (cum %) | before | after |
|---|---:|---:|
| `graph.(*Graph).hasPath` | **36.78%** | **below the profiler's cut** |
| `runtime.gcDrain` | 53.52% | 23.28% |
| `runtime.mapassign_faststr` | 20.29% | 1.30% |

Before: `Duration: 61.49s, Total samples = 142.36s (231.51%)`. After:
`Duration: 3.89s, Total samples = 13.83s (355.14%)`, with `hasPath` among
the 683 nodes dropped at `cum <= 0.07s` — under 0.5% of a profile it used
to own more than a third of. What is left at the top is `go/packages`,
`go/types` and `go/ssa`, which is what a type-checking scan is supposed to
look like.

**Isolated at scan scale.** 4,999 nodes, 12,728 edges — the synthetic shape
#109 sized against its corpus — medians of 3 runs × 3 iterations:

```
go test ./packages/graph -run NONE -bench BenchmarkGraph -benchtime 3x -count 3
```

These three benchmarks are new in `packages/graph` with #150, so their
"before" column is the same benchmark code run against the pre-fix
`graph.go`, not a figure carried over from #109. (#109's own pair in
`packages/codeindex` measured 19.96 s with the cycle check and 6.17 ms
without it, on its tree.)

| | before | after | |
|---|---:|---:|---|
| `BenchmarkGraphAddEdge_ScanSized` | 16.55 s | **6.73 s** | **2.5x** |
| | 13.33 GB / 81,838,283 allocs | **3.38 GB / 384,428 allocs** | **213x fewer allocs** |
| `BenchmarkGraphAddEdge_FanIn` | 11.45 s | **7.74 ms** | **1,479x** |
| | 13.74 GB / 81,745,158 allocs | **8.39 MB / 51,360 allocs** | |
| `BenchmarkGraphAppendEdge_ScanSized` (no cycle check) | 2.35 ms | 2.26 ms | unchanged |

`packages/codeindex`'s copy of the scan-sized benchmark, which prices the
same thing from the caller's side, moved **18.50 s → 6.87 s** on the same
runs.

The two AddEdge shapes measure different things, and the gap between them
is the point. `FanIn` points every edge at a node with no outgoing edges,
so each cycle-check DFS stops on its first step and what is left is the
adjacency map alone: that is now flat, which is the acceptance criterion
issue #150 asked for. `ScanSized` uses a stride that keeps the graph
strongly connected, so its remaining 6.73 s is almost entirely the DFS —
`hasPath` still answers a reachability question per edge, and on a densely
connected graph that walk is O(V+E). The 3.38 GB it still allocates is the
per-call `visited` map.

**That remaining DFS cost is real but it is not what a scan pays.** A Go
call graph is nothing like the synthetic ring — 12,949 edges over 5,100
symbols, mostly shallow — which is why removing the rebuild alone took the
end-to-end scan from 8.60 s to 1.15 s (7.5x) while the synthetic case moved
only 2.5x. Anyone tempted to go further (union-find, an incremental
topological order) should note that the flag needs the *path* answer and
not just connectivity, and should bring a measurement from a real tree
rather than from `ScanSized`.

**End to end.** The whole reason #109's parallelisation moved the total
only 0.9% was that 98% of a scan was inside the Go sub-scanner and better
than a third of *that* was this:

```
go test ./packages/codeindex -run NONE -bench BenchmarkIndexProject -benchtime 3x -count 3
go test ./packages/codeindex -run NONE -bench BenchmarkGoScan_Typed -benchtime 3x -count 3
```

| | before | after | speedup |
|---|---:|---:|---:|
| `BenchmarkIndexProject` (`--jobs=12`) | 8.60 s | **1.15 s** | **7.5x** |
| `BenchmarkIndexProject` (`--jobs=1`) | 8.68 s | **1.26 s** | **6.9x** |
| `BenchmarkGoScan_Typed` (phase A) | 8.02 s | **0.98 s** | **8.2x** |

Allocations, same runs:

| | before | after |
|---|---|---|
| `IndexProject` | 67,824,856 allocs / 7.18 GB | **8,914,570 allocs / 829 MB** |
| `GoScan_Typed` | 66,867,213 allocs / 7.04 GB | **7,957,774 allocs / 686 MB** |

The 7.6x drop in allocations is why the GC share fell by more than half.
This was one defect, and it was most of the scan.

**The `Cycle` flag did not move.** `Edge.Cycle` is serialised into the
golden corpus, so "the tests pass" is not evidence — the corpus holds 52
edges and no cycles at all, and cannot see a cycle-flag regression. Both
implementations were run against a frozen copy of this tree and their full
edge sets diffed, every field on every edge:

```
symbols=5100 edges=12949   (96 with cycle=true, both runs)
diff before after -> no differences; md5 93c4c34555560323bf88f61b99656706
```

Identical, including all 96 `cycle=true` edges. The reason it has to be is
structural: `dfs` walks `outgoing[current]` in slice order, the maintained
map appends in the same edge order a rebuild would have iterated, and the
lists stay ordered slices rather than becoming sets. A set here would make
the cycle flag depend on Go's map iteration order — issue #120's failure
mode, and the golden corpus would flap.

### go/packages load mode (issue #109, candidate 3)

Already addressed, and re-checked here. `packages/resolver` loads with
`NeedName | NeedFiles | NeedCompiledGoFiles | NeedImports | NeedTypes |
NeedSyntax | NeedTypesInfo` and deliberately **without** `NeedDeps`
(`packages/resolver/doc.go` records the 0.5 s → 3.6 s difference that
decision was based on). A direct `resolver.Load` of this repository with a
warm build cache measured **355 ms** on #109's corpus. That was 5% of a
scan then. It has not been re-measured since #150, and the share it
represents has obviously moved — do not quote the 5%.

**#150 overturned the second half of this finding.** #109 compared the
default against `--skip-typed-resolution`, which replaces type checking
with the AST name-matching ladder, and concluded they were within noise of
each other:

| | #109 (`f4d5299`) run 1 | run 2 | #150 (`ba00402`+), median of 3×3 |
|---|---:|---:|---:|
| `BenchmarkGoScan_Typed` | 7.13 s | 9.80 s | **0.98 s** |
| `BenchmarkGoScan_ASTOnly` | 6.89 s | 8.32 s | **0.33 s** |

Both arms built their graph through `AddEdge`, so both were paying the same
quadratic cost, and it was large enough to bury the difference between them.
With it gone the two separate cleanly: **type checking is now roughly
two-thirds of phase A** (0.98 s against 0.33 s), and the after-profile
agrees — `go/types` and `go/ssa` are what sits at the top of it now.

The recommendation does not change, but its reason does. Keeping type
checking costs about 650 ms of a 1.26 s scan, and it is worth it because
`atlas edges` would lose the typed tier for 96% of the graph without it.
That is a deliberate trade for edge quality, not the free lunch #109
recorded.

```
go test ./packages/codeindex -run NONE -bench BenchmarkGoScan_ASTOnly -benchtime 3x -count 3
```

## 2. Parallel per-file passes — what it bought, and what it did not

`Options.Jobs` (`atlas scan --jobs`) bounds the worker count for the two
passes the orchestrator runs itself. Default is `GOMAXPROCS`; `--jobs=1` is
strictly serial and is what the determinism suite runs.

```
ATLAS_BENCH_JOBS=<n> go test ./packages/codeindex -run NONE \
  -bench 'Benchmark(PatternRecognizers|AnnotationWalk)' -benchtime 10x -count 3
```

This sweep is #109's, on the `f4d5299` corpus. It measures the two passes
in isolation, neither of which #150 touches, so it has not been re-taken;
the `jobs=1` column was re-measured on the larger tree at 107 ms and
123 ms, which is the same story on 20 more files.

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

End to end, this was worth nothing measurable before #150, and is worth
something now:

```
ATLAS_BENCH_JOBS=<n> go test ./packages/codeindex -run NONE \
  -bench BenchmarkIndexProject -benchtime 3x -count 3
```

| jobs | `IndexProject` before #150 | after #150 |
|---|---:|---:|
| 1 | 8.684 s | 1.258 s |
| 12 | 8.597 s | 1.153 s |
| **difference** | **1.0%** | **8.3%** |

Medians of 3 runs × 3 iterations, on the `ba00402`+ corpus. Treat the
before column's 1.0% as zero: its `jobs=1` runs were 8.05 / 8.68 / 12.33 s,
a 53% spread on a machine that was not quiet, and #109 measured the same
comparison at 0.9% on its own tree and called it noise. The after column's
runs were 1.24–1.28 s and 1.12–1.16 s, which do not overlap.

Nothing about the parallel passes changed here. What changed is the
denominator. **A fixed saving is worth what the rest of the scan is not.**
#109's sweep predicted the two passes would give back 136 ms; that was 1.6%
of an 8.68 s scan and is 11% of a 1.26 s one, and the measured 8.3% is
consistent with it. It is still not the 2x issue #109's acceptance criteria
asked for, and it never could have been — that criterion assumed per-file
parsing dominates a scan, and it does not, even now.

The change was kept even at 0.9%, for two reasons that are also measured
(both of these are #109's figures on its own corpus, unaffected by #150):

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

1. **`graph.AddEdge`'s adjacency rebuild: done (#150).** It was 36.8% of
   scan CPU plus most of the GC pressure, and removing it took
   `IndexProject` from 8.60 s to 1.15 s — 7.5x, and 7.6x fewer allocations.
   Guarded by `BenchmarkGraphAddEdge_ScanSized` / `_FanIn` in
   `packages/graph`, and by
   `TestAddEdge_CostPerEdgeDoesNotScaleWithGraphSize`, which fails if the
   per-edge cost starts tracking the edge count again.
2. **The cycle check's DFS is what is left, and nobody has shown it
   matters.** `hasPath` still walks the graph once per edge. On the
   synthetic strongly-connected `ScanSized` shape that is 6.73 s and
   3.38 GB of `visited` maps; on this repository it is below the profiler's
   cut. Before replacing it with a union-find or an incremental topological
   order, note that `Edge.Cycle` needs the *path* answer rather than plain
   connectivity, and bring a measurement from a real tree — `ScanSized` is
   not one.
3. **Phase A is now type checking, and that is a deliberate 650 ms.**
   `GoScan_Typed` 0.98 s against `GoScan_ASTOnly` 0.33 s. The typed tier
   for 96% of the graph is what the difference buys; see §1. This is the
   opposite of what #109 concluded, because #109's comparison had the
   quadratic graph cost in both arms.
4. **Re-profile `goscan` phase by phase.** With graph construction removed
   the rest of phase A is visible for the first time, and it is `go/types`
   and `go/ssa`. Parallelising phase 2 is worth reconsidering *after* a
   fresh phase-level measurement, not before.
5. Batched ingest and the parallel orchestrator passes: done, above. The
   parallel passes are worth 8.7% end to end now rather than 0.9%, on the
   same code — the denominator moved.
