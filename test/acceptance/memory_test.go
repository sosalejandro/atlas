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
// hold 709 MB. Peak RSS of a full `atlas init` on this tree is 473 MB — median
// of five (456/469/473/477/507), read from /proc/<pid>/status VmHWM, method in
// docs/performance.md. That is a 10.8% spread against 0.06% on the number this
// gate bounds, which is the practical reason the gate is on churn: an RSS
// ceiling would need ten times the headroom to survive its own noise, and a
// ceiling with 30% of slack in it does not catch anything. Churn and residency
// move for different reasons and a ceiling on one says nothing about the
// other.
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
// machine not quiet:
//
//	go test ./packages/codeindex -run NONE -bench '^BenchmarkIndexProject$' \
//	        -benchtime 1x -benchmem -count 1
//
// Five separate processes each time. TWO observations, because the tree moved
// under the ceiling between them and only the second one is current:
//
//	tree                                  files          B/op    allocs/op
//	mem/gate branch point      674 (675 with this)  835,960,496    9,010,665
//	this branch, ParseRelative fixed        681      708,904,952    8,822,140
//
// The 127 MB between them is not drift: it is the -15.9% the annotation-parse
// fix bought (docs/performance.md §5), measured here from the other side.
// Issue #152's independent measurement of the first pair reported 835,174,704
// and 9,010,894, inside the run-to-run spread below.
//
// HEADROOM AS IT NOW STANDS, which is not the headroom these constants were
// argued for. Against the current observation the ceilings sit at +35.4%
// (bytes) and +17.9% (count), where 15% was the intent. The bytes ceiling is
// loose by roughly the size of the saving that made it loose, and tightening
// it to ~815,000,000 would restore the design. That is a deliberate change
// with its own argument and it is not made here: this branch is fixing what
// the gate SAYS, and re-tuning a blocking CI gate is a separate commit that
// should be able to point at a green run of everything else first.
//
// WHY AN ABSOLUTE NUMBER AND NOT BYTES-PER-FILE. The same reason
// maxSQLUnresolved is a count and not a fraction: a per-file figure is
// satisfiable by adding files, so a scan could double its churn while the
// ratio held steady and the gate stayed green. The price of the absolute form
// is that ordinary repo growth eventually reaches it. That is what the
// headroom is for, and it is why the failure message prints the file count —
// so a reader can tell growth from regression without opening a profiler.
//
// WHY 15% OF HEADROOM WAS ASKED FOR, AND NOT 2% OR 100%. (What the constants
// now deliver against the current observation is the paragraph above; this is
// the argument they were chosen by.) Two different things have to fit under
// the ceiling and only one of them is noise:
//
//   - Noise is almost nothing. Fourteen samples on the mem/gate tree,
//     spanning ATLAS_BENCH_JOBS 1/4/12 and GOMAXPROCS 2/12, spread
//     834,476,160-837,336,792 B/op (0.34%) and 9,006,747-9,013,315 allocs/op
//     (0.07%); the five on this branch spread 0.06% and 0.05%. Unlike the
//     ns/op figures in docs/performance.md, which move up to 35% run to run
//     on this machine, allocation accounting does not care about ambient load
//     or core count. About 1% covers it.
//   - The rest is corpus growth, and that is the real reason the ceiling is
//     not tight. The benchmark scans THIS repository, so B/op rises as the
//     repository does — roughly 1.04 MB of churn per .go file on this
//     branch's 681 (it was 1.24 MB before the annotation-parse fix). The
//     tree grew 653 -> 681 across the whole of #109, #150 and #152, so
//     ordinary growth should need re-arguing about once every five
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
// about to read: 142 MB, 17% of the total) trips it on its own.
//
// SENSITIVITY, said two ways, because they answer differently and an earlier
// version of this comment printed a single "about 12%" that was neither.
//
//   - By arithmetic, which depends on which baseline you stand on. Against
//     the mutation runs' own baseline of 836,447,416 B/op the ceilings are
//     +14.8% and +15.3% away; against this branch's 708,904,952 they are
//     +35.4% and +17.9%. The second pair is what a change landing today has
//     to beat.
//   - By measurement, which is the only claim with runs behind it, and it
//     BRACKETS the threshold rather than locating it. On the mem/gate
//     baseline: +11.2% on bytes passed and +45.0% failed; +7.4% on the count
//     passed and +22.2% failed. Nothing was run in between, so nothing
//     narrower than (+11.2%, +45.0%] and (+7.4%, +22.2%] was observed — and
//     the mutations were not re-run against this branch's lower baseline, so
//     they bracket the OLD arithmetic and not the current one.
//
// Both arithmetic answers sit inside the measured bytes bracket, which is as
// much as the runs support. Percentages in the table below are against the
// CEILING, the way the failure message prints them; the ones here are against
// the unmutated BASELINE, which is what "a change adding X%" means. They are
// different denominators, and reading one for the other is what produced the
// figure this paragraph replaced.
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
	// The trailing percentage is headroom over the CURRENT observation
	// (708,904,952 / 8,822,140, five processes on this branch), not over the
	// mem/gate figures these were set against. See MEASUREMENT above for why
	// the two differ and why the bytes ceiling is looser than it was argued
	// for.
	maxScanBytesPerOp  = 960_000_000 // 708,904,952 observed, +35.4%
	maxScanAllocsPerOp = 10_400_000  // 8,822,140 observed, +17.9%
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
// Two percentage columns per metric, because the two questions are
// different: "vs ceiling" is how far the run sat from the gate (what the
// failure message prints), "vs base" is how much the mutation added to the
// unmutated scan (what a reader asking "how big a regression does this
// catch" means).
//
//	mutation                          B/op   vs ceiling  vs base   gate
//	none                       836,447,416      -12.9%        —    pass
//	scanner buffer +64 KB/file 929,867,736       -3.1%   +11.2%    pass
//	scanner buffer +256 KB/f 1,212,664,856      +26.3%   +45.0%    FAIL bytes
//	scanner buffer +1 MB/file 2,343,121,072     +144.1%  +180.1%   FAIL bytes
//	+1 Sprintf per line        842,490,296      -12.2%    +0.7%    pass
//	+3 Sprintf per line        852,094,600      -11.2%    +1.9%    pass on bytes
//
//	mutation                     allocs/op   vs ceiling  vs base   gate
//	none                         9,015,812      -13.3%        —    pass
//	scanner buffer +64 KB/file   9,015,462      -13.3%    -0.0%    pass
//	scanner buffer +256 KB/f     9,015,068      -13.3%    -0.0%    pass
//	scanner buffer +1 MB/file    9,014,321      -13.3%    -0.0%    pass
//	+1 Sprintf per line          9,680,537       -6.9%    +7.4%    pass
//	+3 Sprintf per line         11,021,266       +6.0%   +22.2%    FAIL allocs
//
// Two things to read out of it. The failures say the gate works; +22.2% on
// the count says it works on a moderate regression, not only on a
// catastrophic one. And the passing rows matter as much: a gate that fails on
// any change at all is a gate that gets deleted within a month. +64 KB per
// file is 93 MB of extra churn (+11.2%) and stays green, which is where the
// 15% headroom went and is far more than ordinary repo growth will produce in
// a year.
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
	files := fileCountLabel(m)

	t.Logf("scan churn over %s .go files: %s cumulative allocation (ceiling %s, %+.1f%%), "+
		"%s allocations (ceiling %s, %+.1f%%)",
		files,
		commas(bytesPerOp), commas(maxScanBytesPerOp), pctOf(bytesPerOp, maxScanBytesPerOp),
		commas(allocsPerOp), commas(maxScanAllocsPerOp), pctOf(allocsPerOp, maxScanAllocsPerOp))

	if bytesPerOp > maxScanBytesPerOp {
		t.Errorf("a scan of this repository now allocates %s bytes per op, %+.1f%% over the "+
			"committed ceiling of %s.\n\n"+
			"These are CUMULATIVE BYTES ALLOCATED, not resident memory — see the constant's "+
			"comment before reasoning about RSS from this number.\n\n"+
			"It scanned %s .go files. If that count has grown a lot since the ceiling was "+
			"set (681 files), this may be the repository rather than the code; if it has not, "+
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

// fileCountLabel renders the scanned-file count for the log line and the
// failure message.
//
// It exists as a function so it can be tested, and it is tested because the
// obvious spelling — `files := m["gofiles"]` — reads a missing metric as
// zero and then prints "It scanned 0 .go files". That number has one job: it
// lets a reader tell corpus growth from a code regression without opening a
// profiler. A fabricated zero answers that question WRONGLY, which is worse
// than declining to answer it, and it does so in the one message someone
// reads while a blocking CI job is red.
//
// gofiles is a ReportMetric the benchmark chooses to emit, unlike B/op and
// allocs/op which -benchmem guarantees, so its absence is an ordinary
// outcome rather than a broken run — which is why it degrades to a label
// here instead of failing the gate the way a missing B/op does above.
func fileCountLabel(m map[string]float64) string {
	n, ok := m["gofiles"]
	if !ok {
		return "an unreported number of"
	}
	return fmt.Sprintf("%.0f", n)
}

// TestDogfood_FileCountLabelNeverInventsAZero is the guard on that. It is a
// pure-function test riding in the dogfood job because the constant it
// protects lives here; it costs microseconds.
func TestDogfood_FileCountLabelNeverInventsAZero(t *testing.T) {
	if got := fileCountLabel(map[string]float64{"B/op": 1, "allocs/op": 2}); strings.Contains(got, "0") {
		t.Errorf("fileCountLabel with no gofiles metric = %q; a reader is told a count that "+
			"was never measured", got)
	}
	if got := fileCountLabel(map[string]float64{"gofiles": 675}); got != "675" {
		t.Errorf("fileCountLabel = %q, want 675", got)
	}
	// Zero really measured is still zero: the label only refuses to invent
	// one, it does not hide one.
	if got := fileCountLabel(map[string]float64{"gofiles": 0}); got != "0" {
		t.Errorf("fileCountLabel of a measured zero = %q, want 0", got)
	}
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
