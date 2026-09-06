package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// seedTwoSymbols returns two symbol ids in two files with the given
// paths, so a test can build edges the language classifier will bucket
// the way it expects.
func seedTwoSymbols(t *testing.T, s *Store, fromFile, toFile string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	from, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: shared.SymbolID("p." + fromFile), Kind: shared.KindFunc, FilePath: fromFile, Line: 1})
	if err != nil {
		t.Fatalf("seed from symbol: %v", err)
	}
	to, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: shared.SymbolID("p." + toFile), Kind: shared.KindFunc, FilePath: toFile, Line: 1})
	if err != nil {
		t.Fatalf("seed to symbol: %v", err)
	}
	return from, to
}

// An edge whose producer never said how it resolved the call must not
// reach the table. This is the guard the whole of #146 rests on: the
// moment an unstated tier is allowed to land as anything at all, the
// column stops being evidence and becomes decoration.
func TestEdges_Insert_RejectsUnsetTier(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	from, to := seedTwoSymbols(t, s, "src/a.go", "src/b.go")

	_, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: from, ToID: to, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 7,
	})
	if err == nil {
		t.Fatal("Insert with no resolution tier succeeded; want an error")
	}
	if !strings.Contains(err.Error(), "resolution_tier") {
		t.Errorf("error %q does not name the missing column", err)
	}
}

// A tier from somebody else's taxonomy is as bad as none: it would sit
// in the column looking like provenance while being uncomparable with
// every other row.
func TestEdges_Insert_RejectsForeignTier(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	from, to := seedTwoSymbols(t, s, "src/a.go", "src/b.go")

	_, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: from, ToID: to, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 7,
		Tier: graph.ResolutionTier("ast_inferred"),
	})
	if err == nil {
		t.Fatal("Insert with a foreign tier value succeeded; want an error")
	}
}

// The database must refuse an untiered row too, not only the Go port.
// The Go guard is what produces a readable message; the CHECK is what
// makes the guarantee hold for a path nobody has written yet.
func TestEdges_Schema_RejectsUntieredRawInsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	from, to := seedTwoSymbols(t, s, "src/a.go", "src/b.go")

	_, err := s.sqlDB().ExecContext(ctx,
		`INSERT INTO edges (from_symbol_id, to_symbol_id, kind, file_path, line)
		 VALUES (?, ?, 'call', 'src/a.go', 7)`, from, to)
	if err == nil {
		t.Fatal("raw INSERT omitting resolution_tier succeeded; the column has a default it must not have")
	}
}

// Migration 0018 has to survive contact with a store that already has
// edges in it, and land every one of them at the WEAKEST tier.
//
// The optimistic alternative is the failure this issue exists to
// prevent: backfilling at `name_resolved` (what the current Go resolver
// reaches most of the time) would launder thousands of pre-existing
// guesses into claims, and the first histogram anyone diffed across #87
// would be measuring the backfill rather than the scanner. The tier is
// per-row information living in the resolver's control flow and is not
// recoverable from a stored row, so the floor is the only honest answer.
//
// The test builds the pre-0018 table shape by hand and stamps
// golang-migrate's bookkeeping at 17, so Open applies 0018 first. Every
// migration added after it runs too -- that is the point of stamping a
// version rather than a file -- so the assertions below are about what
// 0018 did to these rows, and the version check only confirms the runner
// got at least that far.
func TestMigration0018_BackfillsExistingRowsAtTheWeakestTier(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pre-0018.db")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	// The 0001+0007+0008 shape: edges with edge_meta and the widened
	// kind CHECK, and no provenance columns.
	for _, stmt := range []string{
		`CREATE TABLE symbols (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			qualified_name TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL, file_path TEXT NOT NULL, line INTEGER NOT NULL,
			end_line INTEGER, package TEXT, bc_path TEXT,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE edges (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			from_symbol_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
			to_symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
			kind TEXT NOT NULL, file_path TEXT NOT NULL, line INTEGER NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			edge_meta TEXT)`,
		`INSERT INTO symbols (id, qualified_name, kind, file_path, line)
			VALUES (1, 'p.A', 'func', 'src/a.go', 1), (2, 'p.B', 'func', 'src/b.go', 1)`,
		`INSERT INTO edges (id, from_symbol_id, to_symbol_id, kind, file_path, line, edge_meta)
			VALUES (7, 1, 2, 'call', 'src/a.go', 11, NULL),
			       (9, 2, 1, 'import', 'src/b.go', 3, 'module')`,
		`CREATE TABLE schema_migrations (version uint64, dirty bool)`,
		`INSERT INTO schema_migrations (version, dirty) VALUES (17, false)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed pre-0018 store (%.40s...): %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open (applying 0018): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	rows, err := s.sqlDB().QueryContext(ctx,
		`SELECT id, kind, line, edge_meta, resolution_tier, ambiguous FROM edges ORDER BY id`)
	if err != nil {
		t.Fatalf("read migrated edges: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type migrated struct {
		id        int64
		kind      string
		line      int64
		meta      *string
		tier      string
		ambiguous int64
	}
	var got []migrated
	for rows.Next() {
		var m migrated
		if err := rows.Scan(&m.id, &m.kind, &m.line, &m.meta, &m.tier, &m.ambiguous); err != nil {
			t.Fatalf("scan migrated edge: %v", err)
		}
		got = append(got, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated edges: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("migrated edges = %d, want 2 (%+v)", len(got), got)
	}
	for _, m := range got {
		if m.tier != string(graph.TierSyntactic) {
			t.Errorf("edge %d backfilled at %q, want %q -- an optimistic backfill "+
				"launders every pre-existing guess into a claim",
				m.id, m.tier, graph.TierSyntactic)
		}
		if m.ambiguous != 0 {
			t.Errorf("edge %d ambiguous = %d, want 0", m.id, m.ambiguous)
		}
	}
	// Surrogate ids and every pre-existing column survive the table
	// rebuild: an id a caller was holding across the migration must
	// still name the same edge.
	if got[0].id != 7 || got[0].kind != "call" || got[0].line != 11 {
		t.Errorf("edge 7 changed shape: %+v", got[0])
	}
	if got[1].id != 9 || got[1].meta == nil || *got[1].meta != EdgeMetaImportScopeModule {
		t.Errorf("edge 9 lost its edge_meta: %+v", got[1])
	}

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v < 18 {
		t.Errorf("schema version after migrating = %d, want >= 18 "+
			"(0018 is the migration under test; later ones run too)", v)
	}
}

// Tier and Ambiguous must survive a round trip. Ambiguous in particular
// is the flag packages/graph has computed since v0.4 and dropped at the
// storage boundary ever since -- #146 exists partly because that signal
// was being thrown away every scan.
func TestEdges_Insert_RoundTripsTierAndAmbiguous(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	from, to := seedTwoSymbols(t, s, "src/a.go", "src/b.go")

	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: from, ToID: to, Kind: EdgeKindCall, FilePath: "src/a.go", Line: 7,
		Tier: graph.TierSyntactic, Ambiguous: true,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	out, err := s.Edges().Out(ctx, from)
	if err != nil {
		t.Fatalf("Out: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("Out len = %d, want 1", len(out))
	}
	if out[0].Tier != graph.TierSyntactic {
		t.Errorf("Out tier = %q, want %q", out[0].Tier, graph.TierSyntactic)
	}
	if !out[0].Ambiguous {
		t.Error("Out ambiguous = false, want true")
	}

	in, err := s.Edges().In(ctx, to)
	if err != nil {
		t.Fatalf("In: %v", err)
	}
	if len(in) != 1 || in[0].Tier != graph.TierSyntactic || !in[0].Ambiguous {
		t.Errorf("In row = %+v, want tier=%q ambiguous=true", in, graph.TierSyntactic)
	}
}

// The histogram is the reporting surface #87's review depends on: a
// count that holds steady while the composition rots is the failure
// this replaces, so the shape that has to be readable is
// (language, tier) -> count.
func TestEdges_TierHistogram_GroupsByLanguageAndTier(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	goFrom, goTo := seedTwoSymbols(t, s, "internal/a.go", "internal/b.go")
	tsFrom, tsTo := seedTwoSymbols(t, s, "web/src/hook.ts", "web/src/api.tsx")
	pyFrom, pyTo := seedTwoSymbols(t, s, "svc/main.py", "svc/util.py")

	seed := []EdgeRow{
		{FromID: goFrom, ToID: goTo, Kind: EdgeKindCall, FilePath: "internal/a.go", Line: 3,
			Tier: graph.TierNameResolved},
		{FromID: goFrom, ToID: goTo, Kind: EdgeKindCall, FilePath: "internal/a.go", Line: 4,
			Tier: graph.TierSyntactic, Ambiguous: true},
		{FromID: tsFrom, ToID: tsTo, Kind: EdgeKindCall, FilePath: "web/src/hook.ts", Line: 9,
			Tier: graph.TierSyntactic},
		{FromID: pyFrom, ToID: pyTo, Kind: EdgeKindImport, FilePath: "svc/main.py", Line: 2,
			Tier: graph.TierNameResolved},
	}
	for _, e := range seed {
		if _, err := s.Edges().Insert(ctx, e); err != nil {
			t.Fatalf("Insert %+v: %v", e, err)
		}
	}

	hist, err := s.Edges().TierHistogram(ctx)
	if err != nil {
		t.Fatalf("TierHistogram: %v", err)
	}

	got := map[string]int{}
	ambiguous := map[string]int{}
	for _, b := range hist {
		got[b.Lang+"/"+string(b.Tier)] = b.Edges
		ambiguous[b.Lang+"/"+string(b.Tier)] = b.Ambiguous
	}
	want := map[string]int{
		"go/name_resolved": 1,
		"go/syntactic":     1,
		"ts/syntactic":     1,
		"py/name_resolved": 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("histogram[%s] = %d, want %d (full: %+v)", k, got[k], v, hist)
		}
	}
	if len(hist) != len(want) {
		t.Errorf("histogram has %d buckets, want %d: %+v", len(hist), len(want), hist)
	}
	if ambiguous["go/syntactic"] != 1 {
		t.Errorf("go/syntactic ambiguous = %d, want 1", ambiguous["go/syntactic"])
	}
	if ambiguous["go/name_resolved"] != 0 {
		t.Errorf("go/name_resolved ambiguous = %d, want 0", ambiguous["go/name_resolved"])
	}
}

// Bucket order has to be stable or the histogram is not diffable, and
// diffing it across a resolver migration is the entire point.
func TestEdges_TierHistogram_StableOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	goFrom, goTo := seedTwoSymbols(t, s, "a.go", "b.go")
	tsFrom, tsTo := seedTwoSymbols(t, s, "a.ts", "b.ts")

	for _, e := range []EdgeRow{
		{FromID: tsFrom, ToID: tsTo, Kind: EdgeKindCall, FilePath: "a.ts", Line: 1, Tier: graph.TierSyntactic},
		{FromID: goFrom, ToID: goTo, Kind: EdgeKindCall, FilePath: "a.go", Line: 1, Tier: graph.TierSyntactic},
		{FromID: goFrom, ToID: goTo, Kind: EdgeKindCall, FilePath: "a.go", Line: 2, Tier: graph.TierNameResolved},
	} {
		if _, err := s.Edges().Insert(ctx, e); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	hist, err := s.Edges().TierHistogram(ctx)
	if err != nil {
		t.Fatalf("TierHistogram: %v", err)
	}
	// Languages alphabetical, tiers strongest-first within a language.
	want := []string{"go/name_resolved", "go/syntactic", "ts/syntactic"}
	if len(hist) != len(want) {
		t.Fatalf("histogram = %+v, want %d buckets", hist, len(want))
	}
	for i, b := range hist {
		if key := b.Lang + "/" + string(b.Tier); key != want[i] {
			t.Errorf("bucket[%d] = %s, want %s", i, key, want[i])
		}
	}
}
