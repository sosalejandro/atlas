package graph

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Graph-construction benchmarks for issue #150.
//
// packages/codeindex/bench_test.go already prices AddEdge from the caller's
// side, at the edge count a real scan produces. These live here, next to
// the code, because this is the package that has to keep the win: a future
// change to the cycle check has an in-package number to run before it
// claims it costs nothing.
//
// The two shapes are deliberate and they measure different things.
//
//   - ScanSized is the scan's real shape and the number to quote. It pays
//     both costs the cycle check has: maintaining the adjacency map, and
//     the DFS over it.
//   - FanIn is every edge pointing at a node with no outgoing edges, so
//     every DFS stops on its first step. What is left is the map
//     maintenance alone — the part #150 is about. Before the fix the two
//     benchmarks were both quadratic; after it, FanIn is the one that has
//     to be flat, and if it ever stops being, the regression is in the map
//     and not in the walk.
//
// Reproduce:
//
//	go test ./packages/graph -run NONE -bench BenchmarkGraph -benchtime 3x -count 3

const (
	// This repository as scanned on 2026-09-06 — the corpus every figure
	// in docs/performance.md is taken against.
	benchEdges = 12728
	benchNodes = 4999
)

func benchIDs(n int) []shared.SymbolID {
	ids := make([]shared.SymbolID, n)
	for i := range ids {
		ids[i] = shared.SymbolID(fmt.Sprintf("pkg.Sym%05d", i))
	}
	return ids
}

// BenchmarkGraphAddEdge_ScanSized builds a scan-sized graph through the
// public constructor, cycle check included. The stride-7 target keeps the
// graph strongly connected, which is the expensive case for the DFS and
// therefore the honest one to publish.
func BenchmarkGraphAddEdge_ScanSized(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ids := benchIDs(benchNodes)
		g := New()
		for _, id := range ids {
			g.AddNode(&Node{Symbol: shared.Symbol{ID: id}})
		}
		b.StartTimer()
		for e := 0; e < benchEdges; e++ {
			g.AddEdgeTier(ids[e%benchNodes], ids[(e*7+1)%benchNodes], TierTyped)
		}
	}
	b.ReportMetric(float64(benchEdges), "edges")
}

// BenchmarkGraphAppendEdge_ScanSized is the floor: the same edge list
// appended with no cycle check at all. Nothing can beat it, and the gap to
// AddEdge is what the Cycle flag costs.
func BenchmarkGraphAppendEdge_ScanSized(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ids := benchIDs(benchNodes)
		g := New()
		for _, id := range ids {
			g.AddNode(&Node{Symbol: shared.Symbol{ID: id}})
		}
		b.StartTimer()
		for e := 0; e < benchEdges; e++ {
			g.Edges = append(g.Edges, Edge{
				From: ids[e%benchNodes], To: ids[(e*7+1)%benchNodes], Tier: TierTyped,
			})
		}
	}
	b.ReportMetric(float64(benchEdges), "edges")
}

// BenchmarkGraphAddEdge_FanIn is the same edge count with every DFS
// terminating immediately, so it prices the adjacency map alone.
func BenchmarkGraphAddEdge_FanIn(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		g := New()
		b.StartTimer()
		for e := 0; e < benchEdges; e++ {
			g.AddEdgeTier(srcID(e), sinkID(e%64), TierTyped)
		}
	}
	b.ReportMetric(float64(benchEdges), "edges")
}

// BenchmarkGraphGOMAXPROCS records the machine a table was taken on, the
// same way packages/codeindex does, so the numbers carry their context.
func BenchmarkGraphGOMAXPROCS(b *testing.B) {
	b.ReportMetric(float64(runtime.GOMAXPROCS(0)), "procs")
	b.ReportMetric(float64(runtime.NumCPU()), "cpus")
}
