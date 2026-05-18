// Package contract walks a codeindex.Index (and the source tree it was
// derived from) and extracts API contracts — function signatures, HTTP
// routes, Huma operations, and GraphQL operations — into a flat list of
// ContractDef records.
//
// A ContractDef is the unit Atlas persists into the `features` table with
// kind = "contract"; the source symbols that participate in the contract
// (handler funcs, helper types) are linked via `feature_symbols`.
//
// Phase 6c (per docs/plans/abstract-questing-engelbart.md §Phase 6 contract
// bullet) is the LIBRARY layer only. The CLI surface (`atlas contract`)
// lands in Phase 7 and is intentionally out of scope here.
//
// Supported router/operation frameworks:
//
//   - Go func signatures (every Go func discovered by the codeindex/go
//     scanner becomes a candidate; the extractor records the signature +
//     annotation, if any).
//   - HTTP routers — Chi (`r.Get`, `r.Post`, …), Echo
//     (`e.GET`, `e.POST`, …, plus `e.Group`), and stdlib
//     `http.HandleFunc` / `mux.HandleFunc` (including Go 1.22's
//     "POST /path" pattern syntax).
//   - Huma operations declared via
//     `huma.Register(api, huma.Operation{...}, handler)`. The extractor
//     reads the Operation struct literal fields (Method, Path,
//     OperationID, Summary, Tags) and pairs them with the handler func
//     signature.
//   - TypeScript function exports surfaced by packages/codeindex/ts/. The
//     extractor reuses the scanner's symbol output; it does not invoke
//     the TS parser itself.
//   - GraphQL operations declared in `.graphql` / `.graphqls` schema
//     files. When the project contains no GraphQL files the extractor
//     skips the GraphQL pass cleanly and records no warnings.
//
// Annotations: a contract's FeatureID is populated when the same source
// position carries an `@atlas:contract <id>` directive (preferred), an
// `@atlas:feature <id>` (legacy alignment with the old testreg contract
// flow), or — for HTTP handlers — a legacy `@testreg <id>`. The matching
// is line-proximity-based (annotation within the 10 lines immediately
// above the declaration), mirroring the codeindex Go scanner's `@api`
// rule.
package contract
