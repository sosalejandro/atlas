// Package scip ingests an index produced by somebody else's indexer.
//
// This is #105's Tier 3, and it is the tier that costs atlas nothing to
// support: SCIP is a published interchange format with existing indexers for
// Java, Python, Ruby, C#, Rust and more, so ingesting it buys polyglot
// coverage without atlas writing a scanner, a grammar, or a binding rule for
// any of them.
//
// What it does NOT buy is a claim about correctness, and the whole design
// here turns on keeping those apart. Every edge produced by this package is
// graph.TierImported, whose doc comment states the reason plainly: "scip-go
// said so" and "we type-checked it" are different claims even when they
// usually agree. Atlas can offer the coverage and still tell a user exactly
// how much of their graph rests on someone else's work, because every
// consumer already gates on tiers.
//
// The honesty counters on Result are not decoration. #105's acceptance
// criteria name the failure they exist to prevent: without them "an ingest
// that resolves nothing looks identical to a clean no-op". That is the same
// invariant as stmts_unattributed (#100) one layer up, and it is the
// difference between a polyglot index and a reassuring empty one.
package scip

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	upstream "github.com/scip-code/scip/bindings/go/scip"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// roleDefinition is SCIP's SymbolRole bit for "this occurrence declares the
// symbol" rather than referring to it.
const roleDefinition = int32(upstream.SymbolRole_Definition)

// Result is one index.scip, converted.
type Result struct {
	// Tool is what produced the index ("scip-java 0.10.1"), carried so a
	// reader of the store can tell whose fidelity these edges have. An
	// unattributed imported edge is worse than no edge: it looks like ours.
	Tool string
	// ProjectRoot is the root the indexer recorded, kept for the same reason.
	ProjectRoot string

	Symbols []shared.Symbol
	Edges   []graph.Edge

	// Languages counts definitions per SCIP language tag, so `atlas scip
	// ingest` can say WHAT it imported rather than only how much.
	Languages map[string]int

	Stats Stats
}

// Stats is the arithmetic that has to add up, and the reason this package
// cannot report a silent success.
//
// Every reference occurrence lands in exactly one of Edges, ReferencesLocal,
// ReferencesUnresolved or ReferencesOutsideDefinition. Reconciles() checks
// that, so a mapping bug that quietly drops occurrences fails a test rather
// than producing a smaller graph nobody questions.
type Stats struct {
	Documents int `json:"documents"`
	// Occurrences is every occurrence in the index, definitions included.
	Occurrences int `json:"occurrences"`
	// Definitions became symbols.
	Definitions int `json:"definitions"`
	// DefinitionsLocal were function-scoped ("local 4"). They cannot be
	// referenced from another file and atlas keys symbols by qualified
	// name, so they are dropped -- counted, not hidden.
	DefinitionsLocal int `json:"definitions_local"`
	// References is every non-definition occurrence.
	References int `json:"references"`
	// ReferencesLocal pointed at a local symbol.
	ReferencesLocal int `json:"references_local"`
	// ReferencesUnresolved named a symbol this index never defines -- a
	// call into a dependency the indexer did not index. Expected, and
	// expected to be LARGE; it is the count that says how much of the
	// graph's edges lead outside it.
	ReferencesUnresolved int `json:"references_unresolved"`
	// ReferencesOutsideDefinition sat in no definition's enclosing range,
	// so there is no caller to attribute them to: imports, package
	// declarations, top-level type annotations. An indexer that emits no
	// enclosing ranges at all puts every reference here, which is the
	// signal that this index cannot produce edges rather than that the
	// code has none.
	ReferencesOutsideDefinition int `json:"references_outside_definition"`
	// Edges is len(Result.Edges), restated so the JSON envelope carries
	// the whole reconciliation without the caller recomputing it.
	Edges int `json:"edges"`
}

// Reconciles reports whether every reference was accounted for exactly once.
// A false here is a bug in this package, not a property of the input.
func (s Stats) Reconciles() bool {
	return s.References == s.Edges+s.ReferencesLocal+
		s.ReferencesUnresolved+s.ReferencesOutsideDefinition
}

// Read parses an index.scip.
func Read(r io.Reader) (*upstream.Index, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	if len(data) == 0 {
		// Distinguished from a valid index with no documents: one is a
		// broken pipeline, the other is a real (if useless) answer.
		return nil, fmt.Errorf("the index is empty (0 bytes)")
	}
	var idx upstream.Index
	if err := unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// Convert maps a parsed index onto atlas symbols and edges.
//
// Paths are taken from the SCIP document's RelativePath verbatim, which is
// already project-root-relative by the format's definition. They are NOT
// rejoined against ProjectRoot: the indexer may have run on another machine,
// and an absolute path from it would key symbols nothing here can match.
func Convert(idx *upstream.Index) Result {
	res := Result{Languages: map[string]int{}}
	if idx == nil {
		return res
	}
	if m := idx.GetMetadata(); m != nil {
		res.ProjectRoot = m.GetProjectRoot()
		if ti := m.GetToolInfo(); ti != nil {
			res.Tool = strings.TrimSpace(ti.GetName() + " " + ti.GetVersion())
		}
	}

	// Pass 1: every definition in the index, so pass 2 can tell a reference
	// atlas can attribute from one that leads out of the index.
	defs := map[string]*definition{}
	for _, doc := range idx.GetDocuments() {
		res.Stats.Documents++
		collectDefinitions(doc, defs, &res)
	}

	// Pass 2: references become edges, attributed to the definition whose
	// enclosing range contains them.
	for _, doc := range idx.GetDocuments() {
		collectEdges(doc, defs, &res)
	}

	res.Stats.Edges = len(res.Edges)
	sortResult(&res)
	return res
}

// definition is a symbol this index declares, and where.
type definition struct {
	sym shared.Symbol
	// enclosing is the definition's whole body range, 0-indexed and
	// half-open on the end, exactly as SCIP records it. A reference is
	// attributed to this definition when it falls inside.
	startLine, endLine int32
	hasEnclosing       bool
}

func collectDefinitions(doc *upstream.Document, defs map[string]*definition, res *Result) {
	rel := path.Clean(doc.GetRelativePath())
	for _, occ := range doc.GetOccurrences() {
		res.Stats.Occurrences++
		if occ.GetSymbolRoles()&roleDefinition == 0 {
			continue
		}
		res.Stats.Definitions++
		sym := occ.GetSymbol()
		if upstream.IsLocalSymbol(sym) {
			res.Stats.DefinitionsLocal++
			continue
		}
		rng, ok := occ.SourceRange()
		if !ok {
			continue
		}
		d := &definition{
			sym: shared.Symbol{
				ID: shared.SymbolID(qualifiedName(sym)),
				// KindFunc for everything, deliberately. SCIP carries a
				// SymbolInformation.Kind, but it is optional and most
				// indexers leave it unset; inventing a distinction from
				// the descriptor suffix would be atlas guessing about
				// someone else's index, which is exactly what TierImported
				// exists to avoid claiming.
				Kind: shared.KindFunc,
				Position: shared.FilePosition{
					Path: rel,
					// SCIP lines are 0-indexed; atlas's are 1-indexed, and
					// every consumer that joins a diff against a span
					// depends on that. Off by one here is off by one in
					// every coverage attribution downstream.
					Line: int(rng.Start.Line) + 1,
				},
				EndLine: int(rng.End.Line) + 1,
			},
			startLine: rng.Start.Line,
			endLine:   rng.End.Line,
		}
		if enc, encOK := occ.EnclosingSourceRange(); encOK {
			d.startLine, d.endLine = enc.Start.Line, enc.End.Line
			d.hasEnclosing = true
			d.sym.EndLine = int(enc.End.Line) + 1
		}
		if lang := doc.GetLanguage(); lang != "" {
			res.Languages[lang]++
		}
		// Last definition wins, deterministically: documents are visited in
		// index order and occurrences within a document in theirs. A symbol
		// defined twice is the indexer's statement, not ours to arbitrate.
		defs[sym] = d
		res.Symbols = append(res.Symbols, d.sym)
	}
}

func collectEdges(doc *upstream.Document, defs map[string]*definition, res *Result) {
	rel := path.Clean(doc.GetRelativePath())
	// The definitions declared in THIS document, which are the only
	// candidates for enclosing a reference in it.
	var local []*definition
	for _, occ := range doc.GetOccurrences() {
		if occ.GetSymbolRoles()&roleDefinition == 0 {
			continue
		}
		if d, ok := defs[occ.GetSymbol()]; ok && d.sym.Position.Path == rel {
			local = append(local, d)
		}
	}

	for _, occ := range doc.GetOccurrences() {
		if occ.GetSymbolRoles()&roleDefinition != 0 {
			continue
		}
		res.Stats.References++
		target := occ.GetSymbol()
		if upstream.IsLocalSymbol(target) {
			res.Stats.ReferencesLocal++
			continue
		}
		td, defined := defs[target]
		if !defined {
			// A call into something this index does not define. Expected,
			// and expected to be large -- it is how much of the graph
			// leads outside the indexed set.
			res.Stats.ReferencesUnresolved++
			continue
		}
		rng, ok := occ.SourceRange()
		if !ok {
			res.Stats.ReferencesOutsideDefinition++
			continue
		}
		caller := enclosing(local, rng.Start.Line)
		if caller == nil {
			res.Stats.ReferencesOutsideDefinition++
			continue
		}
		if caller.sym.ID == td.sym.ID {
			// A recursive call is a real edge, but atlas's graph treats a
			// self-edge as a cycle of length one, and every renderer then
			// draws it. Counted as attributed rather than dropped.
			res.Stats.ReferencesOutsideDefinition++
			continue
		}
		res.Edges = append(res.Edges, graph.Edge{
			From: caller.sym.ID,
			To:   td.sym.ID,
			Kind: "call",
			// 1-based, like every other producer. Edge.Line is what lets a
			// reader drill from an edge to its true call site.
			Line: int(rng.Start.Line) + 1,
			Tier: graph.TierImported,
		})
	}
}

// enclosing returns the innermost definition containing line, or nil.
//
// Innermost, not first: a method inside a class yields two containing
// definitions in languages that emit enclosing ranges for both, and
// attributing a call to the class rather than the method would put the edge
// on the wrong node.
func enclosing(defs []*definition, line int32) *definition {
	var best *definition
	for _, d := range defs {
		if !d.hasEnclosing || line < d.startLine || line > d.endLine {
			continue
		}
		if best == nil || d.startLine > best.startLine {
			best = d
		}
	}
	return best
}

// qualifiedName renders a SCIP symbol string as something a human can read
// and atlas can key on.
//
// SCIP symbols look like:
//
//	scip-java maven . . com/example/Service#run().
//
// The descriptors carry the structure; the scheme, manager and version do
// not, and including them would make the id churn on every dependency bump.
// A symbol that will not parse is kept VERBATIM rather than dropped: an
// unreadable id still resolves references consistently within one index,
// which is what an edge needs.
func qualifiedName(sym string) string {
	parsed, err := upstream.ParseSymbol(sym)
	if err != nil || parsed == nil {
		return sym
	}
	parts := make([]string, 0, len(parsed.Descriptors)+1)
	if pkg := parsed.GetPackage(); pkg != nil && pkg.GetName() != "" {
		parts = append(parts, strings.Trim(pkg.GetName(), "/"))
	}
	for _, d := range parsed.Descriptors {
		if n := strings.TrimSuffix(d.GetName(), "/"); n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) == 0 {
		return sym
	}
	return strings.Join(parts, ".")
}

// sortResult imposes a total order so two ingests of the same index produce
// byte-identical output (#120, #162). Map iteration reaches nothing here.
func sortResult(res *Result) {
	sort.SliceStable(res.Symbols, func(i, j int) bool {
		a, b := res.Symbols[i], res.Symbols[j]
		if a.Position.Path != b.Position.Path {
			return a.Position.Path < b.Position.Path
		}
		if a.Position.Line != b.Position.Line {
			return a.Position.Line < b.Position.Line
		}
		return a.ID < b.ID
	})
	sort.SliceStable(res.Edges, func(i, j int) bool {
		a, b := res.Edges[i], res.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Line < b.Line
	})
}
