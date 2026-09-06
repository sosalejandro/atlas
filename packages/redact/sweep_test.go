package redact_test

// External test package: see the note at the top of schema_test.go.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	. "github.com/sosalejandro/atlas/packages/redact"
)

// seedOperation inserts one sql_operations row with the given query text.
func seedOperation(t *testing.T, db *sql.DB, ref, sqlText string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO sql_operations (ref, source, name, file_path, line, symbol_name, kind, sql_text)
		 VALUES (?, 'go', ?, 'internal/orders/repo.go', 42, 'orders.Repo.List', 'select', ?)`,
		ref, ref, sqlText)
	if err != nil {
		t.Fatalf("seed sql_operations %s: %v", ref, err)
	}
}

func readSQLText(t *testing.T, db *sql.DB, ref string) string {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT sql_text FROM sql_operations WHERE ref = ?`, ref).Scan(&got); err != nil {
		t.Fatalf("read back %s: %v", ref, err)
	}
	return got
}

func TestSweep_CleanStoreReportsNothing(t *testing.T) {
	db, _ := newTestDB(t)
	seedOperation(t, db, "q1",
		"SELECT id, total FROM orders WHERE tenant_id = $1 ORDER BY id LIMIT 50")

	rep, err := Sweep(context.Background(), db, SweepOptions{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.Hits) != 0 {
		t.Errorf("Hits = %+v, want none", rep.Hits)
	}
	if rep.ColumnsRead == 0 {
		t.Errorf("ColumnsRead = 0; the sweep read nothing at all")
	}
	if rep.RowsRead == 0 {
		t.Errorf("RowsRead = 0 although a row was seeded")
	}
}

func TestSweep_FindsACredentialInStoredQueryText(t *testing.T) {
	db, _ := newTestDB(t)
	const leaky = `SELECT dblink_connect('postgres://reporting:Kq9Xm2Vz7Pw4Rt6Y@warehouse.internal:5432/dw')`
	seedOperation(t, db, "q1", leaky)

	rep, err := Sweep(context.Background(), db, SweepOptions{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.Hits) != 1 {
		t.Fatalf("Hits = %+v, want exactly 1", rep.Hits)
	}
	h := rep.Hits[0]
	if h.Table != "sql_operations" || h.Column != "sql_text" {
		t.Errorf("hit located at %s.%s, want sql_operations.sql_text", h.Table, h.Column)
	}
	if h.Kind != KindConnectionString {
		t.Errorf("Kind = %q, want %q", h.Kind, KindConnectionString)
	}
	if !h.Redactable {
		t.Errorf("sql_text must be redactable")
	}
	if h.Applied {
		t.Errorf("Applied = true without SweepOptions.Apply")
	}
	if h.Row == "" {
		t.Errorf("hit does not say which row it is in")
	}
	// Reporting must not be a second copy of the leak.
	if strings.Contains(h.Context, "Kq9Xm2Vz7Pw4Rt6Y") {
		t.Errorf("Context leaks the password: %q", h.Context)
	}
	// Nothing was asked to change, so nothing may have changed.
	if got := readSQLText(t, db, "q1"); got != leaky {
		t.Errorf("a read-only sweep rewrote the store:\n got %q\nwant %q", got, leaky)
	}
}

func TestSweep_ApplyRewritesTheStoredValueAndIsIdempotent(t *testing.T) {
	db, _ := newTestDB(t)
	const leaky = `SELECT dblink_connect('postgres://reporting:Kq9Xm2Vz7Pw4Rt6Y@warehouse.internal:5432/dw')`
	seedOperation(t, db, "q1", leaky)

	rep, err := Sweep(context.Background(), db, SweepOptions{Apply: true})
	if err != nil {
		t.Fatalf("Sweep(apply): %v", err)
	}
	if rep.ValuesRewritten != 1 {
		t.Errorf("ValuesRewritten = %d, want 1", rep.ValuesRewritten)
	}
	if len(rep.Hits) != 1 || !rep.Hits[0].Applied {
		t.Fatalf("Hits = %+v, want one applied hit", rep.Hits)
	}

	stored := readSQLText(t, db, "q1")
	if strings.Contains(stored, "Kq9Xm2Vz7Pw4Rt6Y") {
		t.Fatalf("the password survived the redaction: %q", stored)
	}
	// The query must remain the query. A redaction that destroys the shape
	// of the statement destroys every analysis built on it.
	for _, keep := range []string{"dblink_connect", "postgres://", "reporting", "warehouse.internal", "5432", "dw"} {
		if !strings.Contains(stored, keep) {
			t.Errorf("redaction removed %q from the query: %q", keep, stored)
		}
	}

	again, err := Sweep(context.Background(), db, SweepOptions{Apply: true})
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if len(again.Hits) != 0 || again.ValuesRewritten != 0 {
		t.Errorf("second sweep was not a no-op: %+v", again)
	}
	if readSQLText(t, db, "q1") != stored {
		t.Errorf("second sweep changed the already-redacted value")
	}
}

func TestSweep_RedactsEveryRowHoldingTheSameSecret(t *testing.T) {
	db, _ := newTestDB(t)
	const leaky = `SELECT 1 -- dsn: postgres://reporting:Kq9Xm2Vz7Pw4Rt6Y@warehouse.internal:5432/dw`
	seedOperation(t, db, "q1", leaky)
	seedOperation(t, db, "q2", leaky)

	rep, err := Sweep(context.Background(), db, SweepOptions{Apply: true})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.Hits) != 2 {
		t.Fatalf("Hits = %+v, want one per row", rep.Hits)
	}
	if rep.Hits[0].Digest != rep.Hits[1].Digest {
		t.Errorf("the same credential in two rows produced different digests: %q / %q",
			rep.Hits[0].Digest, rep.Hits[1].Digest)
	}
	for _, ref := range []string{"q1", "q2"} {
		if strings.Contains(readSQLText(t, db, ref), "Kq9Xm2Vz7Pw4Rt6Y") {
			t.Errorf("row %s still holds the password", ref)
		}
	}
}

// TestSweep_ReportsButNeverRewritesIdentityColumns is the conservative half
// of the design. A credential that ended up in a symbol name or a file path
// has to be fixed in the source; rewriting the index row would break every
// join that name participates in and would not remove the credential from
// the repository it came from.
func TestSweep_ReportsButNeverRewritesIdentityColumns(t *testing.T) {
	db, _ := newTestDB(t)
	const name = "keys.AKIAIOSFODNN7EXAMPLE"
	if _, err := db.Exec(
		`INSERT INTO symbols (qualified_name, kind, file_path, line, node_class)
		 VALUES (?, 'const', 'keys/aws.go', 3, 'declaration')`,
		name); err != nil {
		t.Fatalf("seed symbol: %v", err)
	}

	rep, err := Sweep(context.Background(), db, SweepOptions{Apply: true})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.Hits) != 1 {
		t.Fatalf("Hits = %+v, want 1", rep.Hits)
	}
	h := rep.Hits[0]
	if h.Redactable || h.Applied {
		t.Errorf("symbols.qualified_name was rewritten; Redactable=%v Applied=%v", h.Redactable, h.Applied)
	}
	if rep.Unredactable != 1 {
		t.Errorf("Unredactable = %d, want 1", rep.Unredactable)
	}
	var stored string
	if err := db.QueryRow(`SELECT qualified_name FROM symbols WHERE line = 3`).Scan(&stored); err != nil {
		t.Fatalf("read back symbol: %v", err)
	}
	if stored != name {
		t.Errorf("qualified_name = %q, want it untouched (%q)", stored, name)
	}
}

func TestSweep_LooksInsideSnapshotBlobs(t *testing.T) {
	// index_json carries doc comments and signatures. It is the single
	// biggest disclosure in the database and the easiest to forget, so the
	// sweep has to reach into it.
	db, _ := newTestDB(t)
	blob := `{"symbols":[{"id":"cfg.Load","doc":"Load reads the DSN ` +
		`postgres://svc:Kq9Xm2Vz7Pw4Rt6Y@db.internal:5432/app from the environment."}]}`
	if _, err := db.Exec(
		`INSERT INTO snapshots (git_ref, captured_at, index_json) VALUES ('HEAD', CURRENT_TIMESTAMP, ?)`,
		blob); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	rep, err := Sweep(context.Background(), db, SweepOptions{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.Hits) != 1 || rep.Hits[0].Column != "index_json" {
		t.Fatalf("Hits = %+v, want one in snapshots.index_json", rep.Hits)
	}
}

// TestSweep_DryRunCountsWhatItWouldRewrite.
//
// ValuesRewritten used to be set only inside the apply branch, so a dry run
// over a store full of secrets reported "0 distinct values" -- "running this
// would change nothing", which is the one answer a dry run must never give.
// The count is the entire product of the dry run; Hit.Applied is what says
// whether the database was actually written.
func TestSweep_DryRunCountsWhatItWouldRewrite(t *testing.T) {
	db, _ := newTestDB(t)
	const leaky = `SELECT dblink_connect('postgres://reporting:Kq9Xm2Vz7Pw4Rt6Y@warehouse.internal:5432/dw')`
	seedOperation(t, db, "q1", leaky)
	// The same credential in a second row: one leak, two rows. The count is
	// of distinct VALUES, so it must still be 1.
	seedOperation(t, db, "q2", leaky)

	rep, err := Sweep(context.Background(), db, SweepOptions{})
	if err != nil {
		t.Fatalf("Sweep(dry run): %v", err)
	}
	if rep.ValuesRewritten != 1 {
		t.Errorf("ValuesRewritten = %d on a dry run over 2 rows holding 1 credential, want 1",
			rep.ValuesRewritten)
	}
	if len(rep.Hits) != 2 {
		t.Fatalf("Hits = %+v, want one per row", rep.Hits)
	}
	for _, h := range rep.Hits {
		if h.Applied {
			t.Errorf("a dry-run hit is marked Applied: %+v", h)
		}
	}
	for _, ref := range []string{"q1", "q2"} {
		if readSQLText(t, db, ref) != leaky {
			t.Errorf("the dry run modified row %s", ref)
		}
	}
}

// TestSweep_NamesTheColumnsItDidNotRead.
//
// The sweep iterates the compiled-in registry, so a TEXT column the schema
// has and the registry does not is never read -- and "no secrets found"
// prints identically whether the store is clean or half of it went unlooked
// at. The bound has to be reported, not inferred from an empty hit list.
func TestSweep_NamesTheColumnsItDidNotRead(t *testing.T) {
	db, _ := newTestDB(t)

	// A migrated store must have nothing unregistered: schema_test.go fails
	// the build on drift, and this is the same statement from the sweep's
	// side.
	rep, err := Sweep(context.Background(), db, SweepOptions{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.ColumnsNotSwept) != 0 {
		t.Errorf("ColumnsNotSwept = %v on a freshly migrated store", rep.ColumnsNotSwept)
	}

	// Now stand in for a database newer than the binary reading it.
	if _, err := db.Exec(`CREATE TABLE future_notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("seed unregistered table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO future_notes (body) VALUES ` +
			`('dsn postgres://reporting:Kq9Xm2Vz7Pw4Rt6Y@warehouse.internal:5432/dw')`); err != nil {
		t.Fatalf("seed unregistered row: %v", err)
	}
	rep, err = Sweep(context.Background(), db, SweepOptions{})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rep.Hits) != 0 {
		t.Fatalf("the sweep read an unregistered column after all: %+v", rep.Hits)
	}
	var named bool
	for _, c := range rep.ColumnsNotSwept {
		if c == "future_notes.body" {
			named = true
		}
	}
	if !named {
		t.Errorf("ColumnsNotSwept = %v; it must name future_notes.body, which holds a "+
			"credential nothing looked at", rep.ColumnsNotSwept)
	}
}
