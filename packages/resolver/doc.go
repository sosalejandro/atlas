// Package resolver answers "what does this call actually call?" using the
// Go type checker instead of the shape of the identifier.
//
// # Why it exists
//
// Every scanner in atlas before this one resolved a callee by name:
// render the selector expression as "Receiver.Method", look that string up
// in a table of declarations, and — when it misses — try a substring match
// on a lowercased identifier. Issue #87 measured the cost on a 39-module
// workspace: 210 production files produced no symbols at all because their
// short names collided, and every call through an interface-typed field
// either bound to whichever same-named declaration was walked first or was
// dropped. Name matching cannot distinguish the two `Config.Validate`
// methods in one repo, and it cannot follow `s.repo.Save(...)` to the
// concrete repository, because neither fact is written in the source text
// of the call site. Both are written in the types.
//
// So this package loads the tree with golang.org/x/tools/go/packages and
// answers three questions the AST could not:
//
//  1. Which declaration does this identifier BIND to (types.Info.Uses and
//     types.Info.Selections), across packages, through embedding, and
//     through generic instantiation.
//  2. Which concrete implementations can this interface call reach
//     (class-hierarchy analysis over SSA, golang.org/x/tools/go/callgraph/cha).
//  3. Which packages could not be type-checked at all, so the caller can
//     fall back to name matching for exactly those files and say so.
//
// # What it deliberately does not do
//
// It does not decide symbol ids. The scanner owns identity — the id
// scheme, the collision rules, the exclusion ledger — and this package
// hands back *types.Func objects plus ObjectKey, a stable string the
// scanner uses to map an object onto whatever id it registered. Keeping
// identity out of here is what lets the typed path and the AST path
// produce the same id for the same declaration, which is the only reason
// a scan can mix the two.
//
// It does not require a green build. packages.Load reports errors per
// package; a package with any error is reported as degraded and its files
// are simply absent from this program, so the scanner falls back for them
// and keeps going. Atlas runs mid-edit, and a scanner that needs a
// compiling repo is a scanner nobody can run when they most need it.
//
// # Cost
//
// The load mode omits packages.NeedDeps on purpose. With it, dependency
// syntax is parsed and type-checked from source; without it, dependencies
// come from compiled export data and the initial packages are still fully
// type-checked. Measured on the atlas repository itself (143 packages,
// warm build cache, see docs/languages/go.md for the full numbers), the
// load takes about 0.5s without NeedDeps and about 3.6s with it. Nothing
// this package answers needs dependency syntax, so it does not pay for it.
package resolver
