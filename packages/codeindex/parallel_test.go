package codeindex

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The determinism guard for issue #109's parallel passes, and the reason
// this package is allowed to start goroutines at all.
//
// docs/testing/determinism.md states the constraint: the same tree must
// produce the same index, byte for byte, every time. Parallelism is the
// one change that can break that without changing a single count — a merge
// keyed on completion order rather than walk order reorders annotations,
// and a reordered annotation list re-points which symbol a feature attaches
// to. So the parallel passes are tested against the serial ones directly
// (`--jobs=1` is the reference implementation, not just a fallback), and
// then run concurrently with themselves, because a data race that shows up
// one run in fifty is worse than no parallelism at all.

const parallelCorpus = "go/testdata/goldencorpus"

// renderIndex flattens an Index into a stable text form. Everything
// derived from the source is included; everything derived from the clock
// (GeneratedAt, LastScanned, ModTime) is not, because comparing those
// would only prove that time passes.
func renderIndex(idx *Index) string {
	var b strings.Builder

	syms := make([]string, 0, len(idx.Symbols))
	for _, s := range idx.Symbols {
		syms = append(syms, fmt.Sprintf("sym\t%s\t%s\t%s\t%d\t%d\t%s\t%s",
			s.ID, s.Kind, s.Position.Path, s.Position.Line, s.EndLine, s.Package,
			idx.SymbolLangs[s.ID]))
	}
	sort.Strings(syms)

	var edges []string
	if idx.Graph != nil {
		for _, e := range idx.Graph.Edges {
			edges = append(edges, fmt.Sprintf("edge\t%s\t%s\t%s\t%d\t%s\t%s\t%t\t%t",
				e.From, e.To, e.Kind, e.Line, e.Meta, e.Tier, e.Cycle, e.Ambiguous))
		}
	}
	sort.Strings(edges)

	// Annotations are NOT sorted: their slice order is the walk order, and
	// preserving it is exactly the property under test.
	anns := make([]string, 0, len(idx.Annotations))
	for _, a := range idx.Annotations {
		anns = append(anns, fmt.Sprintf("ann\t%s\t%d\t%s\t%s\t%s\t%s",
			a.Position.Path, a.Position.Line, a.Kind, a.Source, a.Raw,
			strings.Join(a.IDs, ",")))
	}

	hashes := make([]string, 0, len(idx.FileHashes))
	for p, fh := range idx.FileHashes {
		hashes = append(hashes, fmt.Sprintf("hash\t%s\t%s", p, fh.SHA256))
	}
	sort.Strings(hashes)

	var pats []string
	for sym, ms := range idx.PatternMatches {
		for i, m := range ms {
			pats = append(pats, fmt.Sprintf("pat\t%s\t%d\t%s\t%s\t%d",
				sym, i, m.Pattern, m.Position.Path, m.Position.Line))
		}
	}
	sort.Strings(pats)

	// Skipped files and warnings keep their emitted order — both are
	// ledgers, and a ledger whose order drifts is a ledger that diffs
	// against itself.
	var skipped []string
	for _, sf := range idx.SkippedFiles {
		skipped = append(skipped, fmt.Sprintf("skip\t%s\t%s", sf.Path, sf.Reason))
	}
	warns := make([]string, 0, len(idx.Warnings))
	for _, w := range idx.Warnings {
		warns = append(warns, "warn\t"+w)
	}

	for _, block := range [][]string{syms, edges, anns, hashes, pats, skipped, warns} {
		for _, line := range block {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func indexAt(t *testing.T, jobs int) *Index {
	t.Helper()
	idx, err := IndexProject(context.Background(), parallelCorpus, Options{
		HashFiles: true,
		SkipTS:    true,
		SkipPY:    true,
		Jobs:      jobs,
	})
	if err != nil {
		t.Fatalf("IndexProject(jobs=%d): %v", jobs, err)
	}
	return idx
}

// TestParallel_MatchesSerialOutput pins the contract that makes the whole
// change safe: --jobs=1 and --jobs=N produce the same index.
//
// jobs=1 is the reference. If this ever fails, the parallel path is wrong;
// it is never the serial path that needs adjusting to match.
func TestParallel_MatchesSerialOutput(t *testing.T) {
	t.Parallel()

	serial := renderIndex(indexAt(t, 1))
	if strings.TrimSpace(serial) == "" {
		t.Fatal("serial index rendered empty — the corpus is not being scanned")
	}

	for _, jobs := range []int{2, 4, 8, runtime.NumCPU() + 3} {
		parallel := renderIndex(indexAt(t, jobs))
		if parallel != serial {
			t.Fatalf("jobs=%d differs from jobs=1:\n%s", jobs, firstDiff(serial, parallel))
		}
	}
}

// TestParallel_ConcurrentScansAreIdentical runs the same scan N times at
// once. The point is not the repetition — it is the contention: workers
// from independent scans interleaved on the same machine are what surfaces
// a shared parser, a shared FileSet, or a merge that reads someone else's
// slot. Run under -race in CI.
func TestParallel_ConcurrentScansAreIdentical(t *testing.T) {
	t.Parallel()

	const runs = 8
	want := renderIndex(indexAt(t, runtime.NumCPU()))

	got := make([]string, runs)
	var wg sync.WaitGroup
	wg.Add(runs)
	for i := 0; i < runs; i++ {
		go func(i int) {
			defer wg.Done()
			idx, err := IndexProject(context.Background(), parallelCorpus, Options{
				HashFiles: true, SkipTS: true, SkipPY: true, Jobs: runtime.NumCPU(),
			})
			if err != nil {
				got[i] = "ERROR: " + err.Error()
				return
			}
			got[i] = renderIndex(idx)
		}(i)
	}
	wg.Wait()

	for i, g := range got {
		if g != want {
			t.Fatalf("concurrent run %d differs:\n%s", i, firstDiff(want, g))
		}
	}
}

// TestJobsFor pins the worker-count policy: bounded by GOMAXPROCS, never
// by an arbitrary constant, and 1 means strictly serial.
func TestJobsFor(t *testing.T) {
	t.Parallel()

	procs := runtime.GOMAXPROCS(0)
	cases := []struct{ in, want int }{
		{0, procs},  // unset: the machine decides
		{-1, procs}, // nonsense: same as unset rather than an error the caller cannot act on
		{1, 1},
		{3, 3},
		{procs * 4, procs * 4}, // an explicit over-subscription is the operator's call
	}
	for _, c := range cases {
		if got := jobsFor(c.in); got != c.want {
			t.Errorf("jobsFor(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestMapOrdered_ResultsFollowInputOrder is the property the merge depends
// on, tested directly so a failure points at the pool rather than at a
// 900-line index diff.
func TestMapOrdered_ResultsFollowInputOrder(t *testing.T) {
	t.Parallel()

	in := make([]int, 500)
	for i := range in {
		in[i] = i
	}
	// Deliberately uneven work so completion order cannot coincide with
	// input order by luck.
	out := mapOrdered(context.Background(), 8, in, func(_ context.Context, v int) string {
		for j := 0; j < (500-v)*20; j++ {
			_ = j
		}
		return fmt.Sprintf("v%03d", v)
	})
	for i := range in {
		if want := fmt.Sprintf("v%03d", i); out[i] != want {
			t.Fatalf("out[%d] = %q, want %q", i, out[i], want)
		}
	}
}

// firstDiff returns the first differing line pair, with context, so a
// failure reads as a diff rather than as two walls of text.
func firstDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  serial:   %q\n  parallel: %q", i+1, wl, gl)
		}
	}
	return "(no line differs; lengths equal)"
}
