//go:build dogfood

// The memory gate: a ceiling on what indexing this repository allocates.
//
// Everything else in this directory gates what atlas SAYS. This gates what it
// COSTS, and it is here because nothing did. `atlas gates atlas` asserts
// attribution, SQL resolution and patch coverage; the supply-chain job asserts
// reproducibility; and #150 — an O(E^2) adjacency rebuild worth 37% of scan
// CPU and 7.6x the allocation count — sat in the tree for months and was found
// by someone going looking, not by a red X. Issue #152 asked for the red X,
// and this is it.
//
// It lives in the acceptance layer rather than beside the benchmark because a
// gate is a claim about the shipped artefact, and because the constants below
// belong next to minAttributedFraction and maxSQLUnresolved in
// dogfood_test.go: same contract, same obligation to say how each number was
// measured, same rule that raising one costs an argument.

package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The scan's allocation ceiling, and what the numbers mean.
//
// UNITS FIRST, because conflating them is the confusion that produced #152.
// These are `B/op` and `allocs/op` from `b.ReportAllocs`: CUMULATIVE BYTES
// ALLOCATED over one IndexProject call, not resident memory. A scan does not
// hold 836 MB. Peak RSS of a full `atlas init` on this same tree is 436 MB —
// median of five (432/435/436/439/441), read from /proc/<pid>/status VmHWM,
// method in docs/performance.md. Churn and residency move for different
// reasons and a ceiling on one says nothing about the other.
//
// This gate is on churn, and the choice is deliberate rather than convenient:
// churn is what the collector is paid to clean up, it is what #150 blew out
// by 8.6x, and it is stable enough to gate (see the spread below) in a way a
// resident-set figure taken on someone else's CI runner would not be. What it
// does NOT do is bound residency, and nobody should read it as if it did.
// Nobody has measured this repository's resident peak before #150 either, so
// "the fix saved 6 GB of RAM" is a sentence this constant does not support.
//
// MEASUREMENT. linux/amd64, Intel Core i7-10750H, Go 1.26.4, 12 logical CPUs,
// this tree at the mem/gate branch point (674 .go files under the walkers'
// skip rules; 675 once this file exists), machine not quiet:
//
//	go test ./packages/codeindex -run NONE -bench '^BenchmarkIndexProject$' \
//	        -benchtime 1x -benchmem -count 1
//
// Five separate processes. Medians: 835,960,496 B/op and 9,010,665 allocs/op.
// Issue #152's independent measurement of the same benchmark reported
// 835,174,704 and 9,010,894, which is inside the run-to-run spread below.
//
// WHY AN ABSOLUTE NUMBER AND NOT BYTES-PER-FILE. The same reason
// maxSQLUnresolved is a count and not a fraction: a per-file figure is
// satisfiable by adding files, so a scan could double its churn while the
// ratio held steady and the gate stayed green. The price of the absolute form
// is that ordinary repo growth eventually reaches it. That is what the
// headroom is for, and it is why the failure message prints the file count —
// so a reader can tell growth from regression without opening a profiler.
//
// WHY 15% OF HEADROOM AND NOT 2% OR 100%. Two different things have to fit
// under it and only one of them is noise:
//
//   - Noise is almost nothing. Fourteen samples spanning ATLAS_BENCH_JOBS
//     1/4/12 and GOMAXPROCS 2/12 spread 834,476,160-837,336,792 B/op (0.34%)
//     and 9,006,747-9,013,315 allocs/op (0.07%). Unlike the ns/op figures in
//     docs/performance.md, which move up to 35% run to run on this machine,
//     allocation accounting does not care about ambient load or core count.
//     About 1% covers it.
//   - The rest is corpus growth, and that is the real reason the ceiling is
//     not tight. The benchmark scans THIS repository, so B/op rises as the
//     repository does — roughly 1.24 MB of churn per .go file. 15% is about
//     100 more .go files; the tree grew 653 -> 674 across the whole of #109
//     and #150. So this should need re-arguing about once every five
//     milestones rather than every week.
//
// A ceiling too close to the observation fails on a quiet Tuesday and gets
// deleted; one too far never fires. These are set so that neither is the
// likely outcome.
//
// TIGHT ENOUGH TO FIRE. #150's rebuild cost this benchmark 7.18 GB and
// 67,824,856 allocs (docs/performance.md §1) — 7.5x and 6.5x the ceilings,
// caught the day it landed rather than months later. One new allocator the
// size of #152's own first finding (ParseRelative rebuilding every file it is
// about to read: 142 MB, 17% of the total) trips it on its own. Measured
// sensitivity, from the mutation runs recorded on the test below: a single
// change adding more than about 12% to scan churn fails the gate. That is not
// an assertion; the runs are in the table.
//
// TWO CEILINGS, NOT ONE, and the mutation runs are why. The buffer mutations
// moved B/op by up to 144% while allocs/op did not budge — the same number of
// allocations, each larger. The Sprintf mutation did the reverse: +7.4% on the
// count at +0.7% on the bytes. Neither ceiling can see the other's regression,
// and #150 was the second shape (many small short-lived objects, most of the
// cost paid in GC rather than in resident memory), which is exactly the one a
// bytes-only gate would have missed.
//
// IF IT TRIPS. Do not raise these to make CI green. Profile first —
// `-memprofile`, then `go tool pprof -alloc_space` — and if the growth is
// real and wanted, raise the ceiling in a commit that says what grew and by
// how much, the way maxSQLUnresolved makes every new unresolvable query
// argue for itself. A ceiling nobody has to argue with has stopped measuring
// anything.
const (
	maxScanBytesPerOp  = 960_000_000 // 835,960,496 observed, +14.8%
	maxScanAllocsPerOp = 10_400_000  // 9,010,665 observed, +15.4%
)

// benchTimeout bounds the child. It has to build the codeindex test binary,
// which on a cold CI module and build cache is most of the cost; the scan
// itself is about a second. Generous on purpose: a gate that flakes on a slow
// runner teaches people to ignore it.
const benchTimeout = 10 * time.Minute

// TestDogfood_ScanMemoryCeiling runs BenchmarkIndexProject in a child process
// and fails when it allocates more than the ceilings above.
//
// A subprocess rather than testing.Benchmark in-package, for one reason that
// decides it: CI runs `go test ./... -race`, and the race detector changes
// allocation accounting. A gate whose observed value depends on how the suite
// that contains it was invoked is not measuring the scan. The child is built
// and run with flags this test chooses, so the number means the same thing on
// a laptop, in the dogfood job, and in the command printed in the failure
// message.
//
// The name starts with TestDogfood so run.sh's `-run TestDogfood` picks it up
// and it rides in the existing blocking `atlas gates atlas` job. It needs no
// coverprofile, so it deliberately does not call setupDogfood.
//
// MUTATION RECORD. The gate was proven to fire before it was trusted. Both
// mutations went into ParseRelative (packages/codeindex/annotations/parser.go),
// which phase B runs over every file it reads, and both were reverted after —
// parser.go is untouched by this branch. Every number below is from the run,
// not from arithmetic:
//
//	mutation                          B/op    vs ceiling   allocs/op  vs ceiling  gate
//	none                       836,447,416       -12.9%    9,015,812     -13.3%   pass
//	scanner buffer +64 KB/file 929,867,736        -3.1%    9,015,462     -13.3%   pass
//	scanner buffer +256 KB/f 1,212,664,856       +26.3%    9,015,068     -13.3%   FAIL bytes
//	scanner buffer +1 MB/file 2,343,121,072      +144.1%   9,014,321     -13.3%   FAIL bytes
//	+1 Sprintf per line        842,490,296       -12.2%    9,680,537      -6.9%   pass
//	+3 Sprintf per line        852,094,600       -11.2%   11,021,266      +6.0%   FAIL allocs
//
// Two things to read out of it. The failures say the gate works; the +6.0%
// row says it works on a SMALL regression, not only on a catastrophic one.
// And the two passing rows matter as much: a gate that fails on any change at
// all is a gate that gets deleted within a month. +64 KB per file is 93 MB of
// extra churn (+11.2%) and stays green, which is where the 15% headroom went
// and is far more than ordinary repo growth will produce in a year.
//
// The bytes rows barely move allocs/op and the Sprintf rows barely move B/op.
// That is not a quirk of these mutations — it is the reason both constants
// exist. See the comment on maxScanBytesPerOp.
func TestDogfood_ScanMemoryCeiling(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	out, err := runIndexProjectBenchmark(t, root)
	if err != nil {
		t.Fatalf("running the benchmark failed, so nothing was gated:\n%v\n\n%s", err, out)
	}

	m, err := parseBenchmarkLine(out, "BenchmarkIndexProject")
	if err != nil {
		t.Fatalf("could not read the benchmark's own output, so nothing was gated: %v\n\n%s", err, out)
	}

	bytesPerOp, ok := m["B/op"]
	if !ok {
		t.Fatalf("benchmark reported no B/op; -benchmem did not take effect:\n%s", out)
	}
	allocsPerOp, ok := m["allocs/op"]
	if !ok {
		t.Fatalf("benchmark reported no allocs/op; -benchmem did not take effect:\n%s", out)
	}
	files := m["gofiles"]

	t.Logf("scan churn over %.0f .go files: %s cumulative allocation (ceiling %s, %+.1f%%), "+
		"%s allocations (ceiling %s, %+.1f%%)",
		files,
		commas(bytesPerOp), commas(maxScanBytesPerOp), pctOf(bytesPerOp, maxScanBytesPerOp),
		commas(allocsPerOp), commas(maxScanAllocsPerOp), pctOf(allocsPerOp, maxScanAllocsPerOp))

	if bytesPerOp > maxScanBytesPerOp {
		t.Errorf("a scan of this repository now allocates %s bytes per op, %+.1f%% over the "+
			"committed ceiling of %s.\n\n"+
			"These are CUMULATIVE BYTES ALLOCATED, not resident memory — see the constant's "+
			"comment before reasoning about RSS from this number.\n\n"+
			"It scanned %.0f .go files. If that count has grown a lot since the ceiling was "+
			"set (675 files), this may be the repository rather than the code; if it has not, "+
			"something on the scan path started allocating. Either way, find out which:\n\n"+
			"    %s -memprofile /tmp/scan.mem -o /tmp/scan.test\n"+
			"    go tool pprof -top -sample_index=alloc_space /tmp/scan.test /tmp/scan.mem\n\n"+
			"Raise maxScanBytesPerOp only with that profile in hand and the reason in the "+
			"constant.",
			commas(bytesPerOp), pctOf(bytesPerOp, maxScanBytesPerOp), commas(maxScanBytesPerOp),
			files, benchCommand)
	}
	if allocsPerOp > maxScanAllocsPerOp {
		t.Errorf("a scan of this repository now makes %s allocations per op, %+.1f%% over the "+
			"committed ceiling of %s.\n\n"+
			"A rising allocation COUNT at flat bytes is the shape of #150 — many small "+
			"short-lived objects, which the collector pays for even though peak RSS barely "+
			"moves. Profile the count, not the bytes:\n\n"+
			"    %s -memprofile /tmp/scan.mem -o /tmp/scan.test\n"+
			"    go tool pprof -top -sample_index=alloc_objects /tmp/scan.test /tmp/scan.mem\n",
			commas(allocsPerOp), pctOf(allocsPerOp, maxScanAllocsPerOp), commas(maxScanAllocsPerOp),
			benchCommand)
	}
}

// benchCommand is the child, spelled out so the failure message can hand the
// reader something to paste. runIndexProjectBenchmark runs exactly this.
const benchCommand = "go test ./packages/codeindex -run NONE " +
	"-bench '^BenchmarkIndexProject$' -benchtime 1x -benchmem -count 1"

// runIndexProjectBenchmark runs the benchmark and returns its combined output.
//
// -benchtime 1x, one iteration: the measured spread across fourteen samples
// was 0.34%, so more iterations buy nothing but minutes. The single iteration
// also carries the process's one-off costs — compiled regexps, sync.Once
// caches — which biases the figure slightly high, i.e. towards the ceiling
// rather than away from it, and matches how docs/performance.md and issue
// #152 took theirs.
func runIndexProjectBenchmark(t *testing.T, root string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), benchTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "./packages/codeindex",
		"-run", "NONE",
		"-bench", "^BenchmarkIndexProject$",
		"-benchtime", "1x",
		"-benchmem",
		"-count", "1",
		"-timeout", benchTimeout.String(),
	)
	cmd.Dir = root

	// ATLAS_BENCH_ROOT would point the benchmark at some other tree, and
	// ATLAS_BENCH_JOBS would change the worker count. The ceiling was measured
	// against THIS repository; a developer who set either for their own
	// profiling run should not get a spurious red from this gate, and should
	// not silently get a green one measured on a different corpus. Both are
	// stripped rather than honoured. (Worker count turned out not to matter —
	// jobs 1, 4 and 12 agreed to within 0.2% — but the corpus does, and the
	// two are stripped together so the child's inputs are stated in one place.)
	cmd.Env = withoutEnv(os.Environ(), "ATLAS_BENCH_ROOT", "ATLAS_BENCH_JOBS")

	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("benchmark did not finish within %s: %w", benchTimeout, ctx.Err())
	}
	if err != nil {
		return string(out), fmt.Errorf("%s: %w", benchCommand, err)
	}
	return string(out), nil
}

// withoutEnv returns environ with the named variables removed.
func withoutEnv(environ []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	kept := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if ok && drop[k] {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// parseBenchmarkLine pulls the value/unit pairs out of a `go test -bench`
// result line:
//
//	BenchmarkIndexProject-12   1   1218100160 ns/op   674.0 gofiles   836265144 B/op   9009937 allocs/op
//
// Pairs are read positionally from the third field on, because the column set
// and its order depend on which of ReportAllocs, SetBytes and ReportMetric the
// benchmark called — matching on fixed offsets would break the first time
// someone adds a metric.
func parseBenchmarkLine(out, name string) (map[string]float64, error) {
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		// name, iterations, then (value, unit) pairs.
		if len(fields) < 4 || !strings.HasPrefix(fields[0], name) {
			continue
		}
		if _, err := strconv.Atoi(fields[1]); err != nil {
			continue // a log line that happens to start with the name
		}
		m := make(map[string]float64, 4)
		for i := 2; i+1 < len(fields); i += 2 {
			v, err := strconv.ParseFloat(fields[i], 64)
			if err != nil {
				return nil, fmt.Errorf("unit %q has non-numeric value %q in %q", fields[i+1], fields[i], line)
			}
			m[fields[i+1]] = v
		}
		if len(m) == 0 {
			return nil, fmt.Errorf("no value/unit pairs in %q", line)
		}
		return m, nil
	}
	return nil, fmt.Errorf("no %s result line in the output", name)
}

// pctOf is how far observed sits from ceiling, signed, for a message that
// says "8.6% over" rather than making the reader subtract two nine-digit
// numbers.
func pctOf(observed, ceiling float64) float64 {
	return (observed/ceiling - 1) * 100
}

// commas groups an integral float for reading. Nine-digit byte counts are
// compared by eye in a CI log more often than anyone would like.
func commas(v float64) string {
	s := strconv.FormatInt(int64(v), 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
