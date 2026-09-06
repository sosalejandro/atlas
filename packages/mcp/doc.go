// Package mcp serves the atlas index to coding agents over the Model Context
// Protocol, on stdio.
//
// # Why a hand-rolled protocol layer
//
// The surface an agent needs is four methods — initialize, tools/list,
// tools/call, ping — over JSON-RPC 2.0. Taking an SDK dependency for that
// would put a large, fast-moving third-party tree in the path of a binary
// whose whole value proposition is that `go install` works, and would tie the
// revision atlas speaks to that SDK's release cadence. The protocol is
// implemented here against the specification, with encoding/json.
//
// # Read-only by construction
//
// Nothing in this package can write to the store. The tools reach the index
// through GraphIndex / CoverageIndex / Scorer, which declare only reads; the
// writable ports (Upsert, Link, Insert, Ingest) are not reachable from any
// value this package holds. That is deliberate and load-bearing: an
// agent-facing surface that CAN write is a surface that WILL corrupt the index
// on a hallucinated call, and the index is the thing every other atlas answer
// is derived from. Enforcing it at the type level rather than by convention
// means a future tool cannot mutate anything even by accident.
//
// # Answering honestly
//
// Two failure modes drive most of the shape of the results in this package,
// and both are failures of an EMPTY answer rather than a wrong one:
//
//   - An empty list reads to a model as a confident "there is nothing". When
//     the real situation is "nothing has been scanned" or "no coverage has
//     been ingested", every result therefore carries a NoData envelope naming
//     the command that would produce the data, and omits the empty list
//     entirely so there is nothing to misread.
//
//   - A silently truncated list is worse than a large one: the agent reasons
//     from a partial graph believing it is complete. Every result set is
//     bounded, and every bounded result that actually cut something carries a
//     Truncation block saying how much was left out.
package mcp
