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

# Interface dispatch, both ways, over the same packages in one process:
# the go/types index atlas ships and the SSA + CHA it replaced (issue
# #155). Medians of three; ignore ns/op unless the machine is quiet.
go test ./packages/resolver -run NONE -benchtime 1x -count 3 -benchmem \
  -bench BenchmarkDispatchStage

# Ingest-side: a synthetic corpus shaped like this repository.
go test ./packages/store -run NONE -bench BenchmarkIngest -benchtime 5x -count 3

# The two tables an ingest bulk-loads, timed on their own, with their
# indexes maintained during the load and built after it (§3, issue #157).
go test ./packages/store -run NONE -benchtime 5x -count 9 -benchmem \
  -bench 'BenchmarkEdgeLoad|BenchmarkSymbolLoad'

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
- **That caveat is about time, and about ALLOCATION only.** Cumulative
  allocation does not move with ambient load: fourteen `IndexProject` samples
  taken while the machine was busy, spanning `ATLAS_BENCH_JOBS` 1/4/12 and
  `GOMAXPROCS` 2/12, spread 0.34% on `B/op` and 0.07% on `allocs/op`.
- **RESIDENT PEAK IS NOT IN THAT CATEGORY, and this page used to imply it
  was.** Seven `BenchmarkLoad` samples, one process each, spread 0.21% on
  `B/op` and **11.0%** on `VmHWM` — fifty times as wide, on the same runs
  (§Resolver memory has the table). Resident peak depends on when the
  collector happens to run against a heap the runtime may grow differently
  every time, so it is not a deterministic quantity the way an allocation
  count is. One set of five `atlas init` runs held to 2.1%; another set of
  five by the same method on this branch spread 10.8%, so the tight one was
  luck and not a property. **Read any single resident figure on this page as
  ±10%.**
- A single noisy sample is a real hazard here — during #152's investigation
  one reported an 18% memory regression that three samples showed did not
  exist — so every memory figure on this page is a median of at least three,
  and says how many.

Two corpora appear below, and the difference matters when comparing rows.
The §3 ingest figures and the §2 worker sweep are #109's, taken at `f4d5299`
on a 653-file tree; §3's last subsection is #157's, taken later at `2815817`
against the same synthetic corpus and with its own conditions stated there. Everything in §1 was re-taken for #150 at `ba00402` plus
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

The two really are independent. A full `atlas init` of this repository, on
this branch:

| | | |
|---|---:|---|
| cumulative allocation, one `IndexProject` (`B/op`) | **709 MB** | median of 5, spread 0.06% |
| resident peak, whole `atlas init` process (`VmHWM`) | **473 MB** | median of 5 (456/469/473/477/507), spread 10.8% |

Both rows predate issue #156, which took the first to **684 MB** and left the
second where it was; §6 has that pair, measured against a frozen corpus and
interleaved. They are kept here as written because the point they are making
is the independence of the two columns, and re-measuring them would not change
it.

A scan churns roughly half again what it ever holds — and the two columns
demonstrate their own independence in how well they repeat. The allocation
figure was 836 MB before the annotation-parse fix in §5 and is 709 MB after
it, a difference forty times the measurement's own spread. **No before/after
conclusion about residency is available from these runs**: this page has
recorded 436 MB for `atlas init` at one branch point and 473 MB here, and
issue #152's own notes have 461 MB, and all three sit inside one 10.8%
spread of each other. That is not three measurements of a change; it is one
measurement repeated on a quantity that repeats to ±10%.

Nothing on this page has ever measured a scan using 13 GB of memory, and
nothing ever could — the largest
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

Two sets of five runs by exactly this method, at two branch points:
**432, 435, 436, 439, 441 MB — median 436 MB**, a 2.1% spread; and on this
branch **456, 469, 473, 477, 507 MB — median 473 MB**, a 10.8% spread. Issue
#152 reported 435 MB as the median of three by the same method.

**Do not read the 2.1% set as what resident peak always does.** The second
set, the resolver benchmarks in §1 (11.0%) and every other repeated
`VmHWM` measurement on this page say ±10% is the honest tolerance, and a
2.1% run is a lucky one rather than the property of the method. Three
`atlas init` medians spanning 436–473 MB do not establish that anything
changed between them.

### The method, checked against a known answer

A sampler that quietly reports the wrong thing is worse than no number, so
`VmHWM` was pointed at a program whose answer is known before any figure on
this page was taken from it. The program holds 300 MB of touched pages for
its whole run and then churns a further 300 MB in chunks, freeing each before
allocating the next:

```go
held := make([]byte, 300<<20)
touch(held)                                  // every 4 KB page written
for done := 0; done < 300; done += chunkMB { // 300 MB more, in chunks
        b := make([]byte, chunkMB<<20)
        touch(b)
        b = nil
        runtime.GC()
}
runtime.KeepAlive(held)                      // held is live throughout
```

Cumulative allocation for that program is 600 MB whatever `chunkMB` is.
Resident peak is not, and **the chunk size is the whole experiment** — which
is why it is printed here. Three runs at each size, `VmHWM` read from
`/proc/self/status` after the loop; the held-only reading was 303–306 MB
every time:

| churn chunk | `VmHWM` after the churn | what it is |
|---:|---:|---|
| 1 MB | **306 MB** | 300 held + 1 live + runtime |
| 10 MB | **314 MB** | 300 held + 10 live + runtime |
| 50 MB | **353 MB** | 300 held + 50 live + runtime |
| 150 MB | **454 MB** | 300 held + 150 live + runtime |
| 300 MB (one block) | **604 MB** | 300 held + 300 live + runtime |

Every row is 300 MB plus one chunk. That is the definition of resident peak
working exactly as advertised — and it is a sharper lesson than "the method
is blind to churn", which is what this page used to say. `VmHWM` is blind to
churn only in so far as the churn is broken into pieces; a program that frees
300 MB and immediately allocates 300 MB more has both live at the moment the
second one is touched, and 604 MB is the honest answer for it.

An earlier version of this section reported `314, 316, 316 MB` and drew the
`316 against 616` contrast from it without saying that the churn was chunked.
Written the obvious way — allocate 300 MB, drop it, allocate 300 MB again —
the same description produces the bottom row, and a reader who followed it
would have concluded the method was broken. The numbers were right; the
description was not reproducible, and a validation that does not reproduce
validates nothing.

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

Re-measured for #152, median of five processes at `-benchtime 1x`:
**836 MB cumulative allocation and 9,010,665 allocations** per
`IndexProject` on a 675-file tree, and **709 MB and 8,822,140** on this
branch's 681-file tree once the annotation-parse fix of §5 landed. The spread
across the first five, and across a further nine at `ATLAS_BENCH_JOBS` 1/4/12
and `GOMAXPROCS` 2/12, was 0.34% on bytes and 0.07% on the count — **allocation accounting does not care about
ambient load or core count**, unlike every timing on this page. That is what
makes a committed ceiling on it practical, and the ceiling is
`maxScanBytesPerOp` / `maxScanAllocsPerOp` in
[`test/acceptance/memory_test.go`](../test/acceptance/memory_test.go), which
records how the numbers and their headroom were chosen.

**RESIDENT PEAK, for contrast: 436 MB** for the whole `atlas init` process
at that branch point, 473 MB on this one (medians of five, `VmHWM`; method
above — and ±10%, so treat them as one number rather than a trend). The before/after of #150 has never
been measured in resident terms, and the 7.18 GB above must not be read as
one — it is 7.18 GB of CUMULATIVE ALLOCATION against a resident set that was
probably close to today's.

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
NeedSyntax | NeedTypesInfo` and deliberately **without** `NeedDeps`.
`packages/resolver/doc.go` records the A/B that decision rests on, re-taken
for this milestone: the same `packages.Load(./..., Tests: true)` costs
0.52 s, 340 MB of CUMULATIVE ALLOCATION and 243 MB of RESIDENT PEAK without
`NeedDeps`, against 3.16 s, 2,131 MB and 1,326 MB with it — medians of three
interleaved samples, one process each. Six times the wall clock and six
times the churn, for dependency syntax nothing in this repository reads.
A direct `resolver.Load` of this repository with a warm build cache measured
**355 ms** on #109's corpus. That was 5% of a
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

Issue #152 measured 803 MB of CUMULATIVE ALLOCATION for a scan and named
three causes. Two of them are `packages/resolver`'s. The third
(`annotations.ParseRelative`) is not this package's.

**Read this subsection as history, and the one after it as the current
state.** Both of #152's resolver causes were closed as *necessary*: the
saving was real, it is measured below, and it could not be taken without
giving up the call graph. Issue #155 reopened the question by asking
whether the call graph needed the architecture that made it necessary, and
one of the two closes differently now — [Issue #155 — interface dispatch
from go/types, and the 216 MB it
returned](#issue-155--interface-dispatch-from-gotypes-and-the-216-mb-it-returned).
Every figure in the rest of this subsection was taken with SSA in the load
and is still what that program cost.

**Read the units before the numbers.** `B/op` is CUMULATIVE ALLOCATION: every
byte the allocator handed out over the run, including everything the GC took
back moments later. `peakRSS_MB` is RESIDENT PEAK — `VmHWM` from
`/proc/self/status`, the most the kernel ever had mapped at one instant. On
the load below they differ by 1.5x (586 MB allocated, 398 MB resident) and
on a whole scan by 1.9x (837 MB allocated against 435 MB resident), so the
two answer "does this fit in CI" differently — conflating them is what issue
#152 was opened to stop. They differ in a second way that matters as much:
the allocation figure repeats to 0.2% and the resident one to 11%, so they do
not deserve the same number of significant digits. The RSS figures include
the test binary and the Go runtime; `go list` runs as a child process and is
not in them.

```sh
# Whole-load cost. One benchmark per process, because VmHWM is a
# high-water mark for the process and a second benchmark inherits it.
go test -c -o /tmp/resolver.test ./packages/resolver
cd packages/resolver && for i in $(seq 7); do
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

**What a resolver load of this repository costs.** Medians of SEVEN runs,
each in its own process, `IncludeTests: true`, warm build cache:

| | ns/op | B/op (cumulative allocation) | allocs/op | peak RSS (resident peak) |
|---|---:|---:|---:|---:|
| `BenchmarkLoad` — `go list` + type check + SSA + CHA | 637 ms | 586,067,128 | 7,462,094 | **398.0 MB** |
| `BenchmarkPackagesLoad` — the same without SSA or CHA | 534 ms | 340,414,752 | 3,376,048 | 241.1 MB |
| difference: SSA construction and CHA | 103 ms | 245,652,376 | 4,086,046 | 156.9 MB |

The allocation difference is confirmed independently:
`BenchmarkCallGraphScope/all` times the SSA and CHA stage on its own and
reports 245,558,560 B/op and 4,065,027 allocs/op — 0.04% and 0.5% from the
subtraction.

**Which columns are stable, and by how much.** Not one answer for all four:
this page said "the allocation and RSS columns are stable to under 0.2%",
and one of those two is off by a factor of fifty. Spread is (max − min) over
the median, across the same seven runs:

| column | `BenchmarkLoad` | `BenchmarkPackagesLoad` | load dependent? |
|---|---:|---:|---|
| `allocs/op` | **0.06%** | **0.005%** | no |
| `B/op` (cumulative allocation) | **0.21%** | **0.31%** | no |
| peak RSS (resident peak) | **11.0%** | **6.8%** | **yes** |
| ns/op | **4.4%** | **13.5%** | **yes** |

Allocation accounting is deterministic work: the same tree makes the same
allocations whatever else the machine is doing, and 0.2–0.3% is `go list`
output and map iteration, not noise in the measurement. Resident peak is
not deterministic at all — it depends on when the collector happens to run
against a heap the runtime is free to grow differently on every process, and
11% is what that costs. **An RSS figure on this page is a median of at least
three for that reason, and a single one should be read as ±10%.** The two
`atlas init` figures elsewhere on this page are quoted with their spread for
the same reason.

Two things follow that are worth stating before optimising anything else.

A scan's memory very largely *is* the typed load. `BenchmarkIndexProject`
on the same tree and machine, median of three
(`go test ./packages/codeindex -run NONE -bench BenchmarkIndexProject
-benchtime 1x -count 3 -benchmem`), allocates 837,307,280 B/op of
CUMULATIVE ALLOCATION over 9,031,235 allocs — so `resolver.Load` alone is
**70% of a whole scan's cumulative allocation**. On the resident side the
comparison is looser because it crosses two programs and because of the 11%
above: 398 MB of RESIDENT PEAK here against the 435 MB issue #152 measured
for a full `atlas init` by the same `VmHWM` method.

**The timing split between the two moves, and the allocation split does
not.** Across the seven runs above `BenchmarkPackagesLoad` ranged
506–574 ms and `BenchmarkLoad` 619–647 ms, putting SSA and CHA at 16% of
the load by wall clock. An earlier session on this same machine measured
the same pair at 400–421 ms and 637–926 ms, which puts them at a third to
40%. Both were taken on a machine that was not quiet, and the disagreement
between them is the point: SSA and CHA are the parallel part of the load
and they lose whichever cores something else has taken, so their share of
TIME is a property of the afternoon. Their share of ALLOCATION is 42% in
every sample of both sessions. This is the concrete reason issue #152 asks
for medians — and the reason no wall-clock conclusion is drawn from this
table.

#### Cause 2 — `types.Info.Types` is populated, never read by atlas, and was required anyway

The finding was correct as far as it went. `go/types.(*Checker).recordTypeAndValue`
was the largest single allocator in a scan (100.22 MB of CUMULATIVE
ALLOCATION, 12.5% of it), it fills `types.Info.Types`, and nothing in atlas
reads it:

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

98.4 MB of CUMULATIVE ALLOCATION, which corroborates the 100.22 MB
`-alloc_space` pprof attributed to `recordTypeAndValue`. It was
unavailable, because `go/ssa` read the map that atlas does not.
`ssa.Function.typeOf` calls `types.Info.TypeOf`, whose only fallback for a
nil `Types` is `ObjectOf` — which answers for `*ast.Ident` and nothing
else — so the first composite expression in the first function body
panics. `TestSSA_RequiresTypesInfoTypes` is that measurement, run on a
six-declaration program that imports nothing, so the failure cannot be
blamed on anything else.

The failure mode mattered more than the bytes. That panic is contained —
by `buildAllSSA` since this investigation; see the end of cause 3 — so
leaving `Types` nil would not have crashed a scan. It would have done
something worse: `Status.CallGraph` going quietly false and every
interface edge in the repository disappearing. This is the shape of
regression the resolver is built to make loud, and it would have been
silent.

**Issue #155 asked for this to be re-tested once SSA was gone, and it was.
The correctness blocker is gone; the bytes are still not collectable.**
`TestDispatch_DoesNotNeedTypesInfoTypes` type-checks that same
six-declaration program with `Types` nil and resolves its interface call
site to both implementations — identically to the full-`Info` arm. The
dispatch index reads `Selections` and `Defs` and never asks an expression
for its type, so nothing in atlas reads `Types` any more, in production or
in a library it calls.

It buys nothing, because `packages.LoadMode` has no bit for a subset of
`types.Info`: `NeedTypesInfo` fills every map or none, and there is no
`Config` hook to supply one. Taking the 98.4 MB therefore still means
driving `types.Config.Check` by hand, and the two paragraphs below are
still why that is a bad trade. **What changed is the reason: this was
blocked by a correctness failure and is now blocked only by cost.** That
is a smaller obstacle and a different issue — a `packages` upstream that
grew a partial-`Info` mode would unblock it outright — so it is recorded
here rather than closed again as inherent.

Two costs do not appear in the table above and are the reason this would not
be worth doing even if the bytes were free. Driving `types.Config.Check`
directly means reimplementing `packages.Load`'s per-package error handling —
the degradation path issue #87 built, which is what lets atlas run mid-edit
against a tree that does not compile. And it puts the caller in charge of where
dependency types come from, which is the single most expensive decision in
the load: `doc.go`'s A/B prices dependency types from source at 3.16 s and
2,131 MB of CUMULATIVE ALLOCATION against 0.52 s and 340 MB from export data.
Nobody has written the hand-rolled checker, so that pair is the cost of the
choice it would have to make and not a measurement of the program itself.

#### Cause 3 — SSA scope was already at its floor, and the floor was the wrong question

The hypothesis was that `ssautil.Packages(pkgs, ssa.BuilderMode(0))` builds
function bodies for every transitive dependency. It does not.

Everything in this section is correct and none of it saved anything,
because it asked how small the SSA program could be made rather than
whether it had to exist. It did not: see the next section. The census is
kept because it is the evidence for both halves of that — the 245 MB was
real, and it was the scanned tree's own bodies rather than dependencies'.
`ssautil.Packages` passes syntax and `types.Info` only for the packages it
was handed; a dependency reached through `packages.Visit` gets
`CreatePackage(p.Types, nil, nil, true)` — declarations, no code.
`BenchmarkCallGraphScope` counts it. Medians of three, this tree:

| `BenchmarkCallGraphScope` | `all` | `dedup-test-variants` |
|---|---:|---:|
| packages handed to `ssautil.Packages` | 100 | 58 |
| `ssa.Package`s created | 405 | 404 |
| — of those, declarations only, no code | 305 | 346 |
| SSA function bodies built | 10,074 | 7,001 |
| — of those, from a dependency's SYNTAX | **0** | **0** |
| — of those, wrappers over a dependency's methods | 916 | 931 |
| SSA instructions in those wrappers, of 461,331 / 326,008 | 3,651 | 3,683 |
| files compiled into more than one of them | 291 of 600 | 291 of 600 |
| interface call sites CHA resolved | **1,378** | **957** |
| B/op (cumulative allocation) | 245,558,560 | 176,751,632 |

**Two rows where this table used to have one, and the missing one was the
interesting half.** The census counted bodies by walking
`ssautil.AllFunctions` and skipping every function with `fn.Pkg == nil` —
which is every function `go/ssa` synthesises rather than compiles: the
pointer-receiver wrapper it makes whenever the scanned tree needs `*T`'s
method set for a `T` declared elsewhere, every bound method expression,
every generic instantiation wrapper. That is 1,262 of the 10,074 bodies
here — an eighth of the population — dropped from a number presented as a
census, and 916 of them are built over `time`, `os` and `sync/atomic`,
which is exactly the shape of thing "0 bodies outside the scanned tree"
was claiming did not exist.

Counting them does not change the conclusion, and the third row is why: at
about four SSA instructions each (a load, a call and a return) the 916
wrappers are 3,651 instructions out of 461,331, **0.8% of the built
program**. The ~99 MB of CUMULATIVE ALLOCATION the issue attributed to
dependency bodies is the scanned tree's own bodies. But "0" now means the
precise thing it can
support — no body is built from a dependency's syntax, because a
dependency arrives with no syntax — rather than the broader thing it was
being read as.

The 305 declaration-only packages are not free to drop either: SSA calls
each imported package's `init` from the importing package's `init` and
asserts the import was created (`Package(%q).Build(): unsatisfied import`).
Skipping them panics. `TestSSA_DependencyPackagesAreLoadBearing` provokes
that panic through `ssa.Package.Build`, which runs inline, so it can be
observed without dying.

**How the scope is held, and how it was not.**
`TestSSA_NoFunctionBodiesOutsideTheScannedTree` is the gate on the zero row,
and until this milestone it was not one: it built its own SSA program with
its own call to `ssautil.Packages` and then asserted about that, so it was
measuring its own arguments. Both mutations below were run against it; the
figures are from the runs:

| mutation to `buildCallGraph` / `loadMode` | dependency bodies built | old test | current test |
|---|---:|---|---|
| none | 0 | pass | pass |
| `ssautil.Packages` → `ssautil.AllPackages` | 1 | **pass** | FAIL |
| that, plus `NeedDeps` in `loadMode` | **13,163** | **pass** | FAIL |

The bottom row is issue #152's cause 3 made real — 13,163 dependency
function bodies, 830,334 SSA instructions against the honest program's 993
on the same fixture — and the test reported green on it. It now reads the
`ssa.Program` that `buildCallGraph` actually built, through a hook
(`ssaObserver`) that exists for exactly this and holds no reference past the
call.

One claim that did not survive being run: the middle row is the whole of the
risk, and `NeedDeps` alone is not. That test's comment used to say the cheap
way to lose the property was to add `packages.NeedDeps` to `loadMode`, "at
which point every dependency arrives with syntax, becomes an initial package,
and the ~99 MB of cumulative allocation appears for real". It does not.
`ssautil.Packages` decides what is initial from the slice it was handed, not
from whether a package has syntax, so `NeedDeps` alone leaves the census at 0
and costs only the load
time in `doc.go`'s A/B. It takes `AllPackages` to widen the scope.

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

| `BenchmarkLoad` | ns/op | B/op (cumulative allocation) | peak RSS (resident peak) |
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

68.6 MB of CUMULATIVE ALLOCATION for a third of the interface graph is not a
trade this scanner should make, so it was measured and reverted rather than
kept.

### Issue #155 — interface dispatch from go/types, and the 216 MB it returned

Cause 3 above measured the SSA stage at 245 MB of CUMULATIVE ALLOCATION and
concluded that its scope was already minimal. Both are true. Issue #155
asked a different question — whether the stage had to exist — and the
answer was no.

The whole output of `ssautil.Packages` → `buildAllSSA` → `cha.CallGraph` →
`indexInvokes` was one field, `invokes map[token.Pos][]*types.Func`. Its key
is a `go/token` concept and its value a `go/types` one; no `ssa.Program` was
retained past the call. CHA walks SSA for one structural reason — to
enumerate call sites — and `packages/resolver` already enumerates its own:
`resolve.go` walks the AST and reads `p.invokes[call.Lparen]`. The
satisfaction computation underneath CHA (`chautil.LazyCallees`) is pure
`go/types`. So `packages/resolver/typedispatch.go` computes the same map
directly, and `go/ssa` is no longer linked into the atlas binary at all.

**Equivalence first.** The safety argument is in
`packages/resolver/invokeparity_test.go`, which keeps the old CHA
implementation compiled into the test binary as an oracle and compares the
two maps candidate by candidate over this repository, the golden corpus,
`brokencorpus` and `sampleproject`. Summarised at the end of this section;
read it there rather than trusting the numbers below to imply it.

#### The measurements

Machine and corpus: the ones at the top of this page. **Both arms were
built from two trees and run against ONE frozen tree** — the repository at
`2815817`, extracted with `git archive` so neither arm scans its own source
— and the resolver benchmarks were run with the working directory inside
that frozen tree so `selfRepo` resolves to it. 101 packages, 682 `.go`
files.

`BenchmarkLoad` — a whole resolver load, `IncludeTests: true`, warm build
cache. **Five samples each, one benchmark per process** (`VmHWM` is a
per-process high-water mark and a second benchmark inherits it), medians,
spread as (max − min) / median:

| `BenchmarkLoad` | ns/op | B/op (CUMULATIVE ALLOCATION) | allocs/op | peak RSS (RESIDENT PEAK) |
|---|---:|---:|---:|---:|
| before — type check + SSA + CHA | 639 ms *(17.6%)* | 586,321,440 *(0.2%)* | 7,466,662 *(0.0%)* | 401.5 MB *(8.7%)* |
| after — type check + go/types dispatch | 493 ms *(1.6%)* | 370,503,880 *(0.2%)* | 3,784,184 *(0.0%)* | 258.3 MB *(5.0%)* |
| **difference** | **−146 ms (−22.9%)** | **−215,817,560 (−36.8%)** | **−3,682,478 (−49.3%)** | **−143.2 MB (−35.7%)** |

```sh
# in the frozen tree
go test -c -o /tmp/resolver.test ./packages/resolver
cd packages/resolver && for i in $(seq 5); do
  /tmp/resolver.test -test.run NONE -test.bench 'BenchmarkLoad$' \
    -test.benchtime 1x -test.benchmem
done
```

`BenchmarkIndexProject` — the whole scan, not just the load. Three samples
each, `ATLAS_BENCH_ROOT` pointed at the frozen tree:

| `BenchmarkIndexProject` | ns/op | B/op (CUMULATIVE ALLOCATION) | allocs/op |
|---|---:|---:|---:|
| before | 823 ms *(2.0%)* | 709,299,752 *(0.2%)* | 8,825,263 *(0.0%)* |
| after | 678 ms *(0.5%)* | 495,131,664 *(0.3%)* | 5,158,580 *(0.0%)* |
| **difference** | **−145 ms (−17.6%)** | **−214,168,088 (−30.2%)** | **−3,666,683 (−41.5%)** |

The two differences agree to 0.8% (215.8 MB against 214.2 MB), which is
what a saving located entirely in `resolver.Load` should look like from two
different benchmarks. `resolver.Load` is now 74.8% of a whole scan's
cumulative allocation, against 82.7% before — it did not stop dominating,
it just got smaller.

A full `atlas init` into a throwaway database, five samples each, by the
`VmHWM` method in [Measuring resident peak](#measuring-resident-peak):

| `atlas init` on the frozen tree | wall | peak RSS (RESIDENT PEAK) |
|---|---:|---:|
| before | 1,400 ms *(7.7%)* | 462 MB *(7.6%)* |
| after | 1,219 ms *(2.1%)* | 300 MB *(11.3%)* |
| **difference** | **−181 ms (−12.9%)** | **−162 MB (−35.1%)** |

Both RSS spreads are inside this page's ±10% tolerance and the gap between
the medians is sixteen times that tolerance, which is why this one is quoted
as a change rather than as two numbers that happen to differ.

The stage on its own, from `BenchmarkDispatchStage`, which runs both
implementations over the same `[]*packages.Package` in one process —
medians of three, run with `-benchtime 1x -count 3 -benchmem`:

| `BenchmarkDispatchStage` | ns/op | B/op (CUMULATIVE ALLOCATION) | allocs/op |
|---|---:|---:|---:|
| `cha` — `ssautil.Packages` + build + `cha.CallGraph` | 198 ms | 247,141,064 | 4,103,890 |
| `types` — the dispatch index | 60 ms | 29,890,896 | 394,410 |
| **ratio** | **3.3x** | **8.3x** | **10.4x** |

The `cha` arm reproduces cause 3's 245,558,560 B/op to 0.6%, four milestones
later, which is the check that these two tables are measuring the same
program. Do not read the wall-clock row without the caveat cause 3 already
gives: the `cha` arm is the parallel one and its share of TIME is a
property of the afternoon.

The binary shrinks by 962 KB — 8,827,985 to 7,866,270 bytes for the same
`cmd/edgedump` harness built from both trees — which is
`golang.org/x/tools/go/ssa` and `go/callgraph` leaving the link.

#### Equivalence — what was compared, and what differs

The dispatch maps are **not identical**, and the four things that are
identical are why that is acceptable. All of it is asserted, per tree, by
`TestInvokes_TypesPathIsEquivalentToCHA`:

1. **Nothing is lost.** Every candidate CHA names, the types path names.
   Zero exceptions on all four trees. Under-approximating would be a
   correctness regression, and #155 says the work stops there.
2. **Inside the loaded tree the two agree exactly**, site for site,
   candidate for candidate.
3. **Every extra candidate is declared outside the loaded tree** — in a
   dependency, for which the scan holds no symbol and can emit no edge.
   On this repository that is 2,567 extra candidates, all in stdlib or
   module dependencies.
4. **At every site that can emit an edge, `len(Targets) > 1` is
   unchanged**, so `Edge.Ambiguous` cannot move either. This is the
   assertion that matters, because `packages/codeindex/go/typed.go`
   computes ambiguity *before* dropping unindexed candidates, deliberately.
   195 sites do change that verdict; every one of them has no in-tree
   candidate at all and therefore emits nothing.

The difference has one cause. CHA's universe of concrete types is whatever
`ssautil.AllFunctions` reached: package-level functions, exported types of
syntactic packages, and everything structurally reachable from a type that
was converted to an interface — a rule x/tools's own doc comment calls
"unprincipled" and carries a standing TODO to replace. On this repository
that admits `(*embed.file).Name`, a type nothing here can name, while
omitting most of the error types in the same dependencies. The types path
enumerates every named type the loaded tree can reach, which is a
definition rather than a reachability artefact, and which can only be a
superset.

`Status.InvokeSites` moves with it, 1,378 → 1,405 on this repository: 27
call sites that now resolve to at least one candidate, all of them in
dependencies.

**And the output does not move at all.** By the #150 method — two binaries
built from two trees, run against the frozen tree, complete symbol and edge
sets rendered field by field (id, kind, path, line, end line, package,
signature, doc; from, to, kind, line, `cycle`, `ambiguous`, `meta`, `tier`)
and diffed:

| frozen tree | lines compared | differing |
|---|---:|---:|
| the repository at `2815817` | 18,398 | **0** |
| `goldencorpus` | 109 | **0** |
| `brokencorpus` (1 of 2 packages type-checks — the #87 path) | 8 | **0** |
| `sampleproject`, `authoritycorpus`, `generatedproject`, `unseencandidates`, `testfileproject` | 35 | **0** |

An `atlas init --json` of the frozen tree by both binaries differs in three
fields: the timestamp, the temp database path, and the duration.

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
~450 MB resident peak is a share, not a total.

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
there are no lookups to save. That is a deliberate trade — 9 MB of extra
cumulative allocation in short-lived argument slices, one chunk of which is
live at a time, for half the wall time — and it is recorded here rather than
left for someone to discover.

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

### Building the indexes after the bulk load (issue #157) — measured, NOT shipped

**Read this subsection as a costed negative result.** The optimisation works,
the figures below are real, and the code was written, reviewed and then
deliberately not merged. The reason is in
[Why it was not shipped](#why-it-was-not-shipped) at the end.

Every row an `INSERT` writes into `edges` is also threaded into four
B-trees, one of them a five-column `UNIQUE`. Building an index after the
rows land sorts once instead of descending a tree per row, so the candidate
`Ingest` dropped the **non-unique** indexes on `symbols` and `edges` for the
load and rebuilt them at the end — but only when the table it was filling was
empty, which is `atlas init` and nothing else.

Issue #157 predicted 2x on the edge path. It is 6%. The figures below are
what the prediction cost to check, and they are printed in full because the
gap between them and the estimate is the useful part.

Everything in this subsection was taken on this branch at `2815817`, on the
machine in "The machine and the corpus" above, against the synthetic corpus
`benchIngestShape` (661 files, 5,288 symbols, 10,576 edges). **The machine
was shared** — a VM held about 80% of one core throughout, and other Go test
suites came and went — so every wall-clock figure here is a **median of 9
runs x 5 iterations** with its full observed range printed beside it, and
each table was taken in a window where the one-minute load average was under
2. Two runs of the same arm minutes apart differed by up to 7%, which is why
each table below comes from a single interleaved command rather than from
figures collected at different times.

**The edge load in isolation** — 10,576 edges into an empty table, one
transaction, WAL, the same chunked multi-row `VALUES` writer `Ingest` uses:

```sh
go test ./packages/store -run NONE -bench BenchmarkEdgeLoad \
  -benchtime 5x -count 9 -benchmem
```

| arm | median | range over 9 | vs. today |
|---|---:|---|---:|
| `_IndexesDuringInsert` — all four maintained during the load (before) | 147.7 ms | 144.7–153.0 | — |
| `_DeferNonUnique` — the three non-unique built after (the candidate) | **138.5 ms** | 137.7–143.7 | **−9.2 ms, 1.07x** |
| `_DeferAll` — the unique one dropped too (a ceiling, not a candidate) | 137.7 ms | 136.2–194.0 | −10.0 ms |
| `_NoIndexes` — no maintenance and no build (the floor) | 117.7 ms | 115.8–120.5 | −30.0 ms |

The floor row is what makes the other three legible: **all four indexes
together are 30 ms of a 148 ms load, 20%.** A 2x was never on the table.
With #109's batched writer already in place the largest single cost on
`modernc.org/sqlite` is Go-side parameter binding — `conn.bind` is 29% of
`writeEdges` in a CPU profile — and no amount of index scheduling touches
it.

**The `symbols` load in isolation** — 5,288 symbols into an empty table.
Only four of its five indexes can come off: `qualified_name`'s uniqueness is
a column constraint, so SQLite built that index itself, it has no DDL of its
own to rebuild from, and `DROP INDEX` refuses it. The load keeps probing it
either way.

```sh
go test ./packages/store -run NONE -bench BenchmarkSymbolLoad \
  -benchtime 5x -count 9 -benchmem
```

| arm | median | range over 9 |
|---|---:|---|
| `_IndexesDuringInsert` | 100.1 ms | 98.6–103.6 |
| `_DeferNonUnique` (the candidate) | **96.5 ms** | 95.0–97.5 |

−3.6 ms, 1.04x. Small, but the two ranges do not overlap at all, so it is a
real difference rather than a lucky median.

**End to end**, which is the number that should be quoted:

```sh
go test ./packages/store -run NONE -benchtime 5x -count 9 -benchmem \
  -bench 'BenchmarkIngest_(Fresh|RescanChanged)(_IndexesDuringLoad)?$'
```

| benchmark | before (`_IndexesDuringLoad`) | after | change |
|---|---:|---:|---:|
| `BenchmarkIngest_Fresh` — `atlas init`, empty DB | 295.6 ms | **280.5 ms** | **−15.2 ms, 1.05x** |
| `BenchmarkIngest_RescanChanged` — `atlas scan` after an edit | 179.3 ms | 180.8 ms | +1.4 ms, inside the spread |

Ranges: Fresh 277.4–300.2 after against 293.9–302.1 before; RescanChanged
179.8–186.3 after against 177.1–180.6 before.

Against a full `atlas init` at roughly 1,600 ms, 15 ms is **about 1%**. It is
worth having and it is not worth describing as anything more.

The `RescanChanged` row is the one that proves the gate rather than the
saving. A rebuild sorts every row in the table, not only the ones this
ingest wrote; on a rescan that is the whole graph sorted to save maintenance
on the handful of rows that changed, which is a pessimisation. So
`deferIndexBuild` refuses any table that is not empty, and the two arms
differ only by the two `SELECT 1 FROM … LIMIT 1` probes that ask. The +1.4 ms
is inside the run-to-run spread and the ranges overlap.

Allocation, same runs. **Cumulative allocation** (`B/op`) and counts
(`allocs/op`), neither of which is load dependent; no resident-peak figure
was taken for the ingest, and these do not stand in for one:

| benchmark | before | after |
|---|---|---|
| `Ingest_Fresh` | 221,382 allocs / 20.19 MB cumulative | 221,588 allocs / 20.20 MB cumulative |
| `EdgeLoad` | 53,675 allocs / 6.696 MB cumulative | 53,710 allocs / 6.697 MB cumulative |
| `SymbolLoad` | 75,205 allocs / 7.099 MB cumulative | 75,310 allocs / 7.103 MB cumulative |

The deferral costs about 206 extra allocations and 9 KB of extra cumulative
allocation per fresh ingest: the `pragma_index_list` query and seven DDL
statements.

#### The threshold, and why it is 2,000

`deferIndexBuild` also declines batches under `minDeferredIndexRows`,
because taking the indexes off and putting them back is not free even when
there is nothing to sort:

```sh
go test ./packages/store -run NONE -bench BenchmarkDeferIndexBuild_FixedCost \
  -benchtime 2000x -count 5 -benchmem
```

0.563 ms (median of 5 x 2,000; range 0.553–0.573), for one query and six DDL
statements on an empty `edges`, inside an already-open transaction — each
`DROP`/`CREATE` makes SQLite rewrite and reparse the whole schema. Against a
saving of 0.87 us per edge row that crosses over near 640 rows; `symbols`,
with a fourth index to rebuild and a smaller per-row saving (0.68 us),
crosses nearer 1,030. Two thousand clears both with margin.

#### What was measured and rejected

Recorded so the same afternoon is not spent twice. The first three were
measured here, on this branch, by the commands above. The last group was
**not**: it comes from the survey written up in issue #157 and is repeated
here as a pointer, not as a figure this page stands behind.

- **Dropping `edges_dedupe_idx` too: 0.8 ms, and it is the dangerous one.**
  That index is `UNIQUE` and it is not an accelerator —
  `packages/store/queries/edges.sql` writes with `INSERT OR IGNORE`, so the
  index *is* the deduplication. Issue #157's alternative was to move dedup
  into a Go map so the index could be dropped. `_DeferAll` prices that at
  137.7 ms against `_DeferNonUnique`'s 138.5 ms — 0.8 ms, inside the
  run-to-run spread — because building a five-column unique index over the
  whole table costs about what maintaining it during the load costs. Nobody
  should trade the graph's uniqueness key and its surrogate-id ordering for
  that. `TestRebuildableIndexes_NeverOffersAUniqueIndex` is the guard.
- **Rebuilding on a populated table: not measured as a saving, because it
  cannot be one.** The rebuild's cost scales with the table and the saving
  with the batch. This is a gate, not a tuning parameter.
- **Hard-coding the `CREATE INDEX` text in `ingest.go`: rejected on
  maintenance, not speed.** A copy of the migration's DDL is a second
  definition nothing keeps in step — migration 0020 changes an index,
  `ingest.go` keeps rebuilding the 0018 shape, and every query that index
  served silently gets slower with nothing failing. The rebuild statement is
  SQLite's own text read back out of `sqlite_master`, and
  `TestIngestDeferred_RestoresEveryIndexVerbatim` compares the whole of
  `sqlite_master` for both tables against a load that never touched them.

Three more were priced in **issue #157's own survey and are not re-measured
on this page** — go to the issue for the numbers, and do not quote them from
here. In its author's summary: `synchronous` FULL/NORMAL/OFF made no
difference, because the ingest is already one transaction and there is one
fsync either way; compressing the store has nothing to win, the whole
database being 3.9 MB; and normalising `file_path` to an integer FK is a
sub-1 MB saving on a 900 KB table despite 94.5% duplication.

#### Why it was not shipped

Two numbers, one of which is not about speed.

**The gain is about 1% of an `atlas init`** — 15.2 ms off 1,600 ms, from a 6%
improvement on a load that is itself a small part of the run. Real, measured,
and reproducible; also small enough that it earns nothing on its own.

**The cost lands on atlas's own SQL, in the one place that is embarrassing.**
SQLite cannot bind a table or an index name as a parameter, so `DROP INDEX`
and the emptiness probe must be built by string concatenation, and the rebuild
re-executes the `CREATE INDEX` text SQLite itself recorded in `sqlite_master`.
The implementation handles this about as carefully as it can be handled — the
deferrable tables are a closed literal set, and the index names come from
`pragma_index_list` on those tables rather than from a caller — but the
statements are still not statically readable, and `atlas sql` is right to say
so:

| | operations | unresolved | resolved fraction |
| --- | --- | --- | --- |
| without the deferral | 145 | 10 | 0.9310 |
| with it | 149 | **13** | **0.9128** |

That crosses both bars `test/acceptance/dogfood_test.go` commits atlas to —
`minSQLResolvedFraction` 0.9200 and `maxSQLUnresolved` 10 — and it crosses
them in the direction that matters: three more operations that atlas cannot
read, added to atlas's own data layer, in exchange for 1%.

The gate could have been moved instead; its own comment invites that, with a
reason. The reason would have had to be "we added dynamic SQL to the data
layer of the tool whose job is reading SQL, to save 15 ms", and that is not a
reason, it is a description.

**What would change the answer.** The index set on `symbols` and `edges` is
fixed by the migrations, so the DDL could be constants instead of
`sqlite_master` text, with a test asserting the constants still match
`pragma_index_list` after migration. That keeps the 6%, removes the dynamic
SQL, and leaves both floors untouched. It is more code than the saving
justifies today; it is written down so the option is not lost.

Everything above this subsection stays because the measurement is the
deliverable. A 2x prediction that turns out to be 6% is worth more recorded
than repeated.

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
   the rest of phase A is visible for the first time, and it was `go/types`
   and `go/ssa`. `go/ssa` is gone since #155 (§1), which took 216 MB of
   cumulative allocation and 143 MB of resident peak out of a load and left
   the edge set byte-identical; what remains is `go/types` and the
   `go list` it is fed from. Parallelising phase 2 is worth reconsidering
   *after* a fresh phase-level measurement, not before.
5. Batched ingest and the parallel orchestrator passes: done, above. The
   parallel passes are worth 8.7% end to end now rather than 0.9%, on the
   same code — the denominator moved.
6. **The ingest's SQLite-side costs are close to spent (#157).** Deferring
   the index builds took a fresh ingest from 295.6 ms to 280.5 ms, about 1%
   of an `atlas init` — measured, and **not shipped**, because the technique
   needs string-built DDL and that cost atlas three unresolvable operations
   in its own data layer. What is left of the edge load is 20% index work
   and the rest is parameter binding and statement
   preparation inside `modernc.org/sqlite`. Anyone reaching for the next
   ingest win should read §3's "measured and rejected" list first: dropping
   the unique index has been priced here, and durability pragmas, store
   compression and normalising `file_path` were priced in issue #157. None
   of them is where the remaining time is.

## 5. Annotation parsing — allocation (issue #152, cause 1)

**Every figure in this section is CUMULATIVE ALLOCATION (`B/op`, as
`testing`'s `-benchmem` reports it) unless it says RESIDENT PEAK.** A scan
that allocates 838 MB does not hold 838 MB; most of it is freed as it goes.
Conflating the two is what issue #152 was opened to stop, so this section
tags every figure. There are exactly two resident-peak figures here, both
under "What the unbounded read costs" below; everything else is churn.

`ParseRelative` reads one file per source file in the tree, so whatever it
allocates per file is multiplied by the file count. An `-alloc_space` profile
of a scan put it at 142.09 MB cumulative, 17.7% of the 803 MB that scan
allocated, and most of the 33.91 MB of it in `bytes.growSlice`. The cause was in
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

### Two ceilings, because one fixture cannot see both regressions

`TestParseRelative_TinyFileAllocationCeiling` measures the same quantity as
the 551-byte row without a benchmark harness — a
`runtime.MemStats.TotalAlloc` delta over 2,000 parses — and fails above
16 KB. Seven separate processes on the machine above put the current parse
at 5,481 / 5,481 / 5,484 / 5,484 / 5,503 / 5,518 / 5,521 B: **median 5,484,
spread 40 B (0.73%)**. It is not the deterministic figure this page used to
print as `5,463 B`; a `TotalAlloc` delta is repeatable to tens of bytes, not
to the byte, and the regexp engine's own cache is enough to move it.

That ceiling guards ONE of the two things the old reader did wrong, and this
page previously claimed it guarded both. It is 16 KB against a 5.5 KB
observation, so it fires on any FIXED per-file overhead above about 10.9 KB
— the 64 KB scanner buffer, which is 74,029 B and 4.4x the ceiling. It
cannot fire on a reintroduced whole-file or per-line copy, because on a
551-byte file that copy is 551 bytes. `TestParseRelative_LargeFileAllocation
Ratio` is the ceiling for that class: the 512 KB generated file, bounded at
**2.8 bytes allocated per byte of file** against an observed 2.278
(1,194,316 B over 524,349 B, three processes spanning 2.277–2.280).

Both were proven by mutation rather than argued. Each mutation went into
`ParseRelative` and was reverted; every figure is from the run, medians of
three:

| mutation | 551 B fixture | vs 16 KB | 512 KB fixture | ratio | vs 2.8 |
|---|---:|---|---:|---:|---|
| none | 5,484 | pass | 1,194,316 | 2.278 | pass |
| + 64 KB buffer per file | 72,051 | **FAIL** | 1,266,056 | 2.415 | pass |
| + one whole-file copy | 6,009 | pass | 1,739,089 | 3.317 | **FAIL** |
| + one string copy per line | 7,010 | pass | 1,921,849 | 3.665 | **FAIL** |

Read the pass columns as carefully as the failures. The regression that
actually happened — the 64 KB buffer — is 4.4x the small-file ceiling and
6% on the large one. The whole-file copy is 46% on the large file and uses
a tenth of the small file's headroom. Neither fixture gates the other's
regression, which is why the constant is now two constants. The 2.8 is the
observation plus half a copy of the input, so any change that copies the
file once more fails and nothing that merely grows the retained annotations
comes close.

Both carry a `//go:build !race` tag: the race detector's own shadow
allocations land in `TotalAlloc` too and put the small parse at 360,920 B,
so under `-race` the number measures the detector.

### What the unbounded read costs

The change removed the `bufio.Scanner`'s 1 MB token cap, and that is not a
free widening. It fixed a real failure — a file with a longer single line (a
minified bundle, a generated lookup table) used to abort with
`bufio.ErrTooLong` and lose EVERY annotation in that file, not just the long
line's — and it removed the only per-file bound on how much memory one file
could occupy in this parser. `os.ReadFile` stats the file and allocates all
of it.

**RESIDENT PEAK, `VmHWM` either side of one `ParseRelative`, medians of
three processes** — the only two resident figures in this section:

| file | resident peak of one parse |
|---|---:|
| 2 MB, single line | 4.4 MB |
| 32 MB, single line | 63.5 MB |
| 64 MB, single line | 126.6 MB |
| 32 MB, ordinary source | 59.3 MB |

About twice the file, not once, because the file is read whole and then
every comment line is copied into a `logicalLine` string — and on a minified
bundle the banner comment IS the whole file. Multiply by `--jobs`: a 64 MB
generated file in a tree scanned at `--jobs=12` is a resident cost this
parser used to refuse and now accepts.

It was left unbounded on purpose, and **issue #156 has since bounded it — see
§6.** The argument recorded here was that a cap inside `ParseRelative` cannot
degrade gracefully, the parse being whole-file, so a bound could only skip the
file entirely: the same "lost all of its annotations" failure the cap was
removed for, with a different message. That still holds, and it is why the
bound did not end up here. It ended up one level out, in the single function
every pass now reads through (`annotations.ReadSource`), where the caller
that asked for the file is still holding the ledger it can record the refusal
on. The height, 16 MiB against a 106 KB largest file, is chosen so it never
fires on the inputs the 1 MB cap used to fire on.

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

| | B/op (cumulative allocation) | allocs/op |
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

Two caveats on the end-to-end table. The before-total here is 838 MB of
cumulative allocation, not the 803 MB issue #152 measured for the same
quantity: a different tree state, measured fresh on this
branch point, because a before/after pair is only meaningful within one
corpus. And no wall-clock claim is made from it: the first iteration of each
`-count 3` set ran against a cold `go/packages` cache (6.35 s and 3.11 s
against ~0.9 s for the rest), so those medians are a measurement of cache
warmth, not of this change. The `B/op` figures were stable to within 0.2%
across all three.

### What was not done

The remaining per-parse cost of the 551-byte file is ~5.5 KB of cumulative
allocation, and an `-alloc_space` profile of it is now made of things
proportional to what the
parse KEEPS rather than to the file's size: the comment strings retained in
`logicalLine` (38%), the `make([]shared.Annotation, 0, 8)` in `ParseBytes`
(28%), the file itself (9%), and `regexp.FindStringSubmatch` (12%). Shrinking
any of those is a different change with a different argument — in particular
the three regexes are run per comment line and a cheap `@` pre-filter would
skip most of them — and none of it was measured here, so none of it is
claimed.
6. **Allocation is now gated, and the remaining question is whether 684 MB of
   CUMULATIVE ALLOCATION per scan is justified rather than merely stable.**
   (709 MB before issue #156 removed the redundant per-file reads; §6.)
   `TestDogfood_ScanMemoryCeiling` stops it growing quietly; it says nothing
   about whether the current figure is right. Issue #152 names three
   candidates. One is now closed and two are answered NEGATIVELY, on
   measurement:
   `annotations.ParseRelative` rebuilding every file it reads — **fixed**,
   -15.9% of a scan's cumulative allocation (see section 5);
   `types.Info.Types` populated and never read — **inherent**: leaving the map
   nil saves 98 MB of cumulative allocation and then SSA cannot be built at
   all, panicking `no type for *ast.SelectorExpr`;
   SSA bodies for every transitive dependency — **inherent**: building only the
   initial packages panics in `prog.Build()`.
   The gate is the floor under whatever comes next, not a substitute for it.

## 6. One read per file per pass (issue #156)

**Every figure in this section is CUMULATIVE ALLOCATION (`B/op`) or BYTES
READ FROM DISK unless it says RESIDENT PEAK.** The two resident figures are
under "What did not move" below.

### What was wrong

Every non-test `.go` file in a scan was opened and read by atlas's own code
**six times**, each read allocating its own copy of the bytes. Issue #156
names four of them; the other two are the same two sites reached from a
second pass, and they cost the same:

| # | pass | site | why |
|---|---|---|---|
| 1 | Go sub-scanner | generated-header probe | `os.Open` + `bufio.Scanner`, first lines only |
| 2 | Go sub-scanner | `parser.ParseFile(fset, path, nil, …)` | **a nil `src` is what makes go/parser open the file itself** |
| 3 | Go sub-scanner | `annotations.ParseRelative` | `@api` endpoint discovery |
| 4 | pattern recognisers | a second `parser.ParseFile(…, nil, …)` | different AST shapes; see that pass's godoc |
| 5 | annotation walk | `os.ReadFile` | annotation extraction, every language |
| 6 | annotation walk | `os.Open` + `io.Copy` | SHA-256 for the incremental cache |

`go/packages` reads them once more for type checking, inside x/tools where
atlas has no say; that read is not counted here and was not removed.

Only two of the six had names in the `-alloc_space` profile —
`io.copyBuffer` 28.4 MB and `os.readFileContents` 25.9 MB, 8% of a 717 MB
scan. The 28.4 MB was never the hashing: `io.Copy` allocates a 32 KB staging
buffer per file because neither `*os.File` nor a `hash.Hash` offers it a
fast path. SHA-256 itself is about 1% of scan time and is unchanged — a
faster hash was measured and rejected under "What was not done".

### The change

Each pass reads once, through `annotations.ReadSource`, and fans the bytes
out: hash from them, probe the generated header from the first lines of
them, parse annotations from them (`ParseBytes` already existed), and pass
them to `parser.ParseFile` as `src`.

### Reads, measured rather than counted by eye

`/proc/self/io` `rchar` is the kernel's own count of bytes returned by
`read(2)`, page cache included, and nothing inside Go can bypass it — which
is why the assertion is written against it rather than against a package
seam a new reader would walk straight past. One 4 MB fixture file, warm
cache, bytes read divided by the fixture's size:

| | before | after |
|---|---:|---:|
| `IndexProject`, ordinary file | 20,976,046 B — **5.00 copies** | 12,583,211 B — **3.00 copies** |
| `IndexProject`, generated file | 8,392,908 B — 2.00 copies + a 4 KB probe | 8,388,811 B — **2.00 copies** |
| `goscan.Scan` alone, ordinary file | 8,392,941 B — 2.00 copies | 4,194,475 B — **1.00 copy** |
| `goscan.Scan` alone, generated file | 4,203 B | 4,194,460 B — 1.00 copy |

The last row is the one place this costs anything. The header probe used to
stop after one `bufio` fill, so a file the ledger excluded was never read in
full; now it is read before it is classified, because the classification
reads the same bytes as everything else. Row two is why that is not a
regression in the product: `IndexProject`'s annotation walk reads every file
whatever the generated ledger decided, so within a scan the full read is one
the process was going to pay anyway, and the 4 KB probe on top of it is what
disappears. A generated file is 4 KB cheaper per scan, not 4 MB dearer. A
caller using `goscan.Scan` on its own against a codegen-heavy tree is the
case that pays, and it pays one read per excluded file.

`TestScan_ReadsEachFileOnce` and `TestIndexProject_ReadsEachFileOncePerPass`
assert those numbers. They skip on non-Linux, where `/proc/self/io` does not
exist.

### Why the answer is three and not one

Issue #156 asks for one read per scan. One read per PASS is what is
implementable, and the difference is a decision rather than a shortfall.
`IndexProject` makes three sequential passes and each has a recorded reason
it cannot consume the previous one's product: the Go scanner's ASTs are
adopted from the type checker and keyed by node pointers no other pass holds;
the pattern recognisers walk different AST shapes; the annotation walk covers
every language atlas reads and its output ORDER is load-bearing for feature
attribution. Collapsing the three reads into one means caching every file's
bytes from the first pass until the last is done with them — the whole tree's
source resident for the length of a scan, a ceiling that scales with the
repository rather than with `--jobs`. That is the trade #152 was opened
about, taken in the wrong direction. The number worth attacking is the number
of passes, not the number of reads inside one.

### Allocation

Machine and corpus: Intel Core i7-10750H, 12 logical CPUs, `GOMAXPROCS=12`,
linux/amd64, Go 1.26.4, machine not quiet. Both arms scanned a **fixed
681-file corpus** — `git archive HEAD` of `2815817` extracted to a temp
directory — rather than the working tree, because this branch adds four `.go`
files to the repository and the benchmark's default corpus is the repository
itself. Comparing 681 files against 685 would have credited this change with
four files' worth of someone else's allocation.

```sh
ATLAS_BENCH_ROOT=/path/to/frozen/corpus \
go test ./packages/codeindex -run '^$' \
  -bench 'IndexProject|GoScan_Typed|GoScan_ASTOnly|PatternRecognizers|AnnotationWalk' \
  -benchtime 1x -benchmem
```

Three samples per arm, medians. CUMULATIVE ALLOCATION:

| benchmark | before `B/op` | after `B/op` | | before `allocs/op` | after `allocs/op` | |
|---|---:|---:|---:|---:|---:|---:|
| `IndexProject` | 709,129,728 | **683,731,448** | **−3.58%** | 8,821,876 | 8,814,090 | −0.09% |
| `GoScan_Typed` | 636,810,936 | 634,182,704 | −0.41% | 7,984,058 | 7,981,607 | −0.03% |
| `GoScan_ASTOnly` | 96,656,128 | **87,829,648** | **−9.13%** | 1,744,491 | 1,737,375 | −0.41% |
| `PatternRecognizers` | 34,610,728 | 34,615,072 | +0.01% | 816,304 | 816,692 | +0.05% |
| `AnnotationWalk` | 40,495,064 | **16,575,128** | **−59.1%** | 53,355 | 48,344 | −9.4% |

**The bytes and the count say different things again, and both are printed
for the same reason as in §5.** What was removed is a small number of large
allocations per file — a whole copy of the file, three times over, plus a
32 KB `io.Copy` buffer — so `B/op` moves 3.6% end to end while `allocs/op`
barely moves at all. A change that halves an allocation's size does not halve
the number of allocations, and a reader who sees only the first column will
draw the wrong conclusion about what changed.

`GoScan_Typed` moves 0.41% and `GoScan_ASTOnly` 9.13% for the same reason
the two exist: under type checking the parse is adopted from `go/packages`
rather than done here, so read #2 was already not happening on most files,
and 637 MB of that arm is the type checker. The AST-only column is where
this package's own reads live, and that is where they show.

The allocation gate has correspondingly more room: `TestDogfood_ScanMemoryCeiling`
reports **684,388,192 B/op, 8,836,783 allocs/op** on the working tree (685
files), 28.7% under the bytes ceiling where it was 26.2% under before. The
ceilings are not moved here; they are a floor under regressions, and lowering
them belongs in a change that argues for the new number.

### What did not move

**RESIDENT PEAK, `VmHWM` of a whole `atlas init`,** by the method in
§Measuring resident peak, seven INTERLEAVED pairs (before and after run
back to back within each pair, so ambient load drifts across both arms
equally) against the frozen corpus:

|  | samples (MB) | median |
|---|---|---:|
| before | 438, 464, 467, 467, 470, 475, 490 | **467 MB** |
| after | 445, 458, 466, 471, 475, 479, 495 | **471 MB** |

0.9% apart, on a quantity this page documents as repeating to ±10%. **No
resident-peak conclusion is available from these runs**, and the interleaving
is why that is stated confidently rather than hopefully: an earlier
non-interleaved pair of five-run sets put the same two binaries 5.8% apart,
in the same direction, and that difference was drift.

Wall clock, five interleaved pairs of a whole `atlas init` over the frozen
corpus: **1332 ms → 1317 ms**, medians, −1.1%, with the after arm lower in
four pairs of five. Reading that as a speedup would be reading noise; it is
recorded to show the change does not cost time, which is the only claim three
percent of a scan's allocation entitles it to.

### The per-file size bound, decided once

`annotations.MaxSourceBytes` is **16 MiB**, and this is where the pipeline
acquires a per-file bound for the first time since #152 removed the last one.

The argument in full is on the constant. In short: holding a file's bytes for
the whole of its processing is what makes one read enough, and it also puts
one file's size into the momentary footprint next to the AST built from it.
The 1 MB `bufio` token cap that used to bound this was removed for a good
reason — it fired on ORDINARY files (anything with one long line) and cost
every annotation in the file rather than the long line's. A whole-file bound
is a different decision from a line bound: the largest source file in this
repository is 106 KB, 154 times under 16 MiB, and a `.go` file at the bound
would cost `go/parser` several hundred megabytes of AST before producing a
symbol. It also makes the ceiling sayable: **`--jobs × 16 MiB` of source
bytes in flight, 192 MB at `--jobs=12`** — the annotation walk and the pattern
pass are the parallel ones, and the Go sub-scanner's walk is serial, so it
contributes one file, not one per worker. RESIDENT is roughly twice the
source figure, because every pass derives something file-sized from the bytes
it was handed: `logicalLine` strings in the annotation parser, an AST in the
other two. §5's table measured that ratio directly — a 32 MB file, 63.5 MB
resident for one parse.

A file over the bound is not read. Callers report it the way they already
report a file they cannot parse — a warning naming the file and the reason —
and the scan carries on. It gets no content hash either, so
`packages/indexfresh` classifies it `StateAbsent` and its callers fall back to
their non-incremental path for that file: "rescan this every time" rather than
"trust a digest nobody computed".

Nothing in this repository is within two orders of magnitude of the bound, so
**the bound is not measured here and no figure claims it is.** It is a
ceiling, and the evidence for a ceiling is the argument for its height plus
the tests that show it fires and degrades as described
(`TestReadSource_BoundIsCheckedBeforeAnythingIsAllocated`,
`TestReadSource_AtTheBound`, `TestScan_OversizeFileIsSkippedWithAWarning`).

### Determinism (#120)

Same corpus, `atlas init` from a binary built either side of the change,
compared on the resulting databases: **symbols, edges (with kind, path, line,
resolution tier and meta) and file hashes are byte-for-byte identical**, as
are the 25 scan warnings and every count in the `--json` summary. The golden
corpus suite, `TestScan_SkippedFilesAreDeterministic` and the store's
determinism tests pass unchanged.

One thing that comparison found and this change did not cause: **SQLite
rowid order in `symbols` and `edges` is already not stable between two runs
of the same binary.** Two `atlas init` runs of the unmodified baseline differ
in 10,394 rows of insert order; two runs of this branch differ in 10,388;
one of each differ in 10,392. The magnitudes are the same, so the property
predates this branch. It is not a determinism failure of the kind #120 is
about — the scanner's own output order is asserted by the golden corpus and
holds — but it does mean insert order is not something a reader can use to
compare two databases, and this page says so rather than leaving the next
person to rediscover it.

### What was not done

**A faster hash: measured and rejected.** CRC-32 is 28x faster than SHA-256
on this workload and saves 13 ms of a ~1,320 ms `atlas init`. It is refused
on correctness rather than on the 1%: a hash collision here means a CHANGED
file is classified unchanged, and the incremental scan then serves stale
symbols for it indefinitely. Collision resistance is the property being
bought, and 13 ms is not a reason to stop buying it.

**Wrapping `os.ReadFile`: measured and reverted.** The obvious shape for
`ReadSource` — `os.Stat`, compare, `os.ReadFile` — stats every file twice,
because `os.ReadFile` stats again for its own size hint, and the second stat
allocates an `os.fileStat` per file across ~1,850 calls per scan. On
`BenchmarkPatternRecognizers`, the one pass that gains a stat rather than
losing a read, medians of three: 34,610,728 B/op before the change,
34,764,688 with the wrapper (+0.44%), 34,615,072 with the open + fstat +
`ReadFull` loop that shipped (+0.01%). The 154 KB is not the point; a pass
that ends up measurably worse is a pass someone will later be right to
question.

**Collapsing the three passes: not attempted, and the reason is above.** It
is the only remaining way to reach one read per scan and it buys the read by
selling the memory ceiling.
