package sqlops

import (
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// mkOp builds a resolved operation from SQL text, so a case can state the
// query and the row shape and nothing else.
func mkOp(t *testing.T, sql string, scan RowScan) Operation {
	t.Helper()
	st, ok := AnalyzeStatement(sql)
	if !ok {
		t.Fatalf("fixture SQL did not analyse: %s", sql)
	}
	return Operation{
		Source:     SourceGo,
		Position:   shared.FilePosition{Path: "repo.go", Line: 10},
		SymbolName: "Repo.Method",
		Resolved:   true,
		SQL:        sql,
		Statement:  st,
		RowScan:    scan,
	}
}

// perOpOnly silences the two schema-wide checks so a per-operation case
// asserts on the codes it is actually about. They have their own test.
func perOpOnly() AdviseOptions {
	return AdviseOptions{Suppress: []string{CodeOrphanTable, CodeUnusedIndex}}
}

func codesOf(res AdviseResult) []string {
	out := make([]string, 0, len(res.Advisories))
	for _, a := range res.Advisories {
		out = append(out, a.Code)
	}
	sort.Strings(out)
	return out
}

func skippedCodes(res AdviseResult) []string {
	out := make([]string, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		out = append(out, s.Code+"/"+s.Scope)
	}
	sort.Strings(out)
	return out
}

// testSchema is a two-table schema with exactly one non-key index.
func testSchema(t *testing.T) Schema {
	t.Helper()
	sc := Schema{byTable: map[string]bool{"users": true, "events": true}}
	sc.Tables = []SchemaTable{{Name: "events"}, {Name: "users"}}
	sc.Indexes = []Index{
		{Table: "users", Name: "users_pk", Columns: []string{"id"}, Unique: true, Origin: OriginPrimaryKey},
		{Table: "events", Name: "events_created_idx", Columns: []string{"created_at"}, Origin: OriginCreateIndex},
	}
	return sc
}

func TestAdvise_PaginationAndBoundedness(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		scan RowScan
		want string
	}{
		{
			name: "select with no limit scanned into a slice",
			sql:  "SELECT id FROM users",
			scan: ScanSlice,
			want: "sql.unbounded-list",
		},
		{
			name: "single-row read is not unbounded",
			sql:  "SELECT id FROM users WHERE id = $1",
			scan: ScanSingle,
			want: "",
		},
		{
			name: "keyset walk with a page size is bounded",
			sql:  "SELECT id FROM events WHERE created_at < $1 ORDER BY created_at DESC LIMIT $2",
			scan: ScanSlice,
			want: "",
		},
		{
			// The cursor says where the page STARTS, not how big it is: this
			// returns every row before the cursor, which on the first page is
			// the whole table. It used to be classified as keyset pagination
			// and skipped entirely.
			name: "cursor predicate with no page size is still an unbounded read",
			sql:  "SELECT id FROM events WHERE created_at < $1 ORDER BY created_at DESC",
			scan: ScanSlice,
			want: "sql.unbounded-list",
		},
		{
			// A LIMIT inside a subquery bounds the subquery. The outer read
			// still returns a row per matching event.
			name: "limit inside a subquery does not bound the outer read",
			sql:  "SELECT id FROM events WHERE id IN (SELECT id FROM users LIMIT 10)",
			scan: ScanSlice,
			want: "sql.unbounded-list",
		},
		{
			name: "limit and offset with no order by",
			sql:  "SELECT id FROM users LIMIT $1 OFFSET $2",
			scan: ScanSlice,
			want: "sql.offset-depth,sql.unstable-pagination",
		},
		{
			name: "ordered offset pagination still degrades with depth",
			sql:  "SELECT id FROM users ORDER BY id LIMIT $1 OFFSET $2",
			scan: ScanSlice,
			want: "sql.offset-depth",
		},
		{
			name: "constant offset does not degrade with depth",
			sql:  "SELECT id FROM users ORDER BY id LIMIT 10 OFFSET 0",
			scan: ScanSlice,
			want: "",
		},
		{
			name: "select star",
			sql:  "SELECT * FROM users WHERE id = $1",
			scan: ScanSingle,
			want: "sql.select-star",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Advise([]Operation{mkOp(t, tc.sql, tc.scan)}, testSchema(t), perOpOnly())
			if got := strings.Join(codesOf(res), ","); got != tc.want {
				t.Errorf("codes = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdvise_UnboundedConfidenceTracksRowShape(t *testing.T) {
	slice := Advise([]Operation{mkOp(t, "SELECT id FROM users", ScanSlice)}, testSchema(t), perOpOnly())
	unknown := Advise([]Operation{mkOp(t, "SELECT id FROM users", ScanUnknown)}, testSchema(t), perOpOnly())
	if slice.Advisories[0].Confidence != ConfidenceHigh {
		t.Errorf("slice scan confidence = %q, want high", slice.Advisories[0].Confidence)
	}
	if unknown.Advisories[0].Confidence != ConfidenceMedium {
		t.Errorf("unknown scan confidence = %q, want medium", unknown.Advisories[0].Confidence)
	}
}

func TestAdvise_InjectionOnlyForCallerData(t *testing.T) {
	caller := Operation{
		Position: shared.FilePosition{Path: "r.go", Line: 3}, SymbolName: "R.M",
		SQL: "SELECT id FROM users WHERE ", UnresolvedReason: ReasonConcat,
		Interpolated: true, Interpolation: InterpolationConcat,
		CallerData: true, InterpolatedExpr: "column",
	}
	generated := caller
	generated.CallerData = false

	if got := strings.Join(codesOf(Advise([]Operation{caller}, Schema{}, AdviseOptions{})), ","); got != "sql.possible-injection" {
		t.Errorf("caller-data interpolation codes = %q, want sql.possible-injection", got)
	}
	res := Advise([]Operation{generated}, Schema{}, AdviseOptions{})
	if len(res.Advisories) != 0 {
		t.Errorf("interpolation with no caller value must stay quiet, got %v", codesOf(res))
	}
}

func TestAdvise_MissingIndex(t *testing.T) {
	sc := testSchema(t)

	// tenant_id has no index at all.
	res := Advise([]Operation{mkOp(t, "SELECT id FROM users WHERE tenant_id = $1", ScanSingle)}, sc, perOpOnly())
	if got := strings.Join(codesOf(res), ","); got != "sql.missing-index" {
		t.Errorf("codes = %q, want sql.missing-index", got)
	}

	// id is the primary key.
	res = Advise([]Operation{mkOp(t, "SELECT id FROM users WHERE id = $1", ScanSingle)}, sc, perOpOnly())
	if got := strings.Join(codesOf(res), ","); got != "" {
		t.Errorf("primary-key lookup produced %q", got)
	}

	// A table whose DDL was never read must yield "not checked", never a
	// finding: claiming an index is missing from a schema Atlas never saw is
	// the worst kind of wrong answer, because it looks authoritative.
	res = Advise([]Operation{mkOp(t, "SELECT id FROM widgets WHERE owner = $1", ScanSingle)}, sc, perOpOnly())
	if len(res.Advisories) != 0 {
		t.Errorf("unknown table produced advisories: %v", codesOf(res))
	}
	if got := strings.Join(skippedCodes(res), ","); got != "sql.missing-index/table:widgets" {
		t.Errorf("skipped = %q, want the widgets index check to be reported as not run", got)
	}

	// No DDL at all: one global skip, not one per table.
	res = Advise([]Operation{mkOp(t, "SELECT id FROM users WHERE tenant_id = $1", ScanSingle)}, Schema{}, perOpOnly())
	if got := strings.Join(skippedCodes(res), ","); got != "sql.missing-index/" {
		t.Errorf("skipped with no schema = %q", got)
	}
}

func TestAdvise_SuppressionSilencesTheCode(t *testing.T) {
	op := mkOp(t, "SELECT id FROM users", ScanSlice)
	op.Suppressions = []string{CodeUnboundedList}
	if res := Advise([]Operation{op}, testSchema(t), perOpOnly()); len(res.Advisories) != 0 {
		t.Errorf("directive did not silence the code: %v", codesOf(res))
	}

	op.Suppressions = nil
	res := Advise([]Operation{op}, testSchema(t), AdviseOptions{Suppress: []string{CodeUnboundedList, CodeOrphanTable, CodeUnusedIndex}})
	if len(res.Advisories) != 0 {
		t.Errorf("--suppress did not silence the code: %v", codesOf(res))
	}
}

// The schema-wide checks compare the full query inventory against the full
// schema. With even one query unresolved the inventory is incomplete, and a
// table declared orphan on incomplete evidence is a table someone deletes.
func TestAdvise_SchemaWideChecksNeedACompleteInventory(t *testing.T) {
	sc := testSchema(t)
	complete := []Operation{mkOp(t, "SELECT id FROM users WHERE id = $1", ScanSingle)}

	res := Advise(complete, sc, AdviseOptions{})
	if got := strings.Join(codesOf(res), ","); got != "sql.orphan-table,sql.unused-index" {
		t.Errorf("codes = %q, want the events table and its index reported", got)
	}

	partial := append(complete, Operation{
		Position: shared.FilePosition{Path: "r.go", Line: 9},
		Resolved: false, UnresolvedReason: ReasonDynamic,
	})
	res = Advise(partial, sc, AdviseOptions{})
	if got := strings.Join(codesOf(res), ","); got != "" {
		t.Errorf("schema-wide checks ran on an incomplete inventory: %q", got)
	}
	if got := strings.Join(skippedCodes(res), ","); !strings.Contains(got, "sql.orphan-table/") {
		t.Errorf("skipped = %q, want the orphan-table check reported as not run", got)
	}
}
