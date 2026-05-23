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
// Warnings are non-fatal: a missing python3 runtime, a syntactically broken
// file, or any other recoverable issue surfaces here rather than as an
// error. Callers should pass them through to the user.
type Result struct {
	Symbols  []shared.Symbol `json:"symbols"`
	Edges    []graph.Edge    `json:"edges"`
	Files    []FileMeta      `json:"files,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

// rawScannerOutput is the JSON contract emitted by the embedded scanner.py.
// It is intentionally near-1:1 with the tsscan envelope (so the Go-side
// orchestration code stays uniform across language sub-scanners), then
// mapped to Atlas's shared.Symbol + graph.Edge shapes in the Go layer.
type rawScannerOutput struct {
	Nodes    []rawNode  `json:"nodes"`
	Edges    []rawEdge  `json:"edges"`
	Files    []FileMeta `json:"files"`
	Warnings []string   `json:"warnings"`
	Stats    rawStats   `json:"stats"`
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
	//   "import"      — module-level `import X` / `from X import Y`
	//   "inheritance" — `class Child(Parent)`
	//   "call"        — `foo()` inside a function body
	//   "decorator"   — `@dec def f()` (or class-level decorator)
	// Currently advisory; the Go layer drops it into graph.Edge.From/To only.
	Kind string `json:"kind,omitempty"`
}

type rawStats struct {
	FilesScanned   int `json:"files_scanned"`
	SymbolsFound   int `json:"symbols_found"`
	EdgesFound     int `json:"edges_found"`
	SyntaxFailures int `json:"syntax_failures"`
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
