package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/sosalejandro/grunnr/packages/graph"
	"github.com/sosalejandro/grunnr/packages/store"
)

// tieredEdge inserts a call edge at a chosen tier and ambiguity.
//
// repo.edge hardcodes TierNameResolved, which is fine for tests about
// something else and useless for tests about the tier: a fixture that only
// ever produces one value cannot tell a projection that forwards the field
// from one that hardcodes the same constant.
func (r *repo) tieredEdge(
	t *testing.T, from, to, file string, line int, tier graph.ResolutionTier, ambiguous bool,
) {
	t.Helper()
	_, err := r.s.Edges().Insert(context.Background(), store.EdgeRow{
		Tier: tier, Ambiguous: ambiguous,
		FromID: r.ids[from], ToID: r.ids[to],
		Kind: store.EdgeKindCall, FilePath: file, Line: line,
	})
	if err != nil {
		t.Fatalf("insert %s edge %s -> %s: %v", tier, from, to, err)
	}
}

// neighbourByName pulls one row out of a callers/callees list.
func neighbourByName(t *testing.T, raw any, qualified string) map[string]any {
	t.Helper()
	rows, ok := raw.([]any)
	if !ok {
		t.Fatalf("neighbour list is %T, want a JSON array", raw)
	}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if m["qualified_name"] == qualified {
			return m
		}
	}
	t.Fatalf("no neighbour %q in %v", qualified, rows)
	return nil
}

// The defect #175 names: the agent-facing surface projected edges into its own
// shape and dropped the one field that separates grunnr from a grep.
//
// A syntactic edge may name a symbol that does not exist, or the wrong one of
// several with the same name. Arriving indistinguishable from a typed edge, on
// a surface built for consumers that ACT on the list, it becomes a wrong edit
// in a file nobody asked to be touched.
func TestCallers_ForwardTheResolutionTier(t *testing.T) {
	r := seedRepo(t)
	pkg := "checkout"
	r.insert(t, "pkg/checkout.Guessed", "pkg/checkout/guess.go", 8, &pkg)
	r.tieredEdge(t, "pkg/checkout.Guessed", symPay, "pkg/checkout/guess.go", 9,
		graph.TierSyntactic, false)
	w := r.wire(t)

	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symPay}))

	guess := neighbourByName(t, sc["callers"], "pkg/checkout.Guessed")
	if guess["resolution_tier"] != string(graph.TierSyntactic) {
		t.Errorf("syntactic caller reports resolution_tier=%v, want %q",
			guess["resolution_tier"], graph.TierSyntactic)
	}
	// The seeded edge from repo.edge is name_resolved. Both must be present
	// and distinguishable, or the field is decoration.
	sure := neighbourByName(t, sc["callers"], symTestPay)
	if sure["resolution_tier"] != string(graph.TierNameResolved) {
		t.Errorf("name_resolved caller reports resolution_tier=%v, want %q",
			sure["resolution_tier"], graph.TierNameResolved)
	}
}

// resolution_tier carries no omitempty, so it is present on every row even
// when a caller might prefer the shorter payload. An absent tier reads as
// "this edge is fine", which is the one thing grunnr must never imply.
func TestCallers_TierIsAlwaysPresentNeverOmitted(t *testing.T) {
	r := seedRepo(t)
	w := r.wire(t)
	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symPay}))

	rows, _ := sc["callers"].([]any)
	if len(rows) == 0 {
		t.Fatal("no callers seeded; this test would pass vacuously")
	}
	for _, row := range rows {
		m, _ := row.(map[string]any)
		if _, ok := m["resolution_tier"]; !ok {
			t.Errorf("caller %v has no resolution_tier key at all", m["qualified_name"])
		}
	}
}

// Ambiguous is orthogonal to the tier and matters for the same reason: it
// marks the sites where grunnr chose between candidates.
func TestCallers_ForwardAmbiguity(t *testing.T) {
	r := seedRepo(t)
	pkg := "checkout"
	r.insert(t, "pkg/checkout.Picked", "pkg/checkout/pick.go", 4, &pkg)
	r.tieredEdge(t, "pkg/checkout.Picked", symPay, "pkg/checkout/pick.go", 5,
		graph.TierNameResolved, true)
	w := r.wire(t)

	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symPay}))

	picked := neighbourByName(t, sc["callers"], "pkg/checkout.Picked")
	if picked["ambiguous"] != true {
		t.Errorf("ambiguous edge reports ambiguous=%v, want true", picked["ambiguous"])
	}
	// A name_resolved edge CAN be ambiguous, so ambiguity must not be
	// inferred from the tier.
	if picked["resolution_tier"] != string(graph.TierNameResolved) {
		t.Errorf("resolution_tier=%v; ambiguity must not be conflated with the tier",
			picked["resolution_tier"])
	}
	unambiguous := neighbourByName(t, sc["callers"], symTestPay)
	if _, present := unambiguous["ambiguous"]; present {
		t.Errorf("an unambiguous edge emitted an ambiguous key: %v", unambiguous)
	}
}

// An agent handed forty rows will not tally them. The provenance block is the
// sentence that changes what it does next.
func TestCallers_ProvenanceCountsAndNamesTheDoubt(t *testing.T) {
	r := seedRepo(t)
	pkg := "checkout"
	r.insert(t, "pkg/checkout.G1", "pkg/checkout/g1.go", 4, &pkg)
	r.insert(t, "pkg/checkout.G2", "pkg/checkout/g2.go", 4, &pkg)
	r.tieredEdge(t, "pkg/checkout.G1", symPay, "pkg/checkout/g1.go", 5, graph.TierSyntactic, false)
	r.tieredEdge(t, "pkg/checkout.G2", symPay, "pkg/checkout/g2.go", 5, graph.TierSyntactic, true)
	w := r.wire(t)

	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symPay}))
	prov, ok := sc["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("no provenance block: %v", sc)
	}
	byTier, _ := prov["by_tier"].(map[string]any)
	if got := byTier[string(graph.TierSyntactic)]; got != float64(2) {
		t.Errorf("by_tier[syntactic] = %v, want 2", got)
	}
	if got := byTier[string(graph.TierNameResolved)]; got != float64(1) {
		t.Errorf("by_tier[name_resolved] = %v, want 1", got)
	}
	if got := prov["ambiguous"]; got != float64(1) {
		t.Errorf("ambiguous = %v, want 1", got)
	}
	note, _ := prov["note"].(string)
	if !strings.Contains(note, "2") || !strings.Contains(note, "guess") {
		t.Errorf("note does not state how much of this is guessed: %q", note)
	}
}

// A clean list must not be described in the same words as a guessed one, or
// the note is noise an agent learns to skip.
func TestCallers_ProvenanceSaysSoWhenNothingIsGuessed(t *testing.T) {
	r := seedRepo(t)
	w := r.wire(t)
	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symPay}))

	prov, _ := sc["provenance"].(map[string]any)
	note, _ := prov["note"].(string)
	if strings.Contains(note, "guess") {
		t.Errorf("a fully name_resolved list is described as guessed: %q", note)
	}
	// It must still refuse to claim completeness: dynamic dispatch is absent
	// from the index whatever the tiers say.
	if !strings.Contains(note, "lower bound") {
		t.Errorf("note claims a complete answer: %q", note)
	}
}

// Empty is not proof. An agent that reads `callers: []` as "nothing calls
// this" will delete a symbol reached only through an interface.
func TestCallers_EmptyListIsNotReportedAsProofOfNoCallers(t *testing.T) {
	r := seedRepo(t)
	pkg := "checkout"
	const isolated = "pkg/checkout.OnlyReachedByAnInterface"
	r.insert(t, isolated, "pkg/checkout/iface.go", 3, &pkg)
	w := r.wire(t)
	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": isolated}))

	if rows, _ := sc["callers"].([]any); len(rows) != 0 {
		t.Fatalf("expected no callers for %s, got %v", isolated, rows)
	}
	prov, _ := sc["provenance"].(map[string]any)
	note, _ := prov["note"].(string)
	if !strings.Contains(note, "not proof") {
		t.Errorf("an empty caller list does not disclaim proof: %q", note)
	}
}

// The counts describe the rows RETURNED. On a capped result that is a subset,
// and "1 of 2 are guesses" read as a statement about two hundred edges is the
// overclaim the block exists to prevent.
func TestCallers_ProvenanceNamesItsPopulationWhenTruncated(t *testing.T) {
	r := seedRepo(t)
	pkg := "checkout"
	for _, n := range []string{"A", "B", "C"} {
		r.insert(t, "pkg/checkout.C"+n, "pkg/checkout/c"+n+".go", 4, &pkg)
		r.tieredEdge(t, "pkg/checkout.C"+n, symPay, "pkg/checkout/c"+n+".go", 5,
			graph.TierSyntactic, false)
	}
	w := r.wire(t)

	sc := w.structured(w.call(2, "callers", map[string]any{
		"qualified_name": symPay, "limit": 2,
	}))
	if _, ok := sc["truncated"]; !ok {
		t.Fatal("expected a truncated block; the test cannot check the capped note without one")
	}
	prov, _ := sc["provenance"].(map[string]any)
	note, _ := prov["note"].(string)
	if !strings.Contains(note, "shown") {
		t.Errorf("a capped result describes its counts as if they covered everything: %q", note)
	}
}

// The tier is only useful if the agent is told to read it. #163 will measure
// this behaviourally; until then, pin that the instruction is present at all.
func TestCatalog_CallGraphToolsTellTheAgentToReadTheTier(t *testing.T) {
	r := seedRepo(t)
	ts := &toolset{limits: Limits{}.withDefaults()}
	_ = r
	for _, tl := range ts.graphTools() {
		if tl.Name != "callers" && tl.Name != "callees" {
			continue
		}
		d := tl.Description
		for _, must := range []string{"resolution_tier", "syntactic", "ambiguous"} {
			if !strings.Contains(d, must) {
				t.Errorf("%s description never mentions %q: %s", tl.Name, must, d)
			}
		}
		if !strings.Contains(strings.ToLower(d), "guess") {
			t.Errorf("%s description does not call a syntactic edge a guess: %s", tl.Name, d)
		}
	}
}
