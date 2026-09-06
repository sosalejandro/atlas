package graph

// ResolutionTier records WHICH MECHANISM produced an edge.
//
// Atlas resolves the same relationship — "this function calls that one"
// — by very different means depending on the language and the scanner:
// a type checker that knows the receiver's dynamic type, a name lookup
// against a package scope, or a substring match on a lowercased
// identifier. All three land as one row in `edges`, identical in
// (from, to, kind, file, line). Without this field a guess and a proof
// are the same fact, and a resolver migration (#87 replaces the Go call
// resolver with go/packages + callgraph) can halve or double the
// guesses with every count- and tuple-based test still green.
//
// The vocabulary is the A/B/C/D set #105's tiering addendum already
// defines. It is deliberately closed and deliberately NOT accompanied
// by a numeric confidence score: the prior art this borrows from
// (trace-mcp, credited on #105) seeds confidence weights of
// 1.0/0.95/0.7/0.4 from its own tiers, and those constants are
// calibrated against that project's corpus. Nobody here has measured
// ours, and an unmeasured number that downstream ranking multiplies by
// is worse than no number at all.
type ResolutionTier string

const (
	// TierUnset is the zero value and is NOT a tier. It means "this
	// producer did not say", which the store rejects at insert time
	// rather than filling in.
	//
	// The alternative — giving ResolutionTier a valid zero value — is
	// exactly how every edge ends up claiming to be typed: a scanner
	// that forgets the field inherits whichever constant happens to be
	// first, and nothing anywhere notices.
	TierUnset ResolutionTier = ""

	// TierTyped (A) — a type checker resolved it. go/packages +
	// callgraph on Go (#87): interface dispatch, generic instantiation
	// and embedding are all exact. Requires a compilable build.
	//
	// Nothing in atlas produces this tier yet. It is defined here
	// because #87 needs somewhere to move edges TO, and the histogram
	// diff that reviews #87 is only readable if both endpoints of the
	// move exist before the move.
	TierTyped ResolutionTier = "typed"

	// TierNameResolved (B) — a name was bound to a declaration atlas
	// has actually indexed, using scope rules rather than types.
	// tree-sitter + stack-graphs is the tier-B mechanism #105 plans;
	// today's Go scanner reaches it whenever resolveInScope finds the
	// callee in the caller's own package scope or in the global
	// short-name table. Direct calls resolve; anything reached through
	// an interface-typed variable does not.
	TierNameResolved ResolutionTier = "name_resolved"

	// TierSyntactic (C) — the shape of the source said so, and no
	// cross-file binding was performed. Substring and case-insensitive
	// name matching, DI-binding guesses, "the comment ten lines above
	// this declaration", and callee renderings kept verbatim because
	// nothing matched. The target may not exist; it may be the wrong
	// one of several same-named candidates.
	//
	// This is the tier whose SHARE is the signal worth watching. A
	// repo whose call graph is mostly tier C can still be browsed, but
	// change-impact answers over it are guesses stacked on guesses.
	TierSyntactic ResolutionTier = "syntactic"

	// TierImported (D) — the edge came from somebody else's indexer
	// through SCIP (#105 step 1). Its fidelity is that indexer's, not
	// ours, which is why it is a tier of its own rather than being
	// folded into A: "scip-go said so" and "we type-checked it" are
	// different claims even when they usually agree.
	//
	// Nothing produces this tier yet either; SCIP ingest is #105.
	TierImported ResolutionTier = "imported"
)

// AllTiers is the closed vocabulary in strength order, strongest
// first. Reporting code iterates this so a histogram's column order is
// fixed across runs and across repos — a histogram whose columns move
// is not diffable, and diffing it is the whole point.
func AllTiers() []ResolutionTier {
	return []ResolutionTier{TierTyped, TierNameResolved, TierSyntactic, TierImported}
}

// IsValidTier reports whether t is one of the four. TierUnset is not,
// on purpose: callers use this to reject an edge whose producer never
// stated a mechanism, and an "absent is fine" answer here would defeat
// every guard downstream of it.
func IsValidTier(t ResolutionTier) bool {
	switch t {
	case TierTyped, TierNameResolved, TierSyntactic, TierImported:
		return true
	}
	return false
}
