package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The ingest half of the performance harness (issue #109).
//
// Rule for everything in this file: a benchmark that cannot be re-run to
// the same shape is not a measurement, it is an anecdote. So the corpus is
// SYNTHETIC and generated from a fixed recipe — symbol count, edge fan-out
// and file layout are arguments, not whatever happened to be on disk. That
// is what makes a before/after in docs/performance.md comparable across
// machines and across the months between two people reading it.
//
// The shape is taken from a real scan of this repository (4,999 symbols /
// 12,728 edges across 661 files, measured 2026-09-06), so benchIngestShape
// below is a repo-sized run rather than an arbitrary round number.
//
// Note the graph is built by appending to g.Edges directly rather than
// through graph.AddEdge: AddEdge runs a cycle check that rebuilds the whole
// adjacency map per call, which is quadratic in edge count and would put
// the fixture's construction cost — not the ingest's — in front of the
// timer. See docs/performance.md; that cost is itself the largest single
// finding of the profile.

// ingestShape describes one synthetic corpus.
type ingestShape struct {
	name        string
	files       int
	symsPerFile int
	edgeFanout  int // outgoing edges per symbol
	annotations int
}

// benchIngestShape mirrors this repository as scanned on 2026-09-06.
var benchIngestShape = ingestShape{
	name:        "repo_sized",
	files:       661,
	symsPerFile: 8,  // 661*8 = 5,288 symbols (repo: 4,999)
	edgeFanout:  2,  // 10,576 edges (repo: 12,728)
	annotations: 200,
}

// buildBenchIndex generates a deterministic codeindex.Index of the given
// shape. Same arguments in, byte-identical index out — the ids, the paths
// and the edge list are all derived from the loop counters, never from a
// map iteration.
func buildBenchIndex(shape ingestShape) *codeindex.Index {
	g := graph.New()
	total := shape.files * shape.symsPerFile
	symbols := make([]shared.Symbol, 0, total)
	hashes := make(map[string]codeindex.FileHash, shape.files)
	scannedAt := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	for f := 0; f < shape.files; f++ {
		path := fmt.Sprintf("src/pkg%03d/file%03d.go", f/16, f)
		for s := 0; s < shape.symsPerFile; s++ {
			line := 1 + s*20
			sym := shared.Symbol{
				ID:       shared.SymbolID(fmt.Sprintf("pkg%03d.Sym%03d_%02d", f/16, f, s)),
				Kind:     shared.KindFunc,
				Position: shared.FilePosition{Path: path, Line: line},
				EndLine:  line + 15,
				Package:  fmt.Sprintf("github.com/example/pkg%03d", f/16),
			}
			g.AddNode(&graph.Node{Symbol: sym})
			symbols = append(symbols, sym)
		}
		hashes[path] = codeindex.FileHash{
			Path:        path,
			SHA256:      fmt.Sprintf("%064x", f),
			ModTime:     scannedAt,
			LastScanned: scannedAt,
		}
	}

	// Edges fan out to the next symbols in declaration order, wrapping at
	// the end. Deterministic, and it produces the cross-file edges that
	// make the ingest's id lookups do real work.
	for i, sym := range symbols {
		for k := 1; k <= shape.edgeFanout; k++ {
			target := symbols[(i+k*7)%len(symbols)]
			g.Edges = append(g.Edges, graph.Edge{
				From: sym.ID,
				To:   target.ID,
				Kind: "call",
				Line: sym.Position.Line + k,
				Tier: graph.TierTyped,
			})
		}
	}

	anns := make([]shared.Annotation, 0, shape.annotations)
	for i := 0; i < shape.annotations; i++ {
		f := i % shape.files
		anns = append(anns, shared.Annotation{
			Kind:     shared.AnnFeature,
			IDs:      []string{fmt.Sprintf("bench.feature%03d", i%37)},
			Raw:      fmt.Sprintf("bench.feature%03d", i%37),
			Source:   shared.SourceAtlas,
			Position: shared.FilePosition{Path: fmt.Sprintf("src/pkg%03d/file%03d.go", f/16, f), Line: 1},
		})
	}

	return &codeindex.Index{
		Root:        "/bench",
		GeneratedAt: scannedAt,
		Graph:       g,
		Symbols:     symbols,
		Annotations: anns,
		FileHashes:  hashes,
		SymbolLangs: map[shared.SymbolID]string{},
	}
}

func benchStore(b *testing.B) *Store {
	b.Helper()
	st, err := Open(context.Background(), filepath.Join(b.TempDir(), "atlas-state.db"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return st
}

// BenchmarkIngest_Fresh times the cold path: an empty database, every
// symbol and edge new. This is `atlas init` and it is the worst case.
func BenchmarkIngest_Fresh(b *testing.B) {
	idx := buildBenchIndex(benchIngestShape)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st := benchStore(b)
		b.StartTimer()
		stats, err := st.Ingest(ctx, idx)
		if err != nil {
			b.Fatalf("ingest: %v", err)
		}
		b.StopTimer()
		if stats.SymbolsInserted == 0 || stats.EdgesInserted == 0 {
			b.Fatalf("nothing written: %+v", stats)
		}
		b.StartTimer()
	}
}

// BenchmarkIngest_RescanChanged times the path `atlas scan` actually takes
// after an edit: every row already exists, but the file hashes differ, so
// nothing is skipped and every symbol is re-checked and repositioned.
//
// This is the case the row-at-a-time loop punished hardest — pre-#109 it
// cost one INSERT, one UPDATE and one SELECT per already-known symbol.
func BenchmarkIngest_RescanChanged(b *testing.B) {
	first := buildBenchIndex(benchIngestShape)
	// Same symbols, different content hashes: the unchanged-file skip must
	// not fire, or the benchmark would time a no-op.
	second := buildBenchIndex(benchIngestShape)
	for p, fh := range second.FileHashes {
		fh.SHA256 = "ff" + fh.SHA256[2:]
		second.FileHashes[p] = fh
	}

	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st := benchStore(b)
		if _, err := st.Ingest(ctx, first); err != nil {
			b.Fatalf("seed ingest: %v", err)
		}
		b.StartTimer()
		if _, err := st.Ingest(ctx, second); err != nil {
			b.Fatalf("rescan ingest: %v", err)
		}
	}
}

// BenchmarkIngest_RescanUnchanged times the fully-incremental path: same
// hashes, so every file is skipped and the ingest degrades to id lookups
// plus the file_hashes refresh.
func BenchmarkIngest_RescanUnchanged(b *testing.B) {
	idx := buildBenchIndex(benchIngestShape)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		st := benchStore(b)
		if _, err := st.Ingest(ctx, idx); err != nil {
			b.Fatalf("seed ingest: %v", err)
		}
		b.StartTimer()
		if _, err := st.Ingest(ctx, idx); err != nil {
			b.Fatalf("rescan ingest: %v", err)
		}
	}
}
