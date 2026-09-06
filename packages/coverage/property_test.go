package coverage

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
	atlastest "github.com/sosalejandro/atlas/packages/testing"
)

// The attribution property layer.
//
// TestAttribution_MatchesGoToolCover, next door, is the acceptance test: one
// hand-built repo, one real `go test -coverprofile` output, compared against
// what `go tool cover -func` says about the same run. It is the strongest
// evidence the arithmetic is right, and it covers exactly one input.
//
// These are the other half. They generate profiles nobody chose — with the
// duplicated block sets `-coverpkg=./...` emits, files atlas has no symbols
// for, statements between declarations, and part of the symbol index withheld
// — and assert what has to hold for all of them. Issue #85 is the reason the
// distinction matters: the acceptance-shaped test for it would have been "a
// repo where two packages declare the same type", and nobody wrote that
// example for three releases. The conservation property below fails on any
// input where a statement goes missing, whatever the reason.

// attributeCase runs the real pipeline — parse, merge, attribute — over a
// generated case, using the given symbol subset as the index. It returns the
// report and the merged block set the report is accountable for.
//
// Parsing the generated TEXT rather than constructing blocks directly is the
// point: the merge step that de-duplicates `-coverpkg` repeats is inside this
// path, so a property stated over it covers the merge as well as the
// attribution. A test that hand-built []gocover.Block would silently skip it.
func attributeCase(t *testing.T, c atlastest.CoverageCase, index []atlastest.SymbolSpan) (attributionReport, map[string][]gocover.Block) {
	t.Helper()
	blocks, err := gocover.Parse(strings.NewReader(c.Profile))
	if err != nil {
		t.Fatalf("gocover.Parse: %v", err)
	}
	byFile := gocover.BlocksByFile(gocover.MergeBlocks(blocks))
	return attributeStatements(byFile, spansByFile(index)), byFile
}

// spansByFile converts generated spans into the scanner-shaped input
// indexSymbolsByFile produces, going through indexSymbolsByFile itself so the
// property exercises the real span derivation (including its "last symbol in
// the file extends to EOF" fallback) rather than a test-local imitation.
func spansByFile(index []atlastest.SymbolSpan) map[string][]symSpan {
	rows := make([]store.SymbolRow, 0, len(index))
	for _, s := range index {
		row := store.SymbolRow{
			ID:            s.ID,
			QualifiedName: shared.SymbolID(fmt.Sprintf("sym%d", s.ID)),
			FilePath:      s.File,
			Line:          s.Start,
		}
		if !s.NoEnd {
			end := s.End
			row.EndLine = &end
		}
		rows = append(rows, row)
	}
	return indexSymbolsByFile(rows)
}

// totalStmts sums the statements the merged profile holds. This is the
// denominator every accounting claim is made against.
func totalStmts(byFile map[string][]gocover.Block) int {
	n := 0
	for _, bs := range byFile {
		for _, b := range bs {
			n += b.NumStmts
		}
	}
	return n
}

// TestProperty_Attribution_ConservesStatements is issue #85 as an assertion.
//
// The bug was not bad arithmetic; it was arithmetic over a silently truncated
// input. Attribution walked the profile, charged what it could to symbols,
// and dropped the rest — no counter, no gap row, no log line. The percentage
// it printed was internally consistent and externally false, because its
// denominator was "statements atlas could place" while the label said
// "statements".
//
// Conservation is the one assertion dropping input cannot satisfy: every
// statement the profile holds is either charged to a symbol or named as a
// gap, and the two sum to the whole. It holds regardless of how bad the index
// is — that is what makes it a property rather than a threshold.
func TestProperty_Attribution_ConservesStatements(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		c := atlastest.GenCoverageCase(r, atlastest.CoverageCaseOptions{})
		rep, byFile := attributeCase(t, c, c.Indexed)

		if err := atlastest.CheckConservation(rep.stmtsAttributed, rep.stmtsUnattributed, totalStmts(byFile)); err != nil {
			t.Fatalf("seed %d: %v\ngaps: %+v", r.Seed(), err, rep.gaps())
		}
	})
}

// TestProperty_Attribution_ChargesEachStatementOnce closes the other half of
// the conservation claim.
//
// Conservation alone would still be satisfied by charging one block to two
// symbols and counting it once in the total — the sum would balance while
// every per-symbol fraction inflated. Attribution defends against that by
// keying on the block's START line and picking the tightest containing span,
// so a block has exactly one owner. This asserts the consequence: the
// per-symbol totals sum to exactly the attributed figure, no more.
func TestProperty_Attribution_ChargesEachStatementOnce(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		c := atlastest.GenCoverageCase(r, atlastest.CoverageCaseOptions{})
		rep, _ := attributeCase(t, c, c.Indexed)

		sum := 0
		for id, counts := range rep.counts {
			if counts.covered > counts.total {
				t.Fatalf("seed %d: symbol %d covered %d > total %d",
					r.Seed(), id, counts.covered, counts.total)
			}
			sum += counts.total
		}
		if sum != rep.stmtsAttributed {
			t.Fatalf("seed %d: per-symbol totals sum to %d but the run reports %d attributed; a statement is charged more than once",
				r.Seed(), sum, rep.stmtsAttributed)
		}
	})
}

// TestProperty_Attribution_IsMonotoneInTheIndex asserts that indexing more
// symbols never charges fewer statements — WHEN every symbol carries an
// explicit end_line.
//
// The unqualified claim would be that widening the index can only give
// attribution more places to put a statement, so `stmts_attributed` cannot
// fall; a fall would mean the new symbols were stealing blocks the old ones
// already owned, and a feature covered yesterday would report a regression
// nobody caused.
//
// The unqualified claim is false, and writing this property is how that was
// found. When a symbol's end_line is NULL, indexSymbolsByFile has to guess
// its span, and the guess for the LAST symbol in a file is end-of-file
// (1<<30). That symbol therefore absorbs every trailing statement in the
// file, including statements belonging to a declaration atlas has not indexed
// yet. Restore that declaration and it takes its own statements back, leaving
// any trailing gap genuinely unattributed — a decrease. Nothing is broken:
// the wider index is the more truthful one and the narrower one was
// over-claiming. But it means the property holds exactly where the spans are
// real, which is what EverySymbolEnded selects.
//
// Measured over the 64 default seeds with NULL end_lines allowed, 3 of them
// (16, 37, 58) decrease. TestAttribution_EOFFallbackCanOverAttribute below
// pins that behaviour so this caveat stays executable rather than becoming a
// comment nobody rechecks.
func TestProperty_Attribution_IsMonotoneInTheIndex(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		c := atlastest.GenCoverageCase(r, atlastest.CoverageCaseOptions{EverySymbolEnded: true})
		partial, _ := attributeCase(t, c, c.Indexed)
		full, byFile := attributeCase(t, c, c.AllSymbols)

		if err := atlastest.CheckMonotone("stmts_attributed", partial.stmtsAttributed, full.stmtsAttributed); err != nil {
			t.Fatalf("seed %d: %v", r.Seed(), err)
		}
		// The mirror image, and the one a reader should find reassuring:
		// widening the index can only SHRINK the blind spot.
		if full.stmtsUnattributed > partial.stmtsUnattributed {
			t.Fatalf("seed %d: unattributed grew from %d to %d when the index only grew",
				r.Seed(), partial.stmtsUnattributed, full.stmtsUnattributed)
		}
		// And conservation still has to hold at the wider index, otherwise
		// monotonicity could be satisfied by inventing statements.
		if err := atlastest.CheckConservation(full.stmtsAttributed, full.stmtsUnattributed, totalStmts(byFile)); err != nil {
			t.Fatalf("seed %d (full index): %v", r.Seed(), err)
		}
	})
}

// TestProperty_Attribution_IsDeterministic pins the same-input-same-answer
// claim at the level the numbers are computed.
//
// attributeStatements ranges over two maps (blocks by file, symbols by file)
// and writes into a third. Nothing about that is ordered, so a change that
// made the result depend on the traversal — a first-wins tie-break, an
// accumulator that is not commutative — would produce a percentage that
// wobbles between CI runs with no source change. That is exactly the class
// PR #99 and issue #97 were, and it is disqualifying for a tool whose pitch
// is that the numbers are true.
func TestProperty_Attribution_IsDeterministic(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		c := atlastest.GenCoverageCase(r, atlastest.CoverageCaseOptions{})
		first, _ := attributeCase(t, c, c.Indexed)
		second, _ := attributeCase(t, c, c.Indexed)

		if a, b := renderReport(first), renderReport(second); a != b {
			t.Fatalf("seed %d: attribution is not reproducible:\nfirst:\n%s\nsecond:\n%s", r.Seed(), a, b)
		}
	})
}

// TestProperty_Attribution_MergeIsIdempotent guards the per-test ingest's
// folding rule.
//
// The per-test path folds one report per test, and every one of those
// profiles describes the SAME codebase: with -coverpkg every profile names
// every file, so a file atlas cannot index appears in all of them. merge
// therefore takes the per-file MAXIMUM rather than the sum — union, not
// addition. Idempotence is the sharpest statement of that: folding a report
// into itself must change nothing. If merge ever regresses to summation, a
// 1,122-test run reports a blind spot 1,122 times larger than the code that
// exists, and this fails on the first seed.
func TestProperty_Attribution_MergeIsIdempotent(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, atlastest.DefaultCases, func(t *testing.T, r *atlastest.Rand) {
		c := atlastest.GenCoverageCase(r, atlastest.CoverageCaseOptions{})
		rep, _ := attributeCase(t, c, c.Indexed)
		before := renderReport(rep)

		self, _ := attributeCase(t, c, c.Indexed)
		rep.merge(self)

		if after := renderReport(rep); after != before {
			t.Fatalf("seed %d: merging a report into itself changed it:\nbefore:\n%s\nafter:\n%s",
				r.Seed(), before, after)
		}
	})
}

// TestProperty_Attribution_ReIngestIsStable takes the same claim through the
// real store rather than the pure function.
//
// Everything above holds over in-memory maps. This one writes a run through
// the Coverage port and reads it back through the frontier the audit actually
// scores from, twice, and requires the per-symbol numbers to be identical.
// The gap it closes is persistence: a run can compute the right counts and
// still store them against the wrong symbol (issue #97's shape), and no
// in-memory property can see that.
//
// Fewer seeds than the pure properties because each one opens a SQLite file;
// depth here buys much less than it costs.
func TestProperty_Attribution_ReIngestIsStable(t *testing.T) {
	t.Parallel()
	atlastest.ForEachSeed(t, 12, func(t *testing.T, r *atlastest.Rand) {
		c := atlastest.GenCoverageCase(r, atlastest.CoverageCaseOptions{})
		ctx := context.Background()
		s, err := store.Open(ctx, filepath.Join(t.TempDir(), "atlas.db"))
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		defer func() { _ = s.Close() }()

		// Persist the index, keeping the surrogate ids the store assigns —
		// they are what the coverage rows point at, and assuming they match
		// the generator's would be assuming away issue #97.
		for _, sp := range c.Indexed {
			end := sp.End
			if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
				QualifiedName: shared.SymbolID(fmt.Sprintf("sym%d", sp.ID)),
				Kind:          shared.KindFunc,
				FilePath:      sp.File, Line: sp.Start, EndLine: &end,
			}); err != nil {
				t.Fatalf("Symbols().Insert: %v", err)
			}
		}

		first := ingestOnce(ctx, t, s, c)
		second := ingestOnce(ctx, t, s, c)
		if first != second {
			t.Fatalf("seed %d: re-ingesting the same profile changed the per-symbol counts:\nfirst:\n%s\nsecond:\n%s",
				r.Seed(), first, second)
		}
	})
}

// ingestOnce ingests the case's profile and renders the resulting frontier as
// a canonical, sorted string keyed by QUALIFIED NAME rather than by surrogate
// id. The name is the stable identity; the surrogate is an implementation
// detail that would make the comparison pass even if the rows had moved.
func ingestOnce(ctx context.Context, t *testing.T, s *store.Store, c atlastest.CoverageCase) string {
	t.Helper()
	if _, err := IngestGoProfile(ctx, s, RunMeta{Framework: store.FrameworkGoTest}, strings.NewReader(c.Profile)); err != nil {
		t.Fatalf("IngestGoProfile: %v", err)
	}
	frontier, err := s.Coverage().LatestFrontier(ctx)
	if err != nil {
		t.Fatalf("LatestFrontier: %v", err)
	}
	results, err := s.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		t.Fatalf("ListFrontierResults: %v", err)
	}
	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		t.Fatalf("Symbols().List: %v", err)
	}
	nameByID := map[int64]string{}
	for _, sym := range syms {
		nameByID[sym.ID] = string(sym.QualifiedName)
	}

	lines := make([]string, 0, len(results))
	for _, res := range results {
		name := "<nil-symbol>"
		if res.SymbolID != nil {
			name = nameByID[*res.SymbolID]
		}
		lines = append(lines, fmt.Sprintf("%s\t%d/%d\t%s", name, res.CoveredStmts, res.TotalStmts, res.Status))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// renderReport canonicalises a report for equality comparison: per-symbol
// counts and per-file gaps, sorted. Rendering rather than reflect.DeepEqual
// so a failure message shows WHICH symbol moved.
func renderReport(r attributionReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "attributed=%d unattributed=%d matched=%d unmatched=%d\n",
		r.stmtsAttributed, r.stmtsUnattributed, r.filesMatched, r.filesUnmatched)
	for _, id := range sortedCountKeys(r.counts) {
		fmt.Fprintf(&b, "SYM %d %d/%d\n", id, r.counts[id].covered, r.counts[id].total)
	}
	for _, g := range r.gaps() {
		fmt.Fprintf(&b, "GAP %s %d %s\n", g.Path, g.Stmts, g.Reason)
	}
	return b.String()
}

// FuzzAttribution_ConservesStatements is the deep-search entry point for the
// conservation property. `go test ./packages/coverage -fuzz=FuzzAttribution`
// drives the same generator from the fuzzer's corpus instead of from a fixed
// seed list, so a long run explores case shapes the 64 default seeds never
// reach. Under a plain `go test` it runs only the seed corpus below, which
// costs microseconds — the everyday coverage comes from the Test above.
func FuzzAttribution_ConservesStatements(f *testing.F) {
	for _, seed := range []uint64{1, 2, 3, 7, 11, 4242} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		c := atlastest.GenCoverageCase(atlastest.New(seed), atlastest.CoverageCaseOptions{})
		rep, byFile := attributeCase(t, c, c.Indexed)
		if err := atlastest.CheckConservation(rep.stmtsAttributed, rep.stmtsUnattributed, totalStmts(byFile)); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	})
}

// TestAttribution_EOFFallbackCanOverAttribute pins the exception the
// monotonicity property had to be scoped around, so the scope is enforced
// rather than asserted in a comment.
//
// The shape: an index in which some symbols have no end_line. The last symbol
// in a file is then given an end of 1<<30 and absorbs every trailing
// statement, including statements belonging to declarations atlas has not
// indexed yet. Widening the index corrects that downward.
//
// This is deliberately a characterisation test, not an aspiration. If a
// future change makes attribution monotone unconditionally — by refusing to
// extend an unbounded span past the last block it can justify, say — this
// test fails, and the right response is to delete it and drop
// EverySymbolEnded from the property above. A characterisation test that
// stops characterising is information; the comment it replaces would have
// gone quietly out of date instead.
func TestAttribution_EOFFallbackCanOverAttribute(t *testing.T) {
	t.Parallel()

	decreased := 0
	for seed := uint64(1); seed <= atlastest.DefaultCases; seed++ {
		c := atlastest.GenCoverageCase(atlastest.New(seed), atlastest.CoverageCaseOptions{})
		partial, _ := attributeCase(t, c, c.Indexed)
		full, _ := attributeCase(t, c, c.AllSymbols)
		if full.stmtsAttributed < partial.stmtsAttributed {
			decreased++
		}
	}
	if decreased == 0 {
		t.Fatal("no seed over-attributes through the end-of-file fallback any more; " +
			"attribution may now be monotone unconditionally - drop EverySymbolEnded " +
			"from TestProperty_Attribution_IsMonotoneInTheIndex and delete this test")
	}
	t.Logf("%d of %d seeds over-attribute through the end-of-file fallback",
		decreased, atlastest.DefaultCases)
}
