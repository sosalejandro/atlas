package store

import "database/sql"

// Tiny pointer-conversion helpers used by the few adapters that still issue
// raw SQL (Features.List with an IDs filter, Symbols.List with dynamic
// WHERE, the recursive CTE in Edges.Walk). sqlc handles nullable scans via
// pointer types on its own; these helpers cover the remaining raw-SELECT
// rows where Scan still pulls into sql.Null* values.

func nullStringToPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func nullInt64ToIntPtr(ni sql.NullInt64) *int {
	if !ni.Valid {
		return nil
	}
	v := int(ni.Int64)
	return &v
}

// int64PtrToIntPtr narrows a *int64 (returned by sqlc with
// emit_pointers_for_null_types) to a *int for the store-facing domain
// types. Returns nil when the source is nil.
func int64PtrToIntPtr(p *int64) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// intPtrToInt64Ptr widens *int → *int64 for the inverse direction, used
// when handing values back to the sqlc layer (which expects *int64 for the
// `end_line` column).
func intPtrToInt64Ptr(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}
