package patch

import (
	"sort"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// Reasons a changed line lands in the UNKNOWN bucket. Each one is a distinct
// remedy, which is why they are reported separately rather than as one count:
// the first is fixed by indexing the file, the second by re-scanning, the
// third by measuring the language at all.
const (
	// ReasonFileNotIndexed: atlas holds no symbol for this file. A docs
	// edit, a config file, a language no scanner covers, or a source file
	// that was never scanned.
	ReasonFileNotIndexed = "file-not-indexed"
	// ReasonOutsideSymbolSpans: the file IS indexed, but these lines fall
	// outside every indexed symbol's span -- package-level declarations,
	// code added after the last scan, or a symbol whose recorded span has
	// gone stale.
	ReasonOutsideSymbolSpans = "outside-symbol-spans"
	// ReasonNoCoverageData: a symbol owns these lines, but the coverage
	// frontier carries no statement counts for it -- a type or const
	// declaration with nothing to execute, or a framework that reports only
	// pass/fail. Nothing measured it, so nothing can be concluded.
	ReasonNoCoverageData = "no-coverage-data"
	// ReasonIndexStale: the file IS indexed, but the spans atlas holds for it
	// describe a version of the file that is no longer on disk (or cannot be
	// corroborated at all). Joining post-image line numbers against them
	// would charge the changed lines to whichever symbol has since drifted
	// into that range -- a confident wrong number rather than a missing one.
	// Distinct from ReasonOutsideSymbolSpans because the remedy is different:
	// there is nothing to add to the index, only a re-scan at HEAD.
	ReasonIndexStale = "index-stale"
)

// UnboundedSpanHorizon bounds the fallback span of a symbol the index has no
// end_line for.
//
// The profile ingest can afford to let a file's last symbol run to EOF: its
// input is a coverage profile, so every line it attributes is line the
// compiler saw. Patch coverage has no such guarantee -- a diff routinely adds
// code past the end of everything the last scan indexed, and charging it to
// whatever symbol happened to precede it would invent a coverage verdict for
// source atlas has never seen. A bounded horizon keeps the common case (a
// scanner that emits no end_line, such as the TypeScript one) measurable
// while leaving the genuinely-new tail in the honest UNKNOWN bucket.
const UnboundedSpanHorizon = 80

// SymbolCoverage is one symbol's statement accounting from the coverage
// frontier: Σ covered and Σ total over the frontier's results for it.
//
// Total == 0 means "never measured", which is NOT the same as "measured and
// nothing ran" (Covered == 0, Total > 0). The first is unknown; the second is
// a real, gateable gap.
type SymbolCoverage struct {
	Covered int `json:"covered_stmts"`
	Total   int `json:"total_stmts"`
}

// measured reports whether the frontier says anything about this symbol.
func (c SymbolCoverage) measured() bool { return c.Total > 0 }

// fraction is the symbol's covered ratio in [0,1]. Only meaningful when
// measured() is true.
func (c SymbolCoverage) fraction() float64 {
	if c.Total <= 0 {
		return 0
	}
	f := float64(c.Covered) / float64(c.Total)
	if f > 1 {
		// A frontier that pooled two runs measuring the same symbol can
		// over-count; clamp rather than report >100% patch coverage.
		return 1
	}
	return f
}

// Input is everything Score needs, already read from the store by the caller.
// Passing it in rather than taking a *store.Store keeps the accounting a pure
// function -- which is what makes the three-state rules testable at all.
type Input struct {
	// Changes are the diff's post-image line ranges, from Diff/ParseDiff.
	Changes []FileChange
	// Symbols is the whole symbol index (file paths repo-relative, matching
	// the paths git reports).
	Symbols []store.SymbolRow
	// Coverage maps symbol id to the frontier's statement counts. A symbol
	// absent from the map was never measured.
	Coverage map[int64]SymbolCoverage
	// Features maps symbol id to the features it implements. Optional: it
	// only drives the per-feature rollup.
	Features map[int64][]shared.FeatureID
	// StaleSpans names the changed files whose stored spans must NOT be
	// joined against the diff's post-image line numbers, mapped to the state
	// that disqualified them ("stale", "deleted", "absent", "unreadable" --
	// see packages/indexfresh). A file listed here has its changed lines
	// booked to ReasonIndexStale instead of being scored.
	//
	// The map is optional and its absence means "the caller did not check",
	// not "everything is fresh" -- Score cannot verify freshness itself
	// (it never sees the working tree), so the check belongs to the caller
	// and this is where the answer arrives.
	StaleSpans map[string]string
}

// SymbolSpan is one touched symbol and the changed lines inside it.
type SymbolSpan struct {
	Path     string             `json:"path"`
	SymbolID int64              `json:"symbol_id"`
	Symbol   shared.SymbolID    `json:"symbol"`
	Ranges   []LineRange        `json:"ranges"`
	Lines    int                `json:"lines"`
	Covered  int                `json:"covered_stmts"`
	Total    int                `json:"total_stmts"`
	Fraction float64            `json:"fraction"`
	Features []shared.FeatureID `json:"features,omitempty"`
}

// UnknownSpan is a run of changed lines atlas cannot score, and why.
type UnknownSpan struct {
	Path   string      `json:"path"`
	Ranges []LineRange `json:"ranges"`
	Lines  int         `json:"lines"`
	Reason string      `json:"reason"`
}

// StaleFile is one changed file whose stored spans were refused, and why.
//
// Reported as its own list rather than left to be dug out of the unknown
// bucket: a patch-coverage percentage computed over a stale index is a
// confident wrong number, and the reader has to be told the denominator
// shrank before they read the percentage.
type StaleFile struct {
	Path string `json:"path"`
	// State is the indexfresh classification: "stale" (the file changed
	// since the scan), "deleted", "absent" (no hash row to compare against)
	// or "unreadable".
	State string `json:"state"`
	// Lines is how many of the file's changed lines went unscored for it.
	Lines int `json:"lines"`
}

// FileRow is the per-file rollup, at the granularity a reviewer navigates by.
type FileRow struct {
	Path         string   `json:"path"`
	ChangedLines int      `json:"changed_lines"`
	KnownLines   int      `json:"known_lines"`
	CoveredLines float64  `json:"covered_lines"`
	UnknownLines int      `json:"unknown_lines"`
	Percent      *float64 `json:"percent"`
}

// FeatureRow is the per-feature rollup: which capabilities this diff moved,
// and how covered the part that moved is.
type FeatureRow struct {
	FeatureID    shared.FeatureID `json:"feature_id"`
	Lines        int              `json:"lines"`
	CoveredLines float64          `json:"covered_lines"`
	Percent      float64          `json:"percent"`
}

// Result is the full patch-coverage accounting.
//
// CoveredLines is a line-EQUIVALENT and is deliberately fractional: atlas
// knows a symbol's covered/total statement ratio, not which of its individual
// lines ran, so a changed line in a 3-of-4 symbol contributes 0.75. Rounding
// it to a whole line at this layer would make the reported percentage
// disagree with itself; the CLI rounds only for display.
type Result struct {
	ChangedFiles int `json:"changed_files"`
	ChangedLines int `json:"changed_lines"`

	KnownLines     int     `json:"known_lines"`
	CoveredLines   float64 `json:"covered_lines"`
	UncoveredLines float64 `json:"uncovered_lines"`
	UnknownLines   int     `json:"unknown_lines"`

	// Measurable is false when the diff produced no denominator: nothing it
	// changed is both indexed and measured. That is NOT 0% -- see Meets.
	Measurable bool `json:"measurable"`
	// Percent is the known patch fraction, nil when !Measurable.
	Percent *float64 `json:"percent"`

	// StaleIndexFiles are the changed files whose spans were refused, sorted
	// by path, and StaleIndexLines the changed lines they cost the
	// denominator. Both are always present (empty, not null) so a PR bot can
	// branch on length without a nil check.
	StaleIndexFiles []StaleFile `json:"stale_index_files"`
	StaleIndexLines int         `json:"stale_index_lines"`

	// Every slice below is initialised, never nil: a `null` where a consumer
	// expects an array is a runtime error in the PR-comment bot this JSON
	// exists for.
	Uncovered []SymbolSpan  `json:"uncovered"`
	Partial   []SymbolSpan  `json:"partial"`
	Covered   []SymbolSpan  `json:"covered"`
	Unknown   []UnknownSpan `json:"unknown"`
	Files     []FileRow     `json:"files"`
	Features  []FeatureRow  `json:"features"`
}

// Meets reports whether the result clears a --fail-under threshold.
//
// An unmeasurable diff clears EVERY threshold. A diff that changed no indexed
// code has no patch coverage to speak of, and reporting it as 0% would fail
// every docs-only, config-only or vendored change -- a gate that fires on
// work nobody can fix is a gate teams delete.
func (r Result) Meets(threshold float64) bool {
	if !r.Measurable || r.Percent == nil {
		return true
	}
	return *r.Percent >= threshold
}

// Score intersects the diff's changed lines with the symbol index and charges
// each line to one of three buckets: covered, uncovered, or unknown.
func Score(in Input) Result {
	spansByFile := buildSpans(in.Symbols)
	acc := newAccumulator(in)
	for _, change := range in.Changes {
		acc.file(change, spansByFile[change.Path])
	}
	return acc.result()
}

// symSpan is one symbol's effective source range.
type symSpan struct {
	id    int64
	name  shared.SymbolID
	start int
	end   int
}

// width is the span's line count, used to prefer the innermost span when
// several contain the same line.
func (s symSpan) width() int { return s.end - s.start + 1 }

// buildSpans groups symbols by file and resolves each one's effective range.
//
// end_line wins when the index has it. Otherwise the span is bounded twice
// over: by the next symbol's declaration (a symbol cannot extend into its
// successor) and by UnboundedSpanHorizon (see that constant for why an
// unbounded tail is the dangerous case here).
func buildSpans(syms []store.SymbolRow) map[string][]symSpan {
	grouped := map[string][]store.SymbolRow{}
	for _, s := range syms {
		if s.FilePath == "" || s.Line <= 0 {
			continue
		}
		grouped[s.FilePath] = append(grouped[s.FilePath], s)
	}
	out := make(map[string][]symSpan, len(grouped))
	for file, rows := range grouped {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Line != rows[j].Line {
				return rows[i].Line < rows[j].Line
			}
			return rows[i].ID < rows[j].ID
		})
		spans := make([]symSpan, 0, len(rows))
		for i, row := range rows {
			nextStart := 0
			if i+1 < len(rows) {
				nextStart = rows[i+1].Line
			}
			spans = append(spans, symSpan{
				id:    row.ID,
				name:  row.QualifiedName,
				start: row.Line,
				end:   spanEnd(row, nextStart),
			})
		}
		out[file] = spans
	}
	return out
}

// spanEnd resolves one symbol's last line. nextStart is the following
// symbol's declaration line, or 0 when this is the last symbol in the file.
func spanEnd(row store.SymbolRow, nextStart int) int {
	if row.EndLine != nil && *row.EndLine >= row.Line {
		return *row.EndLine
	}
	end := row.Line + UnboundedSpanHorizon
	// A sibling declaration is a hard ceiling: whatever the horizon says, a
	// symbol does not extend into the next one.
	if nextStart > row.Line && nextStart-1 < end {
		end = nextStart - 1
	}
	return end
}

// owner returns the narrowest span containing line, if any. Narrowest rather
// than first so a method nested inside a type's span is answered with the
// method's own coverage, which is the sharper fact.
func owner(spans []symSpan, line int) (symSpan, bool) {
	var best symSpan
	found := false
	for _, s := range spans {
		if line < s.start || line > s.end {
			continue
		}
		if !found || s.width() < best.width() || (s.width() == best.width() && s.id < best.id) {
			best, found = s, true
		}
	}
	return best, found
}

// accumulator folds the per-line verdicts into the rollups Result exposes.
// It exists so Score itself stays a three-line statement of intent rather
// than a hundred lines of bookkeeping.
type accumulator struct {
	in Input

	changedLines int
	changedFiles int
	knownLines   int
	coveredLines float64
	unknownLines int

	// touched is per symbol id: the changed ranges inside it.
	touched map[int64]*SymbolSpan
	// order preserves first-seen symbol order for a deterministic tiebreak.
	order []int64

	unknown []UnknownSpan
	files   []FileRow
	stale   []StaleFile
}

func newAccumulator(in Input) *accumulator {
	return &accumulator{in: in, touched: map[int64]*SymbolSpan{}}
}

// file charges one file's changed ranges against the spans indexed for it.
func (a *accumulator) file(change FileChange, spans []symSpan) {
	lines := change.Lines()
	if lines == 0 {
		return
	}
	a.changedFiles++
	a.changedLines += lines

	row := FileRow{Path: change.Path, ChangedLines: lines}
	// Reasons are collected per file so consecutive lines sharing a reason
	// collapse into one reported range instead of one entry per line.
	unknownRanges := map[string][]LineRange{}

	if state, refused := a.refuseSpans(change.Path, spans); refused {
		a.staleFile(change, state, lines, &row, unknownRanges)
	} else {
		for _, r := range change.Ranges {
			for line := r.Start; line <= r.End; line++ {
				a.line(change.Path, line, spans, &row, unknownRanges)
			}
		}
	}
	a.emitUnknown(change.Path, unknownRanges)
	if row.KnownLines > 0 {
		pct := 100 * row.CoveredLines / float64(row.KnownLines)
		row.Percent = &pct
	}
	a.files = append(a.files, row)
}

// refuseSpans reports whether this file's stored spans are disqualified, and
// the state that disqualified them.
//
// A file with no spans at all is NOT refused here: it already books to
// ReasonFileNotIndexed, which is the sharper answer. Staleness only matters
// where a join would otherwise have happened.
func (a *accumulator) refuseSpans(path string, spans []symSpan) (string, bool) {
	if len(spans) == 0 {
		return "", false
	}
	state, ok := a.in.StaleSpans[path]
	return state, ok
}

// staleFile books every changed line of a file whose spans were refused,
// without consulting those spans.
func (a *accumulator) staleFile(
	change FileChange, state string, lines int, row *FileRow, unknownRanges map[string][]LineRange,
) {
	for _, r := range change.Ranges {
		for line := r.Start; line <= r.End; line++ {
			a.markUnknown(ReasonIndexStale, line, row, unknownRanges)
		}
	}
	a.stale = append(a.stale, StaleFile{Path: change.Path, State: state, Lines: lines})
}

// line charges a single changed line to its bucket.
func (a *accumulator) line(path string, line int, spans []symSpan, row *FileRow, unknownRanges map[string][]LineRange) {
	span, ok := owner(spans, line)
	if !ok {
		reason := ReasonOutsideSymbolSpans
		if len(spans) == 0 {
			reason = ReasonFileNotIndexed
		}
		a.markUnknown(reason, line, row, unknownRanges)
		return
	}
	cov := a.in.Coverage[span.id]
	if !cov.measured() {
		a.markUnknown(ReasonNoCoverageData, line, row, unknownRanges)
		return
	}
	a.knownLines++
	row.KnownLines++
	a.coveredLines += cov.fraction()
	row.CoveredLines += cov.fraction()
	a.record(path, span, cov, line)
}

// markUnknown books a line into the unknown bucket under a reason.
func (a *accumulator) markUnknown(reason string, line int, row *FileRow, unknownRanges map[string][]LineRange) {
	a.unknownLines++
	row.UnknownLines++
	unknownRanges[reason] = append(unknownRanges[reason], LineRange{Start: line, End: line})
}

// record attaches a changed line to its owning symbol's span entry.
func (a *accumulator) record(path string, span symSpan, cov SymbolCoverage, line int) {
	entry, ok := a.touched[span.id]
	if !ok {
		entry = &SymbolSpan{
			Path:     path,
			SymbolID: span.id,
			Symbol:   span.name,
			Covered:  cov.Covered,
			Total:    cov.Total,
			Fraction: cov.fraction(),
			Features: a.in.Features[span.id],
		}
		a.touched[span.id] = entry
		a.order = append(a.order, span.id)
	}
	entry.Ranges = append(entry.Ranges, LineRange{Start: line, End: line})
	entry.Lines++
}

// emitUnknown collapses one file's per-line unknown marks into ranges.
func (a *accumulator) emitUnknown(path string, byReason map[string][]LineRange) {
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		ranges := mergeRanges(byReason[reason])
		n := 0
		for _, r := range ranges {
			n += r.Len()
		}
		a.unknown = append(a.unknown, UnknownSpan{
			Path: path, Ranges: ranges, Lines: n, Reason: reason,
		})
	}
}

// result renders the accumulated state, splitting the touched symbols into
// the three verdicts a reader acts on differently.
func (a *accumulator) result() Result {
	out := Result{
		ChangedFiles:   a.changedFiles,
		ChangedLines:   a.changedLines,
		KnownLines:     a.knownLines,
		CoveredLines:   a.coveredLines,
		UncoveredLines: float64(a.knownLines) - a.coveredLines,
		UnknownLines:   a.unknownLines,
		// Empty rather than nil: these are the wire contract, and a consumer
		// iterating `null` is a crash, not an empty loop. classify() and
		// featureRows() initialise the rest.
		Unknown:         emptyIfNil(a.unknown),
		Files:           emptyIfNil(a.files),
		StaleIndexFiles: emptyIfNil(a.stale),
	}
	sort.Slice(out.StaleIndexFiles, func(i, j int) bool {
		return out.StaleIndexFiles[i].Path < out.StaleIndexFiles[j].Path
	})
	for _, s := range out.StaleIndexFiles {
		out.StaleIndexLines += s.Lines
	}
	if a.knownLines > 0 {
		out.Measurable = true
		pct := 100 * a.coveredLines / float64(a.knownLines)
		out.Percent = &pct
	}
	out.Uncovered, out.Partial, out.Covered = a.classify()
	out.Features = a.featureRows()
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out
}

// classify splits the touched symbols by what atlas can actually assert.
//
// A zero-covered symbol's changed lines are provably uncovered, so they are
// named to the line. A partially covered one is NOT listed among them: atlas
// knows the ratio, not which lines it came from, and printing lines it cannot
// vouch for is worse than printing none.
func (a *accumulator) classify() (uncovered, partial, covered []SymbolSpan) {
	uncovered, partial, covered = []SymbolSpan{}, []SymbolSpan{}, []SymbolSpan{}
	for _, id := range a.order {
		entry := *a.touched[id]
		entry.Ranges = mergeRanges(entry.Ranges)
		switch {
		case entry.Covered == 0:
			uncovered = append(uncovered, entry)
		case entry.Covered >= entry.Total:
			covered = append(covered, entry)
		default:
			partial = append(partial, entry)
		}
	}
	for _, set := range [][]SymbolSpan{uncovered, partial, covered} {
		sortSymbolSpans(set)
	}
	return uncovered, partial, covered
}

// sortSymbolSpans orders by changed lines descending, then by path and line,
// so the biggest gap leads and repeat runs are byte-identical.
func sortSymbolSpans(spans []SymbolSpan) {
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].Lines != spans[j].Lines {
			return spans[i].Lines > spans[j].Lines
		}
		if spans[i].Path != spans[j].Path {
			return spans[i].Path < spans[j].Path
		}
		return spans[i].SymbolID < spans[j].SymbolID
	})
}

// featureRows rolls the touched symbols up by feature.
//
// A symbol linked to several features counts under each: the diff really did
// move all of them, and splitting the lines between them would understate
// every one. The rows therefore do not sum to KnownLines, which is why they
// are a separate view rather than a partition of it.
func (a *accumulator) featureRows() []FeatureRow {
	type acc struct {
		lines   int
		covered float64
	}
	bucket := map[shared.FeatureID]*acc{}
	for _, id := range a.order {
		entry := a.touched[id]
		for _, fid := range a.in.Features[id] {
			b := bucket[fid]
			if b == nil {
				b = &acc{}
				bucket[fid] = b
			}
			b.lines += entry.Lines
			b.covered += float64(entry.Lines) * entry.Fraction
		}
	}
	rows := make([]FeatureRow, 0, len(bucket))
	for fid, b := range bucket {
		pct := 0.0
		if b.lines > 0 {
			pct = 100 * b.covered / float64(b.lines)
		}
		rows = append(rows, FeatureRow{
			FeatureID: fid, Lines: b.lines, CoveredLines: b.covered, Percent: pct,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Lines != rows[j].Lines {
			return rows[i].Lines > rows[j].Lines
		}
		return rows[i].FeatureID < rows[j].FeatureID
	})
	return rows
}

// emptyIfNil returns s, or an empty slice when s is nil, so a marshalled
// Result never carries a `null` where the documented contract is an array.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
