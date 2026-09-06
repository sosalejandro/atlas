package shared

import "strings"

// NodeClass says whether an indexed node is code someone wrote or a marker
// Atlas invented to hang a graph edge on.
//
// Atlas has always had both. A Go function and the string `route:/login`
// both land in the `symbols` table, because the call-graph walk needs a
// vertex for an HTTP route the same way it needs one for a function. The
// difference has never been recorded: it was re-derived, at every call
// site that cared, by matching a reserved prefix on the id
// (`file_path NOT LIKE 'external:py%'` in the dead-code query,
// `strings.HasPrefix(id, "route:")` in the graph's root picker, and so on).
//
// That convention is load-bearing and untyped, which is the defect issue
// #112 names. A prefix check is invisible to the type system, silently
// wrong for any language whose ids legitimately contain a colon, and — the
// part that actually bites — has to be repeated identically in Go and in
// SQL, where the two copies can drift with nothing able to notice. So the
// class is a column now (`symbols.node_class`, migration 0019), this
// function is the ONE place the rule lives, and the migration's backfill is
// the same rule spelled in SQL exactly once, at the moment of the rename.
type NodeClass string

const (
	// NodeClassDeclaration is a real declaration: it was parsed out of a
	// source file someone in this repository wrote, and it has an honest
	// file position. "Real code only" queries mean this.
	NodeClassDeclaration NodeClass = "declaration"

	// NodeClassAnchor is a synthetic vertex Atlas minted so an edge has
	// somewhere to land — an HTTP route, a named SQL query, an API
	// endpoint, an unresolved import target outside the repo. An anchor is
	// a fact about the graph, never a fact about the source, so it must
	// not appear in dead-code candidates, declaration counts, or anything
	// else that claims to describe authored code.
	NodeClassAnchor NodeClass = "anchor"
)

// AnchorPrefixes is the closed set of reserved id prefixes that mark a
// synthetic vertex. The colon is what makes them safe: Atlas file paths are
// always repo-relative with forward slashes, and every scanner's qualified
// name is built from language identifiers and dots, so no real declaration
// can collide.
//
// The set is closed on purpose. A scanner that wants a new kind of anchor
// adds its prefix HERE and gets classification, the store's write-side
// refusal, and migration 0019's backfill rule in one edit — rather than
// adding a new prefix check to the four queries that happen to care.
var AnchorPrefixes = []string{
	"route:",    // HTTP route vertices from the router scanners
	"sql:",      // named sqlc queries, from the sqlc mapper
	"endpoint:", // API endpoints from the contract extractors
	"external:", // unresolved import targets (pyscan's `external:py` stubs)
}

// ClassifyNode returns the NodeClass for a node, given its qualified name
// and the file path it was recorded under.
//
// Both are consulted because Atlas marks anchors in two different places
// and always has: the sqlc mapper puts the marker in the id (`sql:GetUser`,
// pointing at a real .sql file), while pyscan puts it in the position
// (`external:py`, under a real-looking dotted module id). Checking one and
// not the other is how half the anchors on a repo get counted as authored
// code.
func ClassifyNode(qualifiedName SymbolID, filePath string) NodeClass {
	if hasAnchorPrefix(string(qualifiedName)) || hasAnchorPrefix(filePath) {
		return NodeClassAnchor
	}
	return NodeClassDeclaration
}

// hasAnchorPrefix reports whether s starts with any reserved anchor prefix.
func hasAnchorPrefix(s string) bool {
	for _, p := range AnchorPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Valid reports whether c is one of the two classes. The store refuses to
// write anything else: an unset class is a writer that never thought about
// the question, and defaulting it to "declaration" is precisely how a
// synthetic vertex would launder itself into the authored-code counts.
func (c NodeClass) Valid() bool {
	return c == NodeClassDeclaration || c == NodeClassAnchor
}
