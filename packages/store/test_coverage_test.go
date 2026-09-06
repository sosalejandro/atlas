package store

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// seedSymbols ingests a handful of symbols and returns qualified name -> id.
func seedSymbols(t *testing.T, s *Store, names ...string) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	g := graph.New()
	syms := make([]shared.Symbol, 0, len(names))
	for i, n := range names {
		sym := shared.Symbol{
			ID:       shared.SymbolID(n),
			Kind:     shared.KindFunc,
			Position: shared.FilePosition{Path: "src/f.go", Line: 10 * (i + 1)},
			EndLine:  10*(i+1) + 5,
		}
		g.AddNode(&graph.Node{Symbol: sym})
		syms = append(syms, sym)
	}
	if _, err := s.Ingest(ctx, &codeindex.Index{
		Root: ".", Graph: g, Symbols: syms, FileHashes: map[string]codeindex.FileHash{},
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	rows, err := s.Symbols().List(ctx, SymbolFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[string(r.QualifiedName)] = r.ID
	}
	return out
}

func seedRun(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.Coverage().InsertRun(context.Background(), CoverageRun{Framework: FrameworkGoTest})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	return id
}

// Per-test evidence is the whole point of schema 0010: the store must be able
// to answer both directions of the question (what did this test run, and what
// ran this symbol) and how many tests touched each symbol.
func TestTestCoverage_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	ids := seedSymbols(t, s, "pkg.TestLogin", "pkg.TestCheckout", "pkg.Login", "pkg.Shared")
	runID := seedRun(t, s)

	rows := []TestExecution{
		{TestSymbolID: ids["pkg.TestLogin"], SymbolID: ids["pkg.Login"], CoveredStmts: 8, TotalStmts: 10},
		{TestSymbolID: ids["pkg.TestLogin"], SymbolID: ids["pkg.Shared"], CoveredStmts: 2, TotalStmts: 4},
		{TestSymbolID: ids["pkg.TestCheckout"], SymbolID: ids["pkg.Shared"], CoveredStmts: 3, TotalStmts: 4},
	}
	if err := s.TestCoverage().Insert(ctx, runID, rows); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.TestCoverage().SymbolsExecutedBy(ctx, runID, ids["pkg.TestLogin"])
	if err != nil {
		t.Fatalf("SymbolsExecutedBy: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("TestLogin executed %d symbols, want 2", len(got))
	}

	tests, err := s.TestCoverage().TestsExecuting(ctx, runID, ids["pkg.Shared"])
	if err != nil {
		t.Fatalf("TestsExecuting: %v", err)
	}
	if len(tests) != 2 {
		t.Errorf("pkg.Shared was executed by %d tests, want 2 (this is affected-test selection)", len(tests))
	}

	n, err := s.TestCoverage().CountTests(ctx, runID)
	if err != nil {
		t.Fatalf("CountTests: %v", err)
	}
	if n != 2 {
		t.Errorf("CountTests = %d, want 2", n)
	}

	fan, err := s.TestCoverage().FanIn(ctx, runID)
	if err != nil {
		t.Fatalf("FanIn: %v", err)
	}
	if fan[ids["pkg.Shared"]] != 2 || fan[ids["pkg.Login"]] != 1 {
		t.Errorf("fan-in = %v, want Shared:2 Login:1", fan)
	}
}

// A retried ingest of the same run must not fail on the primary key.
func TestTestCoverage_InsertIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	ids := seedSymbols(t, s, "pkg.TestLogin", "pkg.Login")
	runID := seedRun(t, s)
	rows := []TestExecution{{TestSymbolID: ids["pkg.TestLogin"], SymbolID: ids["pkg.Login"], CoveredStmts: 5, TotalStmts: 9}}

	for i := 0; i < 2; i++ {
		if err := s.TestCoverage().Insert(ctx, runID, rows); err != nil {
			t.Fatalf("Insert #%d: %v", i+1, err)
		}
	}
	got, err := s.TestCoverage().SymbolsExecutedBy(ctx, runID, ids["pkg.TestLogin"])
	if err != nil {
		t.Fatalf("SymbolsExecutedBy: %v", err)
	}
	if len(got) != 1 || got[0].CoveredStmts != 5 {
		t.Errorf("got %+v, want exactly one row with covered=5", got)
	}
}

// Deleting a run takes its per-test evidence with it: the table can never
// outlive what it describes (issue #98's lesson, applied at design time).
func TestTestCoverage_CascadesWithRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)
	ids := seedSymbols(t, s, "pkg.TestLogin", "pkg.Login")
	runID := seedRun(t, s)
	if err := s.TestCoverage().Insert(ctx, runID, []TestExecution{
		{TestSymbolID: ids["pkg.TestLogin"], SymbolID: ids["pkg.Login"], CoveredStmts: 1, TotalStmts: 1},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := s.conn.ExecContext(ctx, `DELETE FROM coverage_runs WHERE id = ?`, runID); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	n, err := s.TestCoverage().CountTests(ctx, runID)
	if err != nil {
		t.Fatalf("CountTests: %v", err)
	}
	if n != 0 {
		t.Errorf("after deleting the run, %d tests still have evidence", n)
	}
}
