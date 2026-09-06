package goscan

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The #87 defect suite: the claims from typed_test.go that nothing was
// holding to, plus the two the reviewer found overstated.

const authorityCorpusDir = "testdata/authoritycorpus"

// THE authority rule, as a test.
//
// "The typed resolver is authoritative for a file it type-checked -- the
// name ladder does not get a second opinion" is the sentence that makes
// every tier count in this package mean something, and it lives in one
// line of resolveCallSite: the `if ...; handled { return resolutions }`
// returns on `handled`, not on `len(resolutions) > 0`. Weaken it to the
// latter and the name ladder runs after every typed MISS -- a
// conversion, a call through a func value, every call into the standard
// library -- so the syntactic bucket grows on the change meant to shrink
// it, and it grows with edges that are wrong rather than merely weak.
//
// Nothing tested this before: the reviewer mutated that line and the
// suite stayed green.
func TestTypedResolution_IsAuthoritativeOverTheNameLadder(t *testing.T) {
	res := scanOrFail(t, authorityCorpusDir, Options{})

	// Positive control first. Without it, every assertion below would
	// also pass on a fixture that had silently stopped type-checking and
	// resolved nothing at all -- which is how a test about absent edges
	// rots into a test about an inert corpus.
	if res.Resolution == nil || res.Resolution.TypedIndexedFiles == 0 {
		t.Fatalf("the fixture was not type-checked, so this test cannot say anything: %+v",
			res.Resolution)
	}
	control := edgeIn(res.Graph.Edges, "Report.Add", "Report.total")
	if control == nil {
		t.Fatal("no edge Report.Add -> Report.total; the typed path resolved nothing here")
	}
	if control.Tier != graph.TierTyped {
		t.Fatalf("Report.Add -> Report.total tier = %q, want %q", control.Tier, graph.TierTyped)
	}

	// Three call sites in that body bind to standard-library methods
	// this scan does not index. The typed resolver declines all three.
	// None of them may come back by name.
	if wrong := edgeIn(res.Graph.Edges, "Report.Add", "Builder.WriteString"); wrong != nil {
		t.Errorf("Report.Add -> Builder.WriteString (tier %q): the call is "+
			"strings.Builder.WriteString, so the name ladder was allowed a second opinion "+
			"after the typed resolver declined", wrong.Tier)
	}
	for _, stub := range []shared.SymbolID{"sync.Mutex.Lock", "sync.Mutex.Unlock"} {
		if e := edgeIn(res.Graph.Edges, "Report.Add", stub); e != nil {
			t.Errorf("Report.Add -> %s (tier %q): a rendering of the source text became an "+
				"edge out of a type-checked caller", stub, e.Tier)
		}
	}

	// Stated once more as the invariant rather than as a list of known
	// callees, so a call added to the fixture later is covered too.
	for _, e := range res.Graph.Edges {
		if e.From == "Report.Add" && e.Tier != graph.TierTyped {
			t.Errorf("edge Report.Add -> %s has tier %q; every edge out of a type-checked "+
				"caller must be %q, because the name ladder never ran",
				e.To, e.Tier, graph.TierTyped)
		}
	}
}

// The companion, and the reason the test above has teeth.
//
// An assertion that some edge is ABSENT proves nothing unless something
// would otherwise have produced it. This pins that both shapes are
// reachable on this exact fixture: a wrong edge into a real indexed
// declaration, and a stub node for a symbol that was never scanned.
func TestTypedResolution_NameLadderWouldHaveInventedTheseEdges(t *testing.T) {
	res := scanOrFail(t, authorityCorpusDir, Options{SkipTypedResolution: true})

	// Shape 1: a wrong edge into a real, indexed, unrelated declaration.
	// Given the rendered field type "strings.Builder", fuzzyResolveMethod
	// matches this package's own Builder on the substring "builder".
	wrong := edgeIn(res.Graph.Edges, "Report.Add", "Builder.WriteString")
	if wrong == nil {
		t.Error("the fuzzy resolver no longer reaches Builder.WriteString; the authority test " +
			"now asserts the absence of an edge nothing would produce")
	} else if wrong.Tier != graph.TierSyntactic {
		t.Errorf("Report.Add -> Builder.WriteString tier = %q, want %q",
			wrong.Tier, graph.TierSyntactic)
	}

	// Shape 2: an `external` stub node for a symbol nothing declared here.
	stub := edgeIn(res.Graph.Edges, "Report.Add", "sync.Mutex.Lock")
	if stub == nil {
		t.Error("the AST ladder no longer synthesises sync.Mutex.Lock; the authority test " +
			"now asserts the absence of an edge nothing would produce")
	} else if stub.Tier != graph.TierSyntactic {
		t.Errorf("Report.Add -> sync.Mutex.Lock tier = %q, want %q",
			stub.Tier, graph.TierSyntactic)
	}
}

const unseenCandidatesDir = "testdata/unseencandidates"

// Ambiguity is a count of the candidates CHA found, not of the ones atlas
// happens to have indexed.
//
// The fixture is one interface with two implementations, one of them in a
// generated file the exclusion ledger drops, so the scan emits exactly one
// edge. Counting after the indexed filter (`len(ids) > 1`) records that
// edge as unambiguous: a claim to know which implementation ran, made
// precisely where atlas can see the least. Fewer visible alternatives is
// less evidence, not more.
func TestTypedResolution_AmbiguityCountsCandidatesItCannotSee(t *testing.T) {
	res := scanOrFail(t, unseenCandidatesDir, Options{})

	if res.Resolution == nil || res.Resolution.InvokeSites == 0 {
		t.Fatalf("no interface call sites resolved; the fixture is not exercising CHA: %+v",
			res.Resolution)
	}

	e := edgeIn(res.Graph.Edges, "Pipe.Send", "Direct.Write")
	if e == nil {
		t.Fatal("no edge Pipe.Send -> Direct.Write; interface dispatch did not resolve at all")
	}
	if e.Tier != graph.TierTyped {
		t.Fatalf("Pipe.Send -> Direct.Write tier = %q, want %q", e.Tier, graph.TierTyped)
	}
	if edgeIn(res.Graph.Edges, "Pipe.Send", "Buffered.Write") != nil {
		t.Fatal("Buffered.Write is indexed after all, so this scan can see both candidates " +
			"and the test no longer exercises the dropped-candidate case")
	}
	if !e.Ambiguous {
		t.Error("Pipe.Send -> Direct.Write claims to be unambiguous, but Buffered.Write also " +
			"implements Writer -- it is merely invisible to this scan, which is a reason " +
			"for less confidence in the surviving edge, not more")
	}
}

// The counterfactual. Index the generated file and both candidates become
// visible: two edges, both ambiguous. Nothing about which implementation
// runs changed -- only what atlas could see -- so the flag must read the
// same either way.
func TestTypedResolution_AmbiguityIsUnchangedByWhatIsIndexed(t *testing.T) {
	res := scanOrFail(t, unseenCandidatesDir, Options{IncludeGenerated: true})

	for _, to := range []shared.SymbolID{"Direct.Write", "Buffered.Write"} {
		e := edgeIn(res.Graph.Edges, "Pipe.Send", to)
		if e == nil {
			t.Fatalf("no edge Pipe.Send -> %s with IncludeGenerated set", to)
		}
		if !e.Ambiguous {
			t.Errorf("Pipe.Send -> %s is not ambiguous, but CHA named two implementations", to)
		}
	}
}

// Partial degradation has to reach the scan warnings.
//
// The all-or-nothing cases already spoke for themselves: a load that
// could not run fills Unavailable, and a load covering none of the
// scanned files warns on its own. The common case -- SOME packages red,
// which is the case this whole design exists for -- said nothing, so a
// reader saw syntactic edges in the tier histogram with no hint that a
// package had failed to compile. Result.Warnings is what `atlas scan`
// prints to stderr, so it is the channel that reaches a user who never
// opens the JSON.
func TestTypedResolution_PartialDegradationReachesTheWarnings(t *testing.T) {
	res := scanOrFail(t, brokenCorpusDir, Options{})
	if res.Resolution == nil || res.Resolution.Degraded == 0 || res.Resolution.TypeChecked == 0 {
		t.Fatalf("the fixture is no longer PARTIALLY degraded: %+v", res.Resolution)
	}

	var found string
	for _, w := range res.Warnings {
		if strings.Contains(w, "degraded") {
			found = w
			break
		}
	}
	if found == "" {
		t.Fatalf("no warning names the degraded packages; a scan with %d of %d packages red "+
			"told a user who reads only stderr nothing at all. warnings=%v",
			res.Resolution.Degraded, res.Resolution.Packages, res.Warnings)
	}
	// How many, and why. "Something degraded" is not actionable;
	// "broken/broken.go:19:9 does not compile" is.
	if !strings.Contains(found, "1 of 2") {
		t.Errorf("warning does not name the counts (want %d of %d): %q",
			res.Resolution.Degraded, res.Resolution.Packages, found)
	}
	if !strings.Contains(found, res.Resolution.DegradedPackages[0].Path) {
		t.Errorf("warning does not name the failing package %q: %q",
			res.Resolution.DegradedPackages[0].Path, found)
	}
	if !strings.Contains(found, res.Resolution.DegradedPackages[0].Error) {
		t.Errorf("warning does not carry the type error, so it says that something is wrong "+
			"without saying what: %q", found)
	}
}

// A tree where everything type-checks must NOT carry the warning. A
// diagnostic that fires on healthy scans is one nobody reads, which is
// how the partial case came to be silent in the first place.
func TestTypedResolution_NoDegradationWarningOnAHealthyTree(t *testing.T) {
	res := scanOrFail(t, goldenCorpusDir, Options{})
	if res.Resolution == nil || res.Resolution.Degraded != 0 {
		t.Fatalf("the golden corpus is expected to type-check cleanly: %+v", res.Resolution)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "degraded") {
			t.Errorf("degradation warning on a tree where nothing degraded: %q", w)
		}
	}
}

// The sqlc redirect re-resolves, so it may not keep the callee's tier.
//
// c.sqlcMethods is keyed on a BARE METHOD NAME, and the lookup uses the
// last dot-segment of whatever id the resolver produced. Two unrelated
// typed edges -- MemoryOrderRepository.Save and
// PostgresOrderRepository.Save, bound by the type checker to different
// declarations on different types -- land on one query node here.
// Nothing about that hop is typed; carrying `typed` across would let a
// bare-name match inherit a guarantee the type checker made about the
// edge it replaced.
func TestSQLCRedirect_IsSyntacticBecauseItRematchesOnABareName(t *testing.T) {
	const method = "Save"
	opts := Options{SQLCMethods: map[string]SQLCMapping{
		method: {
			GoMethod:  method,
			SQLFile:   "queries/orders.sql",
			SQLLine:   7,
			QueryName: "SaveOrder",
			QueryType: "exec",
		},
	}}

	// What the edge is before the redirect: typed, and ambiguous because
	// CHA named both repositories. Asserted so a failure below reads as
	// "the redirect kept the tier" rather than "nothing was typed".
	plain := edgeIn(scanOrFail(t, goldenCorpusDir, Options{}).Graph.Edges,
		"OrderService.Create", "MemoryOrderRepository.Save")
	if plain == nil || plain.Tier != graph.TierTyped {
		t.Fatalf("OrderService.Create -> MemoryOrderRepository.Save is not typed (%+v); "+
			"this test has nothing to demote", plain)
	}

	res := scanOrFail(t, goldenCorpusDir, opts)
	e := edgeIn(res.Graph.Edges, "OrderService.Create", "sql:SaveOrder")
	if e == nil {
		t.Fatal("no edge OrderService.Create -> sql:SaveOrder; the sqlc redirect did not fire")
	}
	if e.Tier != graph.TierSyntactic {
		t.Errorf("sqlc edge tier = %q, want %q: the query node was chosen by bare method "+
			"name, which is tier C however the original callee was resolved",
			e.Tier, graph.TierSyntactic)
	}
	// Ambiguity carries over: the redirect adds doubt and removes none.
	if !e.Ambiguous {
		t.Error("sqlc edge is not ambiguous, but the callee it replaced was")
	}

	// The collapse itself, stated: one node, reached from callers whose
	// typed callees were different declarations.
	var into int
	for _, edge := range res.Graph.Edges {
		if edge.To != "sql:SaveOrder" {
			continue
		}
		into++
		if edge.Tier == graph.TierTyped {
			t.Errorf("edge %s -> sql:SaveOrder claims %q", edge.From, graph.TierTyped)
		}
	}
	if into < 2 {
		t.Errorf("only %d edge(s) into sql:SaveOrder; the fixture no longer shows the "+
			"bare-name key collapsing distinct typed callees", into)
	}
}
