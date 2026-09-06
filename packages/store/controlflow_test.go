package store

import (
	"context"
	"errors"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// cfgTestSymbol inserts a symbol to hang flow rows off. Every control-flow
// row is keyed by symbol_id — a flow result that cannot be joined back to the
// graph is a flow result nobody can use — so there is no such thing as a
// symbol-less test here.
func cfgTestSymbol(t *testing.T, s *Store, qn string) int64 {
	t.Helper()
	id, err := s.Symbols().Insert(context.Background(), SymbolRow{
		QualifiedName: shared.SymbolID(qn),
		Kind:          shared.KindFunc,
		FilePath:      "pkg/svc.go",
		Line:          10,
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
	return id
}

func sampleFlow(id int64) SymbolFlow {
	return SymbolFlow{
		SymbolID: id,
		Blocks: []FlowBlock{
			{Index: 0, Kind: "entry", StartLine: 10, EndLine: 10},
			{Index: 1, Kind: "exit", StartLine: 20, EndLine: 20},
			{Index: 2, Kind: "branch", StartLine: 11, EndLine: 12},
			{Index: 3, Kind: "body", StartLine: 13, EndLine: 14},
		},
		Edges: []FlowEdge{
			{Index: 0, From: 0, To: 2, Kind: "seq"},
			{Index: 1, From: 2, To: 3, Kind: "true", Condition: "err != nil"},
			{Index: 2, From: 2, To: 1, Kind: "false", Condition: "err != nil"},
			{Index: 3, From: 3, To: 1, Kind: "seq"},
		},
		Metrics: FlowMetrics{
			SymbolID: id, Complexity: 2, Decisions: 1, BranchArms: 2,
			Conditions: 1, ConditionsIndependent: 1,
		},
	}
}

func TestControlFlow_ReplaceRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := cfgTestSymbol(t, s, "pkg.Handle")

	want := sampleFlow(id)
	if err := s.ControlFlow().Replace(ctx, want); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := s.ControlFlow().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Blocks) != 4 || len(got.Edges) != 4 {
		t.Fatalf("blocks/edges = %d/%d, want 4/4", len(got.Blocks), len(got.Edges))
	}
	if got.Blocks[0].Kind != "entry" || got.Blocks[1].Kind != "exit" {
		t.Errorf("block 0/1 must be the synthetic entry/exit, got %q/%q",
			got.Blocks[0].Kind, got.Blocks[1].Kind)
	}
	if got.Edges[1].Condition != "err != nil" {
		t.Errorf("edge condition = %q, want %q", got.Edges[1].Condition, "err != nil")
	}
	if got.Metrics.Complexity != 2 {
		t.Errorf("complexity = %d, want 2", got.Metrics.Complexity)
	}
	if got.Metrics.BuiltAt.IsZero() {
		t.Error("built_at must be stamped by the write")
	}
}

// TestControlFlow_ReplaceIsIdempotent: re-running the builder over a symbol
// must leave exactly one generation of rows. Two interleaved generations
// would silently double the edge count, and cyclomatic complexity is an edge
// count.
func TestControlFlow_ReplaceIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := cfgTestSymbol(t, s, "pkg.Handle")

	flow := sampleFlow(id)
	for range 2 {
		if err := s.ControlFlow().Replace(ctx, flow); err != nil {
			t.Fatalf("Replace: %v", err)
		}
	}
	got, err := s.ControlFlow().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Edges) != 4 {
		t.Fatalf("edges after two writes = %d, want 4", len(got.Edges))
	}

	// A rebuild that finds FEWER blocks must shrink the stored graph, not
	// leave the surplus behind.
	shrunk := flow
	shrunk.Blocks = flow.Blocks[:2]
	shrunk.Edges = flow.Edges[:1]
	if err := s.ControlFlow().Replace(ctx, shrunk); err != nil {
		t.Fatalf("Replace shrunk: %v", err)
	}
	got, err = s.ControlFlow().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Blocks) != 2 || len(got.Edges) != 1 {
		t.Fatalf("blocks/edges after shrink = %d/%d, want 2/1", len(got.Blocks), len(got.Edges))
	}
}

func TestControlFlow_GetMissingIsNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.ControlFlow().Get(context.Background(), 4242)
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("Get(missing) error = %v, want shared.ErrNotFound", err)
	}
}

// TestControlFlow_DecisionCoverageKeepsTheDenominatorHonest is the point of
// the whole table: the ratio is taken/DECIDABLE, and a symbol with nothing
// decidable reports "unavailable" rather than 0%.
func TestControlFlow_DecisionCoverage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := cfgTestSymbol(t, s, "pkg.Handle")

	dc := DecisionCoverage{
		SymbolID: id, OutcomesTotal: 6, OutcomesDecidable: 4, OutcomesTaken: 1,
		Source: "cover.out",
	}
	if err := s.ControlFlow().SetDecisionCoverage(ctx, dc); err != nil {
		t.Fatalf("SetDecisionCoverage: %v", err)
	}
	got, err := s.ControlFlow().GetDecisionCoverage(ctx, id)
	if err != nil {
		t.Fatalf("GetDecisionCoverage: %v", err)
	}
	pct, ok := got.Percent()
	if !ok {
		t.Fatal("coverage should be available")
	}
	if pct != 25 {
		t.Errorf("percent = %.1f, want 25 (1 of 4 decidable, NOT 1 of 6)", pct)
	}
	if got.Undecidable() != 2 {
		t.Errorf("undecidable = %d, want 2", got.Undecidable())
	}

	blind := DecisionCoverage{SymbolID: id, OutcomesTotal: 2, Source: "cover.out"}
	if err := s.ControlFlow().SetDecisionCoverage(ctx, blind); err != nil {
		t.Fatalf("SetDecisionCoverage (blind): %v", err)
	}
	got, err = s.ControlFlow().GetDecisionCoverage(ctx, id)
	if err != nil {
		t.Fatalf("GetDecisionCoverage: %v", err)
	}
	if _, ok := got.Percent(); ok {
		t.Error("a symbol with no decidable outcome must report unavailable, not 0%")
	}
}

func TestControlFlow_Findings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := cfgTestSymbol(t, s, "pkg.LoadUsers")
	b := cfgTestSymbol(t, s, "pkg.LoadOrders")

	err := s.ControlFlow().ReplaceFindings(ctx, a, []FlowFinding{
		{Kind: FindingQueryInLoop, Confidence: "low", Line: 19, RelatedLine: 18, Detail: "guarded"},
		{Kind: FindingQueryInLoop, Confidence: "high", Line: 30, RelatedLine: 29},
	})
	if err != nil {
		t.Fatalf("ReplaceFindings: %v", err)
	}
	if err := s.ControlFlow().ReplaceFindings(ctx, b, []FlowFinding{
		{Kind: FindingUnreachable, Confidence: "high", Line: 44},
	}); err != nil {
		t.Fatalf("ReplaceFindings b: %v", err)
	}

	got, err := s.ControlFlow().Findings(ctx, FindingQueryInLoop)
	if err != nil {
		t.Fatalf("Findings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2", len(got))
	}
	if got[0].Confidence != "high" {
		t.Errorf("findings must be highest-confidence first, got %q", got[0].Confidence)
	}
	if got[0].SymbolID != a {
		t.Errorf("finding lost its symbol: symbol_id = %d, want %d", got[0].SymbolID, a)
	}

	all, err := s.ControlFlow().Findings(ctx, "")
	if err != nil {
		t.Fatalf("Findings(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all findings = %d, want 3", len(all))
	}

	// Re-running the analysis with nothing to report must clear the old rows,
	// or a fixed N+1 stays on the report forever.
	if err := s.ControlFlow().ReplaceFindings(ctx, a, nil); err != nil {
		t.Fatalf("ReplaceFindings (clear): %v", err)
	}
	all, err = s.ControlFlow().Findings(ctx, "")
	if err != nil {
		t.Fatalf("Findings(all): %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("all findings after clear = %d, want 1", len(all))
	}
}

func TestControlFlow_TopComplexity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	simple := cfgTestSymbol(t, s, "pkg.Simple")
	hairy := cfgTestSymbol(t, s, "pkg.Hairy")

	for id, complexity := range map[int64]int{simple: 2, hairy: 17} {
		flow := sampleFlow(id)
		flow.Metrics.Complexity = complexity
		if err := s.ControlFlow().Replace(ctx, flow); err != nil {
			t.Fatalf("Replace: %v", err)
		}
	}
	got, err := s.ControlFlow().TopComplexity(ctx, 10)
	if err != nil {
		t.Fatalf("TopComplexity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].SymbolID != hairy || got[0].Complexity != 17 {
		t.Errorf("most complex first: got symbol %d complexity %d", got[0].SymbolID, got[0].Complexity)
	}
}

// TestControlFlow_CascadesWithSymbol: the flow of a deleted symbol must not
// outlive it, or a re-scan leaves orphan graphs that no query can reach and
// nothing ever cleans up.
func TestControlFlow_CascadesWithSymbol(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := cfgTestSymbol(t, s, "pkg.Handle")
	if err := s.ControlFlow().Replace(ctx, sampleFlow(id)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := s.Symbols().DeleteByFile(ctx, "pkg/svc.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	if _, err := s.ControlFlow().Get(ctx, id); !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("flow survived its symbol: err = %v", err)
	}
	var blocks int
	if err := s.sqlDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM cfg_blocks`).Scan(&blocks); err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if blocks != 0 {
		t.Errorf("cfg_blocks rows after symbol delete = %d, want 0", blocks)
	}
}
