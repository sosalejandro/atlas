// Package goscan is the Go AST scanner — successor to testreg's
// internal/adapters/go_ast_scanner.go (1,376 LOC).
//
// Output is a *graph.Graph plus a slice of shared.Symbol records;
// downstream consumers (audit, contract, store) decide how to persist or
// render.
//
// # Two resolvers, one scan
//
// docs/architecture.md §3.2.1 originally specified stdlib go/ast +
// go/parser only, on the reasoning that it keeps scans fast and free of
// module-resolution headaches. Issue #87 measured what that bought and
// what it cost: on a 39-module workspace, 210 production files produced
// no symbols because their short names collided, and every call through
// an interface-typed field bound by substring match or not at all.
// Symbol identity and call resolution are what every other number atlas
// reports is computed FROM, so they are the wrong place to save a
// second.
//
// So the scan now has two resolvers and uses both:
//
//   - packages/resolver loads the tree with golang.org/x/tools/go/packages
//     and resolves each call through go/types, with callgraph/cha for
//     interface dispatch. Edges it produces are tier `typed`.
//   - the AST ladder in this package — resolveInScope, then
//     fuzzyResolveMethod, then fuzzyResolve — resolves by name. Edges it
//     produces are tier `name_resolved` or `syntactic`.
//
// The choice is per FILE, not per scan: a package the type checker
// rejects falls back on its own, and the rest of the repo stays typed.
// The typed resolver is authoritative wherever it runs — the ladder does
// not get a second opinion on a call go/types already answered, because
// that would put a guess back into a graph that had an exact answer.
// Options.SkipTypedResolution disables the typed half entirely;
// Result.Resolution reports what actually happened.
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
//  3. Function discovery — walk every .go file (including _test.go by
//     default; see Options.SkipTests), skipping vendor/, node_modules/,
//     hidden dirs, and generated code. Register *ast.FuncDecl as
//     Nodes, collect struct field types for call resolution.
//
//     Generated code is identified by Go's own header convention
//     (`// Code generated ... DO NOT EDIT.` ahead of the package
//     clause), by Options.GeneratedGlobs, and by a "generated" path
//     segment — in that reporting order. Every exclusion lands on
//     Result.SkippedFiles with its reason, because a file dropped from
//     the index is a file dropped from the coverage denominator and
//     that has to be countable. Options.IncludeGenerated indexes them
//     instead.
//
//     Test files are scanned by default because Atlas's feature
//     attribution relies on `@atlas:feature` / `@testreg` annotations
//     that conventionally live on the test that verifies the feature.
//     Set Options.SkipTests=true for pure production graph-only audits.
//  4. Call graph extraction — walk function bodies; resolve each
//     ast.CallExpr to a target Node ID. For a type-checked file that is
//     one go/types lookup (plus, for an interface call, the CHA
//     candidate set — which is why one call site can emit several
//     edges, each marked ambiguous). For the rest it is the legacy
//     ladder: selector chain via struct fields → fieldType.MethodName;
//     package-level call via pkg.FuncName; ambiguous interface call via
//     fuzzy match.
//
// Public API:
//
//	res, err := goscan.Scan(ctx, rootDir, goscan.Options{...})
//	res.Graph        // *graph.Graph
//	res.Symbols      // []shared.Symbol (denormalised view, same data)
//	res.SkippedFiles // []SkippedFile (path + reason, in walk order)
//	res.Resolution   // *ResolutionReport (what type checking achieved)
//	res.Warnings     // []string
//
// What is intentionally NOT in this package (per architecture doc):
//   - No go/packages plumbing: loading, type checking and class-hierarchy
//     analysis live in packages/resolver, which knows nothing about
//     SymbolIDs. Identity stays here; types stay there.
//   - No SQLite persistence (store/ is a tier-2.5 side-channel).
//   - No yaml/json output formatting (each cmd/atlas verb owns its
//     JSON shape).
//   - No frontend / TS awareness (codeindex/ts is its own subpackage).
//   - No route parsing logic — Routes hook receives []Route from
//     packages/routeparse when that lands in Phase 2.
package goscan
