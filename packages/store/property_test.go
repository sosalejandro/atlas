package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	atlastest "github.com/sosalejandro/atlas/packages/testing"
)

// The store's property layer.
//
// determinism_test.go beside this file pins that two scan+ingest runs of the
// GOLDEN CORPUS agree row for row. These generate the tree instead of reading
// a checked-in one, and state the weaker but broader claims that have to hold
// for any tree at all: a row written through a port reads back as itself, an
// edge's endpoints still name the symbols they named before persistence, and
// re-ingesting an unchanged index changes nothing.
//
// The last one is not academic. `atlas scan` is incremental and runs on every
// commit; if a re-ingest of an unchanged file could renumber a symbol, every
// coverage row keyed to the old surrogate would silently point somewhere
// else. Issue #97 is what that looks like when it happens once. A property
// that runs it twice is what stops it happening again.

// genSymbolRows builds a batch of symbol rows with distinct qualified names.
// Kinds are drawn from the closed set §5.4 accepts, because the write path
// deliberately narrows anything else to "func" and a round-trip property
// stated over inputs the schema rewrites would be testing the rewrite.
func genSymbolRows(r *atlastest.Rand, n int) []SymbolRow {
	kinds := []shared.SymbolKind{
		shared.KindType, shared.KindFunc, shared.KindMethod,
		shared.KindInterface, shared.KindVar, shared.KindConst,
	}
	dirs := []string{"pkg", "internal/svc", "src/contexts/billing/app", "cmd/tool"}

	rows := make([]SymbolRow, 0, n)
	for i := range n {
		dir := atlastest.Pick(r, dirs)
		end := r.IntRange(1, 400)
		row := SymbolRow{
			QualifiedName: shared.SymbolID(fmt.Sprintf("pkg%d.Sym%d", r.IntN(8), i)),
			Kind:          atlastest.Pick(r, kinds),
			FilePath:      fmt.Sprintf("%s/f%d.go", dir, r.IntN(5)),
			Line:          r.IntRange(1, 400),
		}
		// A NULL end_line is the common case for rows written before
		// migration 0011 and for the sub-scanners that still do not report
		// one, so the round-trip has to survive the pointer being nil.
		if r.Chance(3, 4) {
			e := row.Line + end
			row.EndLine = &e
		}
		if r.Chance(1, 2) {
			p := dir
			row.Package = &p
		}
		rows = append(rows, row)
	}
	return rows
}

// TestProperty_Store_OpensTheFileItWasGiven asserts that the database a store
// reads is the one at the path the caller named.
//
// It sounds like it cannot fail, and it did. The DSN is a `file:` URI built by
// string concatenation, and SQLite parses it as a URI: an unescaped '?' starts
// a query string and an unescaped '#' starts a fragment, both DISCARDED from
// the filename. Two callers whose paths differed only after a '#' therefore
// shared one database, silently, each reading the other's rows — and the
// symptom is not "cannot open file", it is a symbol coming back as a different
// symbol.
//
// FuzzSymbols_RoundTrip found it by accident: Go names a fuzz seed's temp
// directory ".../<Target>seed#<n>...", so every seed opened the same file. The
// property below states the invariant directly so the next reader does not
// have to rediscover it from a confusing round-trip failure.
func TestProperty_Store_OpensTheFileItWasGiven(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := t.TempDir()

	// Names that differ only in what a URI parser would throw away. If any
	// two of them collide onto one file, the second store sees the first's
	// symbol and the assertion below fails.
	names := []string{"plain", "with#hash", "with#hash-and-more", "with?query", "with%25pct"}
	for i, name := range names {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		s, err := Open(ctx, filepath.Join(dir, "atlas.db"))
		if err != nil {
			t.Fatalf("Open under %q: %v", name, err)
		}
		qn := shared.SymbolID(fmt.Sprintf("pkg.Sym%d", i))
		if _, err := s.Symbols().Insert(ctx, SymbolRow{
			QualifiedName: qn, Kind: shared.KindFunc,
			FilePath: "pkg/f.go", Line: 1,
		}); err != nil {
			t.Fatalf("Insert under %q: %v", name, err)
		}
		rows, err := s.Symbols().List(ctx, SymbolFilter{})
		if err != nil {
			t.Fatalf("List under %q: %v", name, err)
		}
		if len(rows) != 1 || rows[0].QualifiedName != qn {
			t.Fatalf("store at %q holds %d rows (%v); it is sharing a file with another path",
				filepath.Join(dir, "atlas.db"), len(rows), rows)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close under %q: %v", name, err)
		}
		// And the file has to be where it was asked for, not at some prefix
		// of it. A store that works by writing somewhere else is not working.
		if _, err := os.Stat(filepath.Join(dir, "atlas.db")); err != nil {
			t.Fatalf("no database at the path Open was given under %q: %v", name, err)
		}
	}
}

// TestProperty_Symbols_RoundTrip asserts that what a port writes is what it
// reads back.
//
// This is the least glamorous property here and the one everything else
// rests on. Every number atlas prints is a join over these rows: a dropped
// end_line makes coverage attribution guess a span, a dropped package makes
// the layer classification wrong, a rewritten kind makes `codebase find`
// miss. None of those fail loudly — they each produce a plausible number
// that happens to be about a symbol other than the one the user asked about.
func TestProperty_Symbols_RoundTrip(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, 24, func(t *testing.T, r *atlastest.Rand) {
		ctx := context.Background()
		s := openTestStore(t)
		want := genSymbolRows(r, r.IntRange(1, 20))

		ids := make([]int64, 0, len(want))
		for _, row := range want {
			id, err := s.Symbols().Insert(ctx, row)
			if err != nil {
				t.Fatalf("seed %d: Insert(%s): %v", r.Seed(), row.QualifiedName, err)
			}
			ids = append(ids, id)
		}

		for i, row := range want {
			got, err := s.Symbols().FindByQualifiedName(ctx, row.QualifiedName)
			if err != nil {
				t.Fatalf("seed %d: FindByQualifiedName(%s): %v", r.Seed(), row.QualifiedName, err)
			}
			if got.ID != ids[i] {
				t.Fatalf("seed %d: %s came back as id %d, Insert reported %d",
					r.Seed(), row.QualifiedName, got.ID, ids[i])
			}
			if diff := diffSymbolRow(row, got); diff != "" {
				t.Fatalf("seed %d: %s did not round-trip: %s", r.Seed(), row.QualifiedName, diff)
			}
		}

		// List must see exactly the same set. A port that answers one way
		// through a point lookup and another through a listing is worse
		// than one that is wrong both ways: half the commands agree.
		listed, err := s.Symbols().List(ctx, SymbolFilter{})
		if err != nil {
			t.Fatalf("seed %d: List: %v", r.Seed(), err)
		}
		if len(listed) != len(want) {
			t.Fatalf("seed %d: List returned %d rows, %d were written", r.Seed(), len(listed), len(want))
		}
	})
}

// TestProperty_Symbols_InsertIsIdempotent asserts a re-scan cannot renumber a
// symbol.
//
// Insert is documented as an upsert keyed on qualified_name, returning the
// existing id when the row is already there. Everything downstream depends on
// that: coverage results, feature links and edges all hold surrogate ids, and
// a second `atlas scan` that minted fresh ids would leave every one of them
// pointing at a symbol that no longer means what it did. The rows would still
// be there; they would just be about something else.
func TestProperty_Symbols_InsertIsIdempotent(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, 24, func(t *testing.T, r *atlastest.Rand) {
		ctx := context.Background()
		s := openTestStore(t)
		rows := genSymbolRows(r, r.IntRange(1, 20))

		first := make([]int64, 0, len(rows))
		for _, row := range rows {
			id, err := s.Symbols().Insert(ctx, row)
			if err != nil {
				t.Fatalf("seed %d: first Insert: %v", r.Seed(), err)
			}
			first = append(first, id)
		}
		for i, row := range rows {
			id, err := s.Symbols().Insert(ctx, row)
			if err != nil {
				t.Fatalf("seed %d: second Insert: %v", r.Seed(), err)
			}
			if id != first[i] {
				t.Fatalf("seed %d: re-inserting %s returned id %d, the first insert returned %d",
					r.Seed(), row.QualifiedName, id, first[i])
			}
		}
		listed, err := s.Symbols().List(ctx, SymbolFilter{})
		if err != nil {
			t.Fatalf("seed %d: List: %v", r.Seed(), err)
		}
		if len(listed) != len(rows) {
			t.Fatalf("seed %d: re-inserting %d rows left %d in the table", r.Seed(), len(rows), len(listed))
		}
	})
}

// TestProperty_Edges_EndpointsSurvivePersistence is issue #97 stated at the
// port boundary.
//
// Edges are written as a pair of surrogate ids and read back the same way, so
// nothing in the row itself can reveal a mis-mapping — the ids are internally
// consistent whichever symbols they point at. The only way to see it is to
// carry the QUALIFIED NAMES through the round trip independently and compare
// the pairs. That is what this does, on graphs the author did not choose.
func TestProperty_Edges_EndpointsSurvivePersistence(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, 24, func(t *testing.T, r *atlastest.Rand) {
		ctx := context.Background()
		s := openTestStore(t)
		d := atlastest.GenDigraph(r)

		idByName := map[string]int64{}
		nameByID := map[int64]string{}
		for i, n := range d.Nodes {
			id, err := s.Symbols().Insert(ctx, SymbolRow{
				QualifiedName: shared.SymbolID(n),
				Kind:          shared.KindFunc,
				FilePath:      fmt.Sprintf("pkg/f%d.go", i%4),
				Line:          i + 1,
			})
			if err != nil {
				t.Fatalf("seed %d: Insert symbol %s: %v", r.Seed(), n, err)
			}
			idByName[n] = id
			nameByID[id] = n
		}

		rows := make([]EdgeRow, 0, len(d.Edges))
		want := map[string]bool{}
		for _, e := range d.Edges {
			rows = append(rows, EdgeRow{
				Tier: graph.TierNameResolved, FromID: idByName[e.From], ToID: idByName[e.To],
				Kind: EdgeKindCall, FilePath: "pkg/f0.go", Line: e.Line,
			})
			want[e.From+" -> "+e.To] = true
		}

		first := make([]int64, 0, len(rows))
		for i, row := range rows {
			id, err := s.Edges().Insert(ctx, row)
			if err != nil {
				t.Fatalf("seed %d: Insert edge %d: %v", r.Seed(), i, err)
			}
			first = append(first, id)
		}
		// Write the whole set a second time, in the same order — which is
		// exactly what the next `atlas scan` does. Insert is documented as an
		// upsert returning the EXISTING id, and the assertion is explicit
		// because the failure is silent: an id pointing at a DIFFERENT edge
		// row is still a valid id, and nothing downstream can tell.
		for i, row := range rows {
			again, err := s.Edges().Insert(ctx, row)
			if err != nil {
				t.Fatalf("seed %d: re-Insert edge %d: %v", r.Seed(), i, err)
			}
			if again != first[i] {
				t.Fatalf("seed %d: re-inserting edge %d->%d @%d returned id %d, the first insert returned %d",
					r.Seed(), row.FromID, row.ToID, row.Line, again, first[i])
			}
		}

		for _, n := range d.Nodes {
			out, err := s.Edges().Out(ctx, idByName[n])
			if err != nil {
				t.Fatalf("seed %d: Out(%s): %v", r.Seed(), n, err)
			}
			for _, e := range out {
				from, okFrom := nameByID[e.FromID]
				to, okTo := nameByID[e.ToID]
				if !okFrom || !okTo {
					t.Fatalf("seed %d: persisted edge %d->%d resolves to no symbol", r.Seed(), e.FromID, e.ToID)
				}
				if from != n {
					t.Fatalf("seed %d: Out(%s) returned an edge whose source is %s", r.Seed(), n, from)
				}
				if !want[from+" -> "+to] {
					t.Fatalf("seed %d: persisted edge %s -> %s was never written; a surrogate id resolved to the wrong symbol",
						r.Seed(), from, to)
				}
			}
		}
	})
}

// TestProperty_Ingest_ReScanNeverRenumbersAnExistingSymbol is the
// scan-ingest-scan idempotence property.
//
// `atlas scan` is incremental and runs on every commit, so this path executes
// far more often than the first-ingest path everything else tests. What it
// must not do is move anything: the rows a previous scan wrote keep their
// content and, above all, keep their surrogate ids. Every coverage result and
// feature link holds a surrogate; renumber one symbol and all of them point
// at a different symbol, while no count anywhere changes. That is issue #97.
//
// Two phases. Phase 1 re-ingests the identical index; phase 2 adds a package
// and ingests the grown tree, which is what a commit actually looks like.
//
// Phase 1 is only as strong as the tree it runs on, and the first version of
// the generator taught that the hard way: with no calls in the generated code
// the graph had no edges, and reverting ingest.go's RowsAffected check to the
// pre-#97 LastInsertId idiom left this test green. Once bodies call each
// other, the same revert fails all 12 seeds — the edges table comes back with
// every call collapsed onto one symbol, which is the #97 signature exactly.
//
// Phase 2 covers what phase 1 structurally cannot: that surrogate ids of
// symbols that already existed do not MOVE when new ones are inserted beside
// them, and that the edges written in that mixed transaction still name the
// pairs the scanner produced.
//
// Comparison goes through storeDumps, the qualified-name-joined projections
// the determinism suite uses, so a mis-mapped surrogate reads as an edge
// between the wrong two NAMES rather than as a number that moved.
func TestProperty_Ingest_ReScanNeverRenumbersAnExistingSymbol(t *testing.T) {
	t.Parallel()
	// Twelve trees rather than sixty-four: each one scans a directory twice
	// and ingests three times through SQLite, which is milliseconds rather
	// than microseconds, and the shapes stop varying well before then.
	atlastest.ForEachSeed(t, 12, func(t *testing.T, r *atlastest.Rand) {
		ctx := context.Background()
		root := t.TempDir()
		atlastest.WriteProject(t, root, atlastest.GenGoProject(r, atlastest.GoProjectOptions{}))

		idx := indexTree(t, root)
		s := openTestStore(t)
		if _, err := s.Ingest(ctx, idx); err != nil {
			t.Fatalf("seed %d: first Ingest: %v", r.Seed(), err)
		}
		before := dumpAll(t, s)
		idsBefore := symbolIDsByName(t, s)

		// Phase 1 — the same index again.
		if _, err := s.Ingest(ctx, idx); err != nil {
			t.Fatalf("seed %d: second Ingest: %v", r.Seed(), err)
		}
		after := dumpAll(t, s)
		for name, dump := range before {
			if dump != after[name] {
				t.Fatalf("seed %d: table %q changed on re-ingest of an unchanged index:\n%s",
					r.Seed(), name, diffRows(dump, after[name]))
			}
		}
		assertIDsUnmoved(t, r, idsBefore, symbolIDsByName(t, s), "re-ingest of an unchanged index")

		// Phase 2 — a commit lands: new code arrives, everything else is
		// exactly where it was.
		atlastest.WriteProject(t, root, atlastest.Project{Files: map[string]string{
			"newpkg/added.go": "package newpkg\n\nfunc Added(x int) int {\n\tif x > 0 {\n\t\tx++\n\t}\n\treturn x\n}\n",
		}})
		grownIdx := indexTree(t, root)
		if _, err := s.Ingest(ctx, grownIdx); err != nil {
			t.Fatalf("seed %d: Ingest after adding a package: %v", r.Seed(), err)
		}
		grown := symbolIDsByName(t, s)
		if _, ok := grown["newpkg.Added"]; !ok {
			t.Fatalf("seed %d: the added package was not indexed; the phase-2 fixture is not doing its job", r.Seed())
		}
		assertIDsUnmoved(t, r, idsBefore, grown, "ingest of a tree that gained a package")
		assertEdgesMatchGraph(t, r, s, grownIdx)
	})
}

// TestProperty_Ingest_EdgesInsertedCountsOnlyNewRows states the #97 bug class
// for the EDGE path, which the symbol path's RowsAffected fix originally left
// behind.
//
// `edges` is written with INSERT OR IGNORE, and SQLite leaves
// last_insert_rowid untouched when that statement skips a conflicting row. So
// `id, _ := res.LastInsertId(); return id != 0` does not report "this insert
// created a row" — it reports "some insert on this connection created a row",
// which on a re-ingest is whatever the symbol loop touched a moment earlier.
// The observable consequence is stats.EdgesInserted: a re-scan of an unchanged
// tree claims to have inserted every edge again, so `atlas scan`'s headline
// counts describe work that did not happen, and nothing downstream that reads
// them can tell an incremental scan from a first one.
//
// The property is stated as a conservation law rather than a fixed number:
// re-ingesting an unchanged index inserts nothing, and the count the FIRST
// ingest reported is the number of rows the table actually holds.
func TestProperty_Ingest_EdgesInsertedCountsOnlyNewRows(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, 8, func(t *testing.T, r *atlastest.Rand) {
		ctx := context.Background()
		root := t.TempDir()
		atlastest.WriteProject(t, root, atlastest.GenGoProject(r, atlastest.GoProjectOptions{}))

		idx := indexTree(t, root)
		s := openTestStore(t)
		first, err := s.Ingest(ctx, idx)
		if err != nil {
			t.Fatalf("seed %d: first Ingest: %v", r.Seed(), err)
		}
		second, err := s.Ingest(ctx, idx)
		if err != nil {
			t.Fatalf("seed %d: second Ingest: %v", r.Seed(), err)
		}
		if second.EdgesInserted != 0 {
			t.Fatalf("seed %d: re-ingesting an unchanged index reported %d edges inserted; INSERT OR IGNORE inserted none",
				r.Seed(), second.EdgesInserted)
		}

		var held int
		if err := s.sqlDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM edges`).Scan(&held); err != nil {
			t.Fatalf("seed %d: count edges: %v", r.Seed(), err)
		}
		if first.EdgesInserted != held {
			t.Fatalf("seed %d: first Ingest reported %d edges inserted but the table holds %d",
				r.Seed(), first.EdgesInserted, held)
		}
	})
}

// assertEdgesMatchGraph anchors the persisted edge set to the SCANNED one:
// every row in `edges`, read back through the qualified names of both
// endpoints, must be an edge the scanner actually produced.
//
// Comparing ids to ids cannot see a mis-mapping — the ids are internally
// consistent whichever symbols they point at, which is why issue #97 survived
// review. Comparing NAMES to the graph is what makes the wrong wiring visible.
func assertEdgesMatchGraph(t *testing.T, r *atlastest.Rand, s *Store, idx *codeindex.Index) {
	t.Helper()
	inGraph := map[string]bool{}
	for _, e := range idx.Graph.Edges {
		inGraph[string(e.From)+" -> "+string(e.To)] = true
	}
	rows := dumpTable(t, s, storeDumps[1].query)
	if strings.TrimSpace(rows) == "" {
		return // a tree with no call edges is a legitimate outcome
	}
	for _, row := range strings.Split(strings.TrimSuffix(rows, "\n"), "\n") {
		fields := strings.Split(row, "\t")
		pair := fields[0] + " -> " + fields[1]
		if !inGraph[pair] {
			t.Fatalf("seed %d: persisted edge %q has no counterpart in the scanned graph - a surrogate id resolved to the wrong symbol",
				r.Seed(), pair)
		}
	}
}

// indexTree scans a generated tree with the sub-scanners that need no
// external runtime, so the suite stays hermetic on a minimal CI container.
//
// HashFiles stays off for the same reason determinism_test.go turns it off:
// a populated hash table makes the next ingest SKIP unchanged files, which is
// a different code path from the upsert one under test here — and skipping is
// precisely how a re-ingest bug hides.
func indexTree(t *testing.T, root string) *codeindex.Index {
	t.Helper()
	idx, err := codeindex.IndexProject(context.Background(), root, codeindex.Options{
		SkipTS: true, SkipPY: true, HashFiles: false,
	})
	if err != nil {
		t.Fatalf("IndexProject(%s): %v", root, err)
	}
	return idx
}

// assertIDsUnmoved fails when a symbol that already existed changed surrogate
// id. Symbols that appeared since `before` are ignored: growth is expected,
// movement is not.
func assertIDsUnmoved(t *testing.T, r *atlastest.Rand, before, after map[string]int64, what string) {
	t.Helper()
	for name, id := range before {
		got, ok := after[name]
		if !ok {
			t.Fatalf("seed %d: symbol %s vanished on %s", r.Seed(), name, what)
		}
		if got != id {
			t.Fatalf("seed %d: symbol %s was renumbered from %d to %d on %s",
				r.Seed(), name, id, got, what)
		}
	}
}

// dumpAll renders every table the determinism suite tracks, keyed by name.
func dumpAll(t *testing.T, s *Store) map[string]string {
	t.Helper()
	out := make(map[string]string, len(storeDumps))
	for _, d := range storeDumps {
		out[d.name] = dumpTable(t, s, d.query)
	}
	return out
}

// symbolIDsByName reads the surrogate id assigned to every qualified name.
// Reading it back out of the table rather than trusting the ids Ingest
// returned is the point: the question is what the STORE now believes.
func symbolIDsByName(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	rows, err := s.Symbols().List(context.Background(), SymbolFilter{})
	if err != nil {
		t.Fatalf("Symbols().List: %v", err)
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[string(row.QualifiedName)] = row.ID
	}
	return out
}

// diffSymbolRow reports the fields that did not survive the round trip.
// CreatedAt and BCPath are excluded: the first is recorded wall-clock, the
// second is derived from the file path by the write path and is therefore an
// output of the store rather than an input to it.
func diffSymbolRow(want, got SymbolRow) string {
	var diffs []string
	if want.QualifiedName != got.QualifiedName {
		diffs = append(diffs, fmt.Sprintf("qualified_name %q -> %q", want.QualifiedName, got.QualifiedName))
	}
	if want.Kind != got.Kind {
		diffs = append(diffs, fmt.Sprintf("kind %q -> %q", want.Kind, got.Kind))
	}
	if want.FilePath != got.FilePath {
		diffs = append(diffs, fmt.Sprintf("file_path %q -> %q", want.FilePath, got.FilePath))
	}
	if want.Line != got.Line {
		diffs = append(diffs, fmt.Sprintf("line %d -> %d", want.Line, got.Line))
	}
	if !sameIntPtr(want.EndLine, got.EndLine) {
		diffs = append(diffs, fmt.Sprintf("end_line %s -> %s", fmtIntPtr(want.EndLine), fmtIntPtr(got.EndLine)))
	}
	if !sameStrPtr(want.Package, got.Package) {
		diffs = append(diffs, fmt.Sprintf("package %s -> %s", fmtStrPtr(want.Package), fmtStrPtr(got.Package)))
	}
	sort.Strings(diffs)
	return strings.Join(diffs, "; ")
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%d", *p)
}

func fmtStrPtr(p *string) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%q", *p)
}

// FuzzSymbols_RoundTrip is the deep-search entry point for the round-trip
// property: `go test ./packages/store -fuzz=FuzzSymbols`. Under a plain
// `go test` it runs the seed corpus only.
func FuzzSymbols_RoundTrip(f *testing.F) {
	for _, seed := range []uint64{1, 2, 3, 17, 99} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		ctx := context.Background()
		r := atlastest.New(seed)
		s := openTestStore(t)
		for _, row := range genSymbolRows(r, r.IntRange(1, 8)) {
			if _, err := s.Symbols().Insert(ctx, row); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			got, err := s.Symbols().FindByQualifiedName(ctx, row.QualifiedName)
			if err != nil {
				if errors.Is(err, shared.ErrSymbolNotFound) {
					t.Fatalf("seed %d: %s was written and is not there", seed, row.QualifiedName)
				}
				t.Fatalf("FindByQualifiedName: %v", err)
			}
			if diff := diffSymbolRow(row, got); diff != "" {
				t.Fatalf("seed %d: %s did not round-trip: %s", seed, row.QualifiedName, diff)
			}
		}
	})
}
