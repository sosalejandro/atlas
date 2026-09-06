package sqlops

import (
	"sort"
	"strings"
	"testing"
)

// tableSet renders a Statement's table accesses as a comparable string so a
// table-driven case can state the expectation in one field.
func tableSet(st Statement) string {
	out := make([]string, 0, len(st.Tables))
	for _, t := range st.Tables {
		out = append(out, string(t.Access)+":"+t.Table)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// predicateSet renders WHERE/JOIN predicates the same way.
func predicateSet(st Statement) string {
	out := make([]string, 0, len(st.Predicates))
	for _, p := range st.Predicates {
		q := p.Column
		if p.Table != "" {
			q = p.Table + "." + p.Column
		}
		out = append(out, string(p.Clause)+":"+q+" "+p.Operator)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestAnalyzeStatement_Shape(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		kind       StatementKind
		tables     string
		predicates string
		params     int
		limit      bool
		offset     bool
		order      bool
		star       bool
		keyset     bool
		offBound   OffsetBound
	}{
		{
			name:       "plain select with where",
			sql:        `SELECT id, email FROM users WHERE tenant_id = $1 AND status = $2`,
			kind:       KindSelect,
			tables:     "read:users",
			predicates: "where:users.status =,where:users.tenant_id =",
			params:     2,
			offBound:   OffsetNone,
		},
		{
			name:     "select star is visible",
			sql:      `SELECT * FROM users`,
			kind:     KindSelect,
			tables:   "read:users",
			star:     true,
			offBound: OffsetNone,
		},
		{
			name:       "join predicates are attributed through aliases",
			sql:        `SELECT u.id FROM users u JOIN orders o ON o.user_id = u.id WHERE o.state = ?`,
			kind:       KindSelect,
			tables:     "read:orders,read:users",
			predicates: "join:orders.user_id =,where:orders.state =",
			params:     1,
			offBound:   OffsetNone,
		},
		{
			name:     "limit offset order by",
			sql:      `SELECT id FROM events ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
			kind:     KindSelect,
			tables:   "read:events",
			params:   2,
			limit:    true,
			offset:   true,
			order:    true,
			offBound: OffsetParameter,
		},
		{
			name:     "mysql two-arg limit is an offset",
			sql:      `SELECT id FROM events ORDER BY id LIMIT 100, 20`,
			kind:     KindSelect,
			tables:   "read:events",
			limit:    true,
			offset:   true,
			order:    true,
			offBound: OffsetLiteral,
		},
		{
			name:       "keyset pagination is not offset pagination",
			sql:        `SELECT id FROM events WHERE created_at < $1 ORDER BY created_at DESC LIMIT $2`,
			kind:       KindSelect,
			tables:     "read:events",
			predicates: "where:events.created_at <",
			params:     2,
			limit:      true,
			order:      true,
			keyset:     true,
			offBound:   OffsetNone,
		},
		{
			name:       "row-value keyset",
			sql:        `SELECT id FROM events WHERE (created_at, id) < ($1, $2) ORDER BY created_at DESC, id DESC LIMIT 50`,
			kind:       KindSelect,
			tables:     "read:events",
			predicates: "where:events.created_at <,where:events.id <",
			params:     2,
			limit:      true,
			order:      true,
			keyset:     true,
			offBound:   OffsetNone,
		},
		{
			name:     "insert writes",
			sql:      `INSERT INTO audit_log (actor, action) VALUES (?, ?)`,
			kind:     KindInsert,
			tables:   "write:audit_log",
			params:   2,
			offBound: OffsetNone,
		},
		{
			name:       "update writes its target and reads its join source",
			sql:        `UPDATE accounts SET balance = $1 FROM ledger WHERE ledger.account_id = accounts.id`,
			kind:       KindUpdate,
			tables:     "read:ledger,write:accounts",
			predicates: "where:ledger.account_id =",
			params:     1,
			offBound:   OffsetNone,
		},
		{
			name:       "delete writes",
			sql:        `DELETE FROM sessions WHERE expires_at < ?`,
			kind:       KindDelete,
			tables:     "write:sessions",
			predicates: "where:sessions.expires_at <",
			params:     1,
			offBound:   OffsetNone,
		},
		{
			name:       "cte name is not a table",
			sql:        `WITH recent AS (SELECT id FROM events WHERE id > $1) SELECT * FROM recent`,
			kind:       KindSelect,
			tables:     "read:events",
			predicates: "where:events.id >",
			params:     1,
			star:       true,
			offBound:   OffsetNone,
		},
		{
			// A recursive CTE names its columns before AS. Missing that made
			// `chain` look like a table nobody had DDL for, which surfaced as
			// a permanently un-runnable index check on a table that does not
			// exist -- found by running the command against atlas itself.
			name: "recursive cte with a column list is not a table",
			sql: `WITH RECURSIVE chain(id, depth) AS (
			        SELECT id, 0 FROM edges WHERE id = ?
			        UNION ALL
			        SELECT e.id, c.depth + 1 FROM edges e JOIN chain c ON e.id = c.id
			      ) SELECT id FROM chain`,
			kind:       KindSelect,
			tables:     "read:edges",
			predicates: "join:edges.id =,where:edges.id =",
			params:     1,
			offBound:   OffsetNone,
		},
		{
			name:       "comments and question marks inside string literals do not become parameters",
			sql:        "-- name: Whatever :many\nSELECT id FROM notes WHERE body LIKE '%?%' AND owner = ?",
			kind:       KindSelect,
			tables:     "read:notes",
			predicates: "where:notes.body like,where:notes.owner =",
			params:     1,
			offBound:   OffsetNone,
		},
		{
			name:       "repeated postgres placeholder counts once",
			sql:        `SELECT id FROM users WHERE a = $1 OR b = $1`,
			kind:       KindSelect,
			tables:     "read:users",
			predicates: "where:users.a =,where:users.b =",
			params:     1,
			offBound:   OffsetNone,
		},
		{
			name:       "not in and is null are operators",
			sql:        `SELECT id FROM users WHERE role NOT IN ($1, $2) AND deleted_at IS NULL`,
			kind:       KindSelect,
			tables:     "read:users",
			params:     2,
			offBound:   OffsetNone,
			predicates: "where:users.deleted_at is,where:users.role not in",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := AnalyzeStatement(tc.sql)
			if !ok {
				t.Fatalf("AnalyzeStatement(%q) reported not-a-statement", tc.sql)
			}
			if st.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", st.Kind, tc.kind)
			}
			if got := tableSet(st); got != tc.tables {
				t.Errorf("tables = %q, want %q", got, tc.tables)
			}
			if got := predicateSet(st); got != tc.predicates {
				t.Errorf("predicates = %q, want %q", got, tc.predicates)
			}
			if st.ParamCount != tc.params {
				t.Errorf("param count = %d, want %d", st.ParamCount, tc.params)
			}
			if st.HasLimit != tc.limit {
				t.Errorf("has limit = %v, want %v", st.HasLimit, tc.limit)
			}
			if st.HasOffset != tc.offset {
				t.Errorf("has offset = %v, want %v", st.HasOffset, tc.offset)
			}
			if st.HasOrderBy != tc.order {
				t.Errorf("has order by = %v, want %v", st.HasOrderBy, tc.order)
			}
			if st.SelectStar != tc.star {
				t.Errorf("select star = %v, want %v", st.SelectStar, tc.star)
			}
			if st.Keyset != tc.keyset {
				t.Errorf("keyset = %v, want %v", st.Keyset, tc.keyset)
			}
			if st.OffsetBound != tc.offBound {
				t.Errorf("offset bound = %q, want %q", st.OffsetBound, tc.offBound)
			}
		})
	}
}

// A statement Atlas cannot even find a verb for must report not-ok rather than
// coming back as an empty SELECT -- an empty SELECT would then be scored as
// "unbounded read", which is the exact class of wrong advisory issue #126
// forbids.
func TestAnalyzeStatement_RejectsNonStatements(t *testing.T) {
	for _, s := range []string{"", "   ", "-- just a comment", "%s"} {
		if st, ok := AnalyzeStatement(s); ok {
			t.Errorf("AnalyzeStatement(%q) = %+v, true; want not-ok", s, st)
		}
	}
}

// Keyset is the flag that takes a query OUT of the unbounded-read check, so
// everything it claims has to be true. It used to fire on any bound range
// predicate whose column appeared anywhere in the ORDER BY, with no page size
// required at all -- which silently exempted time-window filters and recursion
// guards from the one check they most needed.
func TestAnalyzeStatement_KeysetNeedsAPageSizeAndTheLeadingSortColumn(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{
			name: "cursor on the leading sort column with a page size",
			sql:  `SELECT id FROM events WHERE created_at < $1 ORDER BY created_at DESC LIMIT $2`,
			want: true,
		},
		{
			name: "row-value cursor with a literal page size",
			sql:  `SELECT id FROM events WHERE (created_at, id) < ($1, $2) ORDER BY created_at DESC, id DESC LIMIT 50`,
			want: true,
		},
		{
			name: "a cursor with no page size does not bound anything",
			sql:  `SELECT id FROM events WHERE created_at < $1 ORDER BY created_at DESC`,
			want: false,
		},
		{
			name: "a time window is a filter, not a cursor",
			sql:  `SELECT id FROM events WHERE created_at > $1 ORDER BY id LIMIT 100`,
			want: false,
		},
		{
			name: "a recursion depth guard is not a cursor",
			sql:  `SELECT id FROM edges WHERE depth < $1 ORDER BY tenant_id, depth LIMIT 100`,
			want: false,
		},
		{
			name: "a constant range bound is not a cursor value",
			sql:  `SELECT id FROM events WHERE created_at < 100 ORDER BY created_at LIMIT 20`,
			want: false,
		},
		{
			name: "an equality on the sort column does not walk it",
			sql:  `SELECT id FROM events WHERE created_at = $1 ORDER BY created_at LIMIT 20`,
			want: false,
		},
		{
			name: "the page size must bound this statement, not a subquery",
			sql:  `SELECT id FROM events WHERE created_at < $1 AND id IN (SELECT id FROM users LIMIT 10) ORDER BY created_at`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := AnalyzeStatement(tc.sql)
			if !ok {
				t.Fatalf("AnalyzeStatement(%q) reported not-a-statement", tc.sql)
			}
			if st.Keyset != tc.want {
				t.Errorf("keyset = %v, want %v", st.Keyset, tc.want)
			}
		})
	}
}

// A LIMIT anywhere in the token stream used to mark the statement bounded --
// including one inside a CTE body, a subquery or an IN(...) list, none of which
// says anything about how many rows the outer statement returns.
func TestAnalyzeStatement_LimitMustBindTheOuterStatement(t *testing.T) {
	cases := []struct {
		name              string
		sql               string
		limit, offsetFlag bool
		offBound          OffsetBound
	}{
		{
			name:  "limit in an IN(...) subquery",
			sql:   `SELECT id FROM events WHERE id IN (SELECT id FROM users ORDER BY id LIMIT 10)`,
			limit: false, offBound: OffsetNone,
		},
		{
			name:  "limit in a CTE body",
			sql:   `WITH recent AS (SELECT id FROM events ORDER BY id LIMIT 10) SELECT id FROM recent`,
			limit: false, offBound: OffsetNone,
		},
		{
			name:  "offset in a subquery is not the outer statement's offset",
			sql:   `SELECT id FROM events WHERE id IN (SELECT id FROM users LIMIT 10 OFFSET $1)`,
			limit: false, offsetFlag: false, offBound: OffsetNone,
		},
		{
			name:  "mysql two-arg limit inside a subquery stays inside it",
			sql:   `SELECT id FROM events WHERE id IN (SELECT id FROM users ORDER BY id LIMIT 100, 20)`,
			limit: false, offsetFlag: false, offBound: OffsetNone,
		},
		{
			name:  "the outer limit still counts with a subquery present",
			sql:   `SELECT id FROM events WHERE id IN (SELECT id FROM users) LIMIT 25`,
			limit: true, offBound: OffsetNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := AnalyzeStatement(tc.sql)
			if !ok {
				t.Fatalf("AnalyzeStatement(%q) reported not-a-statement", tc.sql)
			}
			if st.HasLimit != tc.limit {
				t.Errorf("has limit = %v, want %v", st.HasLimit, tc.limit)
			}
			if st.HasOffset != tc.offsetFlag {
				t.Errorf("has offset = %v, want %v", st.HasOffset, tc.offsetFlag)
			}
			if st.OffsetBound != tc.offBound {
				t.Errorf("offset bound = %q, want %q", st.OffsetBound, tc.offBound)
			}
		})
	}
}

func TestAnalyzeStatement_OrderByColumns(t *testing.T) {
	st, ok := AnalyzeStatement(`SELECT id FROM t ORDER BY t.created_at DESC, id ASC LIMIT 10`)
	if !ok {
		t.Fatal("not a statement")
	}
	got := strings.Join(st.OrderBy, ",")
	if got != "created_at,id" {
		t.Fatalf("order by = %q, want %q", got, "created_at,id")
	}
}
