package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/sosalejandro/grunnr/packages/graph"
	"github.com/sosalejandro/grunnr/packages/store"
)

// noDataResult is the ENTIRE result when grunnr cannot answer.
//
// It is a distinct type rather than a field on the normal result so the empty
// list is not merely nil but absent from the wire. A model handed
// `{"features": [], "no_data": {...}}` reads the array first and answers "there
// are none"; there is nothing to read here but the gap and its remedy.
type noDataResult struct {
	Tool   string `json:"tool"`
	NoData NoData `json:"no_data"`
}

func noData(toolName string, nd *NoData) noDataResult {
	return noDataResult{Tool: toolName, NoData: *nd}
}

// symbolRef is how every symbol appears in every result: name plus the
// file:line an agent can cite instead of guessing.
type symbolRef struct {
	QualifiedName string  `json:"qualified_name"`
	Kind          string  `json:"kind"`
	File          string  `json:"file"`
	Line          int     `json:"line"`
	EndLine       *int    `json:"end_line,omitempty"`
	Package       *string `json:"package,omitempty"`
}

func refOf(row store.SymbolRow) symbolRef {
	return symbolRef{
		QualifiedName: string(row.QualifiedName),
		Kind:          string(row.Kind),
		File:          row.FilePath,
		Line:          row.Line,
		EndLine:       row.EndLine,
		Package:       row.Package,
	}
}

// featureMatch is one row of find_feature.
type featureMatch struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Kind          string  `json:"kind"`
	Owner         *string `json:"owner,omitempty"`
	LinkedSymbols int     `json:"linked_symbols"`
	MatchedOn     string  `json:"matched_on"`
}

type findFeatureResult struct {
	Query     string         `json:"query"`
	Features  []featureMatch `json:"features"`
	Truncated *Truncation    `json:"truncated,omitempty"`
}

// surfaceSymbol is one member of a feature's implementation surface. Role is
// set only for symbols a human actually annotated, which lets an agent tell
// the annotation roots from what the derivation added around them.
type surfaceSymbol struct {
	symbolRef
	Role string `json:"role,omitempty"`
}

type featureSurfaceResult struct {
	FeatureID         string           `json:"feature_id"`
	Title             string           `json:"title"`
	SurfaceSource     string           `json:"surface_source"`
	SurfaceSourceNote string           `json:"surface_source_note"`
	Symbols           []surfaceSymbol  `json:"symbols"`
	Truncated         *Truncation      `json:"truncated,omitempty"`
	IndexFreshness    *freshnessReport `json:"index_freshness,omitempty"`
}

// featureLink is one (feature, role) a symbol is annotated for.
type featureLink struct {
	FeatureID string `json:"feature_id"`
	Title     string `json:"title"`
	Role      string `json:"role"`
	Source    string `json:"source"`
}

type symbolInfoResult struct {
	symbolRef
	// Domain is the product-area prefix of the file path (issue #112's
	// rename of bc_path). Absent when the repo does not use the
	// src/contexts/<name>/ layout, which is most of them.
	Domain   *string       `json:"domain,omitempty"`
	Features []featureLink `json:"features"`
	// CallerCount / CalleeCount orient the agent before it pays for the list:
	// "412 distinct callers" is itself the answer to "is this safe to change".
	// They count DISTINCT symbols, not the call sites the edges table holds one
	// row per — five calls from one function are one caller, and counting them
	// as five overstates the blast radius of a signature change.
	CallerCount    int              `json:"caller_count"`
	CalleeCount    int              `json:"callee_count"`
	Notes          []string         `json:"notes"`
	IndexFreshness *freshnessReport `json:"index_freshness,omitempty"`
}

// neighbour is one end of a graph edge, carrying BOTH the neighbour's own
// declaration site and the site of the call itself. They are different
// locations and an agent citing the wrong one sends a reviewer to the wrong
// file.
type neighbour struct {
	symbolRef
	EdgeKind string   `json:"edge_kind"`
	CallSite callSite `json:"call_site"`

	// ResolutionTier is which mechanism established this edge, in the
	// vocabulary of graph.ResolutionTier: typed, name_resolved, syntactic,
	// imported.
	//
	// No omitempty, deliberately, for the reason store.EdgeRow.Tier gives
	// and this projection spent #103 through #175 not honouring: an edge
	// whose provenance is dropped when it is inconvenient is indistinguishable
	// from a typed one, and this surface is consumed by agents that ACT on the
	// list. A human reading a call list applies judgement and notices when
	// something looks wrong; an agent edits the callers it was handed, and a
	// syntactic false positive becomes a wrong edit in a file nobody asked it
	// to touch.
	ResolutionTier string `json:"resolution_tier"`

	// Ambiguous marks an edge where the resolver saw more than one candidate
	// and picked one. Orthogonal to the tier: a name_resolved edge can be
	// ambiguous (two packages declare the short name) and a syntactic one can
	// be unambiguous (one substring matched, still a guess).
	//
	// omitempty here and not on ResolutionTier because false is the honest
	// default -- "we did not have to choose" -- whereas an absent tier would
	// be a claim grunnr cannot make.
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// provenance summarises how much of a neighbour list grunnr could actually
// establish, so a caller can weigh the answer without tallying every row.
//
// The per-edge field is the primary signal; this exists because an agent
// handed forty rows will not count them, and "31 of 40 of these are guesses"
// is the sentence that changes what it does next.
type provenance struct {
	// ByTier counts edges per resolution tier. A map rather than named
	// fields: the tier vocabulary is graph's to extend (#87 moves edges to
	// typed, #105 adds imported), and a struct here would silently drop a
	// tier this package had not been taught about -- which is the failure
	// mode the whole tier system exists to prevent.
	ByTier map[string]int `json:"by_tier"`
	// Ambiguous is how many edges the resolver picked from more than one
	// candidate.
	Ambiguous int `json:"ambiguous"`
	// Note states the caveat in prose, for the same reason
	// surface_source_note does: a caller that reads only one field should
	// still be told what it is looking at.
	Note string `json:"note"`
}

// tierWeakEnoughToDoubt are the tiers whose edges may simply be wrong -- not
// incomplete, wrong: the target may not exist, or may be the wrong one of
// several same-named candidates.
func tierWeakEnoughToDoubt(t string) bool {
	return t == string(graph.TierSyntactic) || t == ""
}

// summarise builds the provenance block for a neighbour list.
//
// t is the truncation block, or nil. It matters: the counts describe the rows
// actually RETURNED, and on a capped result that is a subset. "2 of 40 are
// guesses" read as a statement about all 200 edges is precisely the
// overclaim this block exists to prevent, so when the list was capped the
// note says which population it is describing.
func summarise(rows []neighbour, t *Truncation) provenance {
	p := provenance{ByTier: map[string]int{}}
	weak := 0
	for _, r := range rows {
		tier := r.ResolutionTier
		if tier == "" {
			// An edge stored before the tier was required, or by a producer
			// that did not say. Named rather than counted as some real tier:
			// "unset" is information, and folding it into syntactic would
			// invent a claim.
			tier = "unset"
		}
		p.ByTier[tier]++
		if r.Ambiguous {
			p.Ambiguous++
		}
		if tierWeakEnoughToDoubt(r.ResolutionTier) {
			weak++
		}
	}
	scope := "these"
	if t != nil {
		scope = fmt.Sprintf("the %d shown (of %d)", t.Returned, t.Total)
	}

	switch {
	case len(rows) == 0:
		p.Note = "No call edges were found. That is not proof there are none: " +
			"calls through an interface, a DI container or reflection are not in the index."
	case weak == 0 && p.Ambiguous == 0:
		p.Note = fmt.Sprintf(
			"Every edge in %s was resolved by binding a name to a declaration grunnr indexed. "+
				"Still a lower bound: dynamic dispatch is not represented.", scope)
	default:
		p.Note = fmt.Sprintf(
			"%d of %s are syntactic guesses and %d were picked from more than one candidate. "+
				"A syntactic edge may name a symbol that does not exist, or the wrong one of "+
				"several with the same name. Verify before acting on those rows.",
			weak, scope, p.Ambiguous)
	}
	return p
}

type callSite struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type callersResult struct {
	QualifiedName  string           `json:"qualified_name"`
	Callers        []neighbour      `json:"callers"`
	Provenance     provenance       `json:"provenance"`
	Truncated      *Truncation      `json:"truncated,omitempty"`
	IndexFreshness *freshnessReport `json:"index_freshness,omitempty"`
}

type calleesResult struct {
	QualifiedName  string           `json:"qualified_name"`
	Callees        []neighbour      `json:"callees"`
	Provenance     provenance       `json:"provenance"`
	Truncated      *Truncation      `json:"truncated,omitempty"`
	IndexFreshness *freshnessReport `json:"index_freshness,omitempty"`
}

// coveringTest is one test that executed the symbol, with the statements it
// executed. The counts matter: "covered by TestFoo" and "covered by TestFoo,
// 2 of 40 statements" support very different decisions.
type coveringTest struct {
	symbolRef
	RunID        int64  `json:"run_id"`
	Framework    string `json:"framework"`
	CoveredStmts int    `json:"covered_stmts"`
	TotalStmts   int    `json:"total_stmts"`
}

type testsCoveringResult struct {
	QualifiedName string         `json:"qualified_name"`
	Tests         []coveringTest `json:"tests"`
	Frontier      frontierInfo   `json:"frontier"`
	Truncated     *Truncation    `json:"truncated,omitempty"`
	// Every row here carries the test's own file and line, so this result
	// cites spans and owes the same freshness statement as the others: an
	// agent sent to a test at foo_test.go:31 opens foo_test.go:31.
	IndexFreshness *freshnessReport `json:"index_freshness,omitempty"`
}

type coverageForResult struct {
	FeatureID         string             `json:"feature_id"`
	Title             string             `json:"title"`
	Score             float64            `json:"score"`
	Components        map[string]float64 `json:"components"`
	Reasons           []string           `json:"reasons,omitempty"`
	SurfaceSource     string             `json:"surface_source,omitempty"`
	SurfaceSourceNote string             `json:"surface_source_note,omitempty"`
	SampledAt         string             `json:"sampled_at"`
	Frontier          frontierInfo       `json:"frontier"`
	Notes             []string           `json:"notes,omitempty"`
}

// frontierInfo describes WHICH measurement the numbers came from. Without it
// an agent cannot tell a score computed from this morning's CI run from one
// computed from a coverprofile someone ingested in March.
type frontierInfo struct {
	Group       *string  `json:"group,omitempty"`
	RunIDs      []int64  `json:"run_ids"`
	Frameworks  []string `json:"frameworks"`
	NewestRunID int64    `json:"newest_run_id"`
	FinishedAt  string   `json:"finished_at"`
}

func describeFrontier(f store.CoverageFrontier) frontierInfo {
	out := frontierInfo{Group: f.Group, RunIDs: f.RunIDs(), NewestRunID: f.Newest}
	seen := map[string]bool{}
	var newest time.Time
	for _, r := range f.Runs {
		if !seen[string(r.Framework)] {
			seen[string(r.Framework)] = true
			out.Frameworks = append(out.Frameworks, string(r.Framework))
		}
		if r.FinishedAt.After(newest) {
			newest = r.FinishedAt
		}
	}
	sort.Strings(out.Frameworks)
	if !newest.IsZero() {
		out.FinishedAt = newest.UTC().Format(time.RFC3339)
	}
	return out
}

// freshnessReport says whether the spans in this answer still describe the
// files on disk.
//
// An agent given a symbol at pay.go:20 will open pay.go:20. If the file has
// changed since the scan, that line is now something else — not approximately
// right, arbitrarily wrong, because one inserted line at the top shifts every
// span below it.
type freshnessReport struct {
	FilesChecked       int      `json:"files_checked"`
	UntrustworthyFiles []string `json:"untrustworthy_files,omitempty"`
	Note               string   `json:"note"`
}

const freshnessCurrent = "current"

// checkFreshness classifies the files a result cites. A nil hook (tests, or a
// caller that has no repo root) yields nil rather than a fabricated "all
// current" — silence is honest, a false all-clear is not.
func checkFreshness(ctx context.Context, fn FreshnessFunc, files map[string]bool) *freshnessReport {
	if fn == nil || len(files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(files))
	for f := range files {
		paths = append(paths, f)
	}
	sort.Strings(paths)

	states, err := fn(ctx, paths)
	if err != nil {
		// A freshness check that failed must not fail the whole answer; it
		// must also not be reported as a pass.
		return &freshnessReport{
			FilesChecked: len(paths),
			Note:         "index freshness could not be checked (" + err.Error() + "); treat every span below as unverified",
		}
	}
	var bad []string
	for _, p := range paths {
		if states[p] != freshnessCurrent {
			bad = append(bad, p+" ("+states[p]+")")
		}
	}
	if len(bad) == 0 {
		return &freshnessReport{
			FilesChecked: len(paths),
			Note:         "every file cited below hashes to what the scanner recorded; the spans are safe to cite",
		}
	}
	return &freshnessReport{
		FilesChecked:       len(paths),
		UntrustworthyFiles: bad,
		Note: "the working tree has moved since the scan: spans in these files no longer point where they did. " +
			"Re-run `grunnr scan` before citing line numbers in them.",
	}
}
