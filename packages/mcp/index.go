package mcp

import (
	"context"
	"fmt"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// GraphIndex is the read side of the feature/symbol graph.
//
// It exists so this package cannot write. *store.Store hands out ports with
// Upsert / Link / Insert / DeleteByFile on them; a tool holding one of those
// is one hallucinated call away from corrupting the index that every other
// atlas answer is derived from. Declaring only the reads makes that
// unreachable at the type level instead of by review.
type GraphIndex interface {
	ListFeatures(ctx context.Context) ([]store.Feature, error)
	GetFeature(ctx context.Context, id shared.FeatureID) (store.Feature, error)
	FeatureLinks(ctx context.Context, id shared.FeatureID) ([]store.FeatureSymbolLink, error)
	SymbolLinks(ctx context.Context, symbolID int64) ([]store.FeatureSymbolLink, error)
	// ListSymbols returns the whole symbol table. The store exposes no
	// per-surrogate-id lookup, and every tool here has to turn edge and
	// coverage rows (which carry ids) into names and spans, so the table is
	// read once per call and indexed in memory — the same trade `atlas trace`,
	// `atlas report` and the audit already make.
	ListSymbols(ctx context.Context) ([]store.SymbolRow, error)
	EdgesOut(ctx context.Context, symbolID int64) ([]store.EdgeRow, error)
	EdgesIn(ctx context.Context, symbolID int64) ([]store.EdgeRow, error)
}

// CoverageIndex is the read side of the coverage tables.
type CoverageIndex interface {
	LatestFrontier(ctx context.Context) (store.CoverageFrontier, error)
	TestsExecuting(ctx context.Context, runID, symbolID int64) ([]store.TestExecution, error)
	SymbolsExecutedBy(ctx context.Context, runID, testSymbolID int64) ([]store.TestExecution, error)
	CountTests(ctx context.Context, runID int64) (int, error)
	FanIn(ctx context.Context, runID int64) (map[int64]int, error)
}

// Scorer is the audit's per-feature read. coverage_for delegates to it rather
// than recomputing a score, so `atlas audit --feature X` and the MCP tool can
// never disagree about the same feature on the same frontier.
type Scorer interface {
	ScoreFeature(ctx context.Context, id shared.FeatureID) (audit.FeatureHealth, error)
}

// FreshnessFunc classifies working-tree files against the hashes the scanner
// recorded, so a result can say when its spans predate the file on disk.
//
// It is a function rather than a port because indexfresh.Classify takes the
// writable store.FileHashes port, and threading that through here would
// re-open the write path this package exists to keep shut. The CLI closes over
// the store and the repo root; nil disables freshness reporting, which is what
// tests use.
type FreshnessFunc func(ctx context.Context, paths []string) (map[string]string, error)

// StoreIndex adapts *store.Store to the read-only interfaces above.
//
// The adapter is thin on purpose: it renames nothing and reshapes nothing, so
// a reader comparing an MCP answer to a `--json` CLI answer is looking at the
// same rows.
type StoreIndex struct {
	s *store.Store
}

var (
	_ GraphIndex    = (*StoreIndex)(nil)
	_ CoverageIndex = (*StoreIndex)(nil)
)

// FromStore returns the read-only view of an open store.
func FromStore(s *store.Store) *StoreIndex { return &StoreIndex{s: s} }

// Scorer returns the audit scorer for this store.
//
// GitBlame is deliberately left nil: it shells out to `git blame` per
// annotation site, which is fine for a one-shot CLI invocation and not fine
// inside a request an agent is blocking on. The annotation-freshness signal
// drops out of the score as a result, and the audit re-normalises over the
// signals that remain — see docs/commands/mcp.md.
func (x *StoreIndex) Scorer() Scorer {
	return audit.New(x.s, audit.Options{})
}

func (x *StoreIndex) ListFeatures(ctx context.Context) ([]store.Feature, error) {
	return x.s.Features().List(ctx, store.FeatureFilter{})
}

func (x *StoreIndex) GetFeature(ctx context.Context, id shared.FeatureID) (store.Feature, error) {
	return x.s.Features().Get(ctx, id)
}

func (x *StoreIndex) FeatureLinks(ctx context.Context, id shared.FeatureID) ([]store.FeatureSymbolLink, error) {
	return x.s.FeatureSymbols().ListByFeature(ctx, id)
}

func (x *StoreIndex) SymbolLinks(ctx context.Context, symbolID int64) ([]store.FeatureSymbolLink, error) {
	return x.s.FeatureSymbols().ListBySymbol(ctx, symbolID)
}

func (x *StoreIndex) ListSymbols(ctx context.Context) ([]store.SymbolRow, error) {
	return x.s.Symbols().List(ctx, store.SymbolFilter{})
}

func (x *StoreIndex) EdgesOut(ctx context.Context, symbolID int64) ([]store.EdgeRow, error) {
	return x.s.Edges().Out(ctx, symbolID)
}

func (x *StoreIndex) EdgesIn(ctx context.Context, symbolID int64) ([]store.EdgeRow, error) {
	return x.s.Edges().In(ctx, symbolID)
}

func (x *StoreIndex) LatestFrontier(ctx context.Context) (store.CoverageFrontier, error) {
	return x.s.Coverage().LatestFrontier(ctx)
}

func (x *StoreIndex) TestsExecuting(ctx context.Context, runID, symbolID int64) ([]store.TestExecution, error) {
	return x.s.TestCoverage().TestsExecuting(ctx, runID, symbolID)
}

func (x *StoreIndex) SymbolsExecutedBy(ctx context.Context, runID, testSymbolID int64) ([]store.TestExecution, error) {
	return x.s.TestCoverage().SymbolsExecutedBy(ctx, runID, testSymbolID)
}

func (x *StoreIndex) CountTests(ctx context.Context, runID int64) (int, error) {
	return x.s.TestCoverage().CountTests(ctx, runID)
}

func (x *StoreIndex) FanIn(ctx context.Context, runID int64) (map[int64]int, error) {
	return x.s.TestCoverage().FanIn(ctx, runID)
}

// symbolTable is the symbol table indexed both ways, materialised once per
// tools/call.
//
// It is NOT cached across calls. The store is a re-derivable cache that
// another process (`atlas scan`) rewrites in place, and an agent that gets a
// span from a table loaded before that scan is handed a line number that no
// longer points at the symbol. Paying one table read per call is cheap next to
// citing a stale location confidently.
type symbolTable struct {
	byID   map[int64]store.SymbolRow
	byName map[shared.SymbolID]store.SymbolRow
	rows   []store.SymbolRow
}

func loadSymbolTable(ctx context.Context, g GraphIndex) (*symbolTable, error) {
	rows, err := g.ListSymbols(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: list symbols: %w", err)
	}
	t := &symbolTable{
		byID:   make(map[int64]store.SymbolRow, len(rows)),
		byName: make(map[shared.SymbolID]store.SymbolRow, len(rows)),
		rows:   rows,
	}
	for _, r := range rows {
		t.byID[r.ID] = r
		t.byName[r.QualifiedName] = r
	}
	return t, nil
}

func (t *symbolTable) empty() bool { return len(t.rows) == 0 }

// name resolves a surrogate id to a qualified name, falling back to a marker
// rather than an empty string. An edge whose endpoint is missing from the
// symbol table is a real (if rare) state — a partial re-scan — and blanking it
// would present the agent with an unnamed node it cannot ask about.
func (t *symbolTable) name(id int64) shared.SymbolID {
	if row, ok := t.byID[id]; ok {
		return row.QualifiedName
	}
	return shared.SymbolID(fmt.Sprintf("symbol#%d", id))
}
