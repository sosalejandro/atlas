package codeindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The scan half of the performance harness (issue #109).
//
// These benchmarks exist so that "the scan got faster" is a claim someone
// else can re-run, not a number in a commit message. Every figure in
// docs/performance.md comes from one of them, with the command printed
// next to it.
//
// The corpus is a real Go tree rather than a generated one, because the
// costs that dominate a scan — type checking, call resolution, cycle
// detection over the accumulating edge set — are all superlinear in ways a
// synthetic tree of identical files does not reproduce. The default corpus
// is THIS repository, so the benchmark is runnable by anyone who has
// checked it out; ATLAS_BENCH_ROOT points it at a bigger tree (the issue's
// reference repo, say) without editing code.

// benchRoot resolves the tree to scan: $ATLAS_BENCH_ROOT if set, else the
// atlas checkout this test file lives in.
func benchRoot(tb testing.TB) string {
	tb.Helper()
	if v := os.Getenv("ATLAS_BENCH_ROOT"); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			tb.Fatalf("ATLAS_BENCH_ROOT=%q: %v", v, err)
		}
		return abs
	}
	abs, err := filepath.Abs("../..")
	if err != nil {
		tb.Fatalf("abs repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		tb.Skipf("no go.mod at %s; set ATLAS_BENCH_ROOT", abs)
	}
	return abs
}

// countGoFiles counts what a benchmark is about to scan, so the timing and
// the allocation figures carry their corpus with them. A ns/op with no file
// count next to it cannot be compared against anything, including its own
// future self.
//
// It RETURNS the count rather than reporting it, and callers report it after
// the loop, because b.ResetTimer "deletes user-reported metrics": a
// b.ReportMetric before the reset is dropped silently, and the gofiles column
// never reached the output at all between #109 and the memory gate (#152),
// which needs the number in its failure message to tell repo growth from a
// regression.
//
// The skip rules are the walkers' own: any directory named vendor or
// node_modules, or beginning with a dot.
func countGoFiles(tb testing.TB, root string) int {
	tb.Helper()
	files := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if p != root && (n == "vendor" || n == "node_modules" || len(n) > 0 && n[0] == '.') {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) == ".go" {
			files++
		}
		return nil
	})
	return files
}

// BenchmarkIndexProject is the end-to-end orchestrator: every phase, in
// the order `atlas scan` runs them.
//
// Its B/op and allocs/op are gated. See maxScanBytesPerOp in
// test/acceptance/memory_test.go, which runs exactly this benchmark as a
// subprocess and fails when a scan starts allocating more than a scan should.
// Changing what this benchmark measures changes what that gate means, so keep
// the two in step.
func BenchmarkIndexProject(b *testing.B) {
	root := benchRoot(b)
	files := countGoFiles(b, root)
	ctx := context.Background()
	opts := Options{HashFiles: true, SkipTS: true, SkipPY: true, Jobs: benchJobs(b)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := IndexProject(ctx, root, opts); err != nil {
			b.Fatalf("IndexProject: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(files), "gofiles")
}

// BenchmarkGoScan_Typed is phase A as `atlas scan` runs it by default:
// go/packages type checking plus the AST ladder for what it could not
// type-check.
func BenchmarkGoScan_Typed(b *testing.B) {
	root := benchRoot(b)
	files := countGoFiles(b, root)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := goscan.Scan(ctx, root, goscan.Options{}); err != nil {
			b.Fatalf("goscan: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(files), "gofiles")
}

// BenchmarkGoScan_ASTOnly is phase A with --skip-typed-resolution. Paired
// with the typed benchmark it prices issue #109's third candidate — "check
// whether the go/packages load mode is worth it" — against the name-matching
// ladder that replaces it.
func BenchmarkGoScan_ASTOnly(b *testing.B) {
	root := benchRoot(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := goscan.Scan(ctx, root, goscan.Options{SkipTypedResolution: true}); err != nil {
			b.Fatalf("goscan: %v", err)
		}
	}
}

// BenchmarkPatternRecognizers is phase A.5 — the orchestrator's own
// per-file Go parse, one of the two passes this package parallelises.
func BenchmarkPatternRecognizers(b *testing.B) {
	root := benchRoot(b)
	ctx := context.Background()
	opts := Options{Jobs: benchJobs(b)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, w := runPatternRecognizers(ctx, root, opts, nil); len(w) > 0 && testing.Verbose() {
			b.Logf("%d warnings", len(w))
		}
	}
}

// BenchmarkAnnotationWalk is phase B — read + annotation-parse + SHA-256
// of every source file, the other parallelised pass.
func BenchmarkAnnotationWalk(b *testing.B) {
	root := benchRoot(b)
	ctx := context.Background()
	opts := Options{
		AnnotationExts: defaultAnnotationExts,
		HashFiles:      true,
		Jobs:           benchJobs(b),
	}
	skip := map[string]bool{"vendor": true, "node_modules": true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := walkAnnotations(ctx, root, opts, skip); err != nil {
			b.Fatalf("walkAnnotations: %v", err)
		}
	}
}

// benchJobs reads $ATLAS_BENCH_JOBS so a single `go test` invocation can
// sweep the worker count (1, 2, 4, GOMAXPROCS) without recompiling. Unset
// means the package default.
func benchJobs(b *testing.B) int {
	b.Helper()
	v := os.Getenv("ATLAS_BENCH_JOBS")
	if v == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		b.Fatalf("ATLAS_BENCH_JOBS=%q: %v", v, err)
	}
	return n
}

// BenchmarkGraphAddEdge_ScanSized isolates the single largest cost the
// profile found, and it is not in this package.
//
// graph.AddEdge runs a cycle check (Graph.hasPath) that rebuilds the whole
// adjacency map from g.Edges on every call, so building an E-edge graph
// costs O(E^2) map inserts. The Go scanner calls it once per resolved call
// site — 12,728 times on this repository — and the profile attributes 36.8%
// of scan CPU to hasPath, plus most of the garbage collection that the
// discarded adjacency maps drive.
//
// The benchmark is here rather than in packages/graph because this is where
// the cost is PAID, and because the number is only meaningful at the edge
// count a real scan produces. See docs/performance.md: fixing it is the
// largest win available and it belongs to whoever owns packages/graph.
func BenchmarkGraphAddEdge_ScanSized(b *testing.B) {
	const edges = 12728 // this repository, scanned 2026-09-06
	const nodes = 4999
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		g := graph.New()
		ids := make([]shared.SymbolID, nodes)
		for n := range ids {
			ids[n] = shared.SymbolID(fmt.Sprintf("pkg.Sym%05d", n))
			g.AddNode(&graph.Node{Symbol: shared.Symbol{ID: ids[n]}})
		}
		b.StartTimer()
		for e := 0; e < edges; e++ {
			g.AddEdgeTier(ids[e%nodes], ids[(e*7+1)%nodes], graph.TierTyped)
		}
	}
	b.ReportMetric(float64(edges), "edges")
}

// BenchmarkGraphAppendEdge_ScanSized is the counterfactual for the
// benchmark above: the same edge list, appended without the per-call cycle
// check. The gap between the two is what a cached (or incrementally
// maintained) adjacency map would be worth.
func BenchmarkGraphAppendEdge_ScanSized(b *testing.B) {
	const edges = 12728
	const nodes = 4999
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		g := graph.New()
		ids := make([]shared.SymbolID, nodes)
		for n := range ids {
			ids[n] = shared.SymbolID(fmt.Sprintf("pkg.Sym%05d", n))
			g.AddNode(&graph.Node{Symbol: shared.Symbol{ID: ids[n]}})
		}
		b.StartTimer()
		for e := 0; e < edges; e++ {
			g.Edges = append(g.Edges, graph.Edge{
				From: ids[e%nodes], To: ids[(e*7+1)%nodes], Tier: graph.TierTyped,
			})
		}
	}
	b.ReportMetric(float64(edges), "edges")
}

// BenchmarkGOMAXPROCS records the machine the numbers came from, so a
// docs/performance.md table always carries its own context.
func BenchmarkGOMAXPROCS(b *testing.B) {
	b.ReportMetric(float64(runtime.GOMAXPROCS(0)), "procs")
	b.ReportMetric(float64(runtime.NumCPU()), "cpus")
}
