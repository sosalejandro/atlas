// Package graph is the call-graph data structure Atlas uses everywhere a
// call/edge relationship matters.
//
// Per docs/architecture.md §3.3 this package is a *passive* data structure:
// callers (codeindex/, audit/, contract/) populate it; graph/ does not parse,
// persist, or render. Imports only packages/shared.
//
// The shape ports legacy testreg `internal/domain/graph.go` 1:1 — same
// semantics for cycle detection on AddEdge, same lazy adjacency caches,
// same TraceFrom/FindPathTo/TraceCallersFrom contract — but with
// shared.SymbolID typing the node keys instead of bare strings.
//
// # Choosing between graph.Graph and store.Edges for graph walks
//
// Atlas exposes two call-graph walk surfaces. Pick the one that matches
// your dependency-cone budget:
//
//   - graph.Graph.Callees / TraceFrom / FindPathTo
//     In-memory traversal over a populated graph.Graph. Use when:
//
//   - You already hold a codeindex.Index (e.g. external consumers like
//     bmad-cli that integrate the indexer as a Go library).
//
//   - You want to avoid the SQLite + golang-migrate transitive cone (the
//     full store package pulls ~50MB of binary weight; graph alone is
//     under ~20MB).
//
//   - store.Edges.Walk (recursive CTE against .atlas/atlas.db)
//     Database-backed traversal. Use when:
//
//   - You are inside atlas itself (CLI verbs, audit pipelines) and the
//     Store is already open.
//
//   - You want SQL-side filtering (depth bound, kind filter) without
//     materialising every node in memory.
//
//   - You need persistence guarantees across processes.
//
// External Go consumers SHOULD prefer graph.Graph.Callees — the in-memory
// path is the leaner dep cone and stays fast for typical project sizes
// (atlas's own bmad-cli integration uses this path). atlas.Edges.Walk is
// the in-process default for atlas's own pipelines.
//
// Both surfaces respect the same edge model and produce equivalent results
// for a given graph state. Mixing them within a single workflow is fine —
// they read from the same shape, just through different transports.
package graph
