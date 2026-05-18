// Package patterns runs lightweight parser-based recognisers that detect
// canonical Event-Driven Architecture shapes in Go source files.
//
// Phase 6f (parser-based EDA pattern recognition) complements Phase 6e's
// annotation-driven EDA awareness — the recognisers fire even when code
// is NOT annotated yet, which is the dominant case during the
// nutrition-v2-go cutover.
//
// Three recognisers ship in this phase:
//
//  1. outbox-append      — every `*.outbox.Append(...)` / `*.Outbox.Append(...)`
//     call expression. Marks every code path that publishes a domain event
//     to the transactional outbox. Drift signal: a service that does
//     `repo.Save` without an outbox call in the same closure.
//
//  2. event-recorder-embed — every struct whose field list contains an
//     embedded reference to a named type matching `EventRecorder` (handles
//     both value and pointer embeds, both same-package and qualified
//     selectors). Marks aggregate roots structurally — any aggregate
//     without this embed is a candidate violation.
//
//  3. canonical-service  — every method body that contains
//     `<uow>.Run(ctx, func(...) error { ... repo.Save(...) ... outbox.Append(...) ... })`,
//     i.e. a UoW closure containing BOTH a repo.Save call AND an
//     outbox.Append call. Identifies services that follow the canonical
//     saveWithEvents shape vs services that are partial / drifted.
//
// The recognisers are deliberately syntactic (go/ast only) — they do not
// use go/types. This is fast, cheap, and good enough for the audit/diagnose
// use cases: a false positive surfaces as "here's a candidate to inspect",
// not as a build failure.
//
// Output:
//
//	matches, err := patterns.MatchAll(ctx, fset, file)
//
// Each Match carries a Pattern name, the matching SymbolID, position, a
// human-readable Detail, and a Confidence score (0..1).
//
// Imports: go/ast, go/token, shared. Does NOT import store/, audit/,
// codeindex (one-way dependency — codeindex orchestrator imports this
// package, not the other way around).
package patterns
