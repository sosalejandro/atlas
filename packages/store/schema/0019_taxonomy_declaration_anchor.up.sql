-- 0019_taxonomy_declaration_anchor.up.sql
--
-- Issue #112, the engineering half of the taxonomy RFC. Two renames land
-- here, and both are cheap TODAY and breaking changes the moment #101
-- hardens a public JSON API over this schema.
--
-- 1. symbols.bc_path -> symbols.domain
--
--    bc_path is short for "bounded context path", and it encodes one
--    architecture's vocabulary into a column every repository has to
--    carry. A Django monolith and a Rails app both have a perfectly good
--    answer to "which part of the product does this file belong to" and
--    neither of them calls it a bounded context. `domain` is the general
--    word; "bounded context" survives as the DDD-flavoured display name
--    in the docs and in `atlas onboard`, where it is a description of the
--    repository rather than a claim in the schema.
--
--    The derivation rule is unchanged: still the src/contexts/<name>/
--    prefix, still packages/store/paths.go. Renaming the column does not
--    give atlas a second way to compute a domain, and inventing one here
--    would be a redesign wearing a rename's clothes.
--
-- 2. symbols.node_class -- the declaration/anchor split, as a COLUMN
--
--    This is the defect the RFC is actually about. The `symbols` table
--    holds two different kinds of thing:
--
--      * DECLARATIONS -- a Go func, a TS export, a Python class. Parsed
--        out of a file someone in this repository wrote, at an honest
--        line number.
--
--      * ANCHORS -- vertices atlas minted so an edge would have somewhere
--        to land. `route:/login` for an HTTP route, `sql:GetUserByEmail`
--        for a named sqlc query, `endpoint:POST /v1/login` for an
--        extracted API operation, and the `external:py` stubs pyscan
--        emits for imports it cannot resolve inside the repo.
--
--    Nothing recorded which was which. Every query meaning "real code"
--    re-derived the split by string-matching a reserved prefix on the id
--    or the path -- `s.file_path NOT LIKE 'external:py%'` in the
--    dead-code query, `strings.HasPrefix(id, "route:")` in the graph's
--    root picker, `strings.HasPrefix(qn, "sql:")` in `atlas flow`. Four
--    copies of one rule, two of them in SQL and two in Go, none of them
--    able to notice the others drifting.
--
--    The prefix convention is not wrong, it is just untyped and
--    duplicated. So: the rule runs ONCE, here, as the backfill below;
--    packages/shared/taxonomy.go is the single Go copy that classifies
--    new rows at write time; and every "real code only" query reads the
--    column. TestNoQueryRederivesNodeClassFromAPrefix fails if a reserved
--    prefix reappears anywhere in the query layer.
--
-- WHY THIS IS AN ALTER AND NOT A TABLE REBUILD
--
-- Migration 0018 rebuilt `edges` to get a NOT NULL column with no
-- DEFAULT, and made the case for why a DEFAULT is how a forgotten column
-- becomes a claim nobody earned. The same holds here. A SQL DEFAULT of
-- 'declaration' would fire without ever looking at the row's id or path,
-- so the next scanner that forgets the column would launder its synthetic
-- vertices into the authored-code counts. (Deriving the value in Go is a
-- different thing entirely, and is why the write path is allowed to do it
-- -- see the note below.)
--
-- `symbols` cannot take that treatment. 0018 could drop `edges` because
-- nothing references it; five tables carry ON DELETE CASCADE foreign keys
-- INTO symbols (edges twice, feature_symbols, coverage_results,
-- coverage_symbol_spans, test_coverage), the connection runs with
-- foreign_keys=1 (see store.go Open), and DROP TABLE symbols would take
-- the entire index and every coverage result stored against it. A
-- rename-and-swap dance with the pragma toggled off would work and would
-- also be the single most dangerous thing in this schema's history, for a
-- CHECK constraint.
--
-- So the column is added nullable, with no default, and the guarantee is
-- carried by the two triggers at the bottom instead. They enforce the
-- same closed set a CHECK would, at the same moment, with the same error
-- class -- and unlike a CHECK they also catch the NULL that ADD COLUMN
-- forces us to allow.
--
-- The Go write path does NOT rely on them. It derives the class from the
-- id and the path (shared.ClassifyNode) whenever a caller left it unset,
-- which is the one place this differs from 0018's resolution_tier: a tier
-- is unrecoverable once the resolver has returned, whereas a node's class
-- is a pure function of two values the insert is already holding.
-- Deriving is therefore the same answer, from the same single copy of the
-- rule -- not a default standing in for a measurement. The triggers exist
-- for the writer that bypasses Go entirely.
--
-- BACKFILL is exact rather than conservative, and that is the difference
-- from 0018's deliberately pessimistic 'syntactic' fill. The tier of an
-- existing edge was genuinely unrecoverable from a stored row. The class
-- of an existing symbol is not: it is a pure function of the id and the
-- path, both of which are right here, and it is the same function every
-- consumer has been computing at read time all along. Backfilling it is
-- the rename, not a guess.

-- ---------- 1. bc_path -> domain ----------

ALTER TABLE symbols RENAME COLUMN bc_path TO domain;

-- SQLite rewrites the index's stored definition on RENAME COLUMN but not
-- its NAME, and an index called symbols_bc_idx over a column called
-- `domain` is exactly the sort of half-finished rename that teaches the
-- next reader that bc_path is still the real name.
--
-- IF EXISTS because the index is not load-bearing for correctness and some
-- stores do not have it: the migration suite builds partial schemas by hand
-- to test earlier migrations in isolation, and a rename must not be the
-- thing that refuses to run on them.
DROP INDEX IF EXISTS symbols_bc_idx;
CREATE INDEX symbols_domain_idx ON symbols(domain);

-- ---------- 2. node_class ----------

ALTER TABLE symbols ADD COLUMN node_class TEXT;

-- The one-time application of the prefix rule. Kept as a single UPDATE
-- with the whole closed set inline, rather than four statements, so the
-- set is readable as a set -- it must stay in lockstep with
-- shared.AnchorPrefixes, and a test asserts that it does.
UPDATE symbols SET node_class = 'anchor'
WHERE qualified_name LIKE 'route:%'
   OR qualified_name LIKE 'sql:%'
   OR qualified_name LIKE 'endpoint:%'
   OR qualified_name LIKE 'external:%'
   OR file_path LIKE 'route:%'
   OR file_path LIKE 'sql:%'
   OR file_path LIKE 'endpoint:%'
   OR file_path LIKE 'external:%';

UPDATE symbols SET node_class = 'declaration' WHERE node_class IS NULL;

-- Every "real code only" query now filters on this column, so it is read
-- on the hot path of the dead-code walk and of every declaration count.
CREATE INDEX symbols_node_class_idx ON symbols(node_class);

-- The CHECK constraint SQLite will not let us add to an existing table.
-- Two triggers rather than one: an INSERT-only guard would let an UPDATE
-- walk a row out of the closed set afterwards, and "the value was legal
-- when it was written" is not the invariant the read side depends on.
CREATE TRIGGER symbols_node_class_insert_guard
BEFORE INSERT ON symbols
WHEN NEW.node_class IS NULL OR NEW.node_class NOT IN ('declaration', 'anchor')
BEGIN
  SELECT RAISE(ABORT, 'symbols.node_class must be declaration or anchor');
END;

CREATE TRIGGER symbols_node_class_update_guard
BEFORE UPDATE OF node_class ON symbols
WHEN NEW.node_class IS NULL OR NEW.node_class NOT IN ('declaration', 'anchor')
BEGIN
  SELECT RAISE(ABORT, 'symbols.node_class must be declaration or anchor');
END;
