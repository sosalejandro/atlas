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
	// (issue #85 — best-effort visibility into attribution misses). A high
	// value relative to FilesInProfile means the suffix path-reconciliation
	// is dropping production execution on the floor.
	FilesUnmatched int
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
	stats.BlocksParsed = len(blocks)
	blocksByFile := gocover.BlocksByFile(blocks)
	stats.FilesInProfile = len(blocksByFile)

	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return stats, fmt.Errorf("coverage: list symbols: %w", err)
	}
	byFile := indexSymbolsByFile(syms)

	counts, matched, unmatched := attributeStatements(blocksByFile, byFile)
	stats.FilesMatched = matched
	stats.FilesUnmatched = unmatched
	stats.SymbolsExecuted = len(counts)
	if s.Logger() != nil && unmatched > 0 {
		s.Logger().Debug(ctx, "gocover ingest: unmatched profile files",
			"files_in_profile", stats.FilesInProfile,
			"files_matched", matched,
			"files_unmatched", unmatched)
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
	runID, err := s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: framework, StartedAt: now, FinishedAt: now,
	}, results)
	if err != nil {
		return stats, fmt.Errorf("coverage: persist profile run: %w", err)
	}
	stats.RunID = runID
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
			end := start
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

// attributeStatements maps each profile file's blocks onto the owning symbol
// (the symbol whose [start,end] span contains the block's start line) and
// accumulates per-symbol statement counts: total = Σ NumStmts, covered =
// Σ NumStmts of executed (Count>0) blocks. Returns the per-symbol counts, the
// number of profile files that reconciled to an atlas file, and the number
// that did not (issue #85 visibility).
//
// Attribution is by the block's START line falling inside the symbol span
// (rather than range-overlap) so a block is charged to exactly one symbol and
// statements are never double-counted across adjacent symbols — the basis for
// a per-feature fraction that tracks `go tool cover -func`.
func attributeStatements(blocksByFile map[string][]gocover.Block, byFile map[string][]symSpan) (counts map[int64]symbolCounts, filesMatched, filesUnmatched int) {
	counts = map[int64]symbolCounts{}
	// Index atlas files by basename for suffix-match reconciliation.
	byBase := map[string][]string{}
	for f := range byFile {
		byBase[path.Base(f)] = append(byBase[path.Base(f)], f)
	}
	for pf, blocks := range blocksByFile {
		af := reconcilePath(pf, byBase)
		if af == "" {
			filesUnmatched++
			continue
		}
		filesMatched++
		syms := byFile[af]
		for _, b := range blocks {
			sid, ok := owningSymbol(syms, b.StartLine)
			if !ok {
				continue
			}
			c := counts[sid]
			c.total += b.NumStmts
			if b.Executed() {
				c.covered += b.NumStmts
			}
			counts[sid] = c
		}
	}
	return counts, filesMatched, filesUnmatched
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
