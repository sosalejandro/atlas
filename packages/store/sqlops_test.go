package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

func openSQLOpsStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

func sampleOps() []SQLOperationRecord {
	return []SQLOperationRecord{
		{
			Ref: "go:repo.go:12:UserRepo.List", Source: "go",
			FilePath: "repo.go", Line: 12, SymbolName: "UserRepo.List",
			Kind: "select", Resolved: true, SQLText: "SELECT id FROM users",
			RowScan: "slice", ParamCount: 1, HasOrderBy: true,
			OffsetBound:  "none",
			Suppressions: []string{"sql.select-star"},
			Tables:       []SQLTableAccess{{Table: "users", Access: "read"}},
			Predicates: []SQLPredicate{
				{Clause: "where", Table: "users", Column: "tenant_id", Operator: "=", Bound: true},
			},
		},
		{
			Ref: "go:repo.go:40:UserRepo.Dynamic", Source: "go",
			FilePath: "repo.go", Line: 40, SymbolName: "UserRepo.Dynamic",
			Kind: "unknown", Resolved: false,
			UnresolvedReason: "query text is an expression atlas cannot statically resolve",
			RowScan:          "unknown", OffsetBound: "none",
		},
	}
}

func TestSQLOps_ReplaceAndList(t *testing.T) {
	s, ctx := openSQLOpsStore(t)

	if err := s.SQLOps().Replace(ctx, sampleOps()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := s.SQLOps().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(got))
	}

	first := got[0]
	if first.SymbolName != "UserRepo.List" || first.Kind != "select" || !first.Resolved {
		t.Errorf("first row = %+v", first)
	}
	if len(first.Tables) != 1 || first.Tables[0].Table != "users" || first.Tables[0].Access != "read" {
		t.Errorf("tables = %+v", first.Tables)
	}
	if len(first.Predicates) != 1 || first.Predicates[0].Column != "tenant_id" || !first.Predicates[0].Bound {
		t.Errorf("predicates = %+v", first.Predicates)
	}
	if strings.Join(first.Suppressions, ",") != "sql.select-star" {
		t.Errorf("suppressions = %v", first.Suppressions)
	}

	// The unresolved row must survive the round trip with its reason: a store
	// that drops what it could not analyse turns a partial analysis into a
	// false clean bill of health.
	if got[1].Resolved || got[1].UnresolvedReason == "" {
		t.Errorf("unresolved row = %+v", got[1])
	}
}

func TestSQLOps_ReplaceIsIdempotent(t *testing.T) {
	s, ctx := openSQLOpsStore(t)
	for i := 0; i < 3; i++ {
		if err := s.SQLOps().Replace(ctx, sampleOps()); err != nil {
			t.Fatalf("Replace #%d: %v", i, err)
		}
	}
	got, err := s.SQLOps().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("re-scanning accumulated rows: got %d, want 2", len(got))
	}
}

func TestSQLOps_LinksToSymbolsByQualifiedName(t *testing.T) {
	s, ctx := openSQLOpsStore(t)
	symID, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "UserRepo.List", Kind: shared.KindMethod,
		FilePath: "repo.go", Line: 10,
	})
	if err != nil {
		t.Fatalf("insert symbol: %v", err)
	}
	if err := s.SQLOps().Replace(ctx, sampleOps()); err != nil {
		t.Fatal(err)
	}

	rows, err := s.SQLOps().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].SymbolID == nil || *rows[0].SymbolID != symID {
		t.Fatalf("operation was not linked to symbol %d: %+v", symID, rows[0].SymbolID)
	}
	// An operation whose symbol atlas never indexed keeps its name and gets a
	// NULL link, rather than being dropped.
	if rows[1].SymbolID != nil {
		t.Errorf("unknown symbol name produced a link: %+v", rows[1])
	}

	access, err := s.SQLOps().TableAccessBySymbol(ctx, symID)
	if err != nil {
		t.Fatal(err)
	}
	if len(access) != 1 || access[0].Table != "users" || access[0].Access != "read" {
		t.Errorf("table access for symbol = %+v", access)
	}
}

func TestSQLOps_Resolution(t *testing.T) {
	s, ctx := openSQLOpsStore(t)
	if err := s.SQLOps().Replace(ctx, sampleOps()); err != nil {
		t.Fatal(err)
	}
	resolved, unresolved, err := s.SQLOps().Resolution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || unresolved != 1 {
		t.Errorf("resolution = %d/%d, want 1/1", resolved, unresolved)
	}
}

func TestSQLOps_ReplaceSchema(t *testing.T) {
	s, ctx := openSQLOpsStore(t)
	tables := []SQLTableRow{{Name: "users", FilePath: "db/0001.sql", Line: 3}}
	indexes := []SQLIndexRow{{
		Table: "users", Name: "users_tenant_idx", Columns: []string{"tenant_id", "created_at"},
		Origin: "create-index", FilePath: "db/0001.sql", Line: 9,
	}}
	if err := s.SQLOps().ReplaceSchema(ctx, tables, indexes); err != nil {
		t.Fatalf("ReplaceSchema: %v", err)
	}
	// Replacing again must not duplicate; the schema is a snapshot, not a log.
	if err := s.SQLOps().ReplaceSchema(ctx, tables, indexes); err != nil {
		t.Fatal(err)
	}

	gotTables, err := s.SQLOps().Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotTables) != 1 || gotTables[0].Name != "users" {
		t.Fatalf("tables = %+v", gotTables)
	}
	gotIdx, err := s.SQLOps().Indexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotIdx) != 1 || strings.Join(gotIdx[0].Columns, ",") != "tenant_id,created_at" {
		t.Fatalf("indexes = %+v", gotIdx)
	}
}

// Deleting a symbol must not take the operation with it: an operation records
// what a query does, which stays true whether or not the scanner still has a
// row for the function it lives in.
func TestSQLOps_SymbolDeletionNullsTheLink(t *testing.T) {
	s, ctx := openSQLOpsStore(t)
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "UserRepo.List", Kind: shared.KindMethod,
		FilePath: "repo.go", Line: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SQLOps().Replace(ctx, sampleOps()); err != nil {
		t.Fatal(err)
	}
	if err := s.Symbols().DeleteByFile(ctx, "repo.go"); err != nil {
		t.Fatalf("DeleteByFile: %v", err)
	}
	rows, err := s.SQLOps().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("operations lost to a symbol delete: got %d, want 2", len(rows))
	}
	if rows[0].SymbolID != nil {
		t.Errorf("stale symbol link survived: %+v", rows[0].SymbolID)
	}
}
