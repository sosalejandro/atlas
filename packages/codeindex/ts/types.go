package tsscan

import (
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// RouterKind narrows the TypeScript scanner to one (or more) frontend router
// frameworks. The zero value (empty slice) auto-detects every supported
// router whose marker files exist under the project root.
//
// Each enum value below maps verbatim to the embedded scanner.ts CLI flag
// `--router <kind>`. Adding a new router kind requires:
//
//  1. Add a constant here.
//  2. Implement detection + extraction in scanner.ts.
//  3. Add a fixture under testdata/<kind>/.
type RouterKind string

const (
	// ReactRouter recognises the classic `createBrowserRouter([...])` /
	// `createHashRouter` shapes used in apps/web-* across nutrition-v2-go.
	ReactRouter RouterKind = "react-router"

	// TanStackRouter recognises the `createFileRoute('/path')({ component })`
	// shape produced by TanStack's file-based generator (routeTree.gen.ts).
	TanStackRouter RouterKind = "tanstack"

	// ExpoRouter recognises Expo's file-based routing convention
	// (`app/_layout.tsx`, `app/(tabs)/index.tsx`, `app/[id].tsx`).
	ExpoRouter RouterKind = "expo"
)

// FileMeta is the per-file record the scanner emits so the caller can feed
// docs/schema-v1.md §5.7 `file_hashes`. Hashing happens upstream; this layer
// only records that a file participated in the TS scan.
type FileMeta struct {
	// Path is repo-relative (forward-slash) per shared.FilePosition rules.
	Path string `json:"path"`
}

// Result is what Scanner.Scan returns. The Symbols / Edges shapes mirror
// goscan.Result so codeindex orchestration can append both into the same
// graph.Graph without reshaping.
//
// Warnings are non-fatal: a missing Node runtime, an unparseable .ts file,
// or an unresolved hook → API reference all surface here rather than as
// errors. Callers should pass them through to the user.
type Result struct {
	Symbols  []shared.Symbol `json:"symbols"`
	Edges    []graph.Edge    `json:"edges"`
	Files    []FileMeta      `json:"files,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

// rawScannerOutput is the JSON contract emitted by the embedded scanner.ts.
// It is intentionally a near-1:1 of the testreg ts-scanner output (so we
// keep semantic parity with the legacy tooling), then mapped to Atlas's
// shared.Symbol + graph.Edge shapes in the Go layer.
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
}

type rawStats struct {
	FilesScanned  int `json:"files_scanned"`
	RoutesFound   int `json:"routes_found"`
	APICallsFound int `json:"api_calls_found"`
}

// rawKindToSymbolKind maps the scanner.ts node.kind strings onto Atlas's
// closed shared.SymbolKind enum. Unknown kinds fall back to KindComponent
// so the node still lands in the graph (a warning is appended in scanner.go).
func rawKindToSymbolKind(k string) shared.SymbolKind {
	switch k {
	case "route":
		return shared.KindEndpoint // route: nodes are entry points; kind=endpoint matches the Go scanner
	case "component":
		return shared.KindComponent
	case "hook":
		return shared.KindHook
	case "api-service":
		return shared.KindService
	case "endpoint":
		return shared.KindEndpoint
	default:
		return shared.KindComponent
	}
}
