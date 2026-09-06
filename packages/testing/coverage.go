package atlastest

import (
	"fmt"
	"sort"
	"strings"
)

// SymbolSpan is one symbol's source range as the coverage layer sees it:
// a surrogate id, a repo-relative file, and the inclusive [Start, End] line
// range attribution charges statements to.
type SymbolSpan struct {
	ID    int64
	File  string
	Start int
	End   int

	// NoEnd models a stored symbol whose end_line column is NULL — every
	// row written before migration 0011, and everything the sub-scanners
	// that still do not populate it produce. The coverage layer then has to
	// GUESS the span (next symbol's start minus one, or end-of-file for the
	// last symbol in a file), and those guesses behave differently enough
	// from a real span that a generator which never produced them would be
	// testing a world atlas does not live in.
	NoEnd bool
}

// CoverageCase is one generated attribution scenario: a symbol index, a raw
// coverage profile, and the ground truth about how they relate.
//
// The three parts exist separately because the interesting failures live in
// the gaps between them. AllSymbols is what the source contains; Indexed is
// what atlas managed to record; Profile is what the compiler measured. Issue
// #85 was precisely the case where Indexed was a proper subset of AllSymbols
// and the difference was reported as coverage rather than as a blind spot.
type CoverageCase struct {
	// ModulePath is the import prefix the profile qualifies file paths with.
	// Profiles say "github.com/org/repo/pkg/f.go"; atlas says "pkg/f.go".
	// Keeping the two different is not cosmetic — the suffix reconciliation
	// between them is a step attribution can silently lose whole files at.
	ModulePath string

	// AllSymbols is every symbol in the generated files, ordered by file
	// then start line.
	AllSymbols []SymbolSpan

	// Indexed is the subset of AllSymbols atlas is pretending to know about.
	// The complement models symbols lost to a name collision or skipped as
	// generated: their statements must surface as unattributed, never as
	// somebody else's coverage.
	Indexed []SymbolSpan

	// Profile is the raw cover.out text — a mode header, blocks inside
	// spans, blocks in the gaps between declarations, files atlas has no
	// symbol for at all, and (often) the whole block set repeated the way
	// `-coverpkg=./...` repeats it once per tested package.
	Profile string

	// UnindexedFiles are profile paths that reconcile to no atlas file.
	UnindexedFiles []string
}

// CoverageCaseOptions narrows the generated scenario.
type CoverageCaseOptions struct {
	// EverySymbolEnded suppresses NoEnd spans, so every symbol carries an
	// explicit end_line.
	//
	// It exists for one property and one reason. Attribution is monotone in
	// the index — indexing more symbols never charges fewer statements —
	// only when the spans are real. With a NULL end_line the last symbol in
	// a file is given an end of 1<<30, so it absorbs every trailing
	// statement in the file including ones that belong to a declaration
	// atlas has not indexed yet; restoring that declaration takes its
	// statements back and can leave the trailing gap unattributed, which is
	// a DECREASE. That is not a bug in attribution, it is the guess being
	// corrected — but it does mean the monotonicity property is false in
	// general and true exactly here. See docs/testing/strategy.md.
	EverySymbolEnded bool

	// NoNesting suppresses enclosed spans. Nesting is on by default because
	// the tightest-span tie-break in owningSymbol exists only for it: with
	// disjoint spans every block has exactly one candidate owner and the
	// tie-break is dead code the properties never reach.
	NoNesting bool
}

// GenCoverageCase builds one attribution scenario, adversarial by default:
// some files are unindexed, some statements fall between declarations, some
// symbols are withheld from the index, some spans are nested inside others,
// some carry no end_line at all, and the block set is duplicated often enough
// that a run which forgot to merge fails on most seeds rather than on a lucky
// one.
func GenCoverageCase(r *Rand, opts CoverageCaseOptions) CoverageCase {
	c := CoverageCase{ModulePath: "github.com/example/mod"}

	var lines []string
	nextID := int64(1)
	for fi := range r.IntRange(1, 4) {
		file := fmt.Sprintf("pkg%d/f.go", fi)
		spans, blocks := genFileSpansAndBlocks(r, &nextID, file, opts)
		c.AllSymbols = append(c.AllSymbols, spans...)
		lines = append(lines, blocks...)
	}

	// Files the profile names that atlas has no symbol for at all. This is
	// the "no-indexed-symbol" gap reason, and the half of issue #85 that
	// mattered most: 210 production files whose execution was invisible.
	for gi := range r.IntRange(0, 2) {
		file := fmt.Sprintf("ghost%d/g.go", gi)
		c.UnindexedFiles = append(c.UnindexedFiles, c.ModulePath+"/"+file)
		_, blocks := genFileSpansAndBlocks(r, &nextID, file, opts)
		lines = append(lines, blocks...)
	}

	sortSpans(c.AllSymbols)
	for _, s := range c.AllSymbols {
		if r.Chance(80, 100) {
			c.Indexed = append(c.Indexed, s)
		}
	}

	// The -coverpkg shape: the same block set repeated. A run that sums
	// instead of merging inflates every total by the repeat factor and
	// deflates every ratio.
	repeats := 1
	if r.Chance(1, 2) {
		repeats = r.IntRange(2, 3)
	}
	var b strings.Builder
	b.WriteString("mode: set\n")
	for range repeats {
		for _, l := range lines {
			b.WriteString(c.ModulePath + "/" + l + "\n")
		}
	}
	c.Profile = b.String()
	return c
}

// sortSpans orders spans by file, then start, then end. Outer spans sort
// before the spans nested in them, which is the order indexSymbolsByFile
// expects and the order a reader of a failure message expects too.
func sortSpans(spans []SymbolSpan) {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].File != spans[j].File {
			return spans[i].File < spans[j].File
		}
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End > spans[j].End
	})
}

// genFileSpansAndBlocks lays out one file: ascending disjoint declarations
// separated by gaps, some of them holding a nested declaration, with coverage
// blocks inside the declarations and sometimes in the gaps. It returns the
// spans and the profile lines WITHOUT the module prefix — the caller adds the
// prefix and the repeats.
func genFileSpansAndBlocks(r *Rand, nextID *int64, file string, opts CoverageCaseOptions) ([]SymbolSpan, []string) {
	var spans []SymbolSpan
	var lines []string

	line := r.IntRange(3, 10)
	for range r.IntRange(1, 5) {
		length := r.IntRange(4, 14)
		outer := SymbolSpan{ID: *nextID, File: file, Start: line, End: line + length}
		*nextID++
		if !opts.EverySymbolEnded && r.Chance(1, 5) {
			outer.NoEnd = true
		}
		spans = append(spans, outer)

		// A nested declaration, wholly inside the outer one. Real trees
		// produce these whenever a scanner indexes something inside a
		// function body, and they are the only input under which the
		// tightest-span tie-break decides anything.
		if !opts.NoNesting && outer.End-outer.Start >= 6 && r.Chance(1, 3) {
			innerStart := r.IntRange(outer.Start+1, outer.End-3)
			inner := SymbolSpan{
				ID: *nextID, File: file,
				Start: innerStart, End: r.IntRange(innerStart+1, outer.End-1),
			}
			*nextID++
			spans = append(spans, inner)
			lines = append(lines, blockLine(file, inner.Start, inner.End, r.IntRange(1, 3), r.IntN(3)))
		}

		// Blocks inside the outer declaration.
		for range r.IntRange(1, 3) {
			bs := r.IntRange(outer.Start, outer.End)
			be := r.IntRange(bs, outer.End)
			lines = append(lines, blockLine(file, bs, be, r.IntRange(1, 4), r.IntN(3)))
		}
		line = outer.End + 1

		// A gap: lines belonging to no declaration. Real profiles have
		// these whenever the scanner did not index something the compiler
		// instrumented, and they are the "outside-symbol-spans" reason.
		if r.Chance(1, 3) {
			gap := r.IntRange(1, 4)
			lines = append(lines, blockLine(file, line, line+gap-1, r.IntRange(1, 3), r.IntN(3)))
			line += gap
		}
		line += r.IntRange(1, 3)
	}
	return spans, lines
}

// blockLine renders one profile block. Columns are fixed: attribution keys on
// the START LINE only, so varying columns would add noise without adding a
// case the code can distinguish.
func blockLine(file string, startLine, endLine, stmts, count int) string {
	return fmt.Sprintf("%s:%d.2,%d.3 %d %d", file, startLine, endLine, stmts, count)
}
