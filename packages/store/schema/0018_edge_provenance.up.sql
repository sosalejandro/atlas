-- 0018_edge_provenance.up.sql
--
-- Records WHICH MECHANISM resolved every edge, and persists the
-- ambiguity flag the graph layer has been computing and throwing away
-- since v0.4. Issue #146; prerequisite for #87.
--
-- Why this is a schema change and not a report:
--
--   Atlas derives the same relationship -- "this function calls that
--   one" -- by mechanisms of wildly different reliability. A callee
--   found in the caller's own package scope and a callee guessed from a
--   case-insensitive substring match on a variable name both land as
--   one row, identical in (from_symbol_id, to_symbol_id, kind,
--   file_path, line). The mechanism is in none of those columns, so no
--   test we have can see it change. #87 replaces the Go call resolver
--   wholesale (go/packages + callgraph); without this column it could
--   halve or double the guessed edges with every count-based and
--   tuple-based check still green.
--
--   Provenance is also only recordable WHILE the resolver knows it.
--   Once #87 has landed, nobody can look at a stored edge and say
--   whether the old code guessed it.
--
-- Two new columns:
--
--   * resolution_tier TEXT NOT NULL, no DEFAULT. The A/B/C/D vocabulary
--     from #105's tiering addendum: typed / name_resolved / syntactic /
--     imported. See packages/graph/tier.go for what each one claims.
--
--     The absence of a DEFAULT is the load-bearing part. A default is
--     how every edge ends up claiming to be typed: any writer that
--     forgets the column inherits a value it never earned, and nothing
--     downstream can tell that apart from a measurement. With no
--     default, an INSERT that omits the column is SQLITE_CONSTRAINT_
--     NOTNULL at the moment it is attempted, and the CHECK below
--     rejects the empty string a Go zero value would otherwise supply.
--     packages/store/edges.go raises the same refusal earlier and with
--     a readable message; the constraints are what make the guarantee
--     hold for a writer nobody has written yet.
--
--     There is deliberately no companion confidence score. The prior
--     art this borrows from (trace-mcp, credited on #105) seeds
--     1.0 / 1.0 / 0.95 / 0.7 / 0.4 from its tiers via an insert
--     trigger. Those weights are calibrated against that project's
--     corpus, nobody here has measured ours, and this project does not
--     ship numbers it has not measured. Derivation also stays in Go
--     rather than in a trigger, because the Go path is the one the test
--     suite exercises.
--
--   * ambiguous INTEGER NOT NULL DEFAULT 0. graph.Edge.Ambiguous, set
--     when the resolver had more than one candidate and picked. It has
--     existed since v0.4 and has never reached the store: edge_meta got
--     a column in migration 0008 and this did not, so atlas measured
--     resolution confidence on every edge and discarded it at the
--     storage boundary.
--
--     This one DOES carry a default, and the asymmetry is the point.
--     "The resolver did not flag this edge as ambiguous" is a real,
--     honest state that false describes correctly. "The producer did
--     not say which mechanism it used" has no honest stand-in -- every
--     candidate value is a claim about work that may not have happened.
--
-- BACKFILL: every pre-existing row is set to 'syntactic', the WEAKEST
-- tier, and ambiguous stays 0.
--
--   This is not what the current scanners achieve on average. Today's
--   Go resolver reaches 'name_resolved' whenever resolveInScope binds
--   the callee in the caller's package scope, and drops to 'syntactic'
--   only on the fuzzy paths. The tier is per-row information that lives
--   in the resolver's control flow, and it is simply not recoverable
--   from a stored row: nothing in (from, to, kind, file, line, meta)
--   distinguishes a scope hit from a substring match.
--
--   So the choice is between backfilling optimistically and
--   backfilling honestly. Optimistic backfill would launder every
--   existing guess into a claim, and the first tier histogram anyone
--   diffs would be measuring the backfill rather than the scanner. The
--   weakest tier states the floor -- "at least syntactic, and this row
--   predates provenance" -- which is true of every row here, and it
--   biases the first #87 histogram AGAINST showing an improvement,
--   which is the safe direction for a change detector to be wrong in.
--
--   Anyone who wants real per-row tiers on an existing store gets them
--   the same way they get any other index fact: re-run `atlas scan`.
--   The store is a re-derivable cache (see store.go runMigrations).
--
-- SQLite cannot ADD a NOT NULL column without a default, and cannot
-- ALTER a CHECK in place, so this follows the table-rebuild pattern
-- migrations 0002 and 0007 established on this same table. Nothing FKs
-- INTO edges, so the drop is safe; surrogate ids are preserved so any
-- id a caller is holding across the migration stays valid.
--
-- Column order keeps the pre-migration layout (0001 through 0008) and
-- appends the two new columns, so the SELECT lists in
-- packages/store/queries/edges.sql stay in declaration order.
--
-- Neither new column joins edges_dedupe_idx, for the same reason
-- edge_meta did not in 0008: two mechanisms that produce the same
-- logical edge at the same call site are one relationship, and storing
-- both would double-count it in every walk and every histogram. The
-- consequence is first-tier-wins on a duplicate within a single scan.
-- That is stable rather than arbitrary -- the scanners are
-- deterministic per source state (see the determinism suite in
-- packages/codeindex/go) -- and a re-scan of a changed file deletes the
-- file's edges before re-emitting them, so a tier can never be pinned
-- by a row the current source no longer justifies.

CREATE TABLE edges_new (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  from_symbol_id  INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  to_symbol_id    INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  kind            TEXT    NOT NULL,
  file_path       TEXT    NOT NULL,
  line            INTEGER NOT NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  edge_meta       TEXT,
  resolution_tier TEXT    NOT NULL,
  ambiguous       INTEGER NOT NULL DEFAULT 0,
  CHECK (kind IN (
    'call', 'implement', 'embed', 'construct',
    'inheritance', 'decorator', 'import'
  )),
  CHECK (resolution_tier IN (
    'typed', 'name_resolved', 'syntactic', 'imported'
  )),
  CHECK (ambiguous IN (0, 1))
);

INSERT INTO edges_new
  (id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at, edge_meta,
   resolution_tier, ambiguous)
SELECT
  id, from_symbol_id, to_symbol_id, kind, file_path, line, created_at, edge_meta,
  'syntactic', 0
FROM edges;

DROP TABLE edges;
ALTER TABLE edges_new RENAME TO edges;

CREATE INDEX edges_from_idx ON edges(from_symbol_id);
CREATE INDEX edges_to_idx   ON edges(to_symbol_id);
CREATE UNIQUE INDEX edges_dedupe_idx
  ON edges(from_symbol_id, to_symbol_id, kind, file_path, line);

-- The histogram query in packages/store/edges.go groups by
-- (language, resolution_tier) over the whole table. Indexing the tier
-- keeps that a covering-ish scan rather than a full table read once a
-- monorepo's edge count runs to six figures.
CREATE INDEX edges_tier_idx ON edges(resolution_tier);
