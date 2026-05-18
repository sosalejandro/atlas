// Package annotations is Atlas's multi-language annotation parser.
//
// Per docs/architecture.md §3.2.3 it imports only packages/shared, has no
// awareness of the graph, and emits raw shared.Annotation records that the
// resolver (a future tier-3 component) maps to feature_symbols rows.
//
// Two grammars are accepted side-by-side per docs/annotations.md:
//
//	@atlas:<kind> <id> [<id>...] [<tag>...]   ← canonical
//	@testreg     <id> [<id>...] [<tag>...]   ← legacy, comma-separated IDs tolerated
//	@api METHOD /path                         ← handler discovery (separate grammar)
//
// Supported file extensions: .go, .ts, .tsx, .js, .jsx, .py, .md.
// Comment styles handled: `//`, `/* ... */`, `#`, `<!-- ... -->`.
//
// Parser rules (verbatim from docs/annotations.md):
//   - One annotation per comment line (first match wins).
//   - Multi-line block comments collapse to a single logical line before
//     matching.
//   - Whitespace separates tokens; legacy comma-separated IDs are tolerated.
//   - Tags must follow IDs. The first `#tag` token terminates the ID list.
//   - New-grammar IDs are validated against `[a-z0-9_]+(\.[a-z0-9_]+)*`.
//     Legacy `@testreg` IDs are NOT re-validated — the 1,110 existing
//     annotations in nutrition-v2-go are preserved untouched.
package annotations
