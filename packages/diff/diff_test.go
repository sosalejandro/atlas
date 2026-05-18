package diff

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/codeindex/patterns"
	"github.com/sosalejandro/atlas/packages/contract"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// ----- Test helpers ------------------------------------------------------

func newIndex() *codeindex.Index {
	return &codeindex.Index{
		Root:           "/tmp/p",
		Graph:          graph.New(),
		SymbolLangs:    map[shared.SymbolID]string{},
		PatternMatches: map[shared.SymbolID][]patterns.Match{},
		FileHashes:     map[string]codeindex.FileHash{},
	}
}

func mkSym(id shared.SymbolID, path string, line int) shared.Symbol {
	return shared.Symbol{
		ID:        id,
		Kind:      shared.KindFunc,
		Position:  shared.FilePosition{Path: path, Line: line},
		Signature: "func " + string(id) + "()",
	}
}

func withSymbol(idx *codeindex.Index, s shared.Symbol) {
	idx.Symbols = append(idx.Symbols, s)
	idx.Graph.AddNode(&graph.Node{Symbol: s})
}

func mkAnn(kind shared.AnnotationKind, id, path string, line int) shared.Annotation {
	return shared.Annotation{
		Kind:     kind,
		IDs:      []string{id},
		Source:   shared.SourceAtlas,
		Position: shared.FilePosition{Path: path, Line: line},
	}
}

// ----- Per-dimension delta tests -----------------------------------------

func TestDiffFeatures_AddedRemovedChanged(t *testing.T) {
	a := newIndex()
	b := newIndex()
	a.Annotations = []shared.Annotation{
		mkAnn(shared.AnnFeature, "auth.login", "a.go", 10),
		mkAnn(shared.AnnFeature, "auth.signup", "a.go", 20),
		mkAnn(shared.AnnContract, "api.users", "b.go", 5),
	}
	b.Annotations = []shared.Annotation{
		mkAnn(shared.AnnFeature, "auth.login", "a.go", 12), // unchanged
		mkAnn(shared.AnnFeature, "pantry.add", "c.go", 7),  // added
		mkAnn(shared.AnnFeature, "api.users", "b.go", 5),   // changed: contract → feature
		// auth.signup removed
	}

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if len(d.Features.Added) != 1 || d.Features.Added[0].ID != "pantry.add" {
		t.Errorf("Added: %+v", d.Features.Added)
	}
	if len(d.Features.Removed) != 1 || d.Features.Removed[0].ID != "auth.signup" {
		t.Errorf("Removed: %+v", d.Features.Removed)
	}
	if len(d.Features.Changed) != 1 || d.Features.Changed[0].ID != "api.users" {
		t.Errorf("Changed: %+v", d.Features.Changed)
	}
	if d.Features.Changed[0].Before.Kind != "contract" || d.Features.Changed[0].After.Kind != "feature" {
		t.Errorf("kind change: before=%q after=%q",
			d.Features.Changed[0].Before.Kind, d.Features.Changed[0].After.Kind)
	}
}

func TestDiffSymbols_AddedRemovedChanged(t *testing.T) {
	a := newIndex()
	b := newIndex()
	withSymbol(a, mkSym("pkg.Foo", "x.go", 10))
	withSymbol(a, mkSym("pkg.Bar", "y.go", 20))
	withSymbol(b, mkSym("pkg.Foo", "x.go", 15)) // line shift = Changed
	withSymbol(b, mkSym("pkg.Baz", "z.go", 5))  // added; Bar removed

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.Symbols.Added) != 1 || d.Symbols.Added[0].ID != "pkg.Baz" {
		t.Errorf("Added: %+v", d.Symbols.Added)
	}
	if len(d.Symbols.Removed) != 1 || d.Symbols.Removed[0].ID != "pkg.Bar" {
		t.Errorf("Removed: %+v", d.Symbols.Removed)
	}
	if len(d.Symbols.Changed) != 1 || d.Symbols.Changed[0].ID != "pkg.Foo" {
		t.Errorf("Changed: %+v", d.Symbols.Changed)
	}
}

func TestDiffEdges_AddedRemoved(t *testing.T) {
	a := newIndex()
	b := newIndex()
	withSymbol(a, mkSym("A", "x.go", 1))
	withSymbol(a, mkSym("B", "x.go", 2))
	withSymbol(a, mkSym("C", "x.go", 3))
	a.Graph.AddEdge("A", "B")
	a.Graph.AddEdge("B", "C")

	withSymbol(b, mkSym("A", "x.go", 1))
	withSymbol(b, mkSym("B", "x.go", 2))
	withSymbol(b, mkSym("D", "x.go", 4))
	b.Graph.AddEdge("A", "B") // shared
	b.Graph.AddEdge("A", "D") // added

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.Edges.Added) != 1 || d.Edges.Added[0].To != "D" {
		t.Errorf("Added: %+v", d.Edges.Added)
	}
	if len(d.Edges.Removed) != 1 || d.Edges.Removed[0].To != "C" {
		t.Errorf("Removed: %+v", d.Edges.Removed)
	}
}

func TestDiffAnnotations_KindChangeIsChanged(t *testing.T) {
	a := newIndex()
	b := newIndex()
	// Same anchor (a.go:10), kind shifts feature → contract.
	a.Annotations = []shared.Annotation{
		mkAnn(shared.AnnFeature, "auth.login", "a.go", 10),
	}
	b.Annotations = []shared.Annotation{
		mkAnn(shared.AnnContract, "auth.login", "a.go", 10),
	}

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.Annotations.Changed) != 1 {
		t.Fatalf("Changed len = %d, want 1; %+v", len(d.Annotations.Changed), d.Annotations.Changed)
	}
	c := d.Annotations.Changed[0]
	if c.Before.Kind != shared.AnnFeature || c.After.Kind != shared.AnnContract {
		t.Errorf("kind change: before=%v after=%v", c.Before.Kind, c.After.Kind)
	}
	if len(d.Annotations.Added) != 0 || len(d.Annotations.Removed) != 0 {
		t.Errorf("kind change must NOT surface as Added/Removed; added=%+v removed=%+v",
			d.Annotations.Added, d.Annotations.Removed)
	}
}

func TestDiffPatternMatches_GainedOnZeroPriorMatches(t *testing.T) {
	// Edge case from the spec: pattern match gained on a symbol that
	// previously had zero pattern matches.
	a := newIndex()
	b := newIndex()
	withSymbol(a, mkSym("pkg.S", "x.go", 1))
	withSymbol(b, mkSym("pkg.S", "x.go", 1))
	// A has no matches at all.
	// B has 3 matches.
	b.PatternMatches["pkg.S"] = []patterns.Match{
		{Pattern: patterns.PatternOutboxAppend, Symbol: "pkg.S"},
		{Pattern: patterns.PatternCanonicalService, Symbol: "pkg.S"},
		{Pattern: patterns.PatternEventRecorderEmbed, Symbol: "pkg.S"},
	}

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.PatternMatches.Gained) != 3 {
		t.Errorf("Gained len = %d, want 3; %+v", len(d.PatternMatches.Gained), d.PatternMatches.Gained)
	}
	if len(d.PatternMatches.Lost) != 0 {
		t.Errorf("Lost expected empty; %+v", d.PatternMatches.Lost)
	}
}

// ----- Audit + Coverage --------------------------------------------------

func TestDiffAudit_NoiseFloor(t *testing.T) {
	a := []FeatureHealth{
		{FeatureID: "auth.login", Score: 80},
		{FeatureID: "auth.signup", Score: 70},
		{FeatureID: "api.users", Score: 90},
	}
	b := []FeatureHealth{
		{FeatureID: "auth.login", Score: 82},  // +2 — below default floor of 5
		{FeatureID: "auth.signup", Score: 65}, // -5 — at the floor → surfaces
		{FeatureID: "api.users", Score: 80},   // -10 — surfaces
	}
	eng := NewEngine(nil, Options{}) // default floor = 5
	d, err := eng.Compute(context.Background(), Snapshot{Index: newIndex(), Audit: a}, Snapshot{Index: newIndex(), Audit: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.Audit.Changed) != 2 {
		t.Fatalf("Changed len = %d, want 2; %+v", len(d.Audit.Changed), d.Audit.Changed)
	}
	// Make sure auth.login is NOT in Changed.
	for _, c := range d.Audit.Changed {
		if c.FeatureID == "auth.login" {
			t.Errorf("auth.login (+2) should be below noise floor; %+v", c)
		}
	}
}

func TestDiffAudit_MissingOnOneSide(t *testing.T) {
	// Edge case from the spec: one side missing audit data entirely.
	b := []FeatureHealth{
		{FeatureID: "auth.login", Score: 80},
		{FeatureID: "auth.signup", Score: 70},
	}
	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(),
		Snapshot{Index: newIndex(), Audit: nil},
		Snapshot{Index: newIndex(), Audit: b},
	)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.Audit.MissingOnA) != 2 {
		t.Errorf("MissingOnA len = %d, want 2; %+v", len(d.Audit.MissingOnA), d.Audit.MissingOnA)
	}
	if len(d.Audit.Added) != 0 || len(d.Audit.Removed) != 0 || len(d.Audit.Changed) != 0 {
		t.Errorf("missing-side audit must NOT surface as Added/Removed/Changed; %+v", d.Audit)
	}
}

func TestDiffCoverage_NoiseFloorAndFlip(t *testing.T) {
	a := []FeatureCoverage{
		{FeatureID: "auth.login", Passed: 10, Failed: 0, Total: 10, PassRate: 1.0},
		{FeatureID: "auth.signup", Passed: 10, Failed: 0, Total: 10, PassRate: 1.0},
		{FeatureID: "api.users", Passed: 9, Failed: 1, Total: 10, PassRate: 0.9},
	}
	b := []FeatureCoverage{
		{FeatureID: "auth.login", Passed: 9, Failed: 1, Total: 10, PassRate: 0.9},   // FLIP — must surface
		{FeatureID: "auth.signup", Passed: 10, Failed: 0, Total: 10, PassRate: 1.0}, // unchanged
		{FeatureID: "api.users", Passed: 5, Failed: 5, Total: 10, PassRate: 0.5},    // 40pp drop — surfaces
	}
	eng := NewEngine(nil, Options{}) // default floor = 5.0
	d, err := eng.Compute(context.Background(),
		Snapshot{Index: newIndex(), Coverage: a},
		Snapshot{Index: newIndex(), Coverage: b},
	)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(d.Coverage.Changed) != 2 {
		t.Fatalf("Changed len = %d, want 2; %+v", len(d.Coverage.Changed), d.Coverage.Changed)
	}
	var sawFlip bool
	for _, c := range d.Coverage.Changed {
		if c.FeatureID == "auth.login" {
			if !c.FlippedOff {
				t.Errorf("auth.login should FlippedOff=true; %+v", c)
			}
			sawFlip = true
		}
	}
	if !sawFlip {
		t.Error("expected auth.login flipped-off entry")
	}
}

// ----- Diff symmetry -----------------------------------------------------

func TestDiff_Symmetry(t *testing.T) {
	a := newIndex()
	b := newIndex()
	a.Annotations = []shared.Annotation{
		mkAnn(shared.AnnFeature, "auth.login", "a.go", 10),
		mkAnn(shared.AnnFeature, "auth.signup", "a.go", 20),
	}
	b.Annotations = []shared.Annotation{
		mkAnn(shared.AnnFeature, "auth.login", "a.go", 10),
		mkAnn(shared.AnnFeature, "pantry.add", "c.go", 7),
	}
	withSymbol(a, mkSym("pkg.A", "x.go", 1))
	withSymbol(a, mkSym("pkg.B", "x.go", 2))
	a.Graph.AddEdge("pkg.A", "pkg.B")

	withSymbol(b, mkSym("pkg.A", "x.go", 1))
	withSymbol(b, mkSym("pkg.C", "x.go", 3))
	b.Graph.AddEdge("pkg.A", "pkg.C")

	eng := NewEngine(nil, Options{})
	ab, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute AB: %v", err)
	}
	ba, err := eng.Compute(context.Background(), Snapshot{Index: b}, Snapshot{Index: a})
	if err != nil {
		t.Fatalf("Compute BA: %v", err)
	}

	// Features.Added(AB) == Features.Removed(BA) and vice versa.
	if !reflect.DeepEqual(idsOfFeatures(ab.Features.Added), idsOfFeatures(ba.Features.Removed)) {
		t.Errorf("features symmetry broken:\n  ab.Added=%v\n  ba.Removed=%v",
			idsOfFeatures(ab.Features.Added), idsOfFeatures(ba.Features.Removed))
	}
	if !reflect.DeepEqual(idsOfFeatures(ab.Features.Removed), idsOfFeatures(ba.Features.Added)) {
		t.Errorf("features symmetry broken (removed/added swap)")
	}

	// Symbols
	if !reflect.DeepEqual(idsOfSymbols(ab.Symbols.Added), idsOfSymbols(ba.Symbols.Removed)) {
		t.Errorf("symbols symmetry broken")
	}
	// Edges
	if !reflect.DeepEqual(edgeKeys(ab.Edges.Added), edgeKeys(ba.Edges.Removed)) {
		t.Errorf("edges symmetry broken:\n  ab.Added=%v\n  ba.Removed=%v",
			edgeKeys(ab.Edges.Added), edgeKeys(ba.Edges.Removed))
	}
}

// TestDiff_NoOpDiff covers the spec's "Compute(A, A)" empty contract.
func TestDiff_NoOpDiff(t *testing.T) {
	a := newIndex()
	a.Annotations = []shared.Annotation{
		mkAnn(shared.AnnFeature, "auth.login", "a.go", 10),
	}
	withSymbol(a, mkSym("pkg.A", "x.go", 1))
	withSymbol(a, mkSym("pkg.B", "x.go", 2))
	a.Graph.AddEdge("pkg.A", "pkg.B")
	a.PatternMatches["pkg.A"] = []patterns.Match{
		{Pattern: patterns.PatternOutboxAppend, Symbol: "pkg.A"},
	}

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: a})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if !d.IsEmpty() {
		t.Errorf("Compute(A, A) expected empty SnapshotDiff; got %+v", d)
	}
}

// ----- Spec-required edge cases ------------------------------------------

// TestDiff_ContractSignatureChangeIsChangedNotRemovedPlusAdded covers:
// "contract signature change vs contract-deleted-and-re-added (must
// classify as Changed, not Removed+Added)". When the FeatureID is stable
// across both sides but the signature shifts, that's a Changed — NOT a
// Removed+Added pair, even though the file/line/symbol all moved.
//
// Exercises the pre-extracted ContractDef path: Snapshot.Contracts is
// populated with the full ContractDef carrying Operation/Method/Path
// information, so the diff can detect signature-shape changes.
func TestDiff_ContractSignatureChangeIsChangedNotRemovedPlusAdded(t *testing.T) {
	fid := shared.FeatureID("api.users.create")
	defA := contract.ContractDef{
		Name:      "createUser",
		Kind:      contract.KindHumaOp,
		FilePath:  "old/handler.go",
		Line:      12,
		FeatureID: &fid,
		Signature: "POST /api/v1/users",
		Operation: contract.OperationDetail{
			Method:      "POST",
			Path:        "/api/v1/users",
			OperationID: "createUser",
		},
	}
	defB := contract.ContractDef{
		Name:      "createUser",
		Kind:      contract.KindHumaOp,
		FilePath:  "new/handler.go", // moved
		Line:      30,
		FeatureID: &fid,
		Signature: "POST /api/v2/users",
		Operation: contract.OperationDetail{
			Method:      "POST",
			Path:        "/api/v2/users", // path bumped
			OperationID: "createUser",
		},
	}

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(),
		Snapshot{Index: newIndex(), Contracts: []contract.ContractDef{defA}},
		Snapshot{Index: newIndex(), Contracts: []contract.ContractDef{defB}},
	)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if len(d.Contracts.Removed) != 0 || len(d.Contracts.Added) != 0 {
		t.Errorf("contract Removed+Added must not fire on stable FeatureID; got removed=%d added=%d",
			len(d.Contracts.Removed), len(d.Contracts.Added))
	}
	if len(d.Contracts.Changed) != 1 || d.Contracts.Changed[0].FeatureID != fid {
		t.Fatalf("contract Changed: %+v", d.Contracts.Changed)
	}
	if d.Contracts.Changed[0].Before.Operation.Path != "/api/v1/users" {
		t.Errorf("before path: %q", d.Contracts.Changed[0].Before.Operation.Path)
	}
	if d.Contracts.Changed[0].After.Operation.Path != "/api/v2/users" {
		t.Errorf("after path: %q", d.Contracts.Changed[0].After.Operation.Path)
	}
}

// TestDiff_ContractAnnotationOnlyMove covers the index-only fallback
// path. When no Snapshot.Contracts is provided, contract diff still
// preserves the "Changed not Removed+Added" guarantee at the FeatureID
// granularity — even though the signature richness is unavailable.
func TestDiff_ContractAnnotationOnlyMove(t *testing.T) {
	a := newIndex()
	b := newIndex()
	a.Annotations = []shared.Annotation{
		mkAnn(shared.AnnContract, "api.users.create", "old/handler.go", 12),
	}
	b.Annotations = []shared.Annotation{
		mkAnn(shared.AnnContract, "api.users.create", "new/handler.go", 30),
	}

	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: a}, Snapshot{Index: b})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	// No signature info available → no Changed surfaces, but critically
	// the contract must NOT show as Removed+Added either.
	if len(d.Contracts.Removed) != 0 || len(d.Contracts.Added) != 0 {
		t.Errorf("contract Removed+Added must not fire on stable FeatureID; got removed=%v added=%v",
			d.Contracts.Removed, d.Contracts.Added)
	}
	// Annotation level: the (file, line) anchor changed, so the raw
	// annotations correctly read as Removed+Added at that level.
	if len(d.Annotations.Removed) != 1 || len(d.Annotations.Added) != 1 {
		t.Errorf("annotation diff (anchor moved): removed=%v added=%v",
			d.Annotations.Removed, d.Annotations.Added)
	}
}

// ----- ComputeFromStore round-trip --------------------------------------

func TestDiff_EmptyOnBothSides(t *testing.T) {
	eng := NewEngine(nil, Options{})
	d, err := eng.Compute(context.Background(), Snapshot{Index: newIndex()}, Snapshot{Index: newIndex()})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if !d.IsEmpty() {
		t.Errorf("empty-vs-empty must be empty diff; %+v", d)
	}
}

func TestDiff_ComputeRequiresIndex(t *testing.T) {
	eng := NewEngine(nil, Options{})
	_, err := eng.Compute(context.Background(), Snapshot{}, Snapshot{Index: newIndex()})
	if err == nil {
		t.Fatal("expected error when A.Index == nil")
	}
}

func TestDiff_ComputeFromStoreRequiresStore(t *testing.T) {
	eng := NewEngine(nil, Options{})
	if _, err := eng.ComputeFromStore(context.Background(), 1, 2); err == nil {
		t.Fatal("expected error when engine has no Store")
	}
}

// ----- Local utility helpers --------------------------------------------

func idsOfFeatures(rows []FeatureRow) []shared.FeatureID {
	out := make([]shared.FeatureID, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func idsOfSymbols(syms []shared.Symbol) []shared.SymbolID {
	out := make([]shared.SymbolID, len(syms))
	for i, s := range syms {
		out[i] = s.ID
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func edgeKeys(es []EdgeRecord) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = edgeKey(e)
	}
	sort.Strings(out)
	return out
}
