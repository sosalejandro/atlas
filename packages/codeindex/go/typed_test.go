package goscan

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The #87 suite. Every test here is about the MECHANISM that produced an
// edge, not about how many edges there are: issue #146 exists because a
// resolver swap leaves (from, to, kind, line) identical and moves only
// the tier, so a count is exactly the thing that cannot see this change.

const brokenCorpusDir = "testdata/brokencorpus"

// edgeIn finds one edge by endpoints. Returns nil when absent, which the
// callers assert on directly -- "the edge is missing" and "the edge is
// wrong" are different failures and should read differently.
func edgeIn(edges []graph.Edge, from, to shared.SymbolID) *graph.Edge {
	for i := range edges {
		if edges[i].From == from && edges[i].To == to {
			return &edges[i]
		}
	}
	return nil
}

// tierHistogram counts edges per tier.
func tierHistogram(edges []graph.Edge) map[graph.ResolutionTier]int {
	h := map[graph.ResolutionTier]int{}
	for _, e := range edges {
		h[e.Tier]++
	}
	return h
}

func scanOrFail(t *testing.T, root string, opts Options) *Result {
	t.Helper()
	res, err := Scan(context.Background(), root, opts)
	if err != nil {
		t.Fatalf("Scan(%s): %v", root, err)
	}
	return res
}

// A call through an interface-typed field is the payoff case. Nothing in
// the source text of `s.repo.Save(ctx, o)` names a receiver TYPE, so the
// name resolver had nothing to look up and dropped the call outright.
// Class-hierarchy analysis names both implementations, and both are real
// answers: CHA cannot tell which one the program constructs, so the edges
// are ambiguous AND typed. Those two facts are independent (see #146).
func TestTypedResolution_ResolvesInterfaceDispatch(t *testing.T) {
	res := scanOrFail(t, goldenCorpusDir, Options{})

	for _, to := range []shared.SymbolID{
		"MemoryOrderRepository.Save",
		"PostgresOrderRepository.Save",
	} {
		e := edgeIn(res.Graph.Edges, "OrderService.Create", to)
		if e == nil {
			t.Fatalf("no edge OrderService.Create -> %s; interface dispatch did not resolve", to)
		}
		if e.Tier != graph.TierTyped {
			t.Errorf("OrderService.Create -> %s tier = %q, want %q", to, e.Tier, graph.TierTyped)
		}
		if !e.Ambiguous {
			t.Errorf("OrderService.Create -> %s is not marked ambiguous, but CHA reported two candidates", to)
		}
	}
}

// Two packages in the corpus declare a Config with a Validate method, so
// the short id "Config.Validate" belongs to whichever was walked first --
// internal/persistence. The name resolver bound config.Load's call to
// THAT one: an edge into a package config.Load does not import. The typed
// resolver binds the object, so the edge lands on the declaration in
// internal/platform/config, which the scanner registered under its
// package-qualified id.
func TestTypedResolution_BindsCollidingShortNameToTheRightPackage(t *testing.T) {
	res := scanOrFail(t, goldenCorpusDir, Options{})

	const right = shared.SymbolID("internal/platform/config.Config.Validate")
	const wrong = shared.SymbolID("Config.Validate")

	e := edgeIn(res.Graph.Edges, "config.Load", right)
	if e == nil {
		t.Fatalf("no edge config.Load -> %s; the colliding short name did not bind by type", right)
	}
	if e.Tier != graph.TierTyped {
		t.Errorf("config.Load -> %s tier = %q, want %q", right, e.Tier, graph.TierTyped)
	}
	if bad := edgeIn(res.Graph.Edges, "config.Load", wrong); bad != nil {
		t.Errorf("config.Load still binds to %s (persistence's Validate), which it does not import", wrong)
	}
}

// A method on a generic receiver is one declaration and N type-checked
// instantiations. The scanner indexes the declaration once, as Cache.Put;
// the resolver must map Cache[string,*orders.Order].Put back to it rather
// than mint an id per instantiation.
//
// The AST resolver could not reach this call at all: r.rows.Put is a
// chained selector whose field type renders as "?" because typeExprString
// has no case for an instantiated type.
func TestTypedResolution_MapsGenericInstantiationToDeclaration(t *testing.T) {
	res := scanOrFail(t, goldenCorpusDir, Options{})

	e := edgeIn(res.Graph.Edges, "MemoryOrderRepository.Save", "Cache.Put")
	if e == nil {
		t.Fatalf("no edge MemoryOrderRepository.Save -> Cache.Put; the generic instantiation did not map back")
	}
	if e.Tier != graph.TierTyped {
		t.Errorf("tier = %q, want %q", e.Tier, graph.TierTyped)
	}

	for _, sym := range res.Symbols {
		if strings.Contains(string(sym.ID), "[") {
			t.Errorf("symbol %q carries type arguments; the table fragmented per instantiation", sym.ID)
		}
	}
}

// A tree that does not compile must still be scanned. The degradation is
// per package: `sound` type-checks and its calls are typed, `broken` does
// not and its calls fall back to the name resolver with the tier saying
// so.
func TestTypedResolution_DegradesPerPackageOnBrokenBuild(t *testing.T) {
	res := scanOrFail(t, brokenCorpusDir, Options{})

	if res.Resolution == nil {
		t.Fatal("no resolution report; a scan that tried to type-check must say what happened")
	}
	if res.Resolution.Degraded != 1 {
		t.Errorf("Degraded = %d, want 1 (only the `broken` package fails)", res.Resolution.Degraded)
	}
	if res.Resolution.TypeChecked != 1 {
		t.Errorf("TypeChecked = %d, want 1 (`sound` compiles)", res.Resolution.TypeChecked)
	}

	sound := edgeIn(res.Graph.Edges, "Ledger.Append", "Ledger.push")
	if sound == nil {
		t.Fatal("no edge Ledger.Append -> Ledger.push in the package that compiles")
	}
	if sound.Tier != graph.TierTyped {
		t.Errorf("Ledger.Append -> Ledger.push tier = %q, want %q", sound.Tier, graph.TierTyped)
	}

	broken := edgeIn(res.Graph.Edges, "Store.Put", "Store.write")
	if broken == nil {
		t.Fatal("no edge Store.Put -> Store.write; the broken package was not scanned at all")
	}
	if broken.Tier == graph.TierTyped {
		t.Errorf("Store.Put -> Store.write claims %q, but its package did not type-check", graph.TierTyped)
	}
}

// The acceptance criterion from the brief, as a test: the composition may
// move, but the number of edges atlas is GUESSING about must not grow.
// A resolver swap that keeps the total steady while turning resolutions
// into guesses is the failure this replaces.
func TestTypedResolution_DoesNotIncreaseSyntacticEdges(t *testing.T) {
	before := tierHistogram(scanOrFail(t, goldenCorpusDir, Options{SkipTypedResolution: true}).Graph.Edges)
	after := tierHistogram(scanOrFail(t, goldenCorpusDir, Options{}).Graph.Edges)

	if after[graph.TierSyntactic] > before[graph.TierSyntactic] {
		t.Errorf("syntactic edges rose from %d to %d", before[graph.TierSyntactic], after[graph.TierSyntactic])
	}
	if after[graph.TierTyped] == 0 {
		t.Error("no typed edges after enabling typed resolution; the corpus is not exercising it")
	}
	if after[graph.TierNameResolved] > before[graph.TierNameResolved] {
		t.Errorf("name_resolved edges rose from %d to %d; typed resolution should only take from that bucket",
			before[graph.TierNameResolved], after[graph.TierNameResolved])
	}
}

// SkipTypedResolution has to reproduce the pre-#87 behaviour exactly,
// because it is the escape hatch a user reaches for when the typed path
// misbehaves. If it quietly kept some typed edges it would not be an
// escape hatch.
func TestTypedResolution_SkipReproducesNameResolution(t *testing.T) {
	res := scanOrFail(t, goldenCorpusDir, Options{SkipTypedResolution: true})
	for _, e := range res.Graph.Edges {
		if e.Tier == graph.TierTyped {
			t.Errorf("edge %s -> %s is typed with SkipTypedResolution set", e.From, e.To)
		}
	}
	if res.Resolution != nil {
		t.Errorf("resolution report present with SkipTypedResolution set: %+v", res.Resolution)
	}
}

// A tree that is not inside any module at all must scan without an error
// and without pretending. This is the common case for a snippet
// directory, a vendored drop, or anything copied out of its repository:
// `go list` cannot even start, so the whole load fails at once rather
// than degrading package by package, and the report has to say which of
// the two happened.
func TestTypedResolution_NoModuleFallsBackQuietly(t *testing.T) {
	// t.TempDir() is under the OS temp root, which is not inside a
	// module -- unlike every fixture in testdata/, which is inside the
	// atlas module whether or not it carries a go.mod of its own.
	loose := filepath.Join(t.TempDir(), "loose")
	copyTree(t, "testdata/sampleproject", loose)

	res := scanOrFail(t, loose, Options{})
	for _, e := range res.Graph.Edges {
		if e.Tier == graph.TierTyped {
			t.Errorf("edge %s -> %s is typed, but the tree is in no module", e.From, e.To)
		}
	}
	if res.Resolution == nil {
		t.Fatal("no resolution report; a scan that tried to type-check must say what happened")
	}
	if res.Resolution.Unavailable == "" {
		t.Errorf("Unavailable is empty; want the reason the load could not run: %+v", res.Resolution)
	}
	if strings.Contains(res.Resolution.Unavailable, loose) {
		t.Errorf("Unavailable leaks the absolute scan root: %q", res.Resolution.Unavailable)
	}
	if len(res.Graph.Edges) == 0 {
		t.Error("no edges at all; the AST fallback did not run")
	}
}

// The sampleproject fixture imports package paths that do not exist
// (example.com/sample/...), so two of its three packages fail to
// type-check while the third succeeds. That is the mid-refactor shape
// this whole mechanism exists for, and the report must name the failures
// without pasting an absolute path into a field that ends up in JSON, in
// the store and in golden files.
func TestTypedResolution_ReportsDegradedPackagesRepoRelatively(t *testing.T) {
	res := scanOrFail(t, "testdata/sampleproject", Options{})
	if res.Resolution == nil {
		t.Fatal("no resolution report")
	}
	if res.Resolution.Degraded == 0 {
		t.Fatalf("nothing degraded, but the fixture's imports are fictional: %+v", res.Resolution)
	}
	for _, d := range res.Resolution.DegradedPackages {
		if filepath.IsAbs(strings.SplitN(d.Error, ":", 2)[0]) {
			t.Errorf("degraded package %s reports an absolute path: %q", d.Path, d.Error)
		}
	}
}

// Typed resolution must not change which DECLARATIONS exist. It changes
// which declaration a call binds to; the declarations themselves come
// from the same walk either way, so a diff there would mean the typed
// path had started indexing (or dropping) files the ledger never
// mentioned.
//
// It does change one thing, and the earlier version of this test claimed
// otherwise. `external` stub nodes are synthesised by emitCallEdge for a
// name the AST ladder guessed at and could not find a declaration for
// ("sync.Mutex.Lock", rendered from source text). The typed path never
// reaches that branch: it only offers callees it has already matched to
// an indexed declaration, so a call into the standard library produces
// no node at all. The stubs legitimately disappear, and what disappears
// with them is a queryable node, so it is named in docs/languages/go.md
// beside the tier histogram rather than left for a caller to discover.
func TestTypedResolution_DropsOnlyExternalStubsFromTheSymbolSet(t *testing.T) {
	declared := func(res *Result) []string {
		out := make([]string, 0, len(res.Symbols))
		for _, s := range res.Symbols {
			if s.Kind != shared.KindExternal {
				out = append(out, string(s.ID))
			}
		}
		sort.Strings(out)
		return out
	}
	stubs := func(res *Result) map[shared.SymbolID]bool {
		out := map[shared.SymbolID]bool{}
		for _, s := range res.Symbols {
			if s.Kind == shared.KindExternal {
				out[s.ID] = true
			}
		}
		return out
	}

	for _, dir := range []string{goldenCorpusDir, authorityCorpusDir} {
		before := scanOrFail(t, dir, Options{SkipTypedResolution: true})
		after := scanOrFail(t, dir, Options{})

		if b, a := declared(before), declared(after); strings.Join(b, "\n") != strings.Join(a, "\n") {
			t.Errorf("%s: declared symbol set changed with typed resolution.\nwithout:\n%s\n\nwith:\n%s",
				dir, strings.Join(b, "\n"), strings.Join(a, "\n"))
		}
		for id := range stubs(after) {
			if !stubs(before)[id] {
				t.Errorf("%s: typed resolution INVENTED external stub %s", dir, id)
			}
		}
	}

	// The authority fixture is the one that makes the paragraph above
	// checkable: it calls sync.Mutex through a field, so the AST ladder
	// synthesises stubs there and the typed path does not. Without this
	// assertion the loop above would pass on a corpus with no stubs at
	// all and prove nothing.
	withoutTypes := stubs(scanOrFail(t, authorityCorpusDir, Options{SkipTypedResolution: true}))
	withTypes := stubs(scanOrFail(t, authorityCorpusDir, Options{}))
	if len(withoutTypes) == 0 {
		t.Fatal("the AST fallback synthesised no external stubs on the authority corpus; " +
			"the fixture no longer exercises the branch this test is about")
	}
	if len(withTypes) != 0 {
		t.Errorf("typed resolution kept %d external stubs: %v; if that is now intended, "+
			"this test and docs/languages/go.md both need updating", len(withTypes), withTypes)
	}
}
