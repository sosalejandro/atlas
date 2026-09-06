-- Carryforward reads (issue #136). All three queries share one window:
-- grouped runs only, finished at or before the current frontier, and no older
-- than the lookback horizon. Ungrouped runs are excluded on purpose -- a run
-- group is the operator's declaration that a set of syncs is ONE build, and
-- without it "the previous build" has no meaning, so an ungrouped store keeps
-- its pre-#136 behaviour exactly.

-- name: ListCarrySources :many
-- The newest prior measurement of each symbol, with the span recorded when it
-- was taken (schema 0017) alongside the symbol's span today.
--
-- SQLite defines the bare columns of a query carrying a single max() to come
-- from the row that produced the maximum, so this resolves "which build last
-- measured this symbol" in one grouped read rather than one query per
-- candidate build. The per-run statement totals are NOT read here: a symbol
-- can own several result rows in one run, and picking one of them would
-- under-count. ListCarrySymbolTotals sums them.
SELECT
  r.symbol_id          AS symbol_id,
  MAX(g.finished_at)   AS measured_at,
  g.id                 AS run_id,
  g.run_group          AS run_group,
  r.feature_id         AS feature_id,
  sp.file_path         AS span_file_path,
  sp.line              AS span_line,
  sp.end_line          AS span_end_line,
  y.file_path          AS current_file_path,
  y.line               AS current_line,
  y.end_line           AS current_end_line
FROM coverage_results r
JOIN coverage_runs g ON g.id = r.run_id
JOIN symbols y ON y.id = r.symbol_id
LEFT JOIN coverage_symbol_spans sp
  ON sp.run_id = r.run_id AND sp.symbol_id = r.symbol_id
WHERE r.symbol_id IS NOT NULL
  AND g.run_group IS NOT NULL
  AND g.run_group <> sqlc.arg(current_group)
  AND g.finished_at <= sqlc.arg(frontier_at)
  AND g.finished_at >= sqlc.arg(horizon_at)
GROUP BY r.symbol_id;

-- name: ListCarrySymbolTotals :many
-- Per (BUILD, symbol) statement totals and status rollup over the same window.
-- Summing is what classifyCoverageResults does when it pools a live frontier,
-- so a carried reading is assembled the same way the observed one is -- and a
-- frontier pools the whole BUILD, not one run of it. Grouping per run instead
-- would key the rollup on something ListCarrySources does not identify a
-- source by: it names the newest run that measured the symbol, while a build
-- routinely measures one symbol from two runs (a unit job and an integration
-- job over the same package). Rolling up per run then reads whichever of them
-- finished last and silently discards the rest.
SELECT
  g.run_group                                       AS run_group,
  r.symbol_id                                       AS symbol_id,
  CAST(SUM(r.covered_stmts) AS INTEGER)             AS covered_stmts,
  CAST(SUM(r.total_stmts) AS INTEGER)               AS total_stmts,
  CAST(MAX(CASE WHEN r.status = 'pass' THEN 1 ELSE 0 END) AS INTEGER) AS any_pass,
  CAST(MAX(CASE WHEN r.status = 'skip' THEN 1 ELSE 0 END) AS INTEGER) AS any_skip
FROM coverage_results r
JOIN coverage_runs g ON g.id = r.run_id
WHERE r.symbol_id IS NOT NULL
  AND g.run_group IS NOT NULL
  AND g.run_group <> sqlc.arg(current_group)
  AND g.finished_at <= sqlc.arg(frontier_at)
  AND g.finished_at >= sqlc.arg(horizon_at)
GROUP BY g.run_group, r.symbol_id;

-- name: ListRecentRunGroups :many
-- The most recent grouped frontiers, newest first, so a carry can be measured
-- in BUILDS rather than in wall clock alone. The caller passes a limit of
-- (window + 1) frontiers: a source group that does not appear in the answer is
-- by construction further back than the window allows.
SELECT g.run_group AS run_group
FROM coverage_runs g
WHERE g.run_group IS NOT NULL
  AND g.finished_at <= sqlc.arg(frontier_at)
GROUP BY g.run_group
ORDER BY MAX(g.finished_at) DESC
LIMIT sqlc.arg(max_groups);
