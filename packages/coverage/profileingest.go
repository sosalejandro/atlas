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
	spansByFile := gocover.ExecutedSpansByFile(blocks)
	stats.FilesInProfile = len(spansByFile)

	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return stats, fmt.Errorf("coverage: list symbols: %w", err)
	}
	byFile := indexSymbolsByFile(syms)

	executed, matched := attributeExecution(spansByFile, byFile)
	stats.FilesMatched = matched
	stats.SymbolsExecuted = len(executed)

	results := make([]store.CoverageResult, 0, len(executed))
	for _, sid := range sortedKeys(executed) {
		v := sid
		results = append(results, store.CoverageResult{SymbolID: &v, Status: store.StatusPass})
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

// attributeExecution maps each profile file's executed line spans onto the
// symbols whose range overlaps them, returning the set of executed symbol ids
// and the count of profile files that matched an atlas file.
func attributeExecution(spansByFile map[string][][2]int, byFile map[string][]symSpan) (executed map[int64]bool, filesMatched int) {
	executed = map[int64]bool{}
	// Index atlas files by basename for suffix-match reconciliation.
	byBase := map[string][]string{}
	for f := range byFile {
		byBase[path.Base(f)] = append(byBase[path.Base(f)], f)
	}
	for pf, spans := range spansByFile {
		af := reconcilePath(pf, byBase)
		if af == "" {
			continue
		}
		filesMatched++
		syms := byFile[af]
		for _, sp := range spans {
			for _, sr := range syms {
				if sp[0] <= sr.end && sr.start <= sp[1] { // overlap
					executed[sr.id] = true
				}
			}
		}
	}
	return executed, filesMatched
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

func sortedKeys(m map[int64]bool) []int64 {
	ks := make([]int64, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}
