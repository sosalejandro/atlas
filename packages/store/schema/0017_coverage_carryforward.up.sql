-- 0017_coverage_carryforward: the span snapshot that lets a carried coverage
-- result be CHECKED rather than trusted (issue #136).
--
-- The problem. A run group unions the runs of one build. A symbol that no run
-- in the group measured is simply absent from the frontier's results, and the
-- line-weighted score sums only symbols that HAVE results -- so when the Go
-- job crashes and only the front-end sync lands, every Go statement leaves the
-- denominator and coverage goes UP because testing went DOWN. The fix is to
-- carry the previous build's result for a symbol the current build did not
-- measure. That is only defensible if the carry can be invalidated when the
-- symbol is no longer the symbol that was measured.
--
-- Why a table and not a column. The check needs the symbol's span AS OF the
-- run that measured it. `symbols` holds only the CURRENT span (a rescan
-- updates the row in place, keeping the surrogate id -- see
-- ingest.upsertSymbolTx), so by the time a carry is considered the measured
-- span is gone. This table pins it at measurement time, keyed by
-- (run, symbol) because a symbol may produce several result rows in one run
-- (one per coverprofile block) and they all describe the same declaration.
--
-- Why a trigger and not Go code. The snapshot has to hold for EVERY path that
-- writes a coverage result -- the sqlc-generated insert, the raw-SQL insert
-- that carries the 0009 statement columns, and any future ingester. Making it
-- an invariant of the table removes the possibility of an ingest path that
-- forgets, which is the failure mode that would silently turn every carry back
-- into an article of faith. The cost is one indexed lookup and one
-- INSERT OR IGNORE per result row, inside the ingest's existing transaction.
--
-- Not backfilled. Filling this table from the current `symbols` rows would
-- assert that today's span was the measured span, which is exactly the claim
-- the table exists to verify. Runs ingested before this migration therefore
-- carry no snapshot; packages/store/carryforward.go treats a missing snapshot
-- as "unverifiable" and downgrades the carry to denominator-only rather than
-- refusing it, because refusing is the direction that reproduces the bug.
--
-- The store is a re-derivable cache (see store.go runMigrations) so a failure
-- here is recovered by deleting atlas.db and re-running.

CREATE TABLE coverage_symbol_spans (
  run_id    INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  symbol_id INTEGER NOT NULL REFERENCES symbols(id)       ON DELETE CASCADE,
  file_path TEXT    NOT NULL,
  line      INTEGER NOT NULL,
  end_line  INTEGER,
  PRIMARY KEY (run_id, symbol_id)
);

-- INSERT OR IGNORE, not INSERT: the second and subsequent result rows for the
-- same (run, symbol) describe the same declaration, and the first one already
-- recorded the span. A plain INSERT would abort the whole ingest transaction
-- on the second block of any multi-block function.
--
-- The SELECT ... FROM symbols yields no row for a result whose symbol_id does
-- not resolve, and no snapshot is written; the carry layer reads that as
-- unverifiable rather than as verified.
CREATE TRIGGER coverage_results_span_snapshot
AFTER INSERT ON coverage_results
WHEN NEW.symbol_id IS NOT NULL
BEGIN
  INSERT OR IGNORE INTO coverage_symbol_spans (run_id, symbol_id, file_path, line, end_line)
  SELECT NEW.run_id, NEW.symbol_id, s.file_path, s.line, s.end_line
  FROM symbols s
  WHERE s.id = NEW.symbol_id;
END;
