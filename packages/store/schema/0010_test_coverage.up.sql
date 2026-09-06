-- 0010_test_coverage.up.sql
--
-- Per-test execution evidence: which production symbols each individual test
-- actually ran. This is the dynamic feature-location technique (issue #104) --
-- "software reconnaissance" in the literature -- and it replaces a call-graph
-- BFS with a set operation:
--
--     surface(F) = U { symbols executed by tests annotated for F }
--                - { symbols executed by more than P% of all tests }
--
-- A call-graph walk from the annotated test only reaches what the scanner
-- could resolve statically, which is why a feature whose annotation sits on an
-- e2e test scores zero even after its domain code is thoroughly unit tested
-- (issue #84). Execution evidence has no such blind spot: it is correct
-- through interface dispatch, DI containers, reflection and string-routed
-- handlers alike.
--
-- Schema-shape notes:
--
--   * The grain is (run, test symbol, executed symbol). A row exists only
--     when the test executed at least one statement of that symbol, so the
--     table is sparse -- on a 1,122-test suite over 46k symbols the real row
--     count is in the low hundreds of thousands, not 52 million.
--
--   * WITHOUT ROWID: the primary key IS the row, and every read is either a
--     point lookup or a prefix scan on it.
--
--   * Cascades follow the run and the symbols: pruning a stale symbol
--     (0009-era behaviour, issue #98) or deleting an old run takes its
--     evidence with it, so the table cannot outlive what it describes.
--
--   * covered_stmts is the count for THAT test alone. Summing across tests
--     would double-count shared code, which is exactly why the surface is a
--     set union rather than an arithmetic sum.
--
--   * Migrations are one-way (.up.sql only) -- the store is a re-derivable
--     cache; recover by deleting atlas.db and re-running atlas init.
CREATE TABLE test_coverage (
  run_id         INTEGER NOT NULL REFERENCES coverage_runs(id) ON DELETE CASCADE,
  test_symbol_id INTEGER NOT NULL REFERENCES symbols(id)       ON DELETE CASCADE,
  symbol_id      INTEGER NOT NULL REFERENCES symbols(id)       ON DELETE CASCADE,
  covered_stmts  INTEGER NOT NULL DEFAULT 0,
  total_stmts    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, test_symbol_id, symbol_id)
) WITHOUT ROWID;

-- Reverse lookup: "which tests executed this symbol" powers affected-test
-- selection (issue #90) and the ubiquity cutoff, which needs a per-symbol
-- test fan-in count.
CREATE INDEX test_coverage_symbol_idx ON test_coverage(run_id, symbol_id);
