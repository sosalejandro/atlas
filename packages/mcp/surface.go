package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/store"
)

// The dynamic tier's shared-runtime filter. These mirror the audit package's
// own defaults (see audit.Options.UbiquityCutoff): a symbol executed by more
// than half the suite is the logger, the DI container or the middleware chain,
// and belongs to no feature — but in a five-test suite "run by more than half"
// describes a domain service two features legitimately share, so the ratio is
// only applied once the suite is big enough to carry the signal.
//
// They are re-declared rather than imported because the audit package does not
// export them. If audit's defaults move, this comment is the trail; the
// surface_source string itself comes from audit's exported constants so the
// vocabulary at least cannot drift.
const (
	ubiquityCutoff       = 0.5
	minTestsForUbiquity  = 8
	implSurfaceMaxDepth  = 3
	maxPackageAnchorSyms = 200
)

// surface is a feature's implementation footprint plus the provenance of the
// derivation that produced it.
//
// The provenance is not decoration. The four tiers differ by an order of
// magnitude in how much an agent should trust them — dynamic is execution
// evidence, direct-links is whatever a human typed in an annotation — and a
// coverage number whose derivation is invisible is how issue #84 survived
// three releases. Any consumer that drops SurfaceSource has thrown away the
// only thing that says how far to believe Symbols.
type surface struct {
	Source string
	Note   string
	IDs    []int64
	Roles  map[int64]store.FeatureSymbolRole
}

// surfaceNotes explains each tier to the model that has to weigh it.
var surfaceNotes = map[string]string{
	audit.SurfaceDynamic: "Execution evidence: the union of what this feature's own tests actually ran, " +
		"minus symbols nearly the whole suite runs. The only tier that sees through interface dispatch, " +
		"DI containers and reflection. Trust it.",
	audit.SurfaceStatic: fmt.Sprintf(
		"Static call-edge walk (depth %d) from the annotated symbols. Reaches only what the scanner resolved, "+
			"so anything called through an interface or a DI container is missing. Treat as a lower bound.",
		implSurfaceMaxDepth),
	audit.SurfacePackageAnchor: "Package co-location: the annotated symbols are test stubs with no outgoing call edges, " +
		"so this is every production symbol in their package. Whole-package granularity — it may include code this " +
		"feature has nothing to do with.",
	audit.SurfaceDirectLinks: "Only the symbols a human annotated. No execution evidence and no call graph behind it; " +
		"the real implementation is almost certainly larger than this list.",
}

// deriveSurface picks a feature's implementation set in descending order of
// evidential strength, mirroring the audit's own tiering
// (audit.resolveWantedSet) so `atlas audit` and this tool describe the same
// surface for the same feature. The tiering is duplicated rather than shared
// because audit's is unexported; the shared vocabulary is the exported
// audit.Surface* constants.
func deriveSurface(
	ctx context.Context,
	g GraphIndex,
	c CoverageIndex,
	tab *symbolTable,
	links []store.FeatureSymbolLink,
) (surface, error) {
	roles := rolesBySymbol(links)

	dynamic, err := dynamicSurface(ctx, c, links)
	if err != nil {
		return surface{}, err
	}
	if len(dynamic) > 0 {
		return finish(audit.SurfaceDynamic, dynamic, roles), nil
	}

	static, err := staticSurface(ctx, g, tab, links)
	if err != nil {
		return surface{}, err
	}
	if len(static) > 0 {
		return finish(audit.SurfaceStatic, static, roles), nil
	}

	if anchored := packageAnchorSurface(tab, links); len(anchored) > 0 {
		return finish(audit.SurfacePackageAnchor, anchored, roles), nil
	}

	direct := make(map[int64]bool, len(links))
	for _, l := range links {
		direct[l.SymbolID] = true
	}
	return finish(audit.SurfaceDirectLinks, direct, roles), nil
}

// finish turns a derived id set into a deterministically ordered surface.
// Ordering by qualified name would need the table; ordering by id is enough
// and is stable across calls, which is what matters when an agent diffs two
// answers.
func finish(source string, ids map[int64]bool, roles map[int64]store.FeatureSymbolRole) surface {
	out := make([]int64, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return surface{Source: source, Note: surfaceNotes[source], IDs: out, Roles: roles}
}

func rolesBySymbol(links []store.FeatureSymbolLink) map[int64]store.FeatureSymbolRole {
	out := make(map[int64]store.FeatureSymbolRole, len(links))
	for _, l := range links {
		out[l.SymbolID] = l.Role
	}
	return out
}

func testSymbolIDs(links []store.FeatureSymbolLink) map[int64]bool {
	out := map[int64]bool{}
	for _, l := range links {
		if l.Role == store.RoleTest {
			out[l.SymbolID] = true
		}
	}
	return out
}

// dynamicSurface is the union, across the frontier's runs, of the symbols this
// feature's own tests executed, minus each run's shared runtime.
//
// Ubiquity is a property of a SUITE, so it is applied per run and the results
// unioned: pooling a Go suite and a Playwright suite into one test count would
// dilute both cutoffs by whichever happened to be larger.
func dynamicSurface(ctx context.Context, c CoverageIndex, links []store.FeatureSymbolLink) (map[int64]bool, error) {
	tests := testSymbolIDs(links)
	if len(tests) == 0 {
		return nil, nil
	}
	frontier, err := c.LatestFrontier(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: read coverage frontier: %w", err)
	}
	out := map[int64]bool{}
	for _, runID := range frontier.RunIDs() {
		perRun, err := dynamicSurfaceForRun(ctx, c, tests, runID)
		if err != nil {
			return nil, err
		}
		for id := range perRun {
			out[id] = true
		}
	}
	return out, nil
}

func dynamicSurfaceForRun(ctx context.Context, c CoverageIndex, tests map[int64]bool, runID int64) (map[int64]bool, error) {
	totalTests, err := c.CountTests(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("mcp: count tests in run %d: %w", runID, err)
	}
	if totalTests == 0 {
		return nil, nil // this run carries no per-test evidence at all
	}
	fanIn, err := c.FanIn(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("mcp: test fan-in for run %d: %w", runID, err)
	}
	maxFanIn := totalTests
	if totalTests >= minTestsForUbiquity {
		if maxFanIn = int(float64(totalTests) * ubiquityCutoff); maxFanIn < 1 {
			maxFanIn = 1
		}
	}

	out := map[int64]bool{}
	for testID := range tests {
		rows, err := c.SymbolsExecutedBy(ctx, runID, testID)
		if err != nil {
			return nil, fmt.Errorf("mcp: symbols executed by test %d: %w", testID, err)
		}
		for _, r := range rows {
			if fanIn[r.SymbolID] > maxFanIn {
				continue // shared runtime, not this feature's implementation
			}
			out[r.SymbolID] = true
		}
	}
	return out, nil
}

// staticSurface walks `call` edges from the annotated symbols and keeps what
// lands in production files.
//
// The walk is done edge-by-edge from a bounded frontier rather than by loading
// the whole adjacency map (which the audit does, because it walks every
// feature). One feature's depth-3 neighbourhood is small; a monorepo's edge
// table is 149k rows, and paying for all of it inside a request an agent is
// blocking on would be the wrong trade.
func staticSurface(ctx context.Context, g GraphIndex, tab *symbolTable, links []store.FeatureSymbolLink) (map[int64]bool, error) {
	visited := make(map[int64]bool, len(links))
	frontier := make([]int64, 0, len(links))
	for _, l := range links {
		if !visited[l.SymbolID] {
			visited[l.SymbolID] = true
			frontier = append(frontier, l.SymbolID)
		}
	}
	for depth := 0; depth < implSurfaceMaxDepth && len(frontier) > 0; depth++ {
		var next []int64
		for _, id := range frontier {
			edges, err := g.EdgesOut(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("mcp: outgoing edges of symbol %d: %w", id, err)
			}
			for _, e := range edges {
				if e.Kind != store.EdgeKindCall || visited[e.ToID] {
					continue
				}
				visited[e.ToID] = true
				next = append(next, e.ToID)
			}
		}
		frontier = next
	}

	out := map[int64]bool{}
	for id := range visited {
		if row, ok := tab.byID[id]; ok && isProductionFile(row.FilePath) {
			out[id] = true
		}
	}
	return out, nil
}

// packageAnchorSurface is the last-resort tier: when the annotated symbols are
// test stubs with no outgoing call edges, attribute the production symbols of
// their package instead.
//
// Gated on package size for the reason issue #84 documented: a shared
// infrastructure package with 896 symbols would hand the agent most of the
// repo as one feature's implementation, which is worse than admitting the
// annotation is uninformative.
func packageAnchorSurface(tab *symbolTable, links []store.FeatureSymbolLink) map[int64]bool {
	pkgs := map[string]bool{}
	for _, l := range links {
		row, ok := tab.byID[l.SymbolID]
		if !ok || row.Package == nil || *row.Package == "" {
			continue
		}
		if isProductionFile(row.FilePath) {
			return nil // a production symbol is linked; the weaker tier is not needed
		}
		pkgs[*row.Package] = true
	}
	if len(pkgs) == 0 {
		return nil
	}

	byPkg := map[string][]int64{}
	for _, row := range tab.rows {
		if row.Package == nil || !pkgs[*row.Package] || !isProductionFile(row.FilePath) {
			continue
		}
		byPkg[*row.Package] = append(byPkg[*row.Package], row.ID)
	}
	out := map[int64]bool{}
	for _, ids := range byPkg {
		if len(ids) > maxPackageAnchorSyms {
			continue
		}
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

// isProductionFile reports whether a path is non-test source. Mirrors the
// audit's predicate of the same name; the two must agree or a feature's
// surface and its coverage score describe different sets of files.
func isProductionFile(p string) bool {
	if p == "" || strings.Contains(p, "/__tests__/") {
		return false
	}
	for _, suffix := range []string{
		"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx", ".test.js", ".spec.js",
	} {
		if strings.HasSuffix(p, suffix) {
			return false
		}
	}
	return true
}
