package mcp

import (
	"context"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

// noDataResult is the ENTIRE result when atlas cannot answer.
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
	BCPath   *string       `json:"bc_path,omitempty"`
	Features []featureLink `json:"features"`
	// CallerCount / CalleeCount orient the agent before it pays for the list:
	// "412 callers" is itself the answer to "is this safe to change".
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
}

type callSite struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type callersResult struct {
	QualifiedName  string           `json:"qualified_name"`
	Callers        []neighbour      `json:"callers"`
	Truncated      *Truncation      `json:"truncated,omitempty"`
	IndexFreshness *freshnessReport `json:"index_freshness,omitempty"`
}

type calleesResult struct {
	QualifiedName  string           `json:"qualified_name"`
	Callees        []neighbour      `json:"callees"`
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
			"Re-run `atlas scan` before citing line numbers in them.",
	}
}
