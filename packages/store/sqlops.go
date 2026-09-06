package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/sosalejandro/atlas/packages/store/sqlc"
)

// SQLTableAccess is one table an operation touches, and how.
type SQLTableAccess struct {
	Table  string `json:"table"`
	Access string `json:"access"`
}

// SQLPredicate is one column comparison from a WHERE or JOIN clause.
type SQLPredicate struct {
	Clause   string `json:"clause"`
	Table    string `json:"table,omitempty"`
	Column   string `json:"column"`
	Operator string `json:"operator"`
	Bound    bool   `json:"bound"`
}

// SQLOperationRecord is one row of `sql_operations` plus its children
// (docs/schema-v1.md 5.15).
//
// Resolved is the field every consumer must branch on first. When it is false
// the shape fields are all zero -- and a zero shape is indistinguishable from
// "a SELECT with no LIMIT", which is why an unresolved row must never be fed
// to a check.
type SQLOperationRecord struct {
	ID       int64  `json:"id"`
	Ref      string `json:"ref"`
	Source   string `json:"source"`
	Name     string `json:"name,omitempty"`
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	// SymbolID is nil when the operation's enclosing symbol is not indexed.
	// SymbolName survives either way, so a row stays readable with no link.
	SymbolID   *int64 `json:"symbol_id,omitempty"`
	SymbolName string `json:"symbol_name,omitempty"`

	Kind             string `json:"kind"`
	Resolved         bool   `json:"resolved"`
	UnresolvedReason string `json:"unresolved_reason,omitempty"`
	SQLText          string `json:"sql,omitempty"`
	RowScan          string `json:"row_scan"`
	ParamCount       int    `json:"param_count"`
	Interpolation    string `json:"interpolation,omitempty"`
	CallerData       bool   `json:"caller_data"`
	HasLimit         bool   `json:"has_limit"`
	HasOffset        bool   `json:"has_offset"`
	HasOrderBy       bool   `json:"has_order_by"`
	Keyset           bool   `json:"keyset"`
	SelectStar       bool   `json:"select_star"`
	OffsetBound      string `json:"offset_bound"`

	Suppressions []string         `json:"suppressions,omitempty"`
	Tables       []SQLTableAccess `json:"tables,omitempty"`
	Predicates   []SQLPredicate   `json:"predicates,omitempty"`
}

// SQLTableRow is one table Atlas read a CREATE TABLE for.
type SQLTableRow struct {
	Name     string `json:"name"`
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
}

// SQLIndexRow is one index Atlas read out of the DDL, including the ones
// implied by PRIMARY KEY and UNIQUE constraints.
type SQLIndexRow struct {
	Table     string   `json:"table"`
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	Unique    bool     `json:"unique"`
	Predicate string   `json:"predicate,omitempty"`
	Origin    string   `json:"origin"`
	FilePath  string   `json:"file_path"`
	Line      int      `json:"line"`
}

// SQLOps is the port for the SQL operation inventory (schema 0014).
//
// Both writers replace their whole table set rather than upserting. The
// inventory describes the working tree as it is now: an incremental merge
// would leave rows behind for queries that were deleted, and an advisory
// pointing at a line that no longer exists costs more trust than it saves
// work.
type SQLOps interface {
	// Replace swaps in a new operation inventory. Symbol links are resolved
	// by qualified name; an unindexed name yields a NULL link rather than a
	// dropped row.
	Replace(ctx context.Context, ops []SQLOperationRecord) error

	// ReplaceSchema swaps in the tables and indexes read from the DDL.
	ReplaceSchema(ctx context.Context, tables []SQLTableRow, indexes []SQLIndexRow) error

	// List returns every operation with its tables and predicates, ordered by
	// position.
	List(ctx context.Context) ([]SQLOperationRecord, error)

	// Tables returns the tables whose DDL Atlas read.
	Tables(ctx context.Context) ([]SQLTableRow, error)

	// Indexes returns the indexes Atlas read.
	Indexes(ctx context.Context) ([]SQLIndexRow, error)

	// Resolution returns how many operations Atlas could and could not
	// analyse -- the honesty counter every report leads with.
	Resolution(ctx context.Context) (resolved, unresolved int, err error)

	// TableAccessBySymbol returns the tables one symbol's queries read and
	// write: the data footprint of a capability.
	TableAccessBySymbol(ctx context.Context, symbolID int64) ([]SQLTableAccess, error)
}

var _ SQLOps = (*sqlOpsStore)(nil)

// SQLOps returns the Store's SQLOps port.
func (s *Store) SQLOps() SQLOps { return &sqlOpsStore{db: s, q: s.queries()} }

type sqlOpsStore struct {
	db *Store
	q  *sqlc.Queries
}

func (o *sqlOpsStore) Replace(ctx context.Context, ops []SQLOperationRecord) error {
	tx, err := o.db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sql operations replace: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := o.q.WithTx(tx)
	if err := qtx.DeleteAllSQLOperations(ctx); err != nil {
		return fmt.Errorf("sql operations replace: clear: %w", err)
	}
	for _, op := range ops {
		if err := insertOperation(ctx, qtx, op); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql operations replace: commit: %w", err)
	}
	return nil
}

func insertOperation(ctx context.Context, qtx *sqlc.Queries, op SQLOperationRecord) error {
	symbolID, err := resolveSymbolLink(ctx, qtx, op.SymbolName)
	if err != nil {
		return err
	}
	id, err := qtx.InsertSQLOperation(ctx, sqlc.InsertSQLOperationParams{
		Ref:              op.Ref,
		Source:           op.Source,
		Name:             op.Name,
		FilePath:         op.FilePath,
		Line:             int64(op.Line),
		SymbolID:         symbolID,
		SymbolName:       op.SymbolName,
		Kind:             defaultTo(op.Kind, "unknown"),
		Resolved:         boolToInt(op.Resolved),
		UnresolvedReason: op.UnresolvedReason,
		SqlText:          op.SQLText,
		RowScan:          defaultTo(op.RowScan, "unknown"),
		ParamCount:       int64(op.ParamCount),
		Interpolation:    op.Interpolation,
		CallerData:       boolToInt(op.CallerData),
		HasLimit:         boolToInt(op.HasLimit),
		HasOffset:        boolToInt(op.HasOffset),
		HasOrderBy:       boolToInt(op.HasOrderBy),
		Keyset:           boolToInt(op.Keyset),
		SelectStar:       boolToInt(op.SelectStar),
		OffsetBound:      defaultTo(op.OffsetBound, "none"),
		Suppressions:     strings.Join(op.Suppressions, ","),
	})
	if err != nil {
		return fmt.Errorf("insert sql operation %s: %w", op.Ref, err)
	}
	return insertOperationChildren(ctx, qtx, id, op)
}

func insertOperationChildren(ctx context.Context, qtx *sqlc.Queries, id int64, op SQLOperationRecord) error {
	for _, t := range op.Tables {
		if err := qtx.InsertSQLOperationTable(ctx, sqlc.InsertSQLOperationTableParams{
			OperationID: id, TableName: t.Table, Access: t.Access,
		}); err != nil {
			return fmt.Errorf("insert sql operation table %s/%s: %w", op.Ref, t.Table, err)
		}
	}
	for _, p := range op.Predicates {
		if err := qtx.InsertSQLOperationPredicate(ctx, sqlc.InsertSQLOperationPredicateParams{
			OperationID: id, Clause: p.Clause, TableName: p.Table,
			ColumnName: p.Column, Operator: p.Operator, Bound: boolToInt(p.Bound),
		}); err != nil {
			return fmt.Errorf("insert sql operation predicate %s/%s: %w", op.Ref, p.Column, err)
		}
	}
	return nil
}

// resolveSymbolLink maps a qualified name onto a symbols row id. A name with
// no row is not an error: the scanner skips generated files and the operation
// is still worth recording, so the link is simply NULL.
func resolveSymbolLink(ctx context.Context, qtx *sqlc.Queries, name string) (*int64, error) {
	if name == "" {
		return nil, nil
	}
	id, err := qtx.GetSymbolIDByQualifiedName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve symbol %s: %w", name, err)
	}
	return &id, nil
}

func (o *sqlOpsStore) ReplaceSchema(ctx context.Context, tables []SQLTableRow, indexes []SQLIndexRow) error {
	tx, err := o.db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sql schema replace: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	qtx := o.q.WithTx(tx)
	if err := qtx.DeleteAllSQLTables(ctx); err != nil {
		return fmt.Errorf("sql schema replace: clear tables: %w", err)
	}
	if err := qtx.DeleteAllSQLIndexes(ctx); err != nil {
		return fmt.Errorf("sql schema replace: clear indexes: %w", err)
	}
	for _, t := range tables {
		if err := qtx.InsertSQLTable(ctx, sqlc.InsertSQLTableParams{
			Name: t.Name, FilePath: t.FilePath, Line: int64(t.Line),
		}); err != nil {
			return fmt.Errorf("insert sql table %s: %w", t.Name, err)
		}
	}
	for _, ix := range indexes {
		if err := qtx.InsertSQLIndex(ctx, sqlc.InsertSQLIndexParams{
			TableName: ix.Table, Name: ix.Name,
			Columns:  strings.Join(ix.Columns, ","),
			IsUnique: boolToInt(ix.Unique), Predicate: ix.Predicate,
			Origin: ix.Origin, FilePath: ix.FilePath, Line: int64(ix.Line),
		}); err != nil {
			return fmt.Errorf("insert sql index %s.%s: %w", ix.Table, ix.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sql schema replace: commit: %w", err)
	}
	return nil
}

// List loads the operations and both child tables in three queries and stitches
// them in memory. Loading children per operation would be an N+1 -- the exact
// pattern this feature exists to make visible.
func (o *sqlOpsStore) List(ctx context.Context) ([]SQLOperationRecord, error) {
	rows, err := o.q.ListSQLOperations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sql operations: %w", err)
	}
	tableRows, err := o.q.ListSQLOperationTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sql operation tables: %w", err)
	}
	predRows, err := o.q.ListSQLOperationPredicates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sql operation predicates: %w", err)
	}

	tablesByOp := map[int64][]SQLTableAccess{}
	for _, r := range tableRows {
		tablesByOp[r.OperationID] = append(tablesByOp[r.OperationID],
			SQLTableAccess{Table: r.TableName, Access: r.Access})
	}
	predsByOp := map[int64][]SQLPredicate{}
	for _, r := range predRows {
		predsByOp[r.OperationID] = append(predsByOp[r.OperationID], SQLPredicate{
			Clause: r.Clause, Table: r.TableName, Column: r.ColumnName,
			Operator: r.Operator, Bound: r.Bound != 0,
		})
	}

	out := make([]SQLOperationRecord, 0, len(rows))
	for _, r := range rows {
		rec := toOperationRecord(r)
		rec.Tables = tablesByOp[r.ID]
		rec.Predicates = predsByOp[r.ID]
		out = append(out, rec)
	}
	return out, nil
}

func toOperationRecord(r sqlc.SqlOperation) SQLOperationRecord {
	return SQLOperationRecord{
		ID: r.ID, Ref: r.Ref, Source: r.Source, Name: r.Name,
		FilePath: r.FilePath, Line: int(r.Line),
		SymbolID: r.SymbolID, SymbolName: r.SymbolName,
		Kind: r.Kind, Resolved: r.Resolved != 0,
		UnresolvedReason: r.UnresolvedReason, SQLText: r.SqlText,
		RowScan: r.RowScan, ParamCount: int(r.ParamCount),
		Interpolation: r.Interpolation, CallerData: r.CallerData != 0,
		HasLimit: r.HasLimit != 0, HasOffset: r.HasOffset != 0,
		HasOrderBy: r.HasOrderBy != 0, Keyset: r.Keyset != 0,
		SelectStar:   r.SelectStar != 0,
		OffsetBound:  r.OffsetBound,
		Suppressions: splitCSV(r.Suppressions),
	}
}

func (o *sqlOpsStore) Tables(ctx context.Context) ([]SQLTableRow, error) {
	rows, err := o.q.ListSQLTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sql tables: %w", err)
	}
	out := make([]SQLTableRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SQLTableRow{Name: r.Name, FilePath: r.FilePath, Line: int(r.Line)})
	}
	return out, nil
}

func (o *sqlOpsStore) Indexes(ctx context.Context) ([]SQLIndexRow, error) {
	rows, err := o.q.ListSQLIndexes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sql indexes: %w", err)
	}
	out := make([]SQLIndexRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SQLIndexRow{
			Table: r.TableName, Name: r.Name, Columns: splitCSV(r.Columns),
			Unique: r.IsUnique != 0, Predicate: r.Predicate, Origin: r.Origin,
			FilePath: r.FilePath, Line: int(r.Line),
		})
	}
	return out, nil
}

func (o *sqlOpsStore) Resolution(ctx context.Context) (int, int, error) {
	rows, err := o.q.CountSQLOperationsByResolution(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("count sql operations by resolution: %w", err)
	}
	var resolved, unresolved int
	for _, r := range rows {
		if r.Resolved != 0 {
			resolved += int(r.Total)
		} else {
			unresolved += int(r.Total)
		}
	}
	return resolved, unresolved, nil
}

func (o *sqlOpsStore) TableAccessBySymbol(ctx context.Context, symbolID int64) ([]SQLTableAccess, error) {
	rows, err := o.q.ListTableAccessBySymbol(ctx, &symbolID)
	if err != nil {
		return nil, fmt.Errorf("table access for symbol %d: %w", symbolID, err)
	}
	out := make([]SQLTableAccess, 0, len(rows))
	for _, r := range rows {
		out = append(out, SQLTableAccess{Table: r.TableName, Access: r.Access})
	}
	return out, nil
}

// --- small helpers -------------------------------------------------------

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func defaultTo(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// splitCSV is the inverse of strings.Join for the two comma-packed columns
// (index columns, suppression codes). Both hold short identifier lists that
// cannot contain a comma, which is what makes packing them safe; anything
// richer would want a child table.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
