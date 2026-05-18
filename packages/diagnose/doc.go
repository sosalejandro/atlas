// Package diagnose maps an observed symptom (error message, log line, test
// failure output) back to the symbols + features most likely to have
// produced it.
//
// Per docs/architecture.md §3 this package is the *reverse* of
// packages/trace: trace walks forward from a known root, diagnose walks
// backward from an observed effect. Inputs are a free-form symptom string
// and a populated *store.Store; outputs are a ranked []Match where each
// entry carries the symbol, the feature it belongs to (when known), a
// 0–1 confidence score, and a human-readable reason.
//
// # Algorithm (v0)
//
// Two ranking signals are combined linearly:
//
//  1. Body-text match — the symbol's source body (read lazily from disk
//     via Position.Path + Line/EndLine) is scanned for occurrences of the
//     literal symptom AND for occurrences of the symptom's significant
//     tokens. Regex metacharacters in the symptom are escaped (the user
//     does not type a regex; they paste a stack trace fragment).
//
//  2. Graph centrality — a symbol called by many other symbols is a
//     stronger candidate than a one-off helper. The score uses the count
//     of incoming `call`-kind edges, normalised across the candidate set
//     so the absolute number of edges in the project doesn't bias the
//     answer.
//
// On top of those, the built-in SymptomRule registry (ported from legacy
// testreg) is consulted to derive a *layer bias* — e.g. "401 unauthorized"
// hints "handler / service", "unique constraint" hints "repository /
// query". When a rule matches, symbols whose kind appears in the rule's
// CheckOrder receive a kind-bonus; the rule's own confidence is folded in
// as a multiplier on that bonus.
//
// # Body source strategy
//
// The schema does NOT persist symbol body text. The default implementation
// reads each candidate symbol's source on demand using its repo-relative
// Position.Path and a [Line, EndLine] slice. A per-file content cache
// guards against the obvious quadratic ("100 symbols all in the same
// file" → 100 disk reads). The cache is per-Diagnose call, not global —
// the caller is expected to re-invoke Diagnose for each fresh symptom and
// the working set is small.
//
// Performance: for a project with ~10k symbols across ~2k files, a single
// Diagnose call performs ~2k file reads worst-case (one per unique source
// file containing a candidate). On warm page cache this is well under a
// second; on cold cache it is bounded by the I/O the OS can sustain. If
// this becomes a problem, the path forward is a `symbol_body_excerpts`
// table written at ingest time — but v0 deliberately avoids that schema
// addition (and migration) until profiling shows it's needed.
//
// # Out of scope (v0)
//
//   - Stack-trace parsing (the symptom may contain `file:line` tokens
//     but we don't preferentially weight those — a future input adapter
//     can extract them and pass them via a SymptomHint).
//   - Symptom clustering across multiple inputs.
//   - The `atlas diagnose <symptom>` CLI subcommand — Phase 7 wires it.
//   - ML-assisted matching (Horizon 5).
package diagnose
