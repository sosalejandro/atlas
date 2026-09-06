package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// answerError is a failure of the QUESTION rather than of the call: no such
// feature, no such symbol. It becomes a CallToolResult with isError, per the
// MCP tools spec's split between protocol errors and tool execution errors,
// so the model reads the message and asks a different question instead of
// retrying the same one against a -32602 it cannot interpret.
//
// Anything else a handler returns — a database failure, a corrupt row — is an
// internal error and surfaces as -32603, because it is not something the agent
// can fix by rephrasing.
type answerError struct{ msg string }

func (e answerError) Error() string { return e.msg }

func noSuch(format string, a ...any) error { return answerError{msg: fmt.Sprintf(format, a...)} }

func noFeaturesAnnotated() *NoData {
	return &NoData{
		Reason: ReasonNoFeatures,
		Detail: "the repository is indexed but no symbol carries an @atlas:feature annotation, " +
			"so atlas knows the code and nothing about which capability it serves",
		Run: "annotate an entry point with @atlas:feature <id>, then run atlas scan",
	}
}

// ReasonNoFeatures is its own slug because it is its own gap: the scan ran and
// found code, but nobody has said what any of it is for. Reporting that as
// "index-empty" would send the agent to re-run a scan that will change nothing.
const ReasonNoFeatures = "no-features-annotated"

// ---------------------------------------------------------------------------
// find_feature
// ---------------------------------------------------------------------------

func (ts *toolset) findFeature(ctx context.Context, a *toolArgs) (any, error) {
	query, err := a.requiredString("query")
	if err != nil {
		return nil, err
	}
	limit, err := a.optionalLimit(ts.limits.MaxFeatures)
	if err != nil {
		return nil, err
	}
	features, err := ts.graph.ListFeatures(ctx)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return ts.emptyFeatureAnswer(ctx)
	}

	matches, truncated := bound(matchFeatures(features, query), limit, "features")
	for i := range matches {
		links, err := ts.graph.FeatureLinks(ctx, shared.FeatureID(matches[i].ID))
		if err != nil {
			return nil, err
		}
		matches[i].LinkedSymbols = len(links)
	}
	return findFeatureResult{Query: query, Features: matches, Truncated: truncated}, nil
}

// emptyFeatureAnswer separates "nothing is indexed" from "everything is
// indexed and none of it is annotated". They look identical from here and are
// repaired by entirely different actions.
func (ts *toolset) emptyFeatureAnswer(ctx context.Context) (any, error) {
	table, err := loadSymbolTable(ctx, ts.graph)
	if err != nil {
		return nil, err
	}
	if table.empty() {
		return noData("find_feature", noIndex()), nil
	}
	return noData("find_feature", noFeaturesAnnotated()), nil
}

// matchFeatures does a case-insensitive substring match over id and title.
//
// Substring rather than fuzzy: an agent that asked for "checkout" and got
// "chat" back learns to distrust the tool, and there is no ranking signal here
// good enough to justify guessing.
func matchFeatures(features []store.Feature, query string) []featureMatch {
	needle := strings.ToLower(strings.TrimSpace(query))
	var out []featureMatch
	for _, f := range features {
		inID := strings.Contains(strings.ToLower(string(f.ID)), needle)
		inTitle := strings.Contains(strings.ToLower(f.Title), needle)
		if !inID && !inTitle {
			continue
		}
		out = append(out, featureMatch{
			ID:        string(f.ID),
			Title:     f.Title,
			Kind:      string(f.Kind),
			Owner:     f.Owner,
			MatchedOn: matchedOn(inID, inTitle),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []featureMatch{}
	}
	return out
}

func matchedOn(inID, inTitle bool) string {
	switch {
	case inID && inTitle:
		return "id+title"
	case inID:
		return "id"
	default:
		return "title"
	}
}

// ---------------------------------------------------------------------------
// feature_surface
// ---------------------------------------------------------------------------

func (ts *toolset) featureSurface(ctx context.Context, a *toolArgs) (any, error) {
	id, err := a.requiredString("feature_id")
	if err != nil {
		return nil, err
	}
	limit, err := a.optionalLimit(ts.limits.MaxSymbols)
	if err != nil {
		return nil, err
	}
	feature, err := ts.mustFeature(ctx, id)
	if err != nil {
		return nil, err
	}
	links, err := ts.graph.FeatureLinks(ctx, feature.ID)
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return noData("feature_surface", noFeatureLinks(id)), nil
	}

	table, err := loadSymbolTable(ctx, ts.graph)
	if err != nil {
		return nil, err
	}
	derived, err := deriveSurface(ctx, ts.graph, ts.coverage, table, links)
	if err != nil {
		return nil, err
	}

	symbols, truncated := bound(surfaceSymbols(table, derived), limit, "symbols")
	return featureSurfaceResult{
		FeatureID:         id,
		Title:             feature.Title,
		SurfaceSource:     derived.Source,
		SurfaceSourceNote: derived.Note,
		Symbols:           symbols,
		Truncated:         truncated,
		IndexFreshness:    checkFreshness(ctx, ts.freshness, filesOfSurface(symbols)),
	}, nil
}

func surfaceSymbols(table *symbolTable, derived surface) []surfaceSymbol {
	out := make([]surfaceSymbol, 0, len(derived.IDs))
	for _, id := range derived.IDs {
		row, ok := table.byID[id]
		if !ok {
			continue // a link to a symbol a later scan removed; not worth a fabricated row
		}
		out = append(out, surfaceSymbol{symbolRef: refOf(row), Role: string(derived.Roles[id])})
	}
	return out
}

func filesOfSurface(symbols []surfaceSymbol) map[string]bool {
	out := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		out[s.File] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// symbol_info
// ---------------------------------------------------------------------------

// symbolInfoNotes states what the index does NOT carry. Without it an agent
// reads the absence of a doc field as "this symbol has no doc comment" and
// stops looking, when the truth is that atlas stores spans, not source text.
var symbolInfoNotes = []string{
	"atlas indexes declarations, not source text: doc comments and signatures are not stored. " +
		"Read the declaration at the file:line above for either.",
	"caller_count and callee_count count DISTINCT symbols, not call sites: a function that calls this one " +
		"five times counts once, so both numbers are usually smaller than the row count of `callers`/`callees`. " +
		"They cover statically resolved `call` edges only — calls through interfaces, DI containers or " +
		"reflection are counted by neither.",
}

func (ts *toolset) symbolInfo(ctx context.Context, a *toolArgs) (any, error) {
	name, err := a.requiredString("qualified_name")
	if err != nil {
		return nil, err
	}
	_, row, err := ts.mustSymbol(ctx, name)
	if err != nil {
		return nil, err
	}
	links, err := ts.graph.SymbolLinks(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	owners, err := ts.featureLinks(ctx, links)
	if err != nil {
		return nil, err
	}
	in, err := ts.graph.EdgesIn(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	out, err := ts.graph.EdgesOut(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return symbolInfoResult{
		symbolRef:      refOf(row),
		BCPath:         row.BCPath,
		Features:       owners,
		CallerCount:    countDistinctNeighbours(in, inbound),
		CalleeCount:    countDistinctNeighbours(out, outbound),
		Notes:          symbolInfoNotes,
		IndexFreshness: checkFreshness(ctx, ts.freshness, map[string]bool{row.FilePath: true}),
	}, nil
}

func (ts *toolset) featureLinks(ctx context.Context, links []store.FeatureSymbolLink) ([]featureLink, error) {
	out := make([]featureLink, 0, len(links))
	for _, l := range links {
		title := ""
		if f, err := ts.graph.GetFeature(ctx, l.FeatureID); err == nil {
			title = f.Title
		}
		out = append(out, featureLink{
			FeatureID: string(l.FeatureID), Title: title,
			Role: string(l.Role), Source: string(l.Source),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FeatureID != out[j].FeatureID {
			return out[i].FeatureID < out[j].FeatureID
		}
		return out[i].Role < out[j].Role
	})
	return out, nil
}

// countDistinctNeighbours counts the distinct symbols at the far end of a
// symbol's `call` edges — NOT the edges themselves.
//
// The edges table is keyed on (from, to, kind, file, line), so it holds one row
// per call SITE. A helper invoked five times from one function is five rows,
// and reporting that as "5 callers" answers "is this safe to change" with a
// blast radius five times too large. The callers/callees tools deliberately
// return sites, because an agent citing a call needs its file:line; these two
// fields answer the other question and must count what they are named for.
func countDistinctNeighbours(edges []store.EdgeRow, dir direction) int {
	seen := make(map[int64]bool, len(edges))
	for _, e := range edges {
		if e.Kind != store.EdgeKindCall {
			continue
		}
		if dir == inbound {
			seen[e.FromID] = true
			continue
		}
		seen[e.ToID] = true
	}
	return len(seen)
}

// ---------------------------------------------------------------------------
// callers / callees
// ---------------------------------------------------------------------------

func (ts *toolset) callers(ctx context.Context, a *toolArgs) (any, error) {
	name, rows, truncated, err := ts.neighbours(ctx, a, inbound)
	if err != nil {
		return nil, err
	}
	return callersResult{
		QualifiedName:  name,
		Callers:        rows,
		Truncated:      truncated,
		IndexFreshness: checkFreshness(ctx, ts.freshness, filesOfNeighbours(rows)),
	}, nil
}

func (ts *toolset) callees(ctx context.Context, a *toolArgs) (any, error) {
	name, rows, truncated, err := ts.neighbours(ctx, a, outbound)
	if err != nil {
		return nil, err
	}
	return calleesResult{
		QualifiedName:  name,
		Callees:        rows,
		Truncated:      truncated,
		IndexFreshness: checkFreshness(ctx, ts.freshness, filesOfNeighbours(rows)),
	}, nil
}

type direction int

const (
	inbound direction = iota
	outbound
)

// neighbours is the shared body of callers and callees. The two differ only in
// which end of the edge is the answer, and keeping them one function is what
// guarantees the bound and the truncation notice behave identically in both.
func (ts *toolset) neighbours(
	ctx context.Context, a *toolArgs, dir direction,
) (string, []neighbour, *Truncation, error) {
	name, err := a.requiredString("qualified_name")
	if err != nil {
		return "", nil, nil, err
	}
	limit, err := a.optionalLimit(ts.limits.MaxEdges)
	if err != nil {
		return "", nil, nil, err
	}
	table, row, err := ts.mustSymbol(ctx, name)
	if err != nil {
		return "", nil, nil, err
	}

	var edges []store.EdgeRow
	if dir == inbound {
		edges, err = ts.graph.EdgesIn(ctx, row.ID)
	} else {
		edges, err = ts.graph.EdgesOut(ctx, row.ID)
	}
	if err != nil {
		return "", nil, nil, err
	}

	rows, truncated := bound(toNeighbours(table, edges, dir), limit, "call edges")
	return name, rows, truncated, nil
}

func toNeighbours(table *symbolTable, edges []store.EdgeRow, dir direction) []neighbour {
	out := make([]neighbour, 0, len(edges))
	for _, e := range edges {
		if e.Kind != store.EdgeKindCall {
			continue
		}
		other := e.FromID
		if dir == outbound {
			other = e.ToID
		}
		ref := symbolRef{QualifiedName: string(table.name(other))}
		if row, ok := table.byID[other]; ok {
			ref = refOf(row)
		}
		out = append(out, neighbour{
			symbolRef: ref,
			EdgeKind:  string(e.Kind),
			CallSite:  callSite{File: e.FilePath, Line: e.Line},
		})
	}
	// Deterministic order: the same question must produce the same answer, or
	// an agent comparing two calls sees a change that is not there.
	sort.Slice(out, func(i, j int) bool {
		if out[i].QualifiedName != out[j].QualifiedName {
			return out[i].QualifiedName < out[j].QualifiedName
		}
		return out[i].CallSite.Line < out[j].CallSite.Line
	})
	return out
}

func filesOfNeighbours(rows []neighbour) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.File != "" {
			out[r.File] = true
		}
		out[r.CallSite.File] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// tests_covering
// ---------------------------------------------------------------------------

func (ts *toolset) testsCovering(ctx context.Context, a *toolArgs) (any, error) {
	name, err := a.requiredString("qualified_name")
	if err != nil {
		return nil, err
	}
	limit, err := a.optionalLimit(ts.limits.MaxTests)
	if err != nil {
		return nil, err
	}
	table, row, err := ts.mustSymbol(ctx, name)
	if err != nil {
		return nil, err
	}
	frontier, err := ts.coverage.LatestFrontier(ctx)
	if err != nil {
		return nil, err
	}
	if frontier.Empty() {
		return noData("tests_covering", noCoverage()), nil
	}

	rows, evidence, err := ts.collectCoveringTests(ctx, table, frontier, row.ID)
	if err != nil {
		return nil, err
	}
	// No per-test rows anywhere on the frontier is a different answer from
	// "no test ran this symbol", and only one of the two is fixed by a
	// different ingest.
	if !evidence {
		return noData("tests_covering", noPerTestEvidence()), nil
	}

	tests, truncated := bound(rows, limit, "tests")
	return testsCoveringResult{
		QualifiedName:  name,
		Tests:          tests,
		Frontier:       describeFrontier(frontier),
		Truncated:      truncated,
		IndexFreshness: checkFreshness(ctx, ts.freshness, filesOfCoveringTests(tests)),
	}, nil
}

// filesOfCoveringTests is the set of files this answer sends the agent to. A
// test symbol whose id has no row in the symbol table has no file to check;
// omitting it is what keeps the empty string out of the freshness report.
func filesOfCoveringTests(tests []coveringTest) map[string]bool {
	out := make(map[string]bool, len(tests))
	for _, t := range tests {
		if t.File != "" {
			out[t.File] = true
		}
	}
	return out
}

// collectCoveringTests returns the tests that executed symbolID plus whether
// the frontier carried per-test evidence at all.
func (ts *toolset) collectCoveringTests(
	ctx context.Context, table *symbolTable, frontier store.CoverageFrontier, symbolID int64,
) ([]coveringTest, bool, error) {
	frameworks := map[int64]string{}
	for _, r := range frontier.Runs {
		frameworks[r.ID] = string(r.Framework)
	}

	var out []coveringTest
	evidence := false
	for _, runID := range frontier.RunIDs() {
		count, err := ts.coverage.CountTests(ctx, runID)
		if err != nil {
			return nil, false, err
		}
		if count == 0 {
			continue
		}
		evidence = true
		rows, err := ts.coverage.TestsExecuting(ctx, runID, symbolID)
		if err != nil {
			return nil, false, err
		}
		for _, r := range rows {
			ref := symbolRef{QualifiedName: string(table.name(r.TestSymbolID))}
			if row, ok := table.byID[r.TestSymbolID]; ok {
				ref = refOf(row)
			}
			out = append(out, coveringTest{
				symbolRef: ref, RunID: runID, Framework: frameworks[runID],
				CoveredStmts: r.CoveredStmts, TotalStmts: r.TotalStmts,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].QualifiedName != out[j].QualifiedName {
			return out[i].QualifiedName < out[j].QualifiedName
		}
		return out[i].RunID < out[j].RunID
	})
	if out == nil {
		out = []coveringTest{}
	}
	return out, evidence, nil
}

// ---------------------------------------------------------------------------
// coverage_for
// ---------------------------------------------------------------------------

// coverageForNotes explain what this score is NOT, so an agent does not treat
// a re-normalised blend as a line-coverage percentage.
var coverageForNotes = []string{
	"`score` is the audit's weighted blend of the signals that were available, re-normalised over them — " +
		"not a line-coverage percentage. `components.coverage` is the coverage signal on its own.",
	"The annotation_freshness signal is unavailable over MCP: it shells out to `git blame` per annotation site, " +
		"which is too slow to run inside a request. Run `atlas audit --feature <id>` for a score that includes it.",
}

func (ts *toolset) coverageFor(ctx context.Context, a *toolArgs) (any, error) {
	id, err := a.requiredString("feature_id")
	if err != nil {
		return nil, err
	}
	feature, err := ts.mustFeature(ctx, id)
	if err != nil {
		return nil, err
	}
	frontier, err := ts.coverage.LatestFrontier(ctx)
	if err != nil {
		return nil, err
	}
	if frontier.Empty() {
		return noData("coverage_for", noCoverage()), nil
	}
	health, err := ts.scorer.ScoreFeature(ctx, feature.ID)
	if err != nil {
		return nil, err
	}
	return coverageForResult{
		FeatureID:         id,
		Title:             feature.Title,
		Score:             health.Score,
		Components:        health.Components,
		Reasons:           health.Reasons,
		SurfaceSource:     health.SurfaceSource,
		SurfaceSourceNote: surfaceNotes[health.SurfaceSource],
		SampledAt:         health.SampledAt.UTC().Format(time.RFC3339),
		Frontier:          describeFrontier(frontier),
		Notes:             coverageForNotes,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared lookups
// ---------------------------------------------------------------------------

// mustFeature resolves a feature id, turning "not found" into an answer error
// that names what the agent asked for.
func (ts *toolset) mustFeature(ctx context.Context, id string) (store.Feature, error) {
	feature, err := ts.graph.GetFeature(ctx, shared.FeatureID(id))
	if errors.Is(err, shared.ErrFeatureNotFound) {
		return store.Feature{}, noSuch(
			"no feature %q is indexed. Use find_feature to search by name, or check the @atlas:feature "+
				"annotation and re-run `atlas scan`.", id)
	}
	if err != nil {
		return store.Feature{}, err
	}
	return feature, nil
}

// mustSymbol resolves a qualified name against the symbol table, and returns
// the table too because every caller then needs it to name the ids on the
// other end of an edge or a coverage row.
func (ts *toolset) mustSymbol(ctx context.Context, name string) (*symbolTable, store.SymbolRow, error) {
	table, err := loadSymbolTable(ctx, ts.graph)
	if err != nil {
		return nil, store.SymbolRow{}, err
	}
	if table.empty() {
		return nil, store.SymbolRow{}, noSuch(
			"the atlas store holds no symbols: this repository has not been scanned yet. Run `atlas init` " +
				"(first scan) or `atlas scan` (incremental). An empty index says nothing about the code.")
	}
	row, ok := table.byName[shared.SymbolID(name)]
	if !ok {
		return nil, store.SymbolRow{}, noSuch(
			"no symbol %q is indexed. Qualified names are exactly as the scanner recorded them "+
				"(usually <import path or module>.<Name>); check the spelling, or re-run `atlas scan` if the "+
				"symbol is newer than the index.", name)
	}
	return table, row, nil
}
