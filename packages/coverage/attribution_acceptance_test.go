package coverage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/store"
)

// The acceptance bar for statement-coverage attribution, end to end:
//
//	GIVEN a repo where two packages declare the same type name (billing.Order
//	      and shipping.Order) and each has a package-private helper,
//	 WHEN atlas indexes it and ingests a REAL `go test -coverprofile` profile,
//	 THEN every symbol's covered/total matches `go tool cover -func` exactly,
//	  AND no statement in the profile is left unattributed.
//
// Each clause maps to a bug this fixture used to hit: the colliding type made
// a whole file invisible (#85), the helpers had no symbol to be charged to,
// and the missing end_line made every span a guess of where the next symbol
// started. testdata/attribution/cover.out is the untouched output of
//
//	go test ./... -coverprofile=cover.out -coverpkg=./...
//
// over those exact sources — including the duplicated block set that
// -coverpkg emits, which the ingest has to merge before counting.
func TestAttribution_MatchesGoToolCover(t *testing.T) {
	ctx := context.Background()

	idx, err := codeindex.IndexProject(ctx, "testdata/attribution", codeindex.Options{
		SkipTS: true,
		SkipPY: true,
	})
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

	profile, err := os.Open("testdata/attribution/cover.out")
	if err != nil {
		t.Fatalf("open profile: %v", err)
	}
	defer func() { _ = profile.Close() }()

	stats, err := coverage.IngestGoProfile(ctx, s, coverage.RunMeta{Framework: store.FrameworkGoTest}, profile)
	if err != nil {
		t.Fatalf("IngestGoProfile: %v", err)
	}

	// THEN: nothing is dropped. Both files reconcile, and every statement in
	// the profile is charged to some symbol.
	if stats.FilesMatched != 2 || stats.FilesUnmatched != 0 {
		t.Errorf("files matched=%d unmatched=%d, want 2/0 (gaps: %+v)",
			stats.FilesMatched, stats.FilesUnmatched, stats.Gaps)
	}
	if stats.StmtsUnattributed != 0 {
		t.Errorf("%d statements unattributed, want 0; gaps: %+v",
			stats.StmtsUnattributed, stats.Gaps)
	}

	// THEN: per-symbol fractions equal `go tool cover -func` on the same
	// profile:
	//
	//	billing/order.go:11  Total      100.0%   -> 1/1
	//	billing/order.go:17  Pay          0.0%   -> 0/1
	//	billing/order.go:23  normalize   66.7%   -> 2/3
	//	shipping/order.go:9  Total      100.0%   -> 1/1
	//	shipping/order.go:14 rate        66.7%   -> 2/3
	//	total                            66.7%   -> 6/9
	want := map[string]struct{ covered, total int }{
		"Order.Total":          {1, 1}, // billing keeps the bare id (walked first)
		"Order.Pay":            {0, 1},
		"billing.normalize":    {2, 3},
		"shipping.Order.Total": {1, 1}, // the collision, package-qualified
		"shipping.rate":        {2, 3},
	}

	byName := map[string]store.SymbolRow{}
	rows, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		t.Fatalf("list symbols: %v", err)
	}
	for _, r := range rows {
		byName[string(r.QualifiedName)] = r
	}
	results, err := s.Coverage().ListResults(ctx, stats.RunID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	got := map[string]struct{ covered, total int }{}
	for _, res := range results {
		if res.SymbolID == nil {
			continue
		}
		for name, row := range byName {
			if row.ID == *res.SymbolID {
				got[name] = struct{ covered, total int }{res.CoveredStmts, res.TotalStmts}
			}
		}
	}

	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("no coverage result for %s (indexed symbols: %v)", name, keys(byName))
			continue
		}
		if g != w {
			t.Errorf("%s = %d/%d, want %d/%d (go tool cover -func)", name, g.covered, g.total, w.covered, w.total)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d symbols with results, want %d: %+v", len(got), len(want), got)
	}

	// AND the run total tracks `go tool cover`'s "total: (statements) 66.7%".
	covered, total := 0, 0
	for _, g := range got {
		covered += g.covered
		total += g.total
	}
	if covered != 6 || total != 9 {
		t.Errorf("run total = %d/%d, want 6/9 (66.7%%)", covered, total)
	}
}

func keys(m map[string]store.SymbolRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
