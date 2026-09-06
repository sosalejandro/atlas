package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/store"
)

// edgeProvenance reports, per language, WHICH MECHANISM produced the
// call graph atlas is answering questions from.
//
// Every other check in this package asks whether atlas's picture is
// stale. This one asks something the others cannot: whether the picture
// was ever solid. A `trace` result, a change-impact answer and an
// audit's impl surface are all walks over `edges`, and an edge resolved
// by a type checker and one guessed from a lowercased substring are the
// same row in every column except this one. Reporting the count without
// the composition is the confident-but-wrong answer doctor exists to
// prevent, one level below where the other checks look.
//
// It is also the report #87's review reads. Issue #146 replaces that
// issue's original acceptance criterion ("same symbol and edge counts
// +/- a documented delta") with a histogram comparison, because a total
// can hold steady while every edge underneath it changes mechanism.
type edgeProvenance struct{}

func (edgeProvenance) Name() string { return "index.edge_provenance" }

func (edgeProvenance) Examines() string {
	return "which resolution mechanism produced each edge, tallied per language"
}

// langProvenance is one language's row of the histogram: the total, the
// per-tier breakdown, and how many of the edges the resolver had to
// pick between candidates for.
//
// Tiers is a map keyed by the closed vocabulary rather than four named
// fields, so a tier added later (there is one reserved slot in practice
// -- SCIP's `imported`, #105 step 1) does not silently drop out of
// every report until someone remembers to widen a struct.
type langProvenance struct {
	Lang      string                       `json:"lang"`
	Edges     int                          `json:"edges"`
	Ambiguous int                          `json:"ambiguous"`
	Tiers     map[graph.ResolutionTier]int `json:"tiers"`
}

func (c edgeProvenance) Run(ctx context.Context, env *Env) (Result, error) {
	if res, ok := env.requireStore(); !ok {
		return res, nil
	}
	buckets, err := env.Store.Edges().TierHistogram(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("doctor provenance: tier histogram: %w", err)
	}
	if len(buckets) == 0 {
		return Result{
			Severity: SeverityNotApplicable,
			Finding: "no edges are recorded, so there is no resolution to describe " +
				"(a repo with no call graph, or a scan that has not run)",
			Remediation: "atlas scan",
		}, nil
	}

	langs := foldByLanguage(buckets)
	details := map[string]any{
		"languages":   langs,
		"total_edges": totalEdges(langs),
	}

	// The only threshold here is the degenerate one, and that is
	// deliberate. #146 records the field note that a `text_matched`
	// share above roughly 5% made change-impact answers not worth
	// showing, and equally records that the figure is calibrated
	// against another project's corpus and is NOT adopted: this
	// project does not ship numbers nobody here has measured. "Not one
	// edge in this language was resolved beyond syntax" needs no
	// calibration -- it is a statement about the mechanism, not a
	// judgement about a rate.
	var unresolvedLangs []string
	for _, l := range langs {
		if l.Tiers[graph.TierSyntactic] == l.Edges {
			unresolvedLangs = append(unresolvedLangs, l.Lang)
		}
	}

	if len(unresolvedLangs) > 0 {
		return Result{
			Severity: SeverityWarn,
			Finding: fmt.Sprintf(
				"%s; every %s edge is syntactic, meaning no name was bound across files -- "+
					"change-impact answers over %s are guesses about targets that may not exist",
				renderHistogram(langs),
				strings.Join(unresolvedLangs, " and "),
				pluralLangs(unresolvedLangs)),
			// No remediation command exists: the tier a language
			// reaches is a property of the scanner atlas ships for it,
			// not of anything the user did. Inventing an "atlas scan"
			// suggestion here would tell them to re-run the thing that
			// produced this exact result.
			Details: details,
		}, nil
	}

	return Result{
		Severity: SeverityOK,
		Finding:  renderHistogram(langs),
		Details:  details,
	}, nil
}

// foldByLanguage turns the store's flat (lang, tier) buckets into one
// row per language, preserving the store's ordering (language
// ascending) so the rendered histogram is diffable across runs.
func foldByLanguage(buckets []store.TierBucket) []langProvenance {
	byLang := map[string]*langProvenance{}
	var order []string
	for _, b := range buckets {
		l, ok := byLang[b.Lang]
		if !ok {
			// Seed every tier at zero rather than letting absent ones
			// stay absent. A JSON consumer diffing two runs of this
			// report should see a NUMBER change, not a key appear --
			// "typed went from missing to 40" and "typed went from 0
			// to 40" are the same event, and only one of them is
			// greppable.
			tiers := make(map[graph.ResolutionTier]int, len(graph.AllTiers()))
			for _, tier := range graph.AllTiers() {
				tiers[tier] = 0
			}
			l = &langProvenance{Lang: b.Lang, Tiers: tiers}
			byLang[b.Lang] = l
			order = append(order, b.Lang)
		}
		l.Edges += b.Edges
		l.Ambiguous += b.Ambiguous
		l.Tiers[b.Tier] += b.Edges
	}
	sort.Strings(order)
	out := make([]langProvenance, 0, len(order))
	for _, name := range order {
		out = append(out, *byLang[name])
	}
	return out
}

func totalEdges(langs []langProvenance) int {
	n := 0
	for _, l := range langs {
		n += l.Edges
	}
	return n
}

// renderHistogram is the one-line-per-language tally, in the closed
// tier order rather than by size.
//
// Tiers with no edges are printed as zero rather than omitted. A
// histogram whose columns come and go with the data is not comparable
// against another run of it, and comparing two of them across a
// resolver migration is the whole reason this exists -- an absent
// column reads as "unchanged" exactly where a reader most needs to see
// "went to nothing".
func renderHistogram(langs []langProvenance) string {
	parts := make([]string, 0, len(langs))
	for _, l := range langs {
		tiers := make([]string, 0, len(graph.AllTiers()))
		for _, tier := range graph.AllTiers() {
			tiers = append(tiers, fmt.Sprintf("%s=%d", tier, l.Tiers[tier]))
		}
		part := fmt.Sprintf("%s: %d edges (%s)", l.Lang, l.Edges, strings.Join(tiers, " "))
		if l.Ambiguous > 0 {
			part += fmt.Sprintf(", %d ambiguous", l.Ambiguous)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func pluralLangs(langs []string) string {
	if len(langs) == 1 {
		return "it"
	}
	return "them"
}
