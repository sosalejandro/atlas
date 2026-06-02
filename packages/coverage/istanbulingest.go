package coverage

import (
	"context"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/sosalejandro/atlas/packages/coverage/istanbul"
	"github.com/sosalejandro/atlas/packages/store"
)

// IstanbulIngestStats summarises an istanbul (coverage-final.json) ingest.
// It mirrors ProfileIngestStats so the CLI can report the FE track the same
// way it reports the Go track.
type IstanbulIngestStats struct {
	RunID          int64
	StmtsParsed    int
	FilesInReport  int
	FilesMatched   int
	SymbolsCovered int
	// FilesUnmatched counts report files that reconciled to NO atlas symbol
	// file (the FE analogue of issue #85). A high value relative to
	// FilesInReport means the suffix path-reconciliation is dropping front-end
	// execution on the floor (e.g. files under src/ that atlas never scanned,
	// or a path-shape mismatch between the reporter's paths and atlas's
	// repo-relative paths).
	FilesUnmatched int
}

// IngestIstanbul parses an Istanbul / V8 `coverage-final.json` report and
// records, per FE symbol whose source range contains a statement, a coverage
// result with the line-weighted statement fraction (covered/total). This is
// the front-end analogue of IngestGoProfile: where the Go path attributes
// coverprofile blocks to Go symbols, this attributes istanbul statements to
// the React/TS symbols atlas scanned (file_path like apps/web-*/src/...).
//
// Reporter file paths are absolute (or app-relative); atlas symbol file paths
// are repo-relative (e.g. apps/web-patient/src/...). They are reconciled by
// path-suffix match, exactly like the Go ingester's reconcilePath.
//
// `framework` is the run tag persisted on the coverage_runs row. Istanbul
// coverage IS vitest/jest coverage, so callers pass store.FrameworkVitest —
// this keeps the run within the existing framework CHECK constraint (the same
// trick the go-cover path uses to persist under store.FrameworkGoTest). The
// audit's Tier-B line-weighted signal reads covered_stmts/total_stmts
// regardless of the framework tag.
func IngestIstanbul(ctx context.Context, s *store.Store, framework store.Framework, r io.Reader) (IstanbulIngestStats, error) {
	var stats IstanbulIngestStats
	byFileStmts, err := istanbul.Parse(r)
	if err != nil {
		return stats, fmt.Errorf("coverage: parse istanbul report: %w", err)
	}
	stats.FilesInReport = len(byFileStmts)
	for _, ss := range byFileStmts {
		stats.StmtsParsed += len(ss)
	}

	syms, err := s.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		return stats, fmt.Errorf("coverage: list symbols: %w", err)
	}
	byFile := indexSymbolsByFile(syms)

	counts, matched, unmatched := attributeIstanbulStatements(byFileStmts, byFile)
	stats.FilesMatched = matched
	stats.FilesUnmatched = unmatched
	stats.SymbolsCovered = len(counts)
	if s.Logger() != nil && unmatched > 0 {
		s.Logger().Debug(ctx, "istanbul ingest: unmatched report files",
			"files_in_report", stats.FilesInReport,
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
		return stats, fmt.Errorf("coverage: persist istanbul run: %w", err)
	}
	stats.RunID = runID
	return stats, nil
}

// attributeIstanbulStatements maps each report file's statements onto the
// owning FE symbol (the symbol whose [start,end] span contains the statement's
// START line) and accumulates per-symbol statement counts: total = number of
// statements attributed, covered = number of those that executed. Returns the
// per-symbol counts, the number of report files that reconciled to an atlas
// symbol file, and the number that did not (FilesUnmatched — the FE analogue
// of issue #85).
//
// Attribution is by the statement's START line falling inside the symbol span
// (tightest span wins via owningSymbol) so each statement is charged to exactly
// one symbol and is never double-counted across adjacent symbols — the basis
// for a per-feature fraction that tracks the istanbul "% Stmts" column.
func attributeIstanbulStatements(byFileStmts map[string][]istanbul.Statement, byFile map[string][]symSpan) (counts map[int64]symbolCounts, filesMatched, filesUnmatched int) {
	counts = map[int64]symbolCounts{}
	// Index atlas files by basename for suffix-match reconciliation.
	byBase := map[string][]string{}
	for f := range byFile {
		byBase[path.Base(f)] = append(byBase[path.Base(f)], f)
	}
	for rf, stmts := range byFileStmts {
		af := reconcilePath(rf, byBase)
		if af == "" {
			filesUnmatched++
			continue
		}
		filesMatched++
		syms := byFile[af]
		for _, st := range stmts {
			sid, ok := owningSymbol(syms, st.StartLine)
			if !ok {
				continue
			}
			c := counts[sid]
			c.total++
			if st.Executed() {
				c.covered++
			}
			counts[sid] = c
		}
	}
	return counts, filesMatched, filesUnmatched
}
