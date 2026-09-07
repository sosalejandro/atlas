package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The acceptance suite for the batched ingest (issue #109).
//
// One property matters more here than throughput, and it is the reason the
// original change was cut rather than hand-merged onto migration 0019:
// SURROGATE IDS ARE HANDED OUT IN INSERTION ORDER. `symbols.id` is an
// AUTOINCREMENT rowid, edges reference it, and coverage results, feature
// links and the whole call graph are stored against it. If a batch emits
// its VALUES tuples in any order other than the one the row-at-a-time loop
// used, every id shifts, every edge still points at *an* id, and nothing
// downstream can tell that it is now the wrong one. There is no constraint
// that fires, no count that changes, and no test that fails — which is
// exactly what makes it worth a test of its own.
//
// So the central assertion is differential rather than golden: the same
// index goes into two fresh stores, one down the row-at-a-time path that
// shipped before this change and one down the batched path, and the two
// `symbols` and `edges` tables must come out byte-for-byte identical,
// surrogate ids included. A golden list of ids would only prove the
// batched path is self-consistent; comparing against the implementation it
// replaces is what proves it did not renumber anything.
//
// There is exactly one case where the two disagree numerically and are
// both right — ids handed to rows that are NEW on a rescan, because the old
// writer leaked a sequence value per ignored INSERT. That case has its own
// test and its own explanation: TestIngestBatch_MixedSeedKeepsEveryExistingID.

// ---------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------

// batchFixture describes a corpus shaped to hit every branch the batched
// writer has: more rows than one chunk holds, anchors alongside
// declarations, position-less nodes the writer must drop, and a duplicate
// qualified name so the "already inserted earlier in this same batch"
// path is exercised.
type batchFixture struct {
	files       int
	symsPerFile int
	fanout      int
	annotations int
}

// buildBatchIndex generates a deterministic index. Nothing here is derived
// from map iteration order: ids, paths and the edge list all come from the
// loop counters, so two runs produce byte-identical input and any
// difference between the two ingest paths is the ingest's.
func buildBatchIndex(fx batchFixture) *codeindex.Index {
	g := graph.New()
	scannedAt := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	symbols := make([]shared.Symbol, 0, fx.files*fx.symsPerFile)
	hashes := make(map[string]codeindex.FileHash, fx.files)

	add := func(sym shared.Symbol) {
		g.AddNode(&graph.Node{Symbol: sym})
		symbols = append(symbols, sym)
	}

	for f := 0; f < fx.files; f++ {
		path := fmt.Sprintf("src/pkg%02d/file%03d.go", f/8, f)
		for s := 0; s < fx.symsPerFile; s++ {
			line := 1 + s*20
			add(shared.Symbol{
				ID:       shared.SymbolID(fmt.Sprintf("pkg%02d.Sym%03d_%02d", f/8, f, s)),
				Kind:     shared.KindFunc,
				Position: shared.FilePosition{Path: path, Line: line},
				EndLine:  line + 15,
				Package:  fmt.Sprintf("github.com/example/pkg%02d", f/8),
			})
		}
		hashes[path] = codeindex.FileHash{
			Path:        path,
			SHA256:      fmt.Sprintf("%064x", f),
			ModTime:     scannedAt,
			LastScanned: scannedAt,
		}
	}

	// Anchors, interleaved AFTER the declarations so their surrogate ids
	// land in the middle of the range rather than at a boundary a buggy
	// batcher could accidentally get right.
	for i := 0; i < 12; i++ {
		add(shared.Symbol{
			ID:       shared.SymbolID(fmt.Sprintf("sql:Query%02d", i)),
			Kind:     shared.KindFunc,
			Position: shared.FilePosition{Path: "db/queries.sql", Line: i + 1},
		})
		add(shared.Symbol{
			ID:       shared.SymbolID(fmt.Sprintf("os.path.helper%02d", i)),
			Kind:     shared.KindFunc,
			Position: shared.FilePosition{Path: anchorImportPath, Line: 1},
		})
		// A position-less vertex: the graph needs it, the table must never
		// hold it (file_path is NOT NULL), and it must not consume an id.
		add(shared.Symbol{
			ID:   shared.SymbolID(fmt.Sprintf("route:/thing/%02d", i)),
			Kind: shared.KindFunc,
		})
	}
	hashes["db/queries.sql"] = codeindex.FileHash{
		Path: "db/queries.sql", SHA256: strings.Repeat("a", 64),
		ModTime: scannedAt, LastScanned: scannedAt,
	}

	// A duplicate qualified name at a different position. Row-at-a-time
	// inserts the first and repositions on the second; the batch must do
	// the same, and must not hand the second occurrence a fresh id.
	if len(symbols) > 0 {
		dup := symbols[3]
		dup.Position.Line += 500
		dup.EndLine += 500
		add(dup)
	}

	for i, sym := range symbols {
		if sym.Position.Path == "" {
			continue
		}
		for k := 1; k <= fx.fanout; k++ {
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

	anns := make([]shared.Annotation, 0, fx.annotations)
	for i := 0; i < fx.annotations; i++ {
		f := i % fx.files
		anns = append(anns, shared.Annotation{
			Kind:     shared.AnnFeature,
			IDs:      []string{fmt.Sprintf("batch.feature%03d", i%11)},
			Raw:      fmt.Sprintf("batch.feature%03d", i%11),
			Source:   shared.SourceAtlas,
			Position: shared.FilePosition{Path: fmt.Sprintf("src/pkg%02d/file%03d.go", f/8, f), Line: 1},
		})
	}

	return &codeindex.Index{
		Root:        "/batch",
		GeneratedAt: scannedAt,
		Graph:       g,
		Symbols:     symbols,
		Annotations: anns,
		FileHashes:  hashes,
		SymbolLangs: map[shared.SymbolID]string{},
	}
}

// anchorImportPath is the reserved position pyscan gives an import it could
// not resolve inside the repository. Naming it once keeps the literal out of
// the places that would otherwise repeat it.
const anchorImportPath = "external:py"

// defaultBatchFixture is sized to straddle several chunk boundaries in
// every table: 47 files x 7 symbols = 329 declarations plus 24 positioned
// anchors, against a symbol chunk of 999/8 = 124 rows, and edges against the
// same ceiling. A corpus that fits in one chunk would pass this suite while
// the chunk loop was broken.
var defaultBatchFixture = batchFixture{files: 47, symsPerFile: 7, fanout: 2, annotations: 60}

// ---------------------------------------------------------------------
// Table readers — the raw rows, in id order, for differential comparison
// ---------------------------------------------------------------------

func dumpRows(t *testing.T, s *Store, query string) []string {
	t.Helper()
	rows, err := s.conn.QueryContext(context.Background(), query) //nolint:rowserrcheck // checked below.
	if err != nil {
		t.Fatalf("dump %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		parts := make([]string, len(cols))
		for i, c := range cells {
			parts[i] = fmt.Sprintf("%s=%v", cols[i], c)
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

const symbolDumpSQL = `SELECT id, qualified_name, kind, file_path, line, end_line,
	package, domain, node_class FROM symbols ORDER BY id`

// symbolContentDumpSQL is the same projection without the surrogate id, for
// the one comparison where the two writers are allowed to number differently
// but not to write anything else differently. Ordered by name rather than by
// id so the two sides line up regardless of numbering.
const symbolContentDumpSQL = `SELECT qualified_name, kind, file_path, line, end_line,
	package, domain, node_class FROM symbols ORDER BY qualified_name`

const edgeDumpSQL = `SELECT id, from_symbol_id, to_symbol_id, kind, file_path, line,
	edge_meta, resolution_tier, ambiguous FROM edges ORDER BY id`

const annotationDumpSQL = `SELECT file_path, line, kind, value, source
	FROM annotations ORDER BY file_path, line, kind`

func diffDumps(t *testing.T, label string, want, got []string) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: reference dump is empty — the fixture wrote nothing", label)
	}
	if len(want) != len(got) {
		t.Fatalf("%s: row count %d (row-at-a-time) != %d (batched)", label, len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("%s: row %d differs\n  row-at-a-time: %s\n  batched:       %s",
				label, i, want[i], got[i])
		}
	}
}

// ---------------------------------------------------------------------
// The central property
// ---------------------------------------------------------------------

// TestIngestBatch_AssignsTheSameSurrogateIDs is the test the cut change did
// not have and the reason it could not be hand-merged. It ingests one index
// twice — once through the row-at-a-time writer, once through the batched
// one — and requires the resulting symbol and edge tables to be identical
// down to the surrogate id.
func TestIngestBatch_AssignsTheSameSurrogateIDs(t *testing.T) {
	idx := buildBatchIndex(defaultBatchFixture)
	ctx := t.Context()

	ref := openTestStore(t)
	refStats, err := ref.Ingest(ctx, idx, IngestOptions{rowAtATime: true})
	if err != nil {
		t.Fatalf("row-at-a-time ingest: %v", err)
	}
	batched := openTestStore(t)
	batchStats, err := batched.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("batched ingest: %v", err)
	}

	diffDumps(t, "symbols", dumpRows(t, ref, symbolDumpSQL), dumpRows(t, batched, symbolDumpSQL))
	diffDumps(t, "edges", dumpRows(t, ref, edgeDumpSQL), dumpRows(t, batched, edgeDumpSQL))
	diffDumps(t, "annotations",
		dumpRows(t, ref, annotationDumpSQL), dumpRows(t, batched, annotationDumpSQL))

	assertSameCounts(t, refStats, batchStats)
}

// TestIngestBatch_RescanAssignsTheSameSurrogateIDs covers the path `atlas
// scan` actually takes: every row already exists and every file changed, so
// the batched writer has to split the index into "insert these" and
// "reposition those" without disturbing the ids the first pass handed out.
func TestIngestBatch_RescanAssignsTheSameSurrogateIDs(t *testing.T) {
	first := buildBatchIndex(defaultBatchFixture)
	second := buildBatchIndex(defaultBatchFixture)
	// Move every declaration down a line and dirty every hash, so the
	// unchanged-file skip cannot fire and every stored position is stale.
	for i := range second.Symbols {
		if second.Symbols[i].Position.Path == "" {
			continue
		}
		second.Symbols[i].Position.Line += 3
		if second.Symbols[i].EndLine > 0 {
			second.Symbols[i].EndLine += 3
		}
	}
	for p, fh := range second.FileHashes {
		fh.SHA256 = "ff" + fh.SHA256[2:]
		second.FileHashes[p] = fh
	}

	ctx := t.Context()
	ref := openTestStore(t)
	if _, err := ref.Ingest(ctx, first, IngestOptions{rowAtATime: true}); err != nil {
		t.Fatalf("row-at-a-time seed: %v", err)
	}
	refStats, err := ref.Ingest(ctx, second, IngestOptions{rowAtATime: true})
	if err != nil {
		t.Fatalf("row-at-a-time rescan: %v", err)
	}

	batched := openTestStore(t)
	if _, err := batched.Ingest(ctx, first); err != nil {
		t.Fatalf("batched seed: %v", err)
	}
	batchStats, err := batched.Ingest(ctx, second)
	if err != nil {
		t.Fatalf("batched rescan: %v", err)
	}

	diffDumps(t, "symbols", dumpRows(t, ref, symbolDumpSQL), dumpRows(t, batched, symbolDumpSQL))
	diffDumps(t, "edges", dumpRows(t, ref, edgeDumpSQL), dumpRows(t, batched, edgeDumpSQL))
	assertSameCounts(t, refStats, batchStats)
}

// TestIngestBatch_MixedSeedKeepsEveryExistingID is the interleaving case:
// most symbols already exist when the batch runs, so a handful of new
// inserts land among hundreds of rows that must not move.
//
// It is also the one place the two writers legitimately DISAGREE about the
// numeric value of a NEW id, and that disagreement is worth stating plainly
// because it looks alarming and is not. `symbols.id` is AUTOINCREMENT, and
// SQLite advances `sqlite_sequence` for an INSERT OR IGNORE even when the
// row is ignored. The row-at-a-time writer fires one INSERT per symbol
// including the thousands it knows will be ignored, so every rescan LEAKS a
// surrogate id per already-known symbol; the batched writer never issues an
// INSERT for a row it just read, so it leaks none. Same rows, same relative
// order, denser numbering.
//
// The assertion is therefore the property that actually matters rather than
// a numeric diff: no stored row's id changes, and both writers hand ids out
// in the same order. Renumbering the graph would break both.
func TestIngestBatch_MixedSeedKeepsEveryExistingID(t *testing.T) {
	full := buildBatchIndex(defaultBatchFixture)

	// The unresolved-import anchors are the only symbols held back from the
	// seed, so the full ingest interleaves twelve brand-new rows among
	// hundreds of existing ones.
	seed := buildBatchIndex(defaultBatchFixture)
	kept := seed.Symbols[:0]
	for _, sym := range seed.Symbols {
		if sym.Position.Path != anchorImportPath {
			kept = append(kept, sym)
		}
	}
	seed.Symbols = kept

	ctx := t.Context()
	run := func(opts ...IngestOptions) (*Store, map[string]int64) {
		st := openTestStore(t)
		if _, err := st.Ingest(ctx, seed, opts...); err != nil {
			t.Fatalf("seed ingest: %v", err)
		}
		afterSeed := symbolIDs(t, st)
		if _, err := st.Ingest(ctx, full, opts...); err != nil {
			t.Fatalf("full ingest: %v", err)
		}
		return st, afterSeed
	}
	ref, refSeeded := run(IngestOptions{rowAtATime: true})
	batched, batchSeeded := run()

	// 1. Everything the seed wrote keeps the id the seed gave it. This is
	// the invariant every edge, coverage row and feature link depends on.
	for _, tc := range []struct {
		name   string
		store  *Store
		seeded map[string]int64
	}{
		{"row-at-a-time", ref, refSeeded},
		{"batched", batched, batchSeeded},
	} {
		after := symbolIDs(t, tc.store)
		if len(tc.seeded) == 0 {
			t.Fatalf("%s: the seed ingest wrote nothing", tc.name)
		}
		for qn, id := range tc.seeded {
			if got, ok := after[qn]; !ok || got != id {
				t.Errorf("%s: %s moved from id %d to %d (present=%v)", tc.name, qn, id, got, ok)
			}
		}
	}

	// 2. Both writers hand out ids in the same order, to the same names.
	// Ranking rather than comparing values is what tolerates the leaked
	// sequence gaps while still catching a reordered batch: any permutation
	// of the insert order changes at least one rank.
	refOrder, batchOrder := idOrder(t, ref), idOrder(t, batched)
	if !slices.Equal(refOrder, batchOrder) {
		t.Fatalf("id order differs: %s", firstDiff(refOrder, batchOrder))
	}

	// 3. Non-id content is identical, column for column.
	diffDumps(t, "symbols (content)",
		dumpRows(t, ref, symbolContentDumpSQL), dumpRows(t, batched, symbolContentDumpSQL))

	// 4. And the leak the batched writer stopped, asserted rather than left
	// as folklore: the row-at-a-time path burned one id per already-known
	// symbol, so the batched table is gap-free and its highest id is not.
	refMax, batchMax := maxSymbolID(t, ref), maxSymbolID(t, batched)
	if batchMax != int64(len(batchOrder)) {
		t.Errorf("batched ids are not gap-free: max %d over %d rows", batchMax, len(batchOrder))
	}
	if refMax <= batchMax {
		t.Errorf("row-at-a-time max id %d <= batched %d — the ignored-insert "+
			"sequence leak this test documents did not happen; re-read the comment",
			refMax, batchMax)
	}
}

// symbolIDs reads the whole qualified_name -> id mapping.
func symbolIDs(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	rows, err := s.conn.QueryContext(t.Context(), `SELECT qualified_name, id FROM symbols`)
	if err != nil {
		t.Fatalf("read symbol ids: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var qn string
		var id int64
		if err := rows.Scan(&qn, &id); err != nil {
			t.Fatalf("scan symbol id: %v", err)
		}
		out[qn] = id
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read symbol ids: %v", err)
	}
	return out
}

// idOrder is the qualified names in ascending id order — the sequence the
// inserts happened in, recoverable after the fact.
func idOrder(t *testing.T, s *Store) []string {
	t.Helper()
	return dumpRows(t, s, `SELECT qualified_name FROM symbols ORDER BY id`)
}

func maxSymbolID(t *testing.T, s *Store) int64 {
	t.Helper()
	var v int64
	if err := s.conn.QueryRowContext(t.Context(),
		`SELECT COALESCE(MAX(id), 0) FROM symbols`).Scan(&v); err != nil {
		t.Fatalf("max id: %v", err)
	}
	return v
}

func firstDiff(a, b []string) string {
	for i := range a {
		if i >= len(b) {
			return fmt.Sprintf("row-at-a-time has %d extra entries from %q", len(a)-len(b), a[i])
		}
		if a[i] != b[i] {
			return fmt.Sprintf("at rank %d: %q vs %q", i, a[i], b[i])
		}
	}
	if len(b) > len(a) {
		return fmt.Sprintf("batched has %d extra entries from %q", len(b)-len(a), b[len(a)])
	}
	return "no difference"
}

func assertSameCounts(t *testing.T, want, got *IngestStats) {
	t.Helper()
	cases := []struct {
		name      string
		want, got int
	}{
		{"SymbolsInserted", want.SymbolsInserted, got.SymbolsInserted},
		{"EdgesInserted", want.EdgesInserted, got.EdgesInserted},
		{"AnnotationsInserted", want.AnnotationsInserted, got.AnnotationsInserted},
		{"FileHashesUpserted", want.FileHashesUpserted, got.FileHashesUpserted},
		{"SymbolsPruned", want.SymbolsPruned, got.SymbolsPruned},
		{"FilesScanned", want.FilesScanned, got.FilesScanned},
		{"FilesSkipped", want.FilesSkipped, got.FilesSkipped},
		{"FeaturesMaterialized", want.FeaturesMaterialized, got.FeaturesMaterialized},
		{"FeatureSymbolsLinked", want.FeatureSymbolsLinked, got.FeatureSymbolsLinked},
	}
	for _, c := range cases {
		if c.want != c.got {
			t.Errorf("stats.%s = %d (batched), want %d (row-at-a-time)", c.name, c.got, c.want)
		}
	}
}

// ---------------------------------------------------------------------
// Chunking
// ---------------------------------------------------------------------

// TestRowsPerChunk_NeverReturnsZero guards the termination condition. A
// chunk loop whose stride can be zero does not terminate, and the stride is
// an integer division by a column count — one wide table away from zero.
func TestRowsPerChunk_NeverReturnsZero(t *testing.T) {
	for _, cols := range []int{-1, 0, 1, 4, 8, 998, 999, 1000, 5000} {
		if n := rowsPerChunk(cols); n < 1 {
			t.Errorf("rowsPerChunk(%d) = %d, want >= 1", cols, n)
		}
	}
	if n := rowsPerChunk(8); n != maxBoundParams/8 {
		t.Errorf("rowsPerChunk(8) = %d, want %d", n, maxBoundParams/8)
	}
	// The ceiling exists to keep a statement under the bound-parameter
	// limit; a chunk that exceeds it is the bug this constant prevents.
	for _, cols := range []int{1, 3, 5, 8, 13} {
		if got := rowsPerChunk(cols) * cols; got > maxBoundParams && cols <= maxBoundParams {
			t.Errorf("rowsPerChunk(%d)*%d = %d, over the %d ceiling", cols, cols, got, maxBoundParams)
		}
	}
}

// TestInChunks_VisitsEveryRowOnce is the other half: whatever stride comes
// out, every row is handed to the callback exactly once and in order.
func TestInChunks_VisitsEveryRowOnce(t *testing.T) {
	for _, size := range []int{0, 1, 7, 124, 125, 999, 1001} {
		for _, per := range []int{0, -3, 1, 5, 124, 10000} {
			rows := make([]int, size)
			for i := range rows {
				rows[i] = i
			}
			var seen []int
			err := inChunks(rows, per, func(chunk []int) error {
				if len(chunk) == 0 {
					return fmt.Errorf("empty chunk")
				}
				seen = append(seen, chunk...)
				return nil
			})
			if err != nil {
				t.Fatalf("inChunks(size=%d, per=%d): %v", size, per, err)
			}
			if len(seen) != size {
				t.Fatalf("inChunks(size=%d, per=%d) visited %d rows", size, per, len(seen))
			}
			for i, v := range seen {
				if v != i {
					t.Fatalf("inChunks(size=%d, per=%d) out of order at %d: %d", size, per, i, v)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------
// Post-0019 columns
// ---------------------------------------------------------------------

// TestIngestBatch_CarriesDomainAndNodeClass proves both columns migration
// 0019 added survive the batched write. A batch that dropped either would
// leave every anchor looking like authored code, and `domain` NULL for a
// tree that has one — neither of which any existing test would notice,
// because the schema lets both be absent.
func TestIngestBatch_CarriesDomainAndNodeClass(t *testing.T) {
	scannedAt := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	g := graph.New()
	syms := []shared.Symbol{
		{ID: "app.Handler", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "src/contexts/billing/handler.go", Line: 10}, EndLine: 20},
		{ID: "sql:GetUser", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: "db/queries.sql", Line: 3}},
		{ID: "os.path.join", Kind: shared.KindFunc,
			Position: shared.FilePosition{Path: anchorImportPath, Line: 1}},
	}
	for i := range syms {
		g.AddNode(&graph.Node{Symbol: syms[i]})
	}
	idx := &codeindex.Index{
		Root: "/x", GeneratedAt: scannedAt, Graph: g, Symbols: syms,
		FileHashes:  map[string]codeindex.FileHash{},
		SymbolLangs: map[shared.SymbolID]string{},
	}

	s := openTestStore(t)
	if _, err := s.Ingest(t.Context(), idx); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// domainFor returns the whole `src/contexts/<name>` prefix, not the bare
	// name — see packages/store/paths.go. The point here is that whatever it
	// returns survives the batch, not what shape it has.
	want := map[string][2]string{
		"app.Handler":  {"src/contexts/billing", "declaration"},
		"sql:GetUser":  {"", "anchor"},
		"os.path.join": {"", "anchor"},
	}
	rows, err := s.conn.QueryContext(t.Context(),
		`SELECT qualified_name, domain, node_class FROM symbols`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var qn, class string
		var domain sql.NullString
		if err := rows.Scan(&qn, &domain, &class); err != nil {
			t.Fatalf("scan: %v", err)
		}
		w, ok := want[qn]
		if !ok {
			t.Fatalf("unexpected symbol %q", qn)
		}
		if domain.String != w[0] || class != w[1] {
			t.Errorf("%s: domain=%q node_class=%q, want domain=%q node_class=%q",
				qn, domain.String, class, w[0], w[1])
		}
		seen++
	}
	if seen != len(want) {
		t.Errorf("wrote %d symbols, want %d", seen, len(want))
	}
}

// TestIngestBatch_RefreshesNodeClassOnRescan is the case the comment on
// updateSymbolPositionSQL exists for: a node can legitimately change class.
// An unresolved Python import lands as an `external:py` anchor; the day the
// module it names is added to the repository the same id becomes a real
// declaration, and the batched path has to notice, or that declaration
// stays invisible to every "real code only" query until the store is
// deleted.
func TestIngestBatch_RefreshesNodeClassOnRescan(t *testing.T) {
	build := func(path string, line int) *codeindex.Index {
		g := graph.New()
		sym := shared.Symbol{
			ID:       "app.util.slugify",
			Kind:     shared.KindFunc,
			Position: shared.FilePosition{Path: path, Line: line},
			EndLine:  line + 4,
		}
		g.AddNode(&graph.Node{Symbol: sym})
		return &codeindex.Index{
			Root: "/x", Graph: g, Symbols: []shared.Symbol{sym},
			FileHashes:  map[string]codeindex.FileHash{},
			SymbolLangs: map[shared.SymbolID]string{},
		}
	}

	for _, tc := range []struct {
		name string
		opts []IngestOptions
	}{
		{"batched", nil},
		{"row-at-a-time", []IngestOptions{{rowAtATime: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := t.Context()
			if _, err := s.Ingest(ctx, build("external:py", 1), tc.opts...); err != nil {
				t.Fatalf("first ingest: %v", err)
			}
			if got := readNodeClass(t, s, "app.util.slugify"); got != "anchor" {
				t.Fatalf("after first ingest node_class = %q, want anchor", got)
			}
			// The module arrived in the repository.
			if _, err := s.Ingest(ctx, build("app/util.py", 12), tc.opts...); err != nil {
				t.Fatalf("second ingest: %v", err)
			}
			if got := readNodeClass(t, s, "app.util.slugify"); got != "declaration" {
				t.Errorf("after the module landed node_class = %q, want declaration", got)
			}
		})
	}
}

func readNodeClass(t *testing.T, s *Store, qn string) string {
	t.Helper()
	var class string
	err := s.conn.QueryRowContext(t.Context(),
		`SELECT node_class FROM symbols WHERE qualified_name = ?`, qn).Scan(&class)
	if err != nil {
		t.Fatalf("read node_class for %q: %v", qn, err)
	}
	return class
}

// ---------------------------------------------------------------------
// Batch-local conflicts
// ---------------------------------------------------------------------

// TestIngestBatch_AnnotationLastValueWins pins the one semantic a multi-row
// upsert could plausibly differ on. Two annotations at the same
// (file, line, kind) collide INSIDE a single statement; SQLite resolves that
// the same way a sequence of single-row upserts would — the later row wins —
// and this test is what will notice if a future driver or amalgamation
// stops doing so.
func TestIngestBatch_AnnotationLastValueWins(t *testing.T) {
	g := graph.New()
	sym := shared.Symbol{ID: "app.F", Kind: shared.KindFunc,
		Position: shared.FilePosition{Path: "app/f.go", Line: 2}, EndLine: 6}
	g.AddNode(&graph.Node{Symbol: sym})
	idx := &codeindex.Index{
		Root: "/x", Graph: g, Symbols: []shared.Symbol{sym},
		Annotations: []shared.Annotation{
			{Kind: shared.AnnFeature, IDs: []string{"a.first"}, Raw: "a.first",
				Source: shared.SourceAtlas, Position: shared.FilePosition{Path: "app/f.go", Line: 1}},
			{Kind: shared.AnnFeature, IDs: []string{"a.second"}, Raw: "a.second",
				Source: shared.SourceAtlas, Position: shared.FilePosition{Path: "app/f.go", Line: 1}},
		},
		FileHashes:  map[string]codeindex.FileHash{},
		SymbolLangs: map[shared.SymbolID]string{},
	}

	s := openTestStore(t)
	if _, err := s.Ingest(t.Context(), idx); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var value string
	if err := s.conn.QueryRowContext(t.Context(),
		`SELECT value FROM annotations WHERE file_path='app/f.go' AND line=1 AND kind='feature'`,
	).Scan(&value); err != nil {
		t.Fatalf("read annotation: %v", err)
	}
	if value != "a.second" {
		t.Errorf("annotation value = %q, want the later row's %q", value, "a.second")
	}
}

// TestIngestBatch_FileHashesAreWrittenInPathOrder. Go map iteration is
// randomised, so the pre-batch loop gave a fresh file's rowid to whichever
// path the runtime happened to yield first — two ingests of one index
// produced two different file_hashes tables. Sorting the batch is what makes
// that reproducible, and it is asserted rather than assumed because the
// randomisation means an unsorted implementation passes most of the time.
func TestIngestBatch_FileHashesAreWrittenInPathOrder(t *testing.T) {
	scannedAt := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	hashes := map[string]codeindex.FileHash{}
	for i := 0; i < 64; i++ {
		p := fmt.Sprintf("src/file%03d.go", i)
		hashes[p] = codeindex.FileHash{
			Path: p, SHA256: fmt.Sprintf("%064x", i),
			ModTime: scannedAt, LastScanned: scannedAt,
		}
	}
	idx := &codeindex.Index{
		Root: "/x", Graph: graph.New(), FileHashes: hashes,
		SymbolLangs: map[shared.SymbolID]string{},
	}

	var reference []string
	for run := 0; run < 3; run++ {
		s := openTestStore(t)
		if _, err := s.Ingest(t.Context(), idx); err != nil {
			t.Fatalf("ingest: %v", err)
		}
		// file_hashes is keyed by file_path, so its insertion order is only
		// visible through the implicit rowid.
		got := dumpRows(t, s, `SELECT rowid, file_path FROM file_hashes ORDER BY rowid`)
		if run == 0 {
			reference = got
			continue
		}
		diffDumps(t, "file_hashes", reference, got)
	}
	if len(reference) != 64 {
		t.Fatalf("wrote %d file_hashes rows, want 64", len(reference))
	}
	if !strings.Contains(reference[0], "src/file000.go") {
		t.Errorf("first file_hashes row is %q, want src/file000.go", reference[0])
	}
}
