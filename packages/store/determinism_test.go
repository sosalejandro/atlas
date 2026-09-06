package store

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
)

// The store half of the determinism suite (issue #120). See
// docs/testing/determinism.md.
//
// The scanner-side test in packages/codeindex/go pins that two scans of the
// same tree produce the same index. That is necessary but not sufficient:
// Ingest turns qualified names into surrogate row ids, and a surrogate id
// is exactly where a determinism bug hides without changing a single count.
// #97 was that bug — LastInsertId returned a neighbouring row's id, so
// edges pointed at the wrong symbols while `symbols: N  edges: M` stayed
// reassuringly stable.
//
// So every row here is read back through qualified names, never through
// ids. A mis-assigned surrogate shows up as an edge between the wrong two
// names, which is a diff a human can read.

// goldenCorpusRoot is the shared fixture, owned by packages/codeindex/go.
// It is referenced across the package boundary rather than duplicated:
// one corpus means the scanner-side golden snapshot and the store-side
// row comparison can never drift apart into testing different trees.
const goldenCorpusRoot = "../codeindex/go/testdata/goldencorpus"

// storeDumps are the tables whose content must be identical between two
// independent scan+ingest runs of the same source tree.
//
// Wall-clock columns (created_at, parsed_at, updated_at) are excluded on
// purpose: they are recorded state, not derived state, and comparing them
// would only prove that time passes.
var storeDumps = []struct {
	name  string
	query string
}{
	{
		name: "symbols",
		query: `SELECT qualified_name, kind, file_path, line,
		               COALESCE(end_line, -1), COALESCE(package, ''),
		               COALESCE(bc_path, ''), COALESCE(pattern_matches, '')
		          FROM symbols
		         ORDER BY qualified_name`,
	},
	{
		// Joined through qualified_name in both directions: this is the
		// query that would have caught #97.
		name: "edges",
		query: `SELECT src.qualified_name, dst.qualified_name, e.kind,
		               e.file_path, e.line, COALESCE(e.edge_meta, '')
		          FROM edges e
		          JOIN symbols src ON src.id = e.from_symbol_id
		          JOIN symbols dst ON dst.id = e.to_symbol_id
		         ORDER BY 1, 2, 3, 4, 5`,
	},
	{
		name: "annotations",
		query: `SELECT file_path, line, kind, value, source
		          FROM annotations
		         ORDER BY file_path, line, kind`,
	},
	{
		name: "features",
		query: `SELECT id, title, COALESCE(owner, ''), kind,
		               COALESCE(deprecated_since, ''), COALESCE(introduced_in, '')
		          FROM features
		         ORDER BY id`,
	},
	{
		name: "feature_symbols",
		query: `SELECT fs.feature_id, sym.qualified_name, fs.role, fs.source
		          FROM feature_symbols fs
		          JOIN symbols sym ON sym.id = fs.symbol_id
		         ORDER BY 1, 2, 3`,
	},
}

// TestDeterminism_Ingest_TwoStoresAgree scans the golden corpus, ingests it
// into one store, scans it again under a different GOMAXPROCS, ingests that
// into a second store, and asserts every derived table matches row for row.
//
// Two independent stores rather than one store ingested twice: a second
// ingest into the same store hits the INSERT OR IGNORE paths and would pass
// even if the first ingest had written the wrong rows.
//
// Not parallel — it mutates the process-wide GOMAXPROCS.
func TestDeterminism_Ingest_TwoStoresAgree(t *testing.T) {
	original := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(original) })

	runtime.GOMAXPROCS(1)
	first := scanAndIngest(t, goldenCorpusRoot)

	runtime.GOMAXPROCS(max(2, original))
	second := scanAndIngest(t, goldenCorpusRoot)

	for _, dump := range storeDumps {
		want := dumpTable(t, first, dump.query)
		got := dumpTable(t, second, dump.query)
		if want == got {
			continue
		}
		t.Errorf("table %q differs between two scan+ingest runs of the same tree:\n%s",
			dump.name, diffRows(want, got))
	}
}

// TestDeterminism_Ingest_SurrogateIDsFollowQualifiedNames asserts that every
// edge endpoint resolves back to a symbol that the in-memory graph actually
// declared as that edge's endpoint.
//
// The row-for-row comparison above catches a surrogate-id bug only when the
// two runs disagree. A deterministic mis-mapping — every run wiring edges to
// the same wrong neighbour — would sail through it. This one anchors the
// persisted graph to the scanned graph instead of to itself.
func TestDeterminism_Ingest_SurrogateIDsFollowQualifiedNames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	idx := indexCorpus(t, goldenCorpusRoot)
	s := openTestStore(t)
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Every persisted edge must exist in the scanned graph under the same
	// pair of qualified names.
	inGraph := map[string]bool{}
	for _, e := range idx.Graph.Edges {
		inGraph[string(e.From)+" -> "+string(e.To)] = true
	}

	rows := dumpTable(t, s, storeDumps[1].query)
	if strings.TrimSpace(rows) == "" {
		t.Fatal("no edges persisted; fixture or ingest broken")
	}
	for _, row := range strings.Split(strings.TrimSuffix(rows, "\n"), "\n") {
		fields := strings.Split(row, "\t")
		pair := fields[0] + " -> " + fields[1]
		if !inGraph[pair] {
			t.Errorf("persisted edge %q has no counterpart in the scanned graph "+
				"— a surrogate id was resolved to the wrong symbol", pair)
		}
	}
}

// scanAndIngest runs the full codeindex -> Ingest pipeline into a fresh
// store and returns it.
func scanAndIngest(t *testing.T, root string) *Store {
	t.Helper()
	s := openTestStore(t)
	if _, err := s.Ingest(context.Background(), indexCorpus(t, root)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return s
}

// indexCorpus scans root with the sub-scanners that need no external
// runtime, so the suite stays hermetic on a minimal CI container.
//
// HashFiles stays off: file_hashes carries a wall-clock last_scanned and a
// populated hash table would make a re-ingest skip files, which is a
// different behaviour than the one under test here.
func indexCorpus(t *testing.T, root string) *codeindex.Index {
	t.Helper()
	idx, err := codeindex.IndexProject(context.Background(), root, codeindex.Options{
		SkipTS:    true,
		SkipPY:    true,
		HashFiles: false,
	})
	if err != nil {
		t.Fatalf("IndexProject(%s): %v", root, err)
	}
	if idx.Graph == nil || len(idx.Symbols) == 0 {
		t.Fatalf("IndexProject(%s): empty index; fixture missing?", root)
	}
	return idx
}

// dumpTable renders a query's result set as tab-separated lines, one row
// per line, NULLs rendered as the literal <null> so an absent value and an
// empty string cannot be confused.
func dumpTable(t *testing.T, s *Store, query string) string {
	t.Helper()

	rows, err := s.sqlDB().Query(query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}

	var b strings.Builder
	for rows.Next() {
		cells := make([]any, len(cols))
		for i := range cells {
			cells[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for i, c := range cells {
			if i > 0 {
				b.WriteString("\t")
			}
			raw := *(c.(*sql.RawBytes))
			if raw == nil {
				b.WriteString("<null>")
				continue
			}
			b.WriteString(string(raw))
		}
		b.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return b.String()
}

// diffRows renders the difference between two ordered row dumps as a line
// diff, so a failure names the rows that moved rather than just asserting
// two large strings are unequal.
func diffRows(want, got string) string {
	w := splitRows(want)
	g := splitRows(got)

	var out []string
	var i, j int
	for i < len(w) || j < len(g) {
		switch {
		case j >= len(g) || (i < len(w) && w[i] < g[j]):
			out = append(out, "- "+w[i])
			i++
		case i >= len(w) || w[i] > g[j]:
			out = append(out, "+ "+g[j])
			j++
		default:
			i++
			j++
		}
	}
	const maxShown = 30
	if len(out) > maxShown {
		out = append(out[:maxShown],
			fmt.Sprintf("... and %d more differing rows", len(out)-maxShown))
	}
	if len(out) == 0 {
		return "(row sets are equal; the dumps differ only in ordering)"
	}
	return strings.Join(out, "\n")
}

// splitRows re-sorts with Go's byte ordering rather than trusting SQLite's
// collation to agree with it — the merge diff above needs one total order,
// and the queries mix text with integer columns.
func splitRows(dump string) []string {
	dump = strings.TrimSuffix(dump, "\n")
	if dump == "" {
		return nil
	}
	rows := strings.Split(dump, "\n")
	sort.Strings(rows)
	return rows
}
