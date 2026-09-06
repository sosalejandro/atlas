package patch

import (
	"math"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func sym(id int64, name, path string, line int, end *int) store.SymbolRow {
	return store.SymbolRow{
		ID:            id,
		QualifiedName: shared.SymbolID(name),
		FilePath:      path,
		Line:          line,
		EndLine:       end,
	}
}

func intp(v int) *int { return &v }

// covered/uncovered/partial symbols in one indexed file, plus a file atlas
// has never seen. This is the shape every assertion below leans on.
func baseInput() Input {
	return Input{
		Changes: []FileChange{
			{Path: "pkg/a.go", Ranges: []LineRange{{Start: 5, End: 14}, {Start: 25, End: 29}}},
		},
		Symbols: []store.SymbolRow{
			sym(1, "pkg.Covered", "pkg/a.go", 1, intp(20)),
			sym(2, "pkg.Uncovered", "pkg/a.go", 21, intp(40)),
		},
		Coverage: map[int64]SymbolCoverage{
			1: {Covered: 10, Total: 10},
			2: {Covered: 0, Total: 8},
		},
	}
}

func approx(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// The known fraction is line-weighted: each touched symbol contributes its
// changed lines at the symbol's own covered/total statement ratio. Counting
// symbols instead would let a one-line tweak to a big covered function
// outvote a whole new uncovered one.
func TestScore_KnownFractionIsLineWeighted(t *testing.T) {
	r := Score(baseInput())
	if r.ChangedLines != 15 {
		t.Errorf("ChangedLines = %d, want 15", r.ChangedLines)
	}
	if r.KnownLines != 15 {
		t.Errorf("KnownLines = %d, want 15", r.KnownLines)
	}
	approx(t, "CoveredLines", r.CoveredLines, 10)
	if !r.Measurable || r.Percent == nil {
		t.Fatalf("a diff touching indexed code must be measurable: %+v", r)
	}
	approx(t, "Percent", *r.Percent, 100*10.0/15.0)
}

// A partially covered symbol contributes its fraction of its changed lines --
// atlas knows the symbol's ratio, not which of ITS lines ran, so claiming
// either all or none of the changed lines would be a fabrication.
func TestScore_PartialSymbolContributesItsFraction(t *testing.T) {
	in := baseInput()
	in.Changes = []FileChange{{Path: "pkg/a.go", Ranges: []LineRange{{Start: 45, End: 48}}}}
	in.Symbols = append(in.Symbols, sym(3, "pkg.Partial", "pkg/a.go", 41, intp(60)))
	in.Coverage[3] = SymbolCoverage{Covered: 3, Total: 4}

	r := Score(in)
	approx(t, "CoveredLines", r.CoveredLines, 3)
	approx(t, "Percent", *r.Percent, 75)
	if len(r.Partial) != 1 || r.Partial[0].SymbolID != 3 {
		t.Errorf("partial symbol not reported: %+v", r.Partial)
	}
	// It is NOT in the uncovered list: atlas cannot name which of its lines
	// are the gap, and printing lines it cannot vouch for is worse than
	// printing none.
	if len(r.Uncovered) != 0 {
		t.Errorf("partial symbol leaked into the uncovered list: %+v", r.Uncovered)
	}
}

// The reason this feature has a third state: a changed line atlas has no
// symbol for is UNKNOWN, not uncovered. Scoring it as uncovered fires the
// gate on files atlas simply cannot see, which trains teams to switch the
// gate off; scoring it as covered hides real gaps.
func TestScore_UnknownIsItsOwnBucketAndDoesNotMoveThePercentage(t *testing.T) {
	in := baseInput()
	in.Changes = append(in.Changes, FileChange{
		Path:   "docs/readme.md",
		Ranges: []LineRange{{Start: 1, End: 50}},
	})

	r := Score(in)
	if r.ChangedLines != 65 {
		t.Errorf("ChangedLines = %d, want 65", r.ChangedLines)
	}
	if r.KnownLines != 15 {
		t.Errorf("KnownLines = %d, want 15 (unknown lines must not enter the denominator)", r.KnownLines)
	}
	if r.UnknownLines != 50 {
		t.Errorf("UnknownLines = %d, want 50", r.UnknownLines)
	}
	approx(t, "Percent", *r.Percent, 100*10.0/15.0)

	var found bool
	for _, u := range r.Unknown {
		if u.Path == "docs/readme.md" {
			found = true
			if u.Reason != ReasonFileNotIndexed {
				t.Errorf("reason = %q, want %q", u.Reason, ReasonFileNotIndexed)
			}
		}
	}
	if !found {
		t.Errorf("the unindexed file is not enumerated: %+v", r.Unknown)
	}
}

// No denominator is not zero percent. A docs-only PR must exit clean under
// any --fail-under; reporting 0% would make the gate unfailable-by-design.
func TestScore_NoIndexedChangeIsNotMeasurable(t *testing.T) {
	r := Score(Input{
		Changes: []FileChange{{Path: "docs/readme.md", Ranges: []LineRange{{Start: 1, End: 50}}}},
	})
	if r.Measurable {
		t.Errorf("a diff touching no indexed code must not be measurable: %+v", r)
	}
	if r.Percent != nil {
		t.Errorf("Percent = %v, want nil", *r.Percent)
	}
	if !r.Meets(100) {
		t.Errorf("an unmeasurable diff must satisfy every threshold")
	}
	if r.UnknownLines != 50 {
		t.Errorf("UnknownLines = %d, want 50 -- the bucket still has to be loud", r.UnknownLines)
	}
}

// A number without the lines is not actionable: the changed lines of a
// symbol with zero covered statements are provably uncovered, so they are
// named exactly.
func TestScore_UncoveredChangedLinesAreNamed(t *testing.T) {
	r := Score(baseInput())
	if len(r.Uncovered) != 1 {
		t.Fatalf("uncovered spans = %+v, want exactly one", r.Uncovered)
	}
	u := r.Uncovered[0]
	if u.Path != "pkg/a.go" || u.SymbolID != 2 {
		t.Errorf("uncovered span = %+v", u)
	}
	if len(u.Ranges) != 1 || u.Ranges[0] != (LineRange{Start: 25, End: 29}) {
		t.Errorf("uncovered ranges = %+v, want [{25 29}]", u.Ranges)
	}
}

// A symbol atlas indexed but the frontier never measured (a type decl, a
// language whose runs carry only pass/fail) has no statement counts. That is
// unknown too -- scoring it 0% would gate on the absence of a measurement.
func TestScore_SymbolWithoutStatementCountsIsUnknown(t *testing.T) {
	in := baseInput()
	in.Symbols = append(in.Symbols, sym(4, "pkg.Config", "pkg/a.go", 61, intp(70)))
	in.Changes = []FileChange{{Path: "pkg/a.go", Ranges: []LineRange{{Start: 62, End: 66}}}}

	r := Score(in)
	if r.Measurable {
		t.Errorf("nothing measurable was touched: %+v", r)
	}
	if r.UnknownLines != 5 {
		t.Errorf("UnknownLines = %d, want 5", r.UnknownLines)
	}
	if len(r.Unknown) != 1 || r.Unknown[0].Reason != ReasonNoCoverageData {
		t.Errorf("unknown = %+v, want one %q span", r.Unknown, ReasonNoCoverageData)
	}
}

// A symbol with no end_line gets a BOUNDED fallback span. Letting the last
// symbol in a file run to EOF (which the profile ingest can afford, because
// its input only ever names real code) would silently charge a freshly
// appended, never-indexed function to whatever happened to precede it --
// inventing coverage for code atlas has never seen.
func TestScore_MissingEndLineDoesNotSwallowTheFileTail(t *testing.T) {
	in := Input{
		Changes: []FileChange{{Path: "web/app.ts", Ranges: []LineRange{
			{Start: 32, End: 36},   // inside the fallback horizon: attributed
			{Start: 400, End: 404}, // far past it: unknown, not attributed
		}}},
		Symbols: []store.SymbolRow{
			sym(10, "app.first", "web/app.ts", 1, nil),
			sym(11, "app.last", "web/app.ts", 30, nil),
		},
		Coverage: map[int64]SymbolCoverage{
			10: {Covered: 4, Total: 4},
			11: {Covered: 5, Total: 5},
		},
	}
	r := Score(in)
	if r.KnownLines != 5 {
		t.Errorf("KnownLines = %d, want 5 (only the lines inside the bounded span)", r.KnownLines)
	}
	if r.UnknownLines != 5 {
		t.Errorf("UnknownLines = %d, want 5 (the file tail)", r.UnknownLines)
	}
	if len(r.Unknown) != 1 || r.Unknown[0].Reason != ReasonOutsideSymbolSpans {
		t.Errorf("unknown = %+v, want one %q span", r.Unknown, ReasonOutsideSymbolSpans)
	}
}

// When spans nest, the innermost wins: a method's own coverage is a sharper
// answer than its enclosing type's.
func TestScore_InnermostSpanWins(t *testing.T) {
	in := Input{
		Changes: []FileChange{{Path: "pkg/a.go", Ranges: []LineRange{{Start: 12, End: 13}}}},
		Symbols: []store.SymbolRow{
			sym(1, "pkg.Outer", "pkg/a.go", 1, intp(100)),
			sym(2, "pkg.Outer.Inner", "pkg/a.go", 10, intp(20)),
		},
		Coverage: map[int64]SymbolCoverage{
			1: {Covered: 50, Total: 50},
			2: {Covered: 0, Total: 4},
		},
	}
	r := Score(in)
	approx(t, "CoveredLines", r.CoveredLines, 0)
	if len(r.Uncovered) != 1 || r.Uncovered[0].SymbolID != 2 {
		t.Errorf("uncovered = %+v, want the inner symbol", r.Uncovered)
	}
}

// The rollup a reviewer reads first: which capabilities did this diff touch,
// and how covered is the part of them that moved.
func TestScore_FeatureRollup(t *testing.T) {
	in := baseInput()
	in.Features = map[int64][]shared.FeatureID{
		1: {"billing.checkout"},
		2: {"measurements.log-entry"},
	}
	r := Score(in)
	if len(r.Features) != 2 {
		t.Fatalf("features = %+v, want 2", r.Features)
	}
	// Biggest touched surface first, so a truncated read sees the most of
	// the diff.
	if r.Features[0].FeatureID != "billing.checkout" || r.Features[0].Lines != 10 {
		t.Errorf("features[0] = %+v", r.Features[0])
	}
	approx(t, "billing percent", r.Features[0].Percent, 100)
	if r.Features[1].FeatureID != "measurements.log-entry" {
		t.Errorf("features[1] = %+v", r.Features[1])
	}
	approx(t, "measurements percent", r.Features[1].Percent, 0)
}

// Per-file rollup keeps the three buckets separable at the level a reviewer
// navigates by.
func TestScore_FileRollupSeparatesTheBuckets(t *testing.T) {
	in := baseInput()
	in.Changes = append(in.Changes, FileChange{Path: "docs/readme.md", Ranges: []LineRange{{Start: 1, End: 3}}})
	r := Score(in)
	if r.ChangedFiles != 2 {
		t.Errorf("ChangedFiles = %d, want 2", r.ChangedFiles)
	}
	byPath := map[string]FileRow{}
	for _, f := range r.Files {
		byPath[f.Path] = f
	}
	if got := byPath["pkg/a.go"]; got.KnownLines != 15 || got.UnknownLines != 0 {
		t.Errorf("pkg/a.go row = %+v", got)
	}
	if got := byPath["docs/readme.md"]; got.KnownLines != 0 || got.UnknownLines != 3 || got.Percent != nil {
		t.Errorf("docs/readme.md row = %+v", got)
	}
}

func TestScore_MeetsComparesTheKnownFraction(t *testing.T) {
	r := Score(baseInput()) // 66.7%
	if r.Meets(80) {
		t.Errorf("66.7%% must not meet a target of 80")
	}
	if !r.Meets(66) {
		t.Errorf("66.7%% must meet a target of 66")
	}
}
