// Package codeindex is the orchestrator that runs the language sub-scanners
// (go/, ts/, annotations/) and assembles a single Index.
//
// Per docs/architecture.md §3.2, the sub-packages know how to parse their
// own language. The orchestrator's job is to:
//
//  1. Decide which sub-scanners to run for a project (Phase 1 ships Go +
//     annotations; TS lands in a later phase).
//  2. Merge their outputs into one *graph.Graph + one []shared.Annotation
//     + one per-file hash map.
//  3. Hand the merged Index to downstream consumers (Phase 4's
//     packages/store, audit/, contract/) via a stable Go API.
//
// The Index shape is the contract that Phase 4's SQLite store will consume:
// every row in the v1 schema (docs/schema-v1.md) can be derived from a
// fresh Index without re-parsing.
//
// Imports: shared, graph, codeindex/annotations, codeindex/go.
// Does NOT import: store/ (orchestrator stays parse-only), audit, coverage.
package codeindex
