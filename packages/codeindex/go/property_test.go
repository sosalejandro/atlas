package goscan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	atlastest "github.com/sosalejandro/atlas/packages/testing"
)

// The scanner's property layer. See docs/testing/strategy.md for how this
// relates to the golden corpus beside it.
//
// The distinction worth holding on to: determinism_test.go pins ONE tree —
// the corpus — and proves the scanner is reproducible and root-relative on
// it. That is a strong statement about an input somebody chose. These tests
// generate trees the author did not choose, weighted toward the shapes that
// have actually broken the scanner (colliding receiver+method names across
// packages, unexported helpers, several declarations per file), and assert
// the properties for all of them. The corpus proves an instance; the
// properties prove the class.

// scanGenerated writes a generated project into its own temp dir and scans
// it. The tree lives outside the repo so nothing here can be perturbed by
// (or perturb) the real source.
func scanGenerated(t *testing.T, p atlastest.Project, opts Options) *Result {
	t.Helper()
	root := t.TempDir()
	atlastest.WriteProject(t, root, p)
	res, err := Scan(context.Background(), root, opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Graph == nil {
		t.Fatal("Scan returned a nil graph")
	}
	return res
}

// indexedSymbols drops the synthetic nodes (route:/endpoint: placeholders and
// unresolved external callees) that carry no source position. They are not
// declarations, so no property about declarations can be stated over them.
func indexedSymbols(res *Result) []shared.Symbol {
	out := make([]shared.Symbol, 0, len(res.Symbols))
	for _, s := range res.Symbols {
		if s.Position.Path == "" || s.Position.Line <= 0 {
			continue
		}
		out = append(out, s)
	}
	return out
}

// TestProperty_Scan_EveryDeclarationIsIndexedOrReported is the identity
// property, and the direct guard for issue #85.
//
// The invariant is not "every declaration gets indexed" — the scanner is
// allowed to give up when every candidate id for a declaration is already
// taken. The invariant is that giving up is LOUD: a declaration is either
// present at its own (file, line) under an id unique to it, or it is named in
// a warning saying it was not indexed. What #85 actually was is the third
// case: two declarations computing the same id, the second silently replacing
// or being replaced by the first, and every statement in the losing file
// charged to nobody for three releases.
//
// Keying on (file, line) rather than on a predicted id is deliberate. The
// scanner chooses among three candidate ids depending on what is already
// taken; a test that predicts the choice tests the prediction. A collapse
// shows up here as a declaration with no node at its position, which is the
// user-visible symptom.
func TestProperty_Scan_EveryDeclarationIsIndexedOrReported(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		p := atlastest.GenGoProject(r, atlastest.GoProjectOptions{})
		res := scanGenerated(t, p, Options{})

		atPos := map[string]shared.Symbol{}
		for _, s := range indexedSymbols(res) {
			atPos[posKey(s.Position.Path, s.Position.Line)] = s
		}
		warnings := strings.Join(res.Warnings, "\n")

		var ids, owners []string
		for _, d := range p.Decls {
			sym, ok := atPos[posKey(d.File, d.Line)]
			if !ok {
				// Allowed only if the scanner said so. The message names the
				// short id and the file, which is what a user would grep for.
				dropped := fmt.Sprintf("symbol %s in %s could not be given a unique id and is NOT indexed",
					d.ShortID(), d.File)
				if !strings.Contains(warnings, dropped) {
					t.Fatalf("seed %d: declaration %s at %s:%d is neither indexed nor reported dropped\nwarnings:\n%s",
						r.Seed(), d.ShortID(), d.File, d.Line, warnings)
				}
				continue
			}
			ids = append(ids, string(sym.ID))
			owners = append(owners, posKey(d.File, d.Line))
		}

		// Two declarations must never share an id. This cannot fail while
		// the lookup above succeeds for both — Graph.Nodes is keyed by id, so
		// a shared id means one of them has no node — but it is asserted
		// explicitly because it is the invariant, and the lookup is only its
		// current symptom.
		if err := atlastest.CheckDistinct("symbol id", ids, owners); err != nil {
			t.Fatalf("seed %d: %v", r.Seed(), err)
		}
	})
}

// TestProperty_Scan_SpansDoNotStraddle guards the coverage attribution rule
// at its source.
//
// Attribution charges a coverage block to the symbol whose [line, end_line]
// span contains the block's start line, preferring the tightest span when
// several match. That rule is only well defined if spans within a file form a
// forest — disjoint or strictly nested. A straddle makes ownership depend on
// where a block happens to start, so one function's statements split across
// two symbols and neither percentage means anything. This is the property
// side of the end_line work: TestScan_SymbolsCarryEndLine asserts the field
// is populated, this asserts the populated values are coherent.
func TestProperty_Scan_SpansDoNotStraddle(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		p := atlastest.GenGoProject(r, atlastest.GoProjectOptions{})
		res := scanGenerated(t, p, Options{})

		spans := make([]atlastest.SymbolSpan, 0, len(res.Symbols))
		for i, s := range indexedSymbols(res) {
			end := s.EndLine
			if end < s.Position.Line {
				t.Fatalf("seed %d: symbol %s at %s:%d has end_line %d before its start line",
					r.Seed(), s.ID, s.Position.Path, s.Position.Line, s.EndLine)
			}
			spans = append(spans, atlastest.SymbolSpan{
				ID: int64(i), File: s.Position.Path, Start: s.Position.Line, End: end,
			})
		}
		if err := atlastest.CheckSpansWellNested(spans); err != nil {
			t.Fatalf("seed %d: %v", r.Seed(), err)
		}
	})
}

// TestProperty_Scan_GraphIsReferentiallyClosed asserts every edge endpoint
// names a node the graph actually holds.
//
// Issue #97 is the reason: LastInsertId returned a neighbouring row's id, so
// persisted edges pointed at symbols that were not their endpoints while
// `symbols: N  edges: M` stayed reassuringly stable. The graph was the right
// size and the wrong shape, and everything that walks it — trace, affected,
// the impl-surface derivation coverage scores features from — answered
// confidently about call chains that did not exist. A dangling endpoint is
// the cheapest checkable signature of that family, and it has to hold at the
// SCANNER boundary because the store can only persist what it is handed.
func TestProperty_Scan_GraphIsReferentiallyClosed(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		p := atlastest.GenGoProject(r, atlastest.GoProjectOptions{})
		res := scanGenerated(t, p, Options{})

		nodes := make([]string, 0, len(res.Graph.Nodes))
		for id := range res.Graph.Nodes {
			nodes = append(nodes, string(id))
		}
		edges := make([]atlastest.DiEdge, 0, len(res.Graph.Edges))
		for _, e := range res.Graph.Edges {
			edges = append(edges, atlastest.DiEdge{From: string(e.From), To: string(e.To), Line: e.Line})
		}
		if err := atlastest.CheckEdgesResolve(nodes, edges); err != nil {
			t.Fatalf("seed %d: %v", r.Seed(), err)
		}
	})
}

// TestProperty_Scan_IsReproducibleAndRootRelative is the determinism property
// over generated trees rather than over the corpus.
//
// Two claims in one test because they share a scan: scanning the same tree
// twice renders identically (map-iteration order does not leak into output —
// the failure mode of PR #99), and scanning two byte-identical copies at
// different absolute paths renders identically too (no absolute path leaks
// into an id, a position or a warning). The second is what makes a golden
// snapshot portable between a laptop and CI at all.
func TestProperty_Scan_IsReproducibleAndRootRelative(t *testing.T) {
	t.Parallel()
	// Fewer cases: this one scans three times per seed, and the marginal
	// input diversity past a couple of dozen trees is small next to the cost.
	atlastest.ForEachSeed(t, 24, func(t *testing.T, r *atlastest.Rand) {
		p := atlastest.GenGoProject(r, atlastest.GoProjectOptions{})

		rootA := filepath.Join(t.TempDir(), "checkout-one")
		rootB := filepath.Join(t.TempDir(), "a-differently-named-checkout")
		atlastest.WriteProject(t, rootA, p)
		atlastest.WriteProject(t, rootB, p)

		first := canonicalScan(t, rootA, Options{})
		second := canonicalScan(t, rootA, Options{})
		if first != second {
			t.Fatalf("seed %d: repeated scan of the same tree differs:\n%s",
				r.Seed(), diffCanonical(first, second))
		}
		elsewhere := canonicalScan(t, rootB, Options{})
		if first != elsewhere {
			t.Fatalf("seed %d: scan depends on the checkout path:\n%s",
				r.Seed(), diffCanonical(first, elsewhere))
		}
		for _, root := range []string{rootA, rootB} {
			if strings.Contains(first, root) {
				t.Fatalf("seed %d: absolute path %q leaked into the scan output", r.Seed(), root)
			}
		}
	})
}

func posKey(file string, line int) string { return fmt.Sprintf("%s:%d", file, line) }
