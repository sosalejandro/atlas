-- name: DeleteAllSQLOperations :exec
-- The operation inventory is replaced wholesale on every scan. An incremental
-- upsert would leave rows behind for queries that were deleted, and a stale
-- advisory pointing at a line that no longer exists is worse than no advisory.
DELETE FROM sql_operations;

-- name: DeleteAllSQLTables :exec
DELETE FROM sql_tables;

-- name: DeleteAllSQLIndexes :exec
DELETE FROM sql_indexes;

-- name: InsertSQLOperation :one
-- Column order matches the table declaration so sqlc reuses the model type
-- instead of inventing a per-query row struct.
INSERT INTO sql_operations (
  ref, source, name, file_path, line, symbol_id, symbol_name, kind,
  resolved, unresolved_reason, sql_text, row_scan, param_count,
  interpolation, caller_data, has_limit, has_offset, has_order_by,
  keyset, select_star, offset_bound, suppressions
) VALUES (
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING id;

-- name: InsertSQLOperationTable :exec
INSERT OR REPLACE INTO sql_operation_tables (operation_id, table_name, access)
VALUES (?, ?, ?);

-- name: InsertSQLOperationPredicate :exec
INSERT OR REPLACE INTO sql_operation_predicates
  (operation_id, clause, table_name, column_name, operator, bound)
VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertSQLTable :exec
INSERT OR REPLACE INTO sql_tables (name, file_path, line) VALUES (?, ?, ?);

-- name: InsertSQLIndex :exec
INSERT OR REPLACE INTO sql_indexes
  (table_name, name, columns, is_unique, predicate, origin, file_path, line)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListSQLOperations :many
-- Ordered by position so successive runs diff cleanly.
SELECT id, ref, source, name, file_path, line, symbol_id, symbol_name, kind,
       resolved, unresolved_reason, sql_text, row_scan, param_count,
       interpolation, caller_data, has_limit, has_offset, has_order_by,
       keyset, select_star, offset_bound, suppressions, created_at
FROM sql_operations
ORDER BY file_path, line, name;

-- name: ListSQLOperationTables :many
-- Every child row for every operation in one pass; the caller groups by
-- operation_id. Fetching per operation would be an N+1 against the very
-- pattern this feature exists to find.
SELECT operation_id, table_name, access
FROM sql_operation_tables
ORDER BY operation_id, table_name, access;

-- name: ListSQLOperationPredicates :many
SELECT operation_id, clause, table_name, column_name, operator, bound
FROM sql_operation_predicates
ORDER BY operation_id, clause, table_name, column_name, operator;

-- name: ListSQLTables :many
SELECT name, file_path, line FROM sql_tables ORDER BY name;

-- name: ListSQLIndexes :many
SELECT table_name, name, columns, is_unique, predicate, origin, file_path, line
FROM sql_indexes
ORDER BY table_name, name;

-- name: CountSQLOperationsByResolution :many
-- The honesty counter, straight out of the store: how much of the data layer
-- atlas could actually read.
SELECT resolved, COUNT(*) AS total
FROM sql_operations
GROUP BY resolved;

-- name: ListTableAccessBySymbol :many
-- The data footprint of one symbol: the tables its queries read and write.
SELECT t.table_name, t.access
FROM sql_operation_tables t
JOIN sql_operations o ON o.id = t.operation_id
WHERE o.symbol_id = ?
ORDER BY t.table_name, t.access;
