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
package graph
