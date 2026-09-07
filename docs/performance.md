# Performance

Where atlas actually spends its time, measured rather than assumed, and what
issues #109 and #150 changed.

Every number on this page came out of a benchmark in this repository. The
command that produced it is printed next to it, so it can be re-run and
disagreed with. Nothing here is an estimate, a projection, or a figure
carried over from an earlier branch — if a measurement is missing, the row
says so instead of guessing.

**Every memory figure on this page says which kind of memory it is.** Read
[the next section](#three-kinds-of-memory-and-which-one-a-number-is) before
any of them. This page used to print `13.33 GB` in a table of scan costs
without saying it was cumulative allocation, and a reader who took it for
resident memory would have been wrong by a factor of thirty. That confusion
is what issue #152 was opened about.

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

# Resolver-side: what a typed load costs, and the two experiments behind
# issue #152's causes 2 and 3. Read §1's units note before the numbers.
go test ./packages/resolver -run NONE -benchtime 1x -count 3 -benchmem \
  -bench 'BenchmarkPackagesLoad|BenchmarkTypeCheckInfoFields|BenchmarkCallGraphScope'

# Ingest-side: a synthetic corpus shaped like this repository.
go test ./packages/store -run NONE -bench BenchmarkIngest -benchtime 5x -count 3

# A profile of either.
go test ./packages/codeindex -run NONE -bench BenchmarkGoScan_Typed \
  -benchtime 3x -cpuprofile /tmp/scan.prof -o /tmp/scan.test
go tool pprof -top -cum /tmp/scan.test /tmp/scan.prof

# Where the allocation goes, rather than the time. -alloc_space is cumulative
# bytes, -alloc_objects the count; they rank differently and both matter.
go test ./packages/codeindex -run NONE -bench BenchmarkIndexProject \
  -benchtime 1x -memprofile /tmp/scan.mem -o /tmp/scan.test
go tool pprof -top -sample_index=alloc_space /tmp/scan.test /tmp/scan.mem

# Resident peak of a real command. See "Measuring resident peak" below —
# this is a different quantity from anything -benchmem prints.
```

The memory ceiling that keeps the scan honest is
`TestDogfood_ScanMemoryCeiling` in `test/acceptance/memory_test.go`, run by
`./test/acceptance/run.sh` and by CI's blocking `atlas gates atlas` job. It
runs `BenchmarkIndexProject` in a child process and fails when `B/op` or
`allocs/op` goes over a committed ceiling. Before it existed, nothing in this
repository asserted that a scan does not start allocating gigabytes, and
#150 — a defect worth 37% of scan CPU and 7.6x the allocation count — went
unnoticed for months.

## The machine and the corpus

All figures below were taken on:

- Intel Core i7-10750H @ 2.60GHz, 12 logical CPUs, `GOMAXPROCS=12`
- linux/amd64, Go 1.26.4
- **The machine was not quiet.** Other work ran alongside these
  benchmarks, and the scan-side timings vary by up to 35% run to run
  because of it. Every scan-side figure is the median of the runs shown,
  and where the spread matters it is printed. The ingest-side figures were
  stable to within 2%.
- **That caveat is about time, not about memory.** Allocation counts do not
  move with ambient load: fourteen `IndexProject` samples taken while the
  machine was busy, spanning `ATLAS_BENCH_JOBS` 1/4/12 and `GOMAXPROCS` 2/12,
  spread 0.34% on `B/op` and 0.07% on `allocs/op`. Resident peak was similarly
  stable at 2.1% over five runs. A single noisy sample is a real hazard on
  this page — during #152's investigation one reported an 18% memory
  regression that three samples showed did not exist — so every memory figure
  here is a median of at least three, and says how many.

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

## Three kinds of memory, and which one a number is

Three different quantities on this page have all been written as "MB", and
they answer three different questions. Every figure below is now tagged with
which one it is; a figure with no tag is a bug in this page.

| tag | what it measures | where it comes from | can it be compared to RSS? |
|---|---|---|---|
| **cumulative allocation** | total bytes handed out by the allocator over one benchmark op, freed or not | `B/op` from `b.ReportAllocs()` / `-benchmem` | **no** |
| **live heap** | bytes still reachable at one instant, after a forced GC | `runtime.GC()` + `runtime.ReadMemStats` | it is a floor for it, nothing more |
| **resident peak** | the largest resident set the OS ever gave the process | `/proc/<pid>/status` `VmHWM` | it *is* it |

Cumulative allocation is a throughput number: it counts a 64 KB buffer
allocated once per file across 700 files as about 45 MB, even though only one
of them is ever live. It is the right number for "how hard is this working the garbage
collector", which is why the gate in
[`test/acceptance/memory_test.go`](../test/acceptance/memory_test.go) is set
on it, and it is the wrong number for "will this fit in my container".

The two really are independent. A full `atlas init` of this repository:

| | |
|---|---:|
| cumulative allocation, one `IndexProject` (`B/op`) | **836 MB** |
| resident peak, whole `atlas init` process (`VmHWM`) | **436 MB** |

A scan churns roughly twice what it ever holds. Nothing on this page has ever
measured a scan using 13 GB of memory, and nothing ever could — the largest
number here, `13.33 GB`, is cumulative allocation for one op of a *synthetic*
graph benchmark, most of it adjacency maps that were garbage before the next
edge was added.

### Measuring resident peak

`VmHWM` is the kernel's own high-water mark for the process: it only rises,
so this is not sampling a curve and cannot miss a spike between reads. The
loop exists only to read the value before the process exits and `/proc/<pid>`
disappears.

**`/usr/bin/time -v` is not installed on the machine these figures came from**
(only bash's `time` keyword, which reports no memory at all), so it is not the
method here. `VmHWM` needs nothing that is not already in a Linux kernel.

```sh
peak_rss_mb() {
  "$@" >/dev/null 2>&1 &
  local pid=$! hwm=0 v
  while [ -r "/proc/$pid/status" ]; do
    v=$(awk '/^VmHWM:/{print $2}' "/proc/$pid/status" 2>/dev/null)
    if [ -n "${v:-}" ] && [ "$v" -gt "$hwm" ]; then hwm=$v; fi
    sleep 0.01
  done
  wait "$pid"
  awk -v k="$hwm" 'BEGIN{printf "%.0f\n", k/1024}'
}

# A full init into a throwaway database, so every run does the same work.
db=$(mktemp -d)
peak_rss_mb ./atlas init --root . --db-path "$db/atlas.db" --json
```

Five runs on the machine below gave **432, 435, 436, 439, 441 MB — median
436 MB**, a 2.1% spread. Issue #152 reported 435 MB as the median of three by
the same method, which is the same answer.

**The method was checked against a known answer before being trusted**, since
a sampler that quietly reports the wrong thing is worse than no number. A test
program that holds 300 MB of touched pages and *separately* churns 300 MB that
is never live at the same time reports **314, 316, 316 MB** — it sees the
resident 300 MB plus the Go runtime's own overhead, and is correctly blind to
the 300 MB of churn. That gap between 316 and 616 is this whole section in one
measurement.

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

The byte figures in this table are **cumulative allocation** (`B/op`) for one
op — building the whole 12,728-edge graph — not memory the process holds. The
old implementation threw away one adjacency map per edge, so almost all of its
13.33 GB was garbage before the next edge was appended. Resident peak for this
benchmark was never measured and is not implied by any number here.

| | before | after | |
|---|---:|---:|---|
| `BenchmarkGraphAddEdge_ScanSized` | 16.55 s | **6.73 s** | **2.5x** |
| | 13.33 GB cumulative alloc / 81,838,283 allocs | **3.38 GB cumulative alloc / 384,428 allocs** | **213x fewer allocs** |
| `BenchmarkGraphAddEdge_FanIn` | 11.45 s | **7.74 ms** | **1,479x** |
| | 13.74 GB cumulative alloc / 81,745,158 allocs | **8.39 MB cumulative alloc / 51,360 allocs** | |
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
connected graph that walk is O(V+E). The 3.38 GB of cumulative allocation it
still churns is the per-call `visited` map: one map per edge, live for the
length of one DFS. Peak residency for that shape is one map, not 384,428 of
them.

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

Allocations, same runs. Both columns are **cumulative allocation** (`B/op`)
and allocation counts (`allocs/op`), not resident memory:

| | before | after |
|---|---|---|
| `IndexProject` | 67,824,856 allocs / 7.18 GB cumulative alloc | **8,914,570 allocs / 829 MB cumulative alloc** |
| `GoScan_Typed` | 66,867,213 allocs / 7.04 GB cumulative alloc | **7,957,774 allocs / 686 MB cumulative alloc** |

The 7.6x drop in allocations is why the GC share fell by more than half.
This was one defect, and it was most of the scan.

Re-measured for #152 on the current tree (675 `.go` files), median of five
processes at `-benchtime 1x`: **836 MB cumulative allocation and 9,010,665
allocations** per `IndexProject`. The spread across those five, and across a
further nine at `ATLAS_BENCH_JOBS` 1/4/12 and `GOMAXPROCS` 2/12, was 0.34% on
bytes and 0.07% on the count — **allocation accounting does not care about
ambient load or core count**, unlike every timing on this page. That is what
makes a committed ceiling on it practical, and the ceiling is
`maxScanBytesPerOp` / `maxScanAllocsPerOp` in
[`test/acceptance/memory_test.go`](../test/acceptance/memory_test.go), which
records how the numbers and their headroom were chosen.

**Resident peak, for contrast: 436 MB** for the whole `atlas init` process
(median of five, `VmHWM`; method above). The before/after of #150 has never
been measured in RSS terms, and the 7.18 GB above must not be read as one —
it is 7.18 GB of churn against a resident set that was probably close to
today's.

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

### Resolver memory: what a typed load costs (issue #152, causes 2 and 3)

Issue #152 measured 803 MB of allocation for a scan and named three causes.
Two of them are `packages/resolver`'s, and both close as **necessary** — the
saving is real, it is measured below, and it cannot be taken without giving
up the call graph. The third (`annotations.ParseRelative`) is not this
package's.

**Read the units before the numbers.** `B/op` is CUMULATIVE ALLOCATION: every
byte the allocator handed out over the run, including everything the GC took
back moments later. `peakRSS_MB` is RESIDENT PEAK — `VmHWM` from
`/proc/self/status`, the most the kernel ever had mapped at one instant. On
the load below they differ by 1.5x (584 MB allocated, 377 MB resident) and on
a whole scan by 1.9x (837 MB against 435 MB), so the two answer "does this
fit in CI" differently — conflating them is what issue #152 was opened to
stop. The RSS figures include the test binary and the Go runtime; `go list`
runs as a child process and is not in them.

```sh
# Whole-load cost. One benchmark per process, because VmHWM is a
# high-water mark for the process and a second benchmark inherits it.
go test -c -o /tmp/resolver.test ./packages/resolver
cd packages/resolver && for i in $(seq 6); do
  /tmp/resolver.test -test.run NONE -test.bench 'BenchmarkLoad$' \
    -test.benchtime 1x -test.benchmem
done
# ...and the same with -test.bench 'BenchmarkPackagesLoad$'.

# The two experiments. Medians of three; a single sample of any of these
# is worth nothing. These do not need a process apiece: they report no
# RSS, and their B/op is the stable column.
go test ./packages/resolver -run NONE -benchtime 1x -count 3 -benchmem \
  -bench 'BenchmarkTypeCheckInfoFields|BenchmarkCallGraphScope'
```

**What a resolver load of this repository costs.** Medians of six runs,
each in its own process, `IncludeTests: true`, warm build cache:

| | ns/op | B/op (cumulative) | allocs/op | peak RSS (resident) |
|---|---:|---:|---:|---:|
| `BenchmarkLoad` — `go list` + type check + SSA + CHA | 686 ms | 584,240,484 | 7,439,025 | **377.5 MB** |
| `BenchmarkPackagesLoad` — the same without SSA or CHA | 413 ms | 338,281,000 | 3,362,807 | 238.1 MB |
| difference: SSA construction and CHA | 273 ms | 245,959,484 | 4,076,218 | 139.4 MB |

That difference is confirmed independently: `BenchmarkCallGraphScope/all`
times the SSA and CHA stage on its own and reports 244,747,048 B/op and
4,052,278 allocs/op, within 0.5% of the subtraction. The allocation and
RSS columns are stable to under 0.2%; the timings are not, and the
paragraph below says where the instability lives.

Two things follow that are worth stating before optimising anything else.

A scan's memory very largely *is* the typed load. `BenchmarkIndexProject`
on the same tree and machine, median of three
(`go test ./packages/codeindex -run NONE -bench BenchmarkIndexProject
-benchtime 1x -count 3 -benchmem`), allocates 837,307,280 B/op over
9,031,235 allocs — so `resolver.Load` alone is **70% of a whole scan's
cumulative allocation**. On the resident side the comparison is looser
because it crosses two programs: 377.5 MB here against the 435 MB issue
#152 measured for a full `atlas init` by the same `VmHWM` method.

**All the timing variance is in the SSA stage, and none of it is in the
bytes.** Across those six runs `BenchmarkPackagesLoad` held 400–421 ms
(±2.5%) while `BenchmarkLoad` ranged 637–926 ms, so SSA and CHA measured
anywhere from a third to 40% of the load depending on what else the
machine was doing — they are the parallel part, and they lose whichever
cores something else has taken. Their share of ALLOCATION does not move:
42% in every sample. This is the concrete reason issue #152 asks for
medians. A single timing sample of this benchmark supports almost any
conclusion; a single allocation sample is worth about as much as six.

#### Cause 2 — `types.Info.Types` is populated, never read by atlas, and required anyway

The finding was correct as far as it went. `go/types.(*Checker).recordTypeAndValue`
was the largest single allocator in a scan (100.22 MB, 12.5%), it fills
`types.Info.Types`, and nothing in atlas reads it:

```sh
grep -rn 'TypesInfo\.Types\|info\.Types\[\|\.TypeOf(' --include=*.go packages/ internal/
```

finds nothing. `TypesInfo.Defs`, `info.Uses` and `info.Selections` are all
read; `Types` is not, by anything in this repository.

`BenchmarkTypeCheckInfoFields` prices exactly that field. Both arms
type-check the same syntax with the same checker and differ only in whether
`Types` was allocated. Medians of three:

| | B/op (cumulative) | allocs/op |
|---|---:|---:|
| `all-fields` | 242,486,944 | 1,641,535 |
| `without-Types` | 144,110,000 | 1,586,764 |
| **saving** | **98,376,944 (−40.6%)** | 54,771 (−3.3%) |

98.4 MB, which corroborates the 100.22 MB pprof attributed to
`recordTypeAndValue`. It is unavailable, because `go/ssa` reads the map that
atlas does not. `ssa.Function.typeOf` calls `types.Info.TypeOf`, whose only
fallback for a nil `Types` is `ObjectOf` — which answers for `*ast.Ident`
and nothing else — so the first composite expression in the first function
body panics. `TestSSA_RequiresTypesInfoTypes` is that measurement, run on a
six-declaration program that imports nothing, so the failure cannot be
blamed on anything else.

The failure mode matters more than the bytes. That panic is contained — by
`buildAllSSA` since this investigation; see the end of cause 3 — so leaving
`Types` nil would not crash a scan. It would do something worse:
`Status.CallGraph` would go quietly false and every interface edge in the
repository would disappear. This is the shape of regression the resolver is
built to make loud, and it would have been silent.

Two costs do not appear in the table above and are the reason this would not
be worth doing even if the bytes were free. Driving `types.Config.Check`
directly means reimplementing `packages.Load`'s per-package error handling —
the degradation path issue #87 built, which is what lets atlas run mid-edit
against a tree that does not compile. And it gives up go/packages' export
data caching, which is the difference between a 0.5 s load and a 3.6 s one
(`packages/resolver/doc.go`).

#### Cause 3 — SSA scope is already at its floor

The hypothesis was that `ssautil.Packages(pkgs, ssa.BuilderMode(0))` builds
function bodies for every transitive dependency. It does not.
`ssautil.Packages` passes syntax and `types.Info` only for the packages it
was handed; a dependency reached through `packages.Visit` gets
`CreatePackage(p.Types, nil, nil, true)` — declarations, no code.
`BenchmarkCallGraphScope` counts it, and `TestSSA_NoFunctionBodiesOutsideTheScannedTree`
holds it there:

| `BenchmarkCallGraphScope` | `all` | `dedup-test-variants` |
|---|---:|---:|
| packages handed to `ssautil.Packages` | 100 | 58 |
| `ssa.Package`s created | 405 | 404 |
| — of those, declarations only, no code | 305 | 346 |
| SSA function bodies built | 8,763 | 5,853 |
| — of those, outside the scanned tree | **0** | **0** |
| files compiled into more than one of them | 291 of 596 | 291 of 596 |
| interface call sites CHA resolved | **1,371** | **950** |
| B/op (cumulative) | 244,747,048 | 176,106,760 |

So the ~99 MB the issue attributed to dependency bodies is the scanned tree's
own bodies, and there is nothing to narrow in that direction. The 305
declaration-only packages are not free to drop either: SSA calls each
imported package's `init` from the importing package's `init` and asserts the
import was created (`Package(%q).Build(): unsatisfied import`). Skipping them
panics. `TestSSA_DependencyPackagesAreLoadBearing` provokes that panic through
`ssa.Package.Build`, which runs inline, so it can be observed without dying.

**One defect fell out of reading this code, and it is not a memory one.**
`ssa.Program.Build` runs each package on a goroutine it spawns itself and
recovers nothing, so a panic out of the SSA builder unwound a goroutine that
`buildCallGraph`'s recover was not on the stack of and killed the process —
against the comment right above it promising that a panic there costs
interface dispatch and not the scan. On a mid-edit tree that is a crashed
scan where issue #87 designed a degraded one. `buildAllSSA` now does the
fan-out itself, at the same `GOMAXPROCS` bound, so a recover sits on every
stack that can panic. Interleaved A/B in one binary, five samples each,
medians (the first sample of each side was discarded as a cold `go list`):

| `BenchmarkLoad` | ns/op | B/op (cumulative) | peak RSS |
|---|---:|---:|---:|
| `prog.Build()` | 665 ms | 584,212,464 | 371–420 MB |
| `buildAllSSA` | 682 ms | 584,676,560 | 373–424 MB |

2.5% of wall clock and 0.08% of allocation, both inside the run-to-run
spread this machine shows — which is why the samples were interleaved rather
than taken as two batches. Containment is free here; the RSS column is a
range rather than a median because the two sides do not separate at all.

The one duplication that is left is real and also unavailable. With
`IncludeTests`, a package with in-package tests is loaded twice — plainly and
as its test variant — and **291 of the 596 files these packages compile
are SSA-built under both**. The `dedup-test-variants` column is that saving
taken: drop every package whose file set is a strict subset of another's.
It is worth 68.6 MB of cumulative allocation, 28% of the SSA stage and 12% of
the whole load, and it costs **421 of 1,371 interface call sites — 31% of
every answer CHA gives**. Two mechanisms cause the loss, and neither is
fixable from this side: the plain package and its test variant are distinct
`types.Package`s, so a concrete type from one does not satisfy an interface
declared in the other; and a package created without syntax contributes no
roots to `ssautil.AllFunctions` and materialises no runtime types, so CHA
never learns what its types implement. One example of the 421, from this
tree: `packages/coverage/pertestingest.go:167` loses its only target,
`(*store.testCoverageStore).Insert`.

68.6 MB for a third of the interface graph is not a trade this scanner
should make, so it was measured and reverted rather than kept.

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
file into a slice and only then ran the recognisers over all of them. **Live
heap** with every AST retained — the third quantity in the table above, bytes
still reachable after a forced GC, measured with `runtime.GC()` +
`ReadMemStats` — is **21.7 MB live heap for 386 files**, and it grows linearly
with the tree, so a repository five times this size would hold roughly five
times that (issue #109's reference tree is 1,985 files). This one is a
residency figure rather than a churn figure, which is exactly why it was the
argument for the change: it is memory the process is *holding*, and it was
unbounded in the repository's size. The pass now parses and matches one file
per worker and drops the AST immediately, so the ceiling is `jobs` trees; a
single large file's AST measured **0.30 MB live heap**. This is issue #109's
stated "parser memory" risk, and it is the half of the change that pays for
itself.

Live heap is a floor for resident peak, not equal to it: the Go runtime does
not return freed pages to the OS promptly, so `VmHWM` for the same process
sits above the live-heap high point. 21.7 MB of retained ASTs inside a
436 MB resident peak is a share, not a total.

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

Allocations, same runs. **Cumulative allocation** (`B/op`) and counts
(`allocs/op`); the ingest's resident peak has not been measured separately
from the scan's, and these numbers do not stand in for it:

| benchmark | before | after |
|---|---|---|
| Fresh | 348,861 allocs / 15.7 MB cumulative alloc | 290,362 allocs / 24.9 MB cumulative alloc |
| RescanChanged | 546,827 allocs / 23.6 MB cumulative alloc | 268,091 allocs / 19.8 MB cumulative alloc |
| RescanUnchanged | 164,144 allocs / 6.6 MB cumulative alloc | 127,518 allocs / 7.3 MB cumulative alloc |

The fresh case allocates *more* bytes than it did: a batch builds an
`[]any` of up to 999 bound parameters per statement, and on a cold database
there are no lookups to save. That is a deliberate trade — 9 MB of extra churn
in short-lived argument slices, one chunk of which is live at a time, for half
the wall time — and it is recorded here rather than left for someone to
discover.

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
   3.38 GB of cumulative allocation in `visited` maps (one live at a time,
   not 3.38 GB held); on this repository it is below the profiler's cut.
   Before replacing it with a union-find or an incremental topological
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

## 5. Annotation parsing — allocation (issue #152, cause 1)

**Every number in this section is `B/op`: CUMULATIVE BYTES ALLOCATED over a
run, as `testing`'s `-benchmem` reports it. It is not resident memory.** A
scan that allocates 838 MB does not hold 838 MB; most of it is freed as it
goes. Conflating the two is what issue #152 was opened to stop, so this
section says which it means every time it gives a figure, and claims no RSS
number at all — none was taken here.

`ParseRelative` reads one file per source file in the tree, so whatever it
allocates per file is multiplied by the file count. An `-alloc_space` profile
of a scan put it at 142.09 MB cumulative, 17.7% of the 803 MB that scan
allocated, and most of the 33.91 MB in `bytes.growSlice`. The cause was in
the read, not the matching: a fresh 64 KB `bufio.Scanner` buffer per file,
then every line copied into a growing `bytes.Buffer` to reassemble the file
the scanner had just taken apart — for a whole-file parse that never needed
the file streamed. It is now one `os.ReadFile`, and the unwrappers walk
subslices of that single buffer instead of a `bytes.Split` of it plus a
`string(...)` per line.

### One file

```sh
go test ./packages/codeindex/annotations -run '^$' \
  -bench BenchmarkParseRelative -benchmem -count 5
```

Medians of 5, `mem/parse` against its base `f5b5550`:

| file | B/op before | B/op after | | allocs/op before | after | | ns/op before | after |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 551 B (the Go fixture) | 74,012 | **5,480** | −92.6% | 65 | **54** | −16.9% | 25,391 | **19,245** |
| 8 KB generated | 119,803 | **19,169** | −84.0% | 231 | **105** | −54.5% | 136,923 | **110,683** |
| 512 KB generated | 3,514,202 | **1,194,864** | −66.0% | 12,791 | **5,554** | −56.6% | 8,095,655 | **7,879,990** |

The 551-byte row is the one that scales: a fixed 64 KB per file is 99% of the
cost of a small file and a real tree has a long tail of them. The 512 KB row
is where the whole-file copy dominated instead, which is why its saving is a
third rather than nine tenths.

`TestParseRelative_TinyFileAllocationCeiling` measures the same quantity
without a benchmark harness — a `runtime.MemStats.TotalAlloc` delta over
2,000 parses — and fails above 16 KB. It reported 74,029 B before the change
and 5,463 B after; it was watched failing at the old number, which is the
only evidence that the ceiling can fail. It carries a `//go:build !race`
tag: the race detector's own shadow allocations land in `TotalAlloc` too and
put the same parse at 360,920 B, so under `-race` the number measures the
detector.

### The scan

Phase B on its own — read, annotation-parse and SHA-256 every source file:

```sh
go test ./packages/codeindex -run '^$' \
  -bench '^BenchmarkAnnotationWalk$' -benchtime 1x -benchmem -count 5
```

Medians of 5: **119,772,104 → 41,119,400 B/op** (−65.7%), **187,290 →
52,674 allocs/op** (−71.9%), 38.48 ms → 23.39 ms (−39%). The two timing
samples do not overlap (36.6–44.9 ms before, 21.5–24.2 ms after), which is
why the wall-clock figure is quoted here and not for `IndexProject` below.

End to end:

```sh
go test ./packages/codeindex -run '^$' \
  -bench '^BenchmarkIndexProject$' -benchtime 1x -benchmem -count 3
```

Medians of 3:

| | B/op (cumulative) | allocs/op |
|---|---:|---:|
| before (`f5b5550`) | 837,792,736 | 9,025,088 |
| after | **704,485,808** | **8,778,537** |
| | −133.3 MB, **−15.9%** | −246,551, **−2.7%** |

**Say the two numbers separately, because they are not the same result.**
The bytes moved almost the whole 17.7% the profile attributed to
`ParseRelative`; the allocation *count* moved 2.7%, because what was removed
was a handful of very large allocations per file, not many small ones. A
share of bytes does not have to convert into a share of anything else, and
`-15.9% B/op, -2.7% allocs/op` is the honest pair.

Two caveats on the end-to-end table. The before-total here is 838 MB, not
the 803 MB in issue #152 — a different tree state, measured fresh on this
branch point, because a before/after pair is only meaningful within one
corpus. And no wall-clock claim is made from it: the first iteration of each
`-count 3` set ran against a cold `go/packages` cache (6.35 s and 3.11 s
against ~0.9 s for the rest), so those medians are a measurement of cache
warmth, not of this change. The `B/op` figures were stable to within 0.2%
across all three.

### What was not done

The remaining per-parse cost of the 551-byte file is ~5.5 KB, and an
`-alloc_space` profile of it is now made of things proportional to what the
parse KEEPS rather than to the file's size: the comment strings retained in
`logicalLine` (38%), the `make([]shared.Annotation, 0, 8)` in `ParseBytes`
(28%), the file itself (9%), and `regexp.FindStringSubmatch` (12%). Shrinking
any of those is a different change with a different argument — in particular
the three regexes are run per comment line and a cheap `@` pre-filter would
skip most of them — and none of it was measured here, so none of it is
claimed.
6. **Allocation is now gated, and the remaining question is whether 836 MB of
   churn per scan is justified rather than merely stable.**
   `TestDogfood_ScanMemoryCeiling` stops it growing quietly; it says nothing
   about whether the current figure is right. Issue #152 names three
   candidates. One is now closed and two are answered NEGATIVELY, on
   measurement:
   `annotations.ParseRelative` rebuilding every file it reads — **fixed**,
   -15.9% of scan bytes (see section 5);
   `types.Info.Types` populated and never read — **inherent**: leaving the map
   nil saves 98 MB and then SSA cannot be built at all, panicking
   `no type for *ast.SelectorExpr`;
   SSA bodies for every transitive dependency — **inherent**: building only the
   initial packages panics in `prog.Build()`.
   The gate is the floor under whatever comes next, not a substitute for it.
