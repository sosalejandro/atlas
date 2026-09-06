-- 0015_control_flow.up.sql
--
-- Control flow inside a symbol (issue #127).
--
-- Until now a symbol was an opaque box with a line range: whether its body is
-- a straight line, a five-way switch, or a loop containing a database call was
-- invisible. That blind spot is why atlas can report 90% statement coverage on
-- a function whose every error path is unexercised -- statement coverage says
-- the line ran, never that the branch was taken both ways.
--
-- Five tables, each keyed by symbol_id so every result joins the graph:
--
--   * cfg_blocks / cfg_edges are the graph itself, one row per node and edge.
--     Block 0 is always the synthetic entry and block 1 the synthetic exit, so
--     a renderer can anchor a flowchart without loading the whole function
--     first. The edge `kind` set includes 'seq' -- not in the issue's sketch,
--     but unavoidable: most edges are unconditional continuations, and calling
--     those 'true' would make every straight line look like a taken branch to
--     anything counting branch arms.
--
--   * cfg_edges is keyed by (symbol_id, edge_index) rather than by
--     (from, to, kind). The index is the edge's position in the built graph,
--     which keeps the write order reproducible AND makes it impossible for two
--     structurally identical edges to silently collapse into one row -- an
--     edge count that quietly drops is a cyclomatic complexity that quietly
--     drops with it.
--
--   * cfg_symbols carries the structural metrics: cyclomatic complexity (which
--     the hotspot ranking in #93 wants and which did not exist before), the
--     decision and condition counts, and the unreachable-block count. These
--     are facts about the SOURCE and are valid with no test evidence at all.
--
--   * cfg_decision_coverage carries the execution-derived half, and its column
--     names are the whole point of the table. `outcomes_total` is every branch
--     outcome the source has; `outcomes_decidable` is how many of those a
--     statement-coverage profile can judge AT ALL; `outcomes_taken` is how many
--     of the decidable ones were taken. Decision coverage is
--     taken/decidable -- NEVER taken/total, which would charge a symbol for
--     outcomes no instrumentation could have observed. A row with
--     outcomes_decidable = 0 means "no judgement was possible", which is not
--     the same fact as 0% and must not be rendered as one.
--
--     There is deliberately no MC/DC column. MC/DC is not derivable from Go's
--     statement coverage: the operands of `a && b` share one counter, so no
--     profile can show a condition independently affecting the outcome. What
--     IS recorded (in cfg_symbols) is how many conditions exist and how many
--     could in principle be varied independently -- a property of the source,
--     not a coverage verdict. Anyone who needs a real MC/DC verdict (DO-178C
--     DAL A) needs condition-level instrumentation atlas does not have, and
--     a column called mcdc_percent here would be read as the thing it is not.
--
--   * cfg_findings is the diagnostics list: flow.query-in-loop,
--     flow.unreachable, flow.untested-branch. Every row carries a confidence,
--     because a query in a loop is a smell and not a proof -- one behind a
--     cache is fine -- and a finding that does not say how sure it is gets
--     muted wholesale the first time it is wrong.
--
-- All five cascade from symbols: a re-scan that drops a symbol drops its flow
-- with it, and the store stays a re-derivable cache (see store.go
-- runMigrations). Migrations are one-way; recover by deleting atlas.db.

CREATE TABLE cfg_blocks (
  symbol_id   INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  block_index INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  start_line  INTEGER NOT NULL DEFAULT 0,
  end_line    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (symbol_id, block_index),
  CHECK (kind IN ('entry', 'body', 'branch', 'loop', 'exit'))
) WITHOUT ROWID;

CREATE TABLE cfg_edges (
  symbol_id  INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  edge_index INTEGER NOT NULL,
  from_block INTEGER NOT NULL,
  to_block   INTEGER NOT NULL,
  kind       TEXT    NOT NULL,
  condition  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (symbol_id, edge_index),
  CHECK (kind IN ('seq', 'true', 'false', 'loop-back', 'fallthrough'))
) WITHOUT ROWID;

-- Reads are "this symbol's edges, in build order" and "every edge into this
-- block"; the primary key serves the first, this index the second.
CREATE INDEX cfg_edges_target_idx ON cfg_edges(symbol_id, to_block);

CREATE TABLE cfg_symbols (
  symbol_id              INTEGER PRIMARY KEY REFERENCES symbols(id) ON DELETE CASCADE,
  complexity             INTEGER NOT NULL DEFAULT 1,
  decisions              INTEGER NOT NULL DEFAULT 0,
  branch_arms            INTEGER NOT NULL DEFAULT 0,
  conditions             INTEGER NOT NULL DEFAULT 0,
  conditions_independent INTEGER NOT NULL DEFAULT 0,
  defers                 INTEGER NOT NULL DEFAULT 0,
  unreachable_blocks     INTEGER NOT NULL DEFAULT 0,
  built_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- The hotspot ranking reads "the most complex symbols first" (#93).
CREATE INDEX cfg_symbols_complexity_idx ON cfg_symbols(complexity DESC);

CREATE TABLE cfg_decision_coverage (
  symbol_id          INTEGER PRIMARY KEY REFERENCES symbols(id) ON DELETE CASCADE,
  outcomes_total     INTEGER NOT NULL DEFAULT 0,
  outcomes_decidable INTEGER NOT NULL DEFAULT 0,
  outcomes_taken     INTEGER NOT NULL DEFAULT 0,
  source             TEXT    NOT NULL DEFAULT '',
  measured_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cfg_findings (
  symbol_id     INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  finding_index INTEGER NOT NULL,
  kind          TEXT    NOT NULL,
  confidence    TEXT    NOT NULL,
  line          INTEGER NOT NULL DEFAULT 0,
  related_line  INTEGER NOT NULL DEFAULT 0,
  detail        TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (symbol_id, finding_index),
  CHECK (kind IN ('flow.query-in-loop', 'flow.unreachable', 'flow.untested-branch')),
  CHECK (confidence IN ('high', 'medium', 'low'))
) WITHOUT ROWID;

CREATE INDEX cfg_findings_kind_idx ON cfg_findings(kind, confidence);
