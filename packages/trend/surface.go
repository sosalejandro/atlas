package trend

import (
	"context"
	"fmt"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// statementTally is a statement count summed over some set of symbols.
type statementTally struct {
	covered int
	total   int
}

// measured reports whether the tally rests on statement evidence at all. A
// zero total is "this framework carries no statement counts", not "nothing is
// covered" — the two must never be conflated, which is why the fraction is
// only ever taken behind this predicate.
func (t statementTally) measured() bool { return t.total > 0 }

// surfaceSizer answers "how big is the surface this feature's coverage score
// was computed over, in the unit the score is expressed in".
//
// It exists because the denominator recorded on a history point is a
// measurement guard, and a guard in the wrong unit is no guard: the Tier B
// coverage score is a fraction of STATEMENTS, so a denominator counted in
// symbols cannot see a statement-level deletion at all.
//
// The frontier results are read once and indexed by symbol id, because the
// audit already pays a per-feature query on this path and a second one per
// feature would multiply out across a large repo.
type surfaceSizer struct {
	store *store.Store
	stmts map[int64]statementTally
}

// newSurfaceSizer reads the current coverage frontier once. An empty frontier
// is not an error: every feature then falls back to its scored-symbol count,
// which is the unit the pass/fail coverage model scores in.
func newSurfaceSizer(ctx context.Context, s *store.Store) (*surfaceSizer, error) {
	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		return nil, fmt.Errorf("trend: read coverage frontier: %w", err)
	}
	if frontier.Empty() {
		return &surfaceSizer{store: s, stmts: map[int64]statementTally{}}, nil
	}
	results, err := s.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		return nil, fmt.Errorf("trend: read coverage frontier results: %w", err)
	}
	return &surfaceSizer{store: s, stmts: indexStatements(results)}, nil
}

// forFeature returns the feature's denominator: its statement total when the
// frontier carries statement counts for any of its scored symbols, and
// otherwise the number of scored symbols.
func (z *surfaceSizer) forFeature(ctx context.Context, id shared.FeatureID) (int64, error) {
	wanted, err := scoredSymbolIDs(ctx, z.store, id)
	if err != nil {
		return 0, err
	}
	tally := sumStatements(z.stmts, wanted)
	if tally.measured() {
		return int64(tally.total), nil
	}
	return int64(len(wanted)), nil
}

// scoredSymbolIDs is the set of a feature's linked symbols that coverage is
// scored OVER. Test-role links are excluded: they are the tests themselves,
// not what the tests cover, and counting them inflated the old denominator
// with rows no coverage fraction ever had in its denominator. This mirrors
// the audit's own wantedSymbolIDs, so the recorded denominator and the
// recorded score describe the same surface.
func scoredSymbolIDs(ctx context.Context, s *store.Store, id shared.FeatureID) (map[int64]bool, error) {
	links, err := s.FeatureSymbols().ListByFeature(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("trend: feature %s surface: %w", id, err)
	}
	out := make(map[int64]bool, len(links))
	for _, l := range links {
		if l.Role == store.RoleTest {
			continue
		}
		out[l.SymbolID] = true
	}
	return out, nil
}

// indexStatements sums the per-symbol statement counts across a pool of
// coverage results. One symbol legitimately has several rows — several blocks
// in one file, several runs in one frontier group — and the audit sums them
// the same way, so the denominator matches the score's own accounting.
func indexStatements(results []store.CoverageResult) map[int64]statementTally {
	out := make(map[int64]statementTally, len(results))
	for _, r := range results {
		if r.SymbolID == nil || r.TotalStmts <= 0 {
			continue
		}
		t := out[*r.SymbolID]
		t.covered += r.CoveredStmts
		t.total += r.TotalStmts
		out[*r.SymbolID] = t
	}
	return out
}

// sumStatements totals the statement counts for one set of symbols.
func sumStatements(index map[int64]statementTally, wanted map[int64]bool) statementTally {
	var out statementTally
	for sid := range wanted {
		t, ok := index[sid]
		if !ok {
			continue
		}
		out.covered += t.covered
		out.total += t.total
	}
	return out
}
