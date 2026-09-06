package coverage

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
	"github.com/sosalejandro/atlas/packages/store"
)

// ProfileIngestStats summarises a coverprofile ingest.
type ProfileIngestStats struct {
	RunID           int64
	BlocksParsed    int
	FilesInProfile  int
	FilesMatched    int
	SymbolsExecuted int
	// FilesUnmatched counts profile files that reconciled to NO atlas file
	// (issue #85 — visibility into attribution misses). A high value
	// relative to FilesInProfile means production execution is being
	// dropped: either the file was never indexed (generated code, symbols
	// lost to a name collision) or the suffix path-reconciliation missed.
	FilesUnmatched int

	// StmtsAttributed / StmtsUnattributed split the profile's statements
	// into the ones charged to a symbol and the ones atlas could not place.
	// StmtsUnattributed is the honest size of the coverage blind spot: it
	// is what separates "code that ran" from "code atlas knows about".
	StmtsAttributed   int
	StmtsUnattributed int

	// Gaps enumerates, per profile file, the statements that could not be
	// attributed and why — sorted by statements lost, descending. This is
	// what `cov sync --verbose` prints so the gap is inspectable rather
	// than silently absorbed.
	Gaps []FileGap
}

// Gap reasons for FileGap.Reason.
const (
	// ReasonNoIndexedSymbol: the profile file reconciled to no atlas file
	// at all — atlas has zero symbols for it (never scanned, skipped as
	// generated, or every symbol in it lost a short-name collision).
	ReasonNoIndexedSymbol = "no-indexed-symbol"
	// ReasonOutsideSymbolSpans: the file IS indexed, but these statements
	// fall outside every indexed symbol's [line, end_line] span — code in
	// declarations atlas did not index (e.g. package-private helpers when
	// SkipUnexportedFuncs is on, or stale symbol positions).
	ReasonOutsideSymbolSpans = "outside-symbol-spans"
)

// FileGap is one profile file's unattributed execution.
type FileGap struct {
	// Path is the file as it appears in the coverage profile
	// (import-path-qualified, e.g. github.com/org/repo/pkg/svc.go).
	Path string `json:"path"`
	// Stmts is the number of statements atlas could not attribute.
	Stmts int `json:"stmts"`
	// Reason is one of ReasonNoIndexedSymbol / ReasonOutsideSymbolSpans.
	Reason string `json:"reason"`
}

// symbolCounts accumulates the statement-level coverage for one owned symbol
// (Tier B). covered is Σ NumStmts of blocks that ran; total is Σ NumStmts of
// every block attributed to the symbol's span.
type symbolCounts struct {
	covered int
	total   int
}

// symSpan is a symbol's effective source range for span attribution.
type symSpan struct {
	id    int64
	start int
	end   int
}

// IngestGoProfile parses a Go coverage profile (`go test -coverprofile`) and
// records, per symbol whose source range contains an executed statement, a
// passing coverage result. Unlike the gotest framework parser — which keys
// results to TEST functions by name — this attributes REAL production-code
// execution to the symbols that ran, the basis for true per-feature coverage
// once features are linked to their impl surface (call-graph derivation).
//
// Profile file paths are import-path-qualified (module prefix); atlas symbol
// file paths are repo-relative. They are reconciled by suffix match.
func IngestGoProfile(ctx context.Context, s *store.Store, framework store.Framework, r io.Reader) (ProfileIngestStats, error) {
	var stats ProfileIngestStats
	blocks, err := gocover.Parse(r)
	if err != nil {
		return stats, fmt.Errorf("coverage: parse profile: %w", err)
	}
	// Merge duplicate blocks (max count per span) BEFORE statement accounting.
	// A `-coverpkg=./...` profile repeats every span once per tested package;
	// summing raw NumStmts would inflate totals ~Nx and deflate the ratio. This
	// makes per-symbol covered/total track `go tool cover`'s "(statements)".
	blocks = gocover.MergeBlocks(blocks)
	stats.BlocksParsed = len(blocks)
	blocksByFile := gocover.BlocksByFile(blocks)
	stats.FilesInProfile = len(blocksByFile)

	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return stats, fmt.Errorf("coverage: list symbols: %w", err)
	}
	byFile := indexSymbolsByFile(syms)

	rep := attributeStatements(blocksByFile, byFile)
	counts := rep.counts
	stats.FilesMatched = rep.filesMatched
	stats.FilesUnmatched = rep.filesUnmatched
	stats.StmtsAttributed = rep.stmtsAttributed
	stats.StmtsUnattributed = rep.stmtsUnattributed
	stats.Gaps = rep.gaps()
	stats.SymbolsExecuted = len(counts)
	if s.Logger() != nil && rep.stmtsUnattributed > 0 {
		s.Logger().Debug(ctx, "gocover ingest: unattributed execution",
			"files_in_profile", stats.FilesInProfile,
			"files_matched", rep.filesMatched,
			"files_unmatched", rep.filesUnmatched,
			"stmts_attributed", rep.stmtsAttributed,
			"stmts_unattributed", rep.stmtsUnattributed)
	}

	results := make([]store.CoverageResult, 0, len(counts))
	for _, sid := range sortedCountKeys(counts) {
		v := sid
		c := counts[sid]
		status := store.StatusFail
		if c.covered > 0 {
			status = store.StatusPass
		}
		results = append(results, store.CoverageResult{
			SymbolID:     &v,
			Status:       status,
			CoveredStmts: c.covered,
			TotalStmts:   c.total,
		})
	}
	now := time.Now().UTC()
	runID, err := s.Coverage().InsertRunWithResults(ctx, rep.withAttribution(store.CoverageRun{
		Framework: framework, StartedAt: now, FinishedAt: now,
	}), results)
	if err != nil {
		return stats, fmt.Errorf("coverage: persist profile run: %w", err)
	}
	stats.RunID = runID
	if _, err := s.CoverageGaps().Insert(ctx, runID, rep.gapRows()); err != nil {
		return stats, fmt.Errorf("coverage: persist profile gaps: %w", err)
	}
	return stats, nil
}

// indexSymbolsByFile groups symbols by repo-relative file path, computing each
// symbol's effective [start,end] range: end_line when present, else the line
// before the next symbol in the same file, else the start line (point range).
func indexSymbolsByFile(syms []store.SymbolRow) map[string][]symSpan {
	type lite struct {
		id, start int64
		end       *int
	}
	grouped := map[string][]lite{}
	for _, s := range syms {
		if s.FilePath == "" || s.Line <= 0 {
			continue
		}
		grouped[s.FilePath] = append(grouped[s.FilePath], lite{id: s.ID, start: int64(s.Line), end: s.EndLine})
	}
	out := make(map[string][]symSpan, len(grouped))
	for f, ls := range grouped {
		sort.Slice(ls, func(i, j int) bool { return ls[i].start < ls[j].start })
		spans := make([]symSpan, 0, len(ls))
		for i, l := range ls {
			start := int(l.start)
			var end int
			if l.end != nil && *l.end >= start {
				end = *l.end
			} else if i+1 < len(ls) {
				end = int(ls[i+1].start) - 1
				if end < start {
					end = start
				}
			} else {
				end = 1 << 30 // last symbol in file: extends to EOF
			}
			spans = append(spans, symSpan{id: l.id, start: start, end: end})
		}
		out[f] = spans
	}
	return out
}

// attributionReport is the per-run accounting attributeStatements produces:
// the per-symbol statement counts plus everything that could NOT be placed.
type attributionReport struct {
	counts map[int64]symbolCounts
	// attributedByFile is the profile path -> statements charged to a symbol.
	// Per file rather than one running total for the same reason lostByFile
	// is: the per-test ingest folds many profiles, and only a per-file key
	// lets merge() tell "the same file again" from "another file".
	attributedByFile map[string]int
	// files records every report file and whether it reconciled to an atlas
	// file. It is the file SET rather than a pair of counters because the
	// per-test ingest folds many profiles that all describe the same
	// codebase; counting per profile would multiply one file by the number
	// of tests that compiled it in.
	files map[string]bool
	// lostByFile is the profile path → unattributed statements, and
	// reasonByFile why. Both are keyed by the PROFILE path (not the atlas
	// path) so the report names files the way the profile does.
	lostByFile   map[string]int
	reasonByFile map[string]string

	// Derived by finalize() from the maps above; never written directly.
	filesMatched      int
	filesUnmatched    int
	stmtsAttributed   int
	stmtsUnattributed int
}

// newAttributionReport allocates an empty report ready to accumulate into.
func newAttributionReport() attributionReport {
	return attributionReport{
		counts:           map[int64]symbolCounts{},
		files:            map[string]bool{},
		attributedByFile: map[string]int{},
		lostByFile:       map[string]int{},
		reasonByFile:     map[string]string{},
	}
}

// finalize recomputes the scalar counters from the per-file maps. Keeping one
// source of truth is what lets merge() fold profiles by union without having
// to hold a second set of running totals consistent with them.
func (r *attributionReport) finalize() {
	r.filesMatched, r.filesUnmatched = 0, 0
	r.stmtsAttributed, r.stmtsUnattributed = 0, 0
	for _, matched := range r.files {
		if matched {
			r.filesMatched++
		} else {
			r.filesUnmatched++
		}
	}
	for _, n := range r.attributedByFile {
		r.stmtsAttributed += n
	}
	for _, n := range r.lostByFile {
		r.stmtsUnattributed += n
	}
}

// merge folds another profile's report into r with UNION semantics, and is
// how the per-test ingest aggregates one profile per test.
//
// Union, not sum, because every per-test profile describes the SAME codebase:
// a `-coverpkg=./...` profile names every file whether or not that test
// touched it, so a file atlas cannot index appears in all 1,122 profiles.
// Summing would report a blind spot 1,122x larger than the code that exists.
// Whether a statement CAN be attributed is a property of the symbol index,
// identical in every profile, so per file the largest value seen is the true
// one — and summing those per-file maxima gives the run's total, which is
// correct whether the profiles all describe the whole tree or each names only
// its own package.
func (r *attributionReport) merge(o attributionReport) {
	for f, matched := range o.files {
		r.files[f] = matched
	}
	for f, n := range o.attributedByFile {
		if n > r.attributedByFile[f] {
			r.attributedByFile[f] = n
		}
	}
	for f, n := range o.lostByFile {
		if n > r.lostByFile[f] {
			r.lostByFile[f] = n
			r.reasonByFile[f] = o.reasonByFile[f]
		}
	}
	r.finalize()
}

// gapRows renders the report's gaps as store rows, ready to persist.
func (r attributionReport) gapRows() []store.CoverageGap {
	gaps := r.gaps()
	out := make([]store.CoverageGap, 0, len(gaps))
	for _, g := range gaps {
		out = append(out, store.CoverageGap{Path: g.Path, Stmts: g.Stmts, Reason: g.Reason})
	}
	return out
}

// withAttribution stamps the report's accounting onto a run row so it is
// persisted with the run rather than printed once and dropped (issue #100).
func (r attributionReport) withAttribution(run store.CoverageRun) store.CoverageRun {
	run.FilesInReport = len(r.files)
	run.FilesMatched = r.filesMatched
	run.FilesUnmatched = r.filesUnmatched
	run.StmtsAttributed = r.stmtsAttributed
	run.StmtsUnattributed = r.stmtsUnattributed
	return run
}

// gaps renders the report's unattributed execution as a stable, sorted list:
// biggest loss first, ties broken by path so output is deterministic.
func (r attributionReport) gaps() []FileGap {
	out := make([]FileGap, 0, len(r.lostByFile))
	for p, n := range r.lostByFile {
		if n <= 0 {
			continue
		}
		out = append(out, FileGap{Path: p, Stmts: n, Reason: r.reasonByFile[p]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Stmts != out[j].Stmts {
			return out[i].Stmts > out[j].Stmts
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// attributeStatements maps each profile file's blocks onto the owning symbol
// (the symbol whose [start,end] span contains the block's start line) and
// accumulates per-symbol statement counts: total = Σ NumStmts, covered =
// Σ NumStmts of executed (Count>0) blocks.
//
// Everything it cannot place is recorded rather than dropped (issue #85):
// a profile file that reconciles to no atlas file is counted whole, and a
// block inside a reconciled file that falls outside every symbol span is
// counted against that file. The caller reports both, so the difference
// between "code that ran" and "code atlas knows about" is visible.
//
// Attribution is by the block's START line falling inside the symbol span
// (rather than range-overlap) so a block is charged to exactly one symbol and
// statements are never double-counted across adjacent symbols — the basis for
// a per-feature fraction that tracks `go tool cover -func`.
func attributeStatements(blocksByFile map[string][]gocover.Block, byFile map[string][]symSpan) attributionReport {
	rep := newAttributionReport()
	// Index atlas files by basename for suffix-match reconciliation.
	byBase := map[string][]string{}
	for f := range byFile {
		byBase[path.Base(f)] = append(byBase[path.Base(f)], f)
	}
	for pf, blocks := range blocksByFile {
		af := reconcilePath(pf, byBase)
		rep.files[pf] = af != ""
		if af == "" {
			lost := 0
			for _, b := range blocks {
				lost += b.NumStmts
			}
			rep.lostByFile[pf] = lost
			rep.reasonByFile[pf] = ReasonNoIndexedSymbol
			continue
		}
		syms := byFile[af]
		for _, b := range blocks {
			sid, ok := owningSymbol(syms, b.StartLine)
			if !ok {
				rep.lostByFile[pf] += b.NumStmts
				rep.reasonByFile[pf] = ReasonOutsideSymbolSpans
				continue
			}
			c := rep.counts[sid]
			c.total += b.NumStmts
			if b.Executed() {
				c.covered += b.NumStmts
			}
			rep.counts[sid] = c
			rep.attributedByFile[pf] += b.NumStmts
		}
	}
	rep.finalize()
	return rep
}

// owningSymbol returns the id of the symbol whose [start,end] span contains
// `line`. When several symbols' spans contain the line (e.g. an overly-broad
// EOF fallback overlapping a later symbol), the tightest (smallest) span
// wins — that is the most specific owner.
func owningSymbol(syms []symSpan, line int) (int64, bool) {
	best := int64(0)
	bestSpan := 1<<31 - 1
	found := false
	for _, sr := range syms {
		if sr.start <= line && line <= sr.end {
			span := sr.end - sr.start
			if !found || span < bestSpan {
				best = sr.id
				bestSpan = span
				found = true
			}
		}
	}
	return best, found
}

// reconcilePath finds the atlas (repo-relative) file path that the import-
// path-qualified profile file ends with. Matches on basename first, then
// confirms the atlas path is a path-suffix of the profile path.
func reconcilePath(profileFile string, byBase map[string][]string) string {
	cands := byBase[path.Base(profileFile)]
	best := ""
	for _, af := range cands {
		if profileFile == af || hasPathSuffix(profileFile, af) {
			if len(af) > len(best) { // prefer the most specific match
				best = af
			}
		}
	}
	return best
}

// hasPathSuffix reports whether full ends with the path segment suffix,
// aligned on a '/' boundary (so "a/bc.go" does not match suffix "c.go").
func hasPathSuffix(full, suffix string) bool {
	if len(suffix) >= len(full) {
		return false
	}
	if full[len(full)-len(suffix):] != suffix {
		return false
	}
	return full[len(full)-len(suffix)-1] == '/'
}

func sortedCountKeys(m map[int64]symbolCounts) []int64 {
	ks := make([]int64, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}
