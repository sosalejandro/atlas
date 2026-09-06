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

	// StmtsAttributed / StmtsUnattributed split the report's statements into
	// the ones charged to a symbol and the ones atlas could not place; Gaps
	// enumerates the latter per file, biggest loss first (issue #85's FE
	// analogue — same reporting contract as ProfileIngestStats).
	StmtsAttributed   int
	StmtsUnattributed int
	Gaps              []FileGap
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
func IngestIstanbul(ctx context.Context, s *store.Store, meta RunMeta, r io.Reader) (IstanbulIngestStats, error) {
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

	rep := attributeIstanbulStatements(byFileStmts, byFile)
	counts := rep.counts
	stats.FilesMatched = rep.filesMatched
	stats.FilesUnmatched = rep.filesUnmatched
	stats.StmtsAttributed = rep.stmtsAttributed
	stats.StmtsUnattributed = rep.stmtsUnattributed
	stats.Gaps = rep.gaps()
	stats.SymbolsCovered = len(counts)
	if s.Logger() != nil && rep.stmtsUnattributed > 0 {
		s.Logger().Debug(ctx, "istanbul ingest: unattributed execution",
			"files_in_report", stats.FilesInReport,
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
		Framework: meta.Framework, StartedAt: now, FinishedAt: now,
		RunGroup: meta.runGroup(),
	}), results)
	if err != nil {
		return stats, fmt.Errorf("coverage: persist istanbul run: %w", err)
	}
	stats.RunID = runID
	if _, err := s.CoverageGaps().Insert(ctx, runID, rep.gapRows()); err != nil {
		return stats, fmt.Errorf("coverage: persist istanbul gaps: %w", err)
	}
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
func attributeIstanbulStatements(byFileStmts map[string][]istanbul.Statement, byFile map[string][]symSpan) attributionReport {
	rep := newAttributionReport()
	// Index atlas files by basename for suffix-match reconciliation.
	byBase := map[string][]string{}
	for f := range byFile {
		byBase[path.Base(f)] = append(byBase[path.Base(f)], f)
	}
	for rf, stmts := range byFileStmts {
		af := reconcilePath(rf, byBase)
		rep.files[rf] = af != ""
		if af == "" {
			rep.lostByFile[rf] = len(stmts)
			rep.reasonByFile[rf] = ReasonNoIndexedSymbol
			continue
		}
		syms := byFile[af]
		for _, st := range stmts {
			sid, ok := owningSymbol(syms, st.StartLine)
			if !ok {
				rep.lostByFile[rf]++
				rep.reasonByFile[rf] = ReasonOutsideSymbolSpans
				continue
			}
			c := rep.counts[sid]
			c.total++
			if st.Executed() {
				c.covered++
			}
			rep.counts[sid] = c
			rep.attributedByFile[rf]++
		}
	}
	rep.finalize()
	return rep
}
