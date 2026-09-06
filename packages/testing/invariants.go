package atlastest

import (
	"fmt"
	"sort"
)

// The checks below are the property layer's assertions. Each names the bug
// class it guards, because an invariant with no failure story attached is
// how a suite accumulates assertions nobody dares delete and nobody can
// justify. They return an error rather than taking *testing.T so the same
// check can be used from a test, from a fuzz target, and from the acceptance
// layer, where the "test" is a real repo rather than a generated one.

// CheckSpansWellNested reports the first pair of symbol spans in one file
// that PARTIALLY overlap — that is, they intersect without one enclosing the
// other.
//
// Bug class: mis-attributed statements. Coverage charges a block to the
// symbol whose span contains the block's start line, choosing the tightest
// span when several match. That rule is only well defined when spans form a
// forest: disjoint, or strictly nested. Two spans that straddle each other
// make ownership depend on which line the block happens to start on, so the
// same function's statements split across two symbols and neither one's
// percentage means anything. Real declarations nest (a method inside nothing,
// a closure inside a func); real declarations never straddle. When the
// scanner emits a straddle it is because an end_line is wrong, and every
// number downstream of it is wrong with it.
//
// Enclosure is permitted and deliberately so: the coverage layer resolves it
// by preferring the tightest span, which is the correct owner.
func CheckSpansWellNested(spans []SymbolSpan) error {
	byFile := map[string][]SymbolSpan{}
	for _, s := range spans {
		byFile[s.File] = append(byFile[s.File], s)
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, f := range files {
		in := byFile[f]
		// (Start asc, End DESC). The descending tie-break is what makes
		// enclosure on a shared start line legal: two spans opening on the
		// same line are only well nested if the WIDER one encloses the
		// narrower, and the scan below reads `in[i]` as the potential
		// enclosure of every later span. Sorting End ascending puts the
		// enclosing span second, so the pair (a=[10,12], b=[10,40]) is read
		// as "b starts inside a and ends past a" — a straddle — when it is
		// the ordinary shape of a one-line func literal inside a func
		// declared on the same line.
		sort.Slice(in, func(i, j int) bool {
			if in[i].Start != in[j].Start {
				return in[i].Start < in[j].Start
			}
			return in[i].End > in[j].End
		})
		for i := 0; i < len(in); i++ {
			for j := i + 1; j < len(in); j++ {
				a, b := in[i], in[j]
				if b.Start > a.End {
					break // sorted by Start: nothing further can overlap a
				}
				// b.Start is inside a. That is legal only if b is wholly
				// inside a; otherwise the two straddle.
				if b.End > a.End {
					return fmt.Errorf(
						"straddling spans in %s: symbol %d [%d,%d] and symbol %d [%d,%d] overlap without nesting",
						f, a.ID, a.Start, a.End, b.ID, b.Start, b.End)
				}
			}
		}
	}
	return nil
}

// CheckConservation reports a mismatch between the statements a coverage run
// accounted for and the statements the profile actually held.
//
// Bug class: issue #85. Attribution walked the profile, charged what it could
// to symbols, and dropped the rest on the floor — no counter, no log line, no
// gap row. The reported coverage was arithmetically fine and semantically a
// lie, because its denominator was "statements atlas could place" while the
// header said "statements". Conservation is the one assertion that cannot be
// satisfied by dropping input: everything measured is either charged to a
// symbol or named as a gap, and the two sum to the whole.
func CheckConservation(attributed, unattributed, total int) error {
	if attributed+unattributed != total {
		return fmt.Errorf(
			"statements not conserved: attributed %d + unattributed %d = %d, profile holds %d (%d lost)",
			attributed, unattributed, attributed+unattributed, total, total-attributed-unattributed)
	}
	return nil
}

// CheckEdgesResolve reports the first edge whose endpoint names no node in
// nodes.
//
// Bug class: issue #97 — LastInsertId returned a neighbouring row's id, so
// edges were persisted pointing at symbols that were not their endpoints.
// The counts printed by `atlas scan` (symbols: N edges: M) were unchanged,
// which is why it survived review: the graph was the right SIZE and the wrong
// SHAPE. Anything that walks the graph — trace, affected, the impl-surface
// derivation feeding coverage — then answers confidently about a call chain
// that does not exist. A dangling endpoint is the cheap, checkable signature
// of that whole family.
func CheckEdgesResolve(nodes []string, edges []DiEdge) error {
	set := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		set[n] = true
	}
	for _, e := range edges {
		if !set[e.From] {
			return fmt.Errorf("edge %s -> %s: source resolves to no node", e.From, e.To)
		}
		if !set[e.To] {
			return fmt.Errorf("edge %s -> %s: target resolves to no node", e.From, e.To)
		}
	}
	return nil
}

// CheckMonotone reports a decrease where the caller expected a widened input
// to be scored no worse.
//
// Bug class: the attribution equivalent of a lost update. Indexing MORE
// symbols can only give attribution more places to charge statements to, so
// stmts_attributed must never fall. When it does, the extra symbols are
// stealing blocks from the ones that already owned them — the tightest-span
// tie-break has gone wrong, or spans have started to straddle — and a feature
// that was covered yesterday reports a regression nobody caused.
func CheckMonotone(label string, before, after int) error {
	if after < before {
		return fmt.Errorf("%s decreased when the input only grew: %d -> %d", label, before, after)
	}
	return nil
}

// CheckDistinct reports the first identifier that names two different things.
//
// Bug class: issue #85's mechanism. The graph keys nodes by SymbolID, so two
// declarations that compute the same id do not collide loudly — the second
// silently replaces or is silently dropped by the first, and every symbol in
// the losing FILE disappears from the store along with the coverage charged
// to it. `keys` are the identifiers, `owners` the thing each one is supposed
// to identify uniquely (a file:line, a qualified name); they must be the same
// length.
func CheckDistinct(kind string, keys, owners []string) error {
	if len(keys) != len(owners) {
		return fmt.Errorf("CheckDistinct: %d keys but %d owners", len(keys), len(owners))
	}
	seen := make(map[string]string, len(keys))
	for i, k := range keys {
		if prev, dup := seen[k]; dup && prev != owners[i] {
			return fmt.Errorf("%s %q identifies two things: %s and %s", kind, k, prev, owners[i])
		}
		seen[k] = owners[i]
	}
	return nil
}
