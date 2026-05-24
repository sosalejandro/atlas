package pyscan

import (
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// FileMeta is the per-file record the scanner emits so the caller can feed
// docs/schema-v1.md §5.7 `file_hashes`. Hashing happens upstream; this layer
// only records that a file participated in the Python scan.
type FileMeta struct {
	// Path is repo-relative (forward-slash) per shared.FilePosition rules.
	Path string `json:"path"`

	// SyntaxError, when non-empty, indicates ast.parse rejected the file.
	// The scanner reports the file in this list so callers can surface it,
	// then continues with the rest of the project (matches scanner.ts
	// contract: a bad file MUST NOT crash the scan).
	SyntaxError string `json:"syntax_error,omitempty"`
}

// Result is what Scanner.Scan returns. The Symbols / Edges shapes mirror
// goscan.Result + tsscan.Result so codeindex orchestration can append all
// three into the same graph.Graph without reshaping.
//
// Annotations carries any `@atlas:<kind> <id>` records the Python AST
// walker surfaces:
//
//   - Comment-style hits on the line above a def/class
//     (e.g. `# @atlas:feature billing.subscribe`).
//   - Decorator-style hits using the no-op runtime helper shipped at
//     `assets/python/atlas.py` (e.g. `@atlas.feature("billing.subscribe")`
//     or `@feature("billing.subscribe")` when imported as
//     `from atlas import feature`).
//   - Class-level propagation records: when a class carries an annotation,
//     one record is emitted per method with the method's line as the
//     anchor so the store-side LookupSymbolAtOrAfterLine resolves to the
//     method symbol. This addresses the v0.3.0 gotcha #3 in
//     docs/languages/py.md.
//
// The standard `feature_symbols` materialisation pipeline consumes these
// identically to records discovered by the Go-side annotations parser; the
// two paths are complementary, not exclusive, and the store's idempotent
// upsert collapses duplicate link rows.
//
// Warnings are non-fatal: a missing python3 runtime, a syntactically broken
// file, or any other recoverable issue surfaces here rather than as an
// error. Callers should pass them through to the user.
type Result struct {
	Symbols     []shared.Symbol     `json:"symbols"`
	Edges       []graph.Edge        `json:"edges"`
	Annotations []shared.Annotation `json:"annotations,omitempty"`
	Files       []FileMeta          `json:"files,omitempty"`
	Warnings    []string            `json:"warnings,omitempty"`
}

// rawScannerOutput is the JSON contract emitted by the embedded scanner.py.
// It is intentionally near-1:1 with the tsscan envelope (so the Go-side
// orchestration code stays uniform across language sub-scanners), then
// mapped to Atlas's shared.Symbol + graph.Edge shapes in the Go layer.
type rawScannerOutput struct {
	Nodes       []rawNode       `json:"nodes"`
	Edges       []rawEdge       `json:"edges"`
	Annotations []rawAnnotation `json:"annotations"`
	Files       []FileMeta      `json:"files"`
	Warnings    []string        `json:"warnings"`
	Stats       rawStats        `json:"stats"`
}

// rawAnnotation is the wire form of one `@atlas:<kind> <id>` record
// surfaced by scanner.py. Kind matches the closed set of
// annotations.Kinds keys; only the kinds the Go-side parser recognises
// are mapped onto shared.Annotation downstream, so a future scanner.py
// emitting an unrecognised kind degrades gracefully into a "skipped"
// counter rather than a crash.
//
// ID carries the first id token; the raw payload (which may contain
// tags or extra ids in the comment form) is preserved on `Raw` for
// diagnostic display.
type rawAnnotation struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	File string `json:"file"`
	Line int    `json:"line"`
	Raw  string `json:"raw,omitempty"`
}

type rawNode struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
	Doc  string `json:"doc,omitempty"`
}

type rawEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Kind labels what relationship this edge represents:
	//   "import"      — `import X` / `from X import Y` at any depth
	//   "inheritance" — `class Child(Parent)`
	//   "call"        — `foo()` inside a function body
	//   "decorator"   — `@dec def f()` (or class-level decorator)
	// Currently advisory; the Go layer drops it into graph.Edge.From/To only.
	Kind string `json:"kind,omitempty"`
	// Line is the 1-based source line of the AST node that produced
	// this edge (the `import` statement, the `@deco` decorator line,
	// the call site, etc.). Zero means "scanner.py did not supply
	// one" — the ingestor then falls back to the from-symbol's
	// declaration line so the wire stays back-compat with pre-fix
	// scanner builds. Closes issue #17 (PR #68).
	Line int `json:"line,omitempty"`
	// Scope is set ONLY on import edges and records the lexical
	// context the import was found in. Possible values mirror
	// scanner.py's SCOPE_* constants:
	//
	//   "module"         — top-level statement
	//   "function"       — inside a def/async def body
	//   "conditional"    — inside `if/elif/else` (non-TYPE_CHECKING)
	//   "type_checking"  — inside `if TYPE_CHECKING:`
	//   "try_guard"      — inside `try:` whose handlers catch ImportError
	//
	// Empty string means "scope was not reported" (legacy scanners
	// or non-import edges). Closes issue #16: the legacy scanner
	// only walked module-level imports, so anything in a function
	// body or conditional was silently dropped — false-positive
	// dead code.
	Scope string `json:"scope,omitempty"`
}

type rawStats struct {
	FilesScanned     int `json:"files_scanned"`
	SymbolsFound     int `json:"symbols_found"`
	EdgesFound       int `json:"edges_found"`
	AnnotationsFound int `json:"annotations_found"`
	SyntaxFailures   int `json:"syntax_failures"`
}

// rawKindToSymbolKind maps the scanner.py node.kind strings onto Atlas's
// closed shared.SymbolKind enum. The Python scanner emits source-shape
// kinds (function, class, method, const) which the orchestrator surfaces
// as the closest semantic SymbolKind for cross-language traceability.
//
// Unknown kinds fall back to KindFunc so the node still lands in the
// graph (a warning is appended in scanner.go).
func rawKindToSymbolKind(k string) shared.SymbolKind {
	switch k {
	case "function":
		return shared.KindFunc
	case "class":
		return shared.KindType
	case "method":
		return shared.KindMethod
	case "const":
		return shared.KindConst
	default:
		return shared.KindFunc
	}
}

// _ uses graph to keep the import live for future direct Edge construction.
// The Go layer currently routes raw edges through graph.Edge in scanner.go.
var _ = graph.Edge{}
