package coverage_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Per-test ingest over the same fixture the whole-run acceptance test uses.
// Two tests, two packages, one shared expectation: each test's evidence names
// only what that test ran, and the union matches what a whole-run ingest would
// have recorded.
func TestPerTestIngest_RecordsWhatEachTestRan(t *testing.T) {
	ctx := context.Background()

	idx, err := codeindex.IndexProject(ctx, "testdata/attribution", codeindex.Options{SkipTS: true, SkipPY: true})
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Each profile is what that package's test alone executed — the billing
	// test never enters shipping, and vice versa. Line numbers match the
	// checked-in fixture sources.
	billing := `mode: set
github.com/example/attribution/billing/order.go:11.29,13.2 1 1
github.com/example/attribution/billing/order.go:17.23,19.2 1 0
github.com/example/attribution/billing/order.go:23.31,24.15 1 1
github.com/example/attribution/billing/order.go:24.15,26.3 1 0
github.com/example/attribution/billing/order.go:27.2,27.14 1 1
`
	shipping := `mode: set
github.com/example/attribution/shipping/order.go:9.29,11.2 1 1
github.com/example/attribution/shipping/order.go:14.27,15.17 1 1
github.com/example/attribution/shipping/order.go:15.17,17.3 1 0
github.com/example/attribution/shipping/order.go:18.2,18.15 1 1
`

	stats, err := coverage.IngestGoProfilePerTest(ctx, s, store.FrameworkGoTest, []coverage.PerTestProfile{
		{Test: "Order.Total", Profile: strings.NewReader(billing)},       // billing keeps the bare id
		{Test: "shipping.rate", Profile: strings.NewReader(shipping)},    // stand-in test symbol in shipping
		{Test: "nope.TestMissing", Profile: strings.NewReader(shipping)}, // no such symbol
	})
	if err != nil {
		t.Fatalf("IngestGoProfilePerTest: %v", err)
	}

	if stats.TestsIngested != 2 {
		t.Errorf("TestsIngested = %d, want 2", stats.TestsIngested)
	}
	if len(stats.TestsUnresolved) != 1 || stats.TestsUnresolved[0] != shared.SymbolID("nope.TestMissing") {
		t.Errorf("unresolved = %v, want [nope.TestMissing] — an unknown test name must be reported, not dropped", stats.TestsUnresolved)
	}
	if stats.StmtsUnattributed != 0 {
		t.Errorf("%d statements unattributed, want 0: %+v", stats.StmtsUnattributed, stats.Gaps)
	}

	byName := map[string]int64{}
	rows, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		t.Fatalf("List symbols: %v", err)
	}
	for _, r := range rows {
		byName[string(r.QualifiedName)] = r.ID
	}

	// The billing test's evidence names billing symbols and nothing else.
	got, err := s.TestCoverage().SymbolsExecutedBy(ctx, stats.RunID, byName["Order.Total"])
	if err != nil {
		t.Fatalf("SymbolsExecutedBy: %v", err)
	}
	executed := map[int64]bool{}
	for _, r := range got {
		executed[r.SymbolID] = true
	}
	if !executed[byName["Order.Total"]] || !executed[byName["billing.normalize"]] {
		t.Errorf("billing test evidence missing its own symbols: %+v", got)
	}
	if executed[byName["shipping.Order.Total"]] || executed[byName["shipping.rate"]] {
		t.Error("billing test credited with shipping symbols — the per-test split is not real")
	}
	if executed[byName["Order.Pay"]] {
		t.Error("Order.Pay never ran; it must not appear as executed evidence")
	}

	// Fan-in: every symbol here was run by exactly one test.
	fan, err := s.TestCoverage().FanIn(ctx, stats.RunID)
	if err != nil {
		t.Fatalf("FanIn: %v", err)
	}
	for name, id := range map[string]int64{
		"Order.Total": byName["Order.Total"], "billing.normalize": byName["billing.normalize"],
	} {
		if fan[id] != 1 {
			t.Errorf("fan-in for %s = %d, want 1", name, fan[id])
		}
	}

	// And the union run still matches the whole-run numbers: 6 covered of 9.
	results, err := s.Coverage().ListResults(ctx, stats.RunID)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	covered, total := 0, 0
	for _, r := range results {
		covered += r.CoveredStmts
		total += r.TotalStmts
	}
	if covered != 6 || total != 9 {
		t.Errorf("union run = %d/%d, want 6/9 (same as the whole-run ingest)", covered, total)
	}
}
