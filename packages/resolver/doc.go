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
//     (class-hierarchy analysis, computed from go/types — see
//     typedispatch.go).
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
// type-checked. Nothing this package answers needs dependency syntax, so
// it does not pay for it.
//
// It also no longer builds an SSA program. Issue #155 observed that the
// entire output of ssautil.Packages -> cha.CallGraph was one
// map[token.Pos][]*types.Func, both halves of which are go/token and
// go/types concepts, and that CHA walks SSA only to enumerate call sites
// — which this package's caller already does. Computing the same
// dispatch from go/types took 216 MB of cumulative allocation and 3.7M
// allocations out of a load of this repository, and 143 MB off its
// resident peak. docs/performance.md §1 has the measurement;
// invokeparity_test.go is the reason it was safe to take, and keeps the
// old implementation as the oracle it is checked against.
//
// The A/B, on this repository, warm build and module caches, three
// interleaved samples of each arm in a process apiece, medians (machine
// and method: docs/performance.md). Both arms are the same
// packages.Load(./..., Tests: true); only the mode differs:
//
//	                    wall     cumulative alloc   resident peak
//	without NeedDeps    0.52 s          340 MB           243 MB
//	with NeedDeps       3.16 s         2,131 MB        1,326 MB
//
// Six times the wall clock, six times the churn and five times the
// resident set, for dependency syntax nothing here reads. Note which
// column is which: docs/performance.md §"Three kinds of memory" is what
// the last two mean, and they are not the same quantity.
//
// docs/languages/go.md records 0.39-0.42 s and 2.70-2.80 s for the same
// pair, taken in an earlier session on a quieter machine. The ratio is
// what carries between the two, and it is about six either way.
//
// This A/B is NOT a price for driving types.Config.Check by hand instead
// of packages.Load — that program has never been written or measured, and
// an importer built on gcexportdata would read the same export data this
// arm does. What the pair prices is exactly what it varies: dependency
// types from source against dependency types from export data.
package resolver
