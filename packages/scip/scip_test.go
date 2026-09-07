package scip_test

import (
	"bytes"
	"reflect"
	"testing"

	upstream "github.com/scip-code/scip/bindings/go/scip"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/scip"
	"google.golang.org/protobuf/proto"
)

// Fixtures are built as real scip.Index values and marshalled, so the tests
// exercise the same protobuf path a file from scip-java would take. Hand-
// writing bytes would test the parser; this tests the mapping.

func occ(sym string, roles int32, startLine, endLine int32) *upstream.Occurrence {
	o := &upstream.Occurrence{Symbol: sym, SymbolRoles: roles}
	o.SetSourceRange(upstream.Range{
		Start: upstream.Position{Line: startLine},
		End:   upstream.Position{Line: endLine},
	})
	return o
}

// def is a definition occurrence carrying an enclosing range -- the body the
// definition spans, which is what makes a reference attributable to it.
func def(sym string, declLine, bodyStart, bodyEnd int32) *upstream.Occurrence {
	o := occ(sym, int32(upstream.SymbolRole_Definition), declLine, declLine)
	o.SetEnclosingSourceRange(upstream.Range{
		Start: upstream.Position{Line: bodyStart},
		End:   upstream.Position{Line: bodyEnd},
	})
	return o
}

func index(docs ...*upstream.Document) *upstream.Index {
	return &upstream.Index{
		Metadata: &upstream.Metadata{
			ProjectRoot: "file:///repo",
			ToolInfo:    &upstream.ToolInfo{Name: "scip-java", Version: "0.10.1"},
		},
		Documents: docs,
	}
}

func doc(rel, lang string, occs ...*upstream.Occurrence) *upstream.Document {
	return &upstream.Document{RelativePath: rel, Language: lang, Occurrences: occs}
}

// roundTrip marshals and re-parses, so every test covers Read as well as
// Convert and a field that fails to serialise cannot pass.
func roundTrip(t *testing.T, idx *upstream.Index) scip.Result {
	t.Helper()
	raw, err := proto.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	parsed, err := scip.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("scip.Read: %v", err)
	}
	return scip.Convert(parsed)
}

const (
	svcRun  = "scip-java maven . . com/example/Service#run()."
	svcHelp = "scip-java maven . . com/example/Service#help()."
	extLog  = "scip-java maven . . org/slf4j/Logger#info()."
)

func TestConvert_DefinitionsBecomeSymbolsOnOneBasedLines(t *testing.T) {
	res := roundTrip(t, index(doc("src/Service.java", "java",
		def(svcRun, 9, 9, 20),
	)))

	if len(res.Symbols) != 1 {
		t.Fatalf("got %d symbols, want 1: %+v", len(res.Symbols), res.Symbols)
	}
	s := res.Symbols[0]
	// SCIP is 0-indexed; atlas is 1-indexed. Every coverage attribution
	// downstream joins a diff against these spans, so an off-by-one here is
	// an off-by-one in every number that follows.
	if s.Position.Line != 10 {
		t.Errorf("line = %d, want 10 (SCIP line 9, 1-indexed)", s.Position.Line)
	}
	if s.EndLine != 21 {
		t.Errorf("end_line = %d, want 21 (enclosing range end, 1-indexed)", s.EndLine)
	}
	if s.Position.Path != "src/Service.java" {
		t.Errorf("path = %q, want the document's relative path", s.Position.Path)
	}
	if got, want := string(s.ID), "com.example.Service.run"; got != want {
		t.Errorf("id = %q, want %q", got, want)
	}
	if res.Tool != "scip-java 0.10.1" {
		t.Errorf("tool = %q; an imported edge with no attribution looks like ours", res.Tool)
	}
	if res.Languages["java"] != 1 {
		t.Errorf("languages = %v, want java:1", res.Languages)
	}
}

// The edge case in both senses: a reference inside a definition's body is
// the only thing that produces an edge, and it must carry TierImported.
func TestConvert_ReferenceInsideADefinitionBecomesAnImportedEdge(t *testing.T) {
	res := roundTrip(t, index(doc("src/Service.java", "java",
		def(svcRun, 9, 9, 20),
		def(svcHelp, 30, 30, 40),
		occ(svcHelp, 0, 12, 12), // run() calls help()
	)))

	if len(res.Edges) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(res.Edges), res.Edges)
	}
	e := res.Edges[0]
	if string(e.From) != "com.example.Service.run" || string(e.To) != "com.example.Service.help" {
		t.Errorf("edge = %s -> %s, want run -> help", e.From, e.To)
	}
	if e.Tier != graph.TierImported {
		t.Errorf("tier = %q, want %q: atlas did not type-check this, scip-java did",
			e.Tier, graph.TierImported)
	}
	if e.Line != 13 {
		t.Errorf("line = %d, want 13 (SCIP line 12, 1-indexed)", e.Line)
	}
}

// #105's acceptance criterion, and the reason this package has counters at
// all: "an ingest that resolves nothing looks identical to a clean no-op".
func TestConvert_AnIngestThatResolvesNothingIsNotACleanNoOp(t *testing.T) {
	// Every reference points outside the index -- a real shape, e.g. an
	// indexer run without its dependencies.
	res := roundTrip(t, index(doc("src/Service.java", "java",
		def(svcRun, 9, 9, 20),
		occ(extLog, 0, 12, 12),
		occ(extLog, 0, 14, 14),
	)))

	if len(res.Edges) != 0 {
		t.Fatalf("expected no edges, got %+v", res.Edges)
	}
	if res.Stats.ReferencesUnresolved != 2 {
		t.Errorf("references_unresolved = %d, want 2; an empty graph with a zero "+
			"counter is indistinguishable from a clean run",
			res.Stats.ReferencesUnresolved)
	}
	if !res.Stats.Reconciles() {
		t.Errorf("stats do not reconcile: %+v", res.Stats)
	}
}

// Every reference lands in exactly one bucket. A mapping bug that drops
// occurrences would otherwise just produce a smaller graph nobody questions.
func TestConvert_EveryReferenceIsAccountedForExactlyOnce(t *testing.T) {
	res := roundTrip(t, index(
		doc("src/Service.java", "java",
			def(svcRun, 9, 9, 20),
			def(svcHelp, 30, 30, 40),
			occ(svcHelp, 0, 12, 12),   // -> edge
			occ(extLog, 0, 13, 13),    // -> unresolved
			occ("local 3", 0, 14, 14), // -> local
			occ(svcHelp, 0, 2, 2),     // -> outside any definition (an import line)
		),
	))

	st := res.Stats
	if st.References != 4 {
		t.Fatalf("references = %d, want 4: %+v", st.References, st)
	}
	if !st.Reconciles() {
		t.Errorf("references (%d) != edges (%d) + local (%d) + unresolved (%d) + outside (%d)",
			st.References, st.Edges, st.ReferencesLocal,
			st.ReferencesUnresolved, st.ReferencesOutsideDefinition)
	}
	if st.Edges != 1 || st.ReferencesLocal != 1 ||
		st.ReferencesUnresolved != 1 || st.ReferencesOutsideDefinition != 1 {
		t.Errorf("buckets = %+v, want one in each", st)
	}
}

// A function-scoped symbol cannot be referenced from another file and atlas
// keys symbols by qualified name. Dropped, but counted.
func TestConvert_LocalDefinitionsAreDroppedAndCounted(t *testing.T) {
	res := roundTrip(t, index(doc("src/Service.java", "java",
		def(svcRun, 9, 9, 20),
		def("local 7", 11, 11, 12),
	)))

	if len(res.Symbols) != 1 {
		t.Errorf("got %d symbols, want 1 (the local one is not addressable): %+v",
			len(res.Symbols), res.Symbols)
	}
	if res.Stats.DefinitionsLocal != 1 {
		t.Errorf("definitions_local = %d, want 1", res.Stats.DefinitionsLocal)
	}
}

// A method inside a class yields two containing definitions where the
// indexer emits enclosing ranges for both. Attributing the call to the class
// would put the edge on the wrong node.
func TestConvert_InnermostEnclosingDefinitionWins(t *testing.T) {
	const cls = "scip-java maven . . com/example/Service#"
	res := roundTrip(t, index(doc("src/Service.java", "java",
		def(cls, 5, 5, 60),    // the class body
		def(svcRun, 9, 9, 20), // the method inside it
		def(svcHelp, 30, 30, 40),
		occ(svcHelp, 0, 12, 12), // inside BOTH; belongs to run()
	)))

	if len(res.Edges) != 1 {
		t.Fatalf("got %d edges, want 1: %+v", len(res.Edges), res.Edges)
	}
	if got := string(res.Edges[0].From); got != "com.example.Service.run" {
		t.Errorf("edge attributed to %q, want the innermost definition", got)
	}
}

// An indexer that emits no enclosing ranges cannot produce edges. That must
// read as "this index cannot attribute calls", not as "this code has none".
func TestConvert_NoEnclosingRangesMeansNoEdgesButALoudCounter(t *testing.T) {
	res := roundTrip(t, index(doc("src/Service.java", "java",
		occ(svcRun, int32(upstream.SymbolRole_Definition), 9, 9),
		occ(svcHelp, int32(upstream.SymbolRole_Definition), 30, 30),
		occ(svcHelp, 0, 12, 12),
	)))

	if len(res.Edges) != 0 {
		t.Fatalf("edges without an enclosing range: %+v", res.Edges)
	}
	if res.Stats.ReferencesOutsideDefinition != 1 {
		t.Errorf("references_outside_definition = %d, want 1",
			res.Stats.ReferencesOutsideDefinition)
	}
	if len(res.Symbols) != 2 {
		t.Errorf("definitions must still be indexed without enclosing ranges: %+v", res.Symbols)
	}
}

// Two ingests of the same bytes must produce identical output (#120, #162).
func TestConvert_IsDeterministic(t *testing.T) {
	build := func() *upstream.Index {
		return index(
			doc("src/B.java", "java", def(svcHelp, 30, 30, 40)),
			doc("src/A.java", "java", def(svcRun, 9, 9, 20), occ(svcHelp, 0, 12, 12)),
		)
	}
	a, b := roundTrip(t, build()), roundTrip(t, build())
	if !reflect.DeepEqual(a.Symbols, b.Symbols) {
		t.Errorf("symbol order is not stable:\n%+v\n%+v", a.Symbols, b.Symbols)
	}
	if !reflect.DeepEqual(a.Edges, b.Edges) {
		t.Errorf("edge order is not stable:\n%+v\n%+v", a.Edges, b.Edges)
	}
	// Sorted by path, so B.java's symbol cannot come first.
	if len(a.Symbols) == 2 && a.Symbols[0].Position.Path != "src/A.java" {
		t.Errorf("symbols are not in path order: %+v", a.Symbols)
	}
}

// A broken pipeline and a real-but-empty answer are different, and only one
// of them is an error.
func TestRead_EmptyInputIsAnErrorButAnEmptyIndexIsNot(t *testing.T) {
	if _, err := scip.Read(bytes.NewReader(nil)); err == nil {
		t.Error("0 bytes parsed as an index; a broken pipeline must not look like an empty repo")
	}
	raw, err := proto.Marshal(index())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := scip.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("a valid index with no documents must parse: %v", err)
	}
	res := scip.Convert(parsed)
	if len(res.Symbols) != 0 || len(res.Edges) != 0 {
		t.Errorf("an empty index produced content: %+v", res)
	}
}

// An unparseable symbol is kept verbatim: an unreadable id still resolves
// references consistently within one index, which is what an edge needs.
func TestConvert_UnparseableSymbolIsKeptNotDropped(t *testing.T) {
	const weird = "not-a-scip-symbol"
	res := roundTrip(t, index(doc("src/x.rb", "ruby", def(weird, 1, 1, 5))))
	if len(res.Symbols) != 1 {
		t.Fatalf("dropped an unparseable symbol: %+v", res.Symbols)
	}
	if string(res.Symbols[0].ID) != weird {
		t.Errorf("id = %q, want the symbol verbatim", res.Symbols[0].ID)
	}
}
