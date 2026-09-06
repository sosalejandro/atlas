package redact

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/store"
)

// newTestDB returns a fully migrated atlas state database and its path.
//
// It goes through store.Open rather than replaying DDL of its own, because
// the point of every test in this file is to compare the registry against
// the schema atlas ACTUALLY ships. A hand-written fixture schema would let
// the registry and the migrations drift together and still pass.
func newTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atlas.db")
	ctx := context.Background()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// liveTextColumns returns every TEXT column of every user table, as
// "table.column".
func liveTextColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, table := range liveTables(t, db) {
		rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		for rows.Next() {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				t.Fatalf("scan column: %v", err)
			}
			if strings.EqualFold(typ, "TEXT") {
				out = append(out, table+"."+name)
			}
		}
		_ = rows.Close()
	}
	sort.Strings(out)
	return out
}

func liveTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		out = append(out, n)
	}
	return out
}

// TestColumns_MatchesTheLiveSchema is the check that keeps docs/security.md
// honest. Every TEXT column is a place a credential can sit; a column the
// registry does not know about is a column the secret sweep never reads and
// the data-handling statement never mentions. When a migration adds one,
// this test fails until someone classifies it.
func TestColumns_MatchesTheLiveSchema(t *testing.T) {
	db, _ := newTestDB(t)
	live := liveTextColumns(t, db)

	registered := make([]string, 0, len(Columns()))
	seen := map[string]bool{}
	for _, c := range Columns() {
		key := c.Table + "." + c.Name
		if seen[key] {
			t.Errorf("column %s is registered twice", key)
		}
		seen[key] = true
		registered = append(registered, key)
	}
	sort.Strings(registered)

	missing := difference(live, seen)
	if len(missing) > 0 {
		t.Errorf("TEXT columns in the schema with no entry in Columns(): %v\n"+
			"Classify each one, and say what it holds in docs/security.md.", missing)
	}
	liveSet := map[string]bool{}
	for _, k := range live {
		liveSet[k] = true
	}
	if extra := difference(registered, liveSet); len(extra) > 0 {
		t.Errorf("Columns() names columns that do not exist in the schema: %v", extra)
	}
}

func TestTables_MatchesTheLiveSchema(t *testing.T) {
	db, _ := newTestDB(t)
	live := liveTables(t, db)

	known := map[string]bool{}
	for _, tb := range Tables() {
		if known[tb.Name] {
			t.Errorf("table %s is registered twice", tb.Name)
		}
		if tb.Purpose == "" {
			t.Errorf("table %s has no Purpose; the report prints it", tb.Name)
		}
		known[tb.Name] = true
	}
	if missing := difference(live, known); len(missing) > 0 {
		t.Errorf("tables in the schema with no entry in Tables(): %v", missing)
	}
	liveSet := map[string]bool{}
	for _, n := range live {
		liveSet[n] = true
	}
	registered := make([]string, 0, len(Tables()))
	for _, tb := range Tables() {
		registered = append(registered, tb.Name)
	}
	if extra := difference(registered, liveSet); len(extra) > 0 {
		t.Errorf("Tables() names tables that do not exist: %v", extra)
	}
}

// TestRedactableColumns_AreNeverPartOfAKey guards the one way an in-place
// redaction could corrupt the index instead of cleaning it. Rewriting a
// value that participates in a primary key or a unique index either fails
// the constraint or silently collapses two rows into one.
func TestRedactableColumns_AreNeverPartOfAKey(t *testing.T) {
	db, _ := newTestDB(t)
	for _, c := range Columns() {
		if !c.Redactable {
			continue
		}
		if inPrimaryKey(t, db, c.Table, c.Name) {
			t.Errorf("%s.%s is redactable but is part of the primary key", c.Table, c.Name)
		}
		if inUniqueIndex(t, db, c.Table, c.Name) {
			t.Errorf("%s.%s is redactable but participates in a unique index", c.Table, c.Name)
		}
	}
}

// TestRedactableColumnsHoldSourceText: a column atlas is willing to rewrite
// must be one that carries copied source or free text. Rewriting an
// identifier or a path would change what the index MEANS, not just what it
// discloses.
func TestRedactableColumnsHoldSourceText(t *testing.T) {
	for _, c := range Columns() {
		if !c.Redactable {
			continue
		}
		if c.Class != ClassSourceText && c.Class != ClassUserText {
			t.Errorf("%s.%s is redactable but classified %q", c.Table, c.Name, c.Class)
		}
	}
}

func TestColumns_EveryEntryIsClassifiedAndExplained(t *testing.T) {
	valid := map[Class]bool{
		ClassPath: true, ClassIdentifier: true, ClassSourceText: true,
		ClassUserText: true, ClassEnum: true, ClassDigest: true,
	}
	for _, c := range Columns() {
		if !valid[c.Class] {
			t.Errorf("%s.%s has unknown class %q", c.Table, c.Name, c.Class)
		}
		if c.Holds == "" {
			t.Errorf("%s.%s has no Holds description", c.Table, c.Name)
		}
	}
}

func TestTake_ReportsThePathSizeAndRowCounts(t *testing.T) {
	db, path := newTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO features (id, title) VALUES ('a', 'Alpha'), ('b', 'Beta')`); err != nil {
		t.Fatalf("seed features: %v", err)
	}

	inv, err := Take(context.Background(), db, path)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if inv.Path != path {
		t.Errorf("Path = %q, want %q", inv.Path, path)
	}
	if inv.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want the real on-disk size", inv.SizeBytes)
	}
	if want, err := os.Stat(path); err == nil && inv.SizeBytes != want.Size() {
		t.Errorf("SizeBytes = %d, want %d", inv.SizeBytes, want.Size())
	}
	if inv.SchemaVersion <= 0 {
		t.Errorf("SchemaVersion = %d, want the applied migration version", inv.SchemaVersion)
	}
	var features *TableStat
	for i := range inv.Tables {
		if inv.Tables[i].Name == "features" {
			features = &inv.Tables[i]
		}
	}
	if features == nil {
		t.Fatalf("features table missing from inventory: %+v", inv.Tables)
	}
	if features.Rows != 2 {
		t.Errorf("features.Rows = %d, want 2", features.Rows)
	}
	if len(features.Classes) == 0 {
		t.Errorf("features has no content classes")
	}
	if len(inv.Unclassified) != 0 {
		t.Errorf("Unclassified = %v on a stock schema, want none", inv.Unclassified)
	}
}

// TestTake_NamesWhatItCannotClassify: the inventory must never imply it
// described the whole database when it did not. A table the registry has
// never heard of is reported as such rather than omitted, because a silent
// omission is precisely the failure a data-handling statement cannot afford.
func TestTake_NamesWhatItCannotClassify(t *testing.T) {
	db, path := newTestDB(t)
	if _, err := db.Exec(`CREATE TABLE surprise (id INTEGER PRIMARY KEY, blob TEXT)`); err != nil {
		t.Fatalf("create surprise table: %v", err)
	}
	inv, err := Take(context.Background(), db, path)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if !contains(inv.Unclassified, "surprise") {
		t.Errorf("Unclassified = %v, want it to name the unknown table", inv.Unclassified)
	}
	if !contains(inv.Unclassified, "surprise.blob") {
		t.Errorf("Unclassified = %v, want it to name the unknown TEXT column", inv.Unclassified)
	}
}

// ---- helpers ----------------------------------------------------------

func difference(have []string, known map[string]bool) []string {
	var out []string
	for _, h := range have {
		if !known[h] {
			out = append(out, h)
		}
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func inPrimaryKey(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var pk int
	err := db.QueryRow(
		`SELECT pk FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&pk)
	if err != nil {
		t.Fatalf("pragma_table_info(%s).%s: %v", table, column, err)
	}
	return pk > 0
}

func inUniqueIndex(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name, "unique" FROM pragma_index_list(?)`, table)
	if err != nil {
		t.Fatalf("pragma_index_list(%s): %v", table, err)
	}
	type idx struct {
		name   string
		unique bool
	}
	var idxs []idx
	for rows.Next() {
		var i idx
		if err := rows.Scan(&i.name, &i.unique); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		idxs = append(idxs, i)
	}
	_ = rows.Close()

	for _, i := range idxs {
		if !i.unique {
			continue
		}
		cols, err := db.Query(fmt.Sprintf(`SELECT name FROM pragma_index_info(%q)`, i.name))
		if err != nil {
			t.Fatalf("pragma_index_info(%s): %v", i.name, err)
		}
		for cols.Next() {
			var n sql.NullString
			if err := cols.Scan(&n); err != nil {
				t.Fatalf("scan index column: %v", err)
			}
			if n.String == column {
				_ = cols.Close()
				return true
			}
		}
		_ = cols.Close()
	}
	return false
}
