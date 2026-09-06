-- Control-flow queries (migration 0015, issue #127).
--
-- Every statement here is per-symbol by design: a flow result that does not
-- carry the symbol it belongs to cannot be joined back to the graph, and an
-- analysis nobody can join is an analysis nobody uses.
--
-- Writes are all INSERT OR REPLACE and the port clears a symbol's rows before
-- rewriting them, so re-running the builder over an unchanged file is a no-op
-- and re-running it over a changed one cannot leave two generations of blocks
-- interleaved.

-- name: DeleteCFGBlocks :exec
DELETE FROM cfg_blocks WHERE symbol_id = ?;

-- name: InsertCFGBlock :exec
INSERT OR REPLACE INTO cfg_blocks (symbol_id, block_index, kind, start_line, end_line)
VALUES (?, ?, ?, ?, ?);

-- name: ListCFGBlocks :many
SELECT symbol_id, block_index, kind, start_line, end_line
FROM cfg_blocks
WHERE symbol_id = ?
ORDER BY block_index;

-- name: DeleteCFGEdges :exec
DELETE FROM cfg_edges WHERE symbol_id = ?;

-- name: InsertCFGEdge :exec
INSERT OR REPLACE INTO cfg_edges (symbol_id, edge_index, from_block, to_block, kind, condition)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListCFGEdges :many
SELECT symbol_id, edge_index, from_block, to_block, kind, condition
FROM cfg_edges
WHERE symbol_id = ?
ORDER BY edge_index;

-- name: UpsertCFGSymbol :exec
INSERT OR REPLACE INTO cfg_symbols (
  symbol_id, complexity, decisions, branch_arms,
  conditions, conditions_independent, defers, unreachable_blocks, built_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: GetCFGSymbol :one
SELECT symbol_id, complexity, decisions, branch_arms,
       conditions, conditions_independent, defers, unreachable_blocks, built_at
FROM cfg_symbols
WHERE symbol_id = ?;

-- name: ListCFGSymbolsByComplexity :many
-- Most complex first: the order the hotspot ranking wants. Ties break by
-- symbol_id so a paged read is stable across calls.
SELECT symbol_id, complexity, decisions, branch_arms,
       conditions, conditions_independent, defers, unreachable_blocks, built_at
FROM cfg_symbols
ORDER BY complexity DESC, symbol_id ASC
LIMIT ?;

-- name: UpsertCFGDecisionCoverage :exec
-- outcomes_decidable is the denominator; outcomes_total is NOT. The gap
-- between them is the part of the branching no statement-coverage profile can
-- judge, and it is stored so a reader can report it instead of dividing it
-- away.
INSERT OR REPLACE INTO cfg_decision_coverage (
  symbol_id, outcomes_total, outcomes_decidable, outcomes_taken, source, measured_at
) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: GetCFGDecisionCoverage :one
SELECT symbol_id, outcomes_total, outcomes_decidable, outcomes_taken, source, measured_at
FROM cfg_decision_coverage
WHERE symbol_id = ?;

-- name: DeleteCFGDecisionCoverage :exec
DELETE FROM cfg_decision_coverage WHERE symbol_id = ?;

-- name: DeleteCFGFindings :exec
DELETE FROM cfg_findings WHERE symbol_id = ?;

-- name: InsertCFGFinding :exec
INSERT OR REPLACE INTO cfg_findings (
  symbol_id, finding_index, kind, confidence, line, related_line, detail
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListCFGFindingsBySymbol :many
SELECT symbol_id, finding_index, kind, confidence, line, related_line, detail
FROM cfg_findings
WHERE symbol_id = ?
ORDER BY finding_index;

-- name: ListCFGFindingsByKind :many
-- Highest confidence first so a consumer reading only the head of the list
-- sees the findings most likely to be real.
SELECT symbol_id, finding_index, kind, confidence, line, related_line, detail
FROM cfg_findings
WHERE kind = ?
ORDER BY CASE confidence WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
         symbol_id, finding_index;

-- name: ListAllCFGFindings :many
SELECT symbol_id, finding_index, kind, confidence, line, related_line, detail
FROM cfg_findings
ORDER BY CASE confidence WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
         kind, symbol_id, finding_index;
