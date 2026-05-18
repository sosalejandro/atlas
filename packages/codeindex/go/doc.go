// Package goscan is the Go AST scanner — successor to testreg's
// internal/adapters/go_ast_scanner.go (1,376 LOC).
//
// Per docs/architecture.md §3.2.1 it uses ONLY stdlib go/ast + go/parser
// (no go/types, no golang.org/x/tools) — keeps scans fast and free of
// module-resolution headaches. Output is a *graph.Graph plus a slice of
// shared.Symbol records; downstream consumers (audit, contract, store)
// decide how to persist or render.
//
// The 4-phase scan model is preserved:
//
//  1. Pre-resolution — optional SQLC method→SQL mappings and DI bindings
//     supplied by the caller as PreResolved hooks. Phase 1 ships the
//     hooks as interfaces; the actual route/sqlcmap/resolver packages
//     are separate ports landing in later phases.
//  2. Route discovery — also via an optional Routes hook (Phase 1 has no
//     in-package router parser; nutrition-v2-go uses Huma + Chi which
//     is the routeparse/ package's job).
//  3. Function discovery — walk every .go file (excluding _test.go,
//     vendor/, node_modules/, hidden dirs, and "generated" subtrees),
//     register *ast.FuncDecl as Nodes, collect struct field types for
//     call resolution.
//  4. Call graph extraction — walk function bodies; resolve each
//     ast.CallExpr to a target Node ID (selector chain via struct
//     fields → fieldType.MethodName; package-level call via
//     pkg.FuncName; ambiguous interface call via fuzzy match).
//
// Public API:
//
//	res, err := goscan.Scan(ctx, rootDir, goscan.Options{...})
//	res.Graph      // *graph.Graph
//	res.Symbols    // []shared.Symbol (denormalised view, same data)
//	res.Warnings   // []string
//
// What is intentionally NOT in this package (per architecture doc):
//   - No SQLite persistence (store/ is a tier-2.5 side-channel).
//   - No yaml/json output formatting (each cmd/atlas verb owns its
//     JSON shape).
//   - No frontend / TS awareness (codeindex/ts is its own subpackage).
//   - No route parsing logic — Routes hook receives []Route from
//     packages/routeparse when that lands in Phase 2.
package goscan
