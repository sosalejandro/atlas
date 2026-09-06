package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// seedEdge writes one edge between two freshly-created symbols in
// file, at the given tier.
func (f *fixture) seedEdge(t *testing.T, file string, tier graph.ResolutionTier, ambiguous bool) {
	t.Helper()
	ctx := context.Background()
	from, err := f.store.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(file + ".from" + string(tier) + boolTag(ambiguous)),
		Kind:          shared.KindFunc, FilePath: file, Line: 1,
	})
	if err != nil {
		t.Fatalf("seed from symbol: %v", err)
	}
	to, err := f.store.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(file + ".to" + string(tier) + boolTag(ambiguous)),
		Kind:          shared.KindFunc, FilePath: file, Line: 2,
	})
	if err != nil {
		t.Fatalf("seed to symbol: %v", err)
	}
	if _, err := f.store.Edges().Insert(ctx, store.EdgeRow{
		FromID: from, ToID: to, Kind: store.EdgeKindCall, FilePath: file, Line: 1,
		Tier: tier, Ambiguous: ambiguous,
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
}

func boolTag(b bool) string {
	if b {
		return "amb"
	}
	return ""
}

// A store with no edges has nothing to report, and saying "ok" would
// be the confident-but-wrong answer this package exists to catch.
func TestEdgeProvenance_NoEdgesIsNotApplicable(t *testing.T) {
	f := newFixture(t)
	res, err := edgeProvenance{}.Run(context.Background(), f.env(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Severity != SeverityNotApplicable {
		t.Errorf("severity = %q, want %q", res.Severity, SeverityNotApplicable)
	}
}

// A mixed repo: the histogram is reported per language, and the check
// does not fail on a threshold nobody measured.
func TestEdgeProvenance_ReportsHistogramPerLanguage(t *testing.T) {
	f := newFixture(t)
	f.seedEdge(t, "internal/a.go", graph.TierNameResolved, false)
	f.seedEdge(t, "internal/b.go", graph.TierSyntactic, true)
	f.seedEdge(t, "web/app.ts", graph.TierSyntactic, false)
	f.seedEdge(t, "web/other.ts", graph.TierNameResolved, false)

	res, err := edgeProvenance{}.Run(context.Background(), f.env(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Severity != SeverityOK {
		t.Errorf("severity = %q, want %q (finding: %s)", res.Severity, SeverityOK, res.Finding)
	}

	langs, ok := res.Details["languages"].([]langProvenance)
	if !ok {
		t.Fatalf("details[languages] has type %T, want []langProvenance", res.Details["languages"])
	}
	if len(langs) != 2 {
		t.Fatalf("languages = %d, want 2 (%+v)", len(langs), langs)
	}
	assertLang(t, langs[0], "go", 2, map[graph.ResolutionTier]int{
		graph.TierNameResolved: 1, graph.TierSyntactic: 1})
	assertLang(t, langs[1], "ts", 2, map[graph.ResolutionTier]int{
		graph.TierNameResolved: 1, graph.TierSyntactic: 1})

	// The ambiguity count rides along: it is the signal packages/graph
	// computed and dropped for four minor versions, and burying it now
	// would repeat the mistake at one level up.
	if langs[0].Ambiguous != 1 {
		t.Errorf("go ambiguous = %d, want 1", langs[0].Ambiguous)
	}

	// Every tier is present with an explicit zero. A histogram whose
	// columns come and go with the data is not diffable, and diffing
	// two of them across #87 is the entire point.
	for _, l := range langs {
		for _, tier := range graph.AllTiers() {
			if _, ok := l.Tiers[tier]; !ok {
				t.Errorf("%s histogram omits tier %q; a missing column reads as "+
					"'unchanged' exactly where a reader needs to see 'went to nothing'",
					l.Lang, tier)
			}
		}
	}
}

func assertLang(t *testing.T, got langProvenance, lang string, edges int, tiers map[graph.ResolutionTier]int) {
	t.Helper()
	if got.Lang != lang {
		t.Errorf("lang = %q, want %q", got.Lang, lang)
	}
	if got.Edges != edges {
		t.Errorf("%s edges = %d, want %d", lang, got.Edges, edges)
	}
	for tier, want := range tiers {
		if got.Tiers[tier] != want {
			t.Errorf("%s tier %q = %d, want %d", lang, tier, got.Tiers[tier], want)
		}
	}
}

// A language whose every edge is syntactic warns. This is the only
// threshold the check applies and it is the degenerate one: not that
// the syntactic share crossed some calibrated line, but that NOTHING
// in this language was resolved beyond syntax, so change-impact
// answers over it are guesses end to end. Any number between 0 and 1
// would be a constant nobody here has measured.
func TestEdgeProvenance_WarnsWhenALanguageResolvedNothing(t *testing.T) {
	f := newFixture(t)
	f.seedEdge(t, "internal/a.go", graph.TierNameResolved, false)
	f.seedEdge(t, "web/app.ts", graph.TierSyntactic, false)
	f.seedEdge(t, "web/other.ts", graph.TierSyntactic, false)

	res, err := edgeProvenance{}.Run(context.Background(), f.env(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Severity != SeverityWarn {
		t.Errorf("severity = %q, want %q (finding: %s)", res.Severity, SeverityWarn, res.Finding)
	}
	if !strings.Contains(res.Finding, "ts") {
		t.Errorf("finding does not name the language that resolved nothing: %s", res.Finding)
	}
	// Go resolved something, so it must not be named as the problem.
	if strings.Contains(res.Finding, "every go edge") {
		t.Errorf("finding blames go, which reached name_resolved: %s", res.Finding)
	}
}

// The check must never fail. #146 declines the 5% text_matched
// threshold from trace-mcp explicitly, because that constant is
// calibrated against somebody else's corpus; a gate built on a number
// this project has not measured is exactly what the standing rule
// forbids.
func TestEdgeProvenance_NeverFails(t *testing.T) {
	f := newFixture(t)
	f.seedEdge(t, "internal/a.go", graph.TierSyntactic, true)
	f.seedEdge(t, "web/app.ts", graph.TierSyntactic, true)

	res, err := edgeProvenance{}.Run(context.Background(), f.env(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Severity == SeverityFail {
		t.Errorf("severity = fail on an all-syntactic repo; the check has acquired an "+
			"unmeasured threshold (finding: %s)", res.Finding)
	}
}

// The check has to be in the default set, or it reports to nobody.
func TestEdgeProvenance_IsInDefaultChecks(t *testing.T) {
	for _, c := range DefaultChecks() {
		if c.Name() == (edgeProvenance{}).Name() {
			return
		}
	}
	t.Fatalf("%s is not in DefaultChecks", (edgeProvenance{}).Name())
}
