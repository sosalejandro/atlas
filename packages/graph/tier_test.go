package graph

import "testing"

// The tier vocabulary is the one #105's addendum defines (A/B/C/D:
// typed / name_resolved / syntactic / imported). These tests pin the
// vocabulary itself, because the whole point of #146 is that a second
// taxonomy invented later would make two scanners' provenance
// incomparable — and an incomparable histogram is the same blindness
// the column exists to remove.
func TestResolutionTier_Vocabulary(t *testing.T) {
	valid := []ResolutionTier{TierTyped, TierNameResolved, TierSyntactic, TierImported}
	want := []string{"typed", "name_resolved", "syntactic", "imported"}
	for i, tier := range valid {
		if string(tier) != want[i] {
			t.Errorf("tier %d = %q, want %q", i, tier, want[i])
		}
		if !IsValidTier(tier) {
			t.Errorf("IsValidTier(%q) = false, want true", tier)
		}
	}
}

// The zero value must NOT be a tier. A valid zero value is how every
// edge ends up claiming to be typed: a producer that forgets the field
// gets whatever the first constant happens to be.
func TestResolutionTier_ZeroValueIsNotATier(t *testing.T) {
	var unset ResolutionTier
	if unset != TierUnset {
		t.Fatalf("zero value = %q, want TierUnset", unset)
	}
	if IsValidTier(TierUnset) {
		t.Fatal("IsValidTier(TierUnset) = true; the absence of a tier must never validate")
	}
	if IsValidTier("ast_inferred") {
		t.Fatal("IsValidTier accepted a foreign taxonomy's value")
	}
}

// AddEdgeTier is the constructor a scanner uses: the tier is a
// parameter, so a producer cannot emit an edge without stating how it
// resolved it.
func TestAddEdgeTier_RecordsTier(t *testing.T) {
	g := New()
	g.AddNode(&Node{})
	g.AddEdgeTier("A", "B", TierNameResolved)
	g.AddAmbiguousEdgeTier("A", "C", TierSyntactic)

	if len(g.Edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(g.Edges))
	}
	if g.Edges[0].Tier != TierNameResolved {
		t.Errorf("edge[0].Tier = %q, want %q", g.Edges[0].Tier, TierNameResolved)
	}
	if g.Edges[0].Ambiguous {
		t.Error("edge[0].Ambiguous = true, want false")
	}
	if g.Edges[1].Tier != TierSyntactic {
		t.Errorf("edge[1].Tier = %q, want %q", g.Edges[1].Tier, TierSyntactic)
	}
	if !g.Edges[1].Ambiguous {
		t.Error("edge[1].Ambiguous = false, want true")
	}
}

// The provenance-free constructors still exist for in-memory consumers
// (diff, chain, tests) that never persist. They must leave the tier
// UNSET rather than picking one, so the store can reject them by name.
func TestAddEdge_LeavesTierUnset(t *testing.T) {
	g := New()
	g.AddEdge("A", "B")
	g.AddEdgeKindLineMeta("A", "C", "import", 4, "module")
	for i, e := range g.Edges {
		if e.Tier != TierUnset {
			t.Errorf("edge[%d].Tier = %q, want unset", i, e.Tier)
		}
	}
}

// Cycle marking is orthogonal to provenance: the tier-carrying
// constructor must still close cycles the way AddEdge does, or the
// scanners that move onto it would silently stop reporting them.
func TestAddEdgeTier_StillDetectsCycles(t *testing.T) {
	g := New()
	g.AddEdgeTier("A", "B", TierTyped)
	g.AddEdgeTier("B", "A", TierTyped)
	if g.Edges[0].Cycle {
		t.Error("edge[0].Cycle = true, want false")
	}
	if !g.Edges[1].Cycle {
		t.Error("edge[1].Cycle = false, want true (B->A closes A->B)")
	}
}
