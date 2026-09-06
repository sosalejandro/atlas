package sqlops

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const ddlFixture = `-- 0001_initial.up.sql
CREATE TABLE users (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  email     TEXT NOT NULL UNIQUE,
  tenant_id INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL
);

CREATE INDEX users_tenant_idx ON users(tenant_id, created_at);

CREATE TABLE memberships (
  user_id INTEGER NOT NULL REFERENCES users(id),
  org_id  INTEGER NOT NULL,
  -- a semicolon in a comment; must not split the statement
  PRIMARY KEY (user_id, org_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS memberships_org_idx
  ON memberships (org_id) WHERE org_id > 0;
`

func indexSummary(s Schema) []string {
	out := make([]string, 0, len(s.Indexes))
	for _, ix := range s.Indexes {
		row := ix.Table + "." + ix.Name + "(" + strings.Join(ix.Columns, ",") + ")/" + string(ix.Origin)
		if ix.Unique {
			row += "/unique"
		}
		if ix.Predicate != "" {
			row += "/partial"
		}
		out = append(out, row)
	}
	sort.Strings(out)
	return out
}

func TestParseSchema_TablesIndexesAndKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0001_initial.up.sql")
	if err := os.WriteFile(path, []byte(ddlFixture), 0o600); err != nil {
		t.Fatal(err)
	}

	sc, err := ParseSchemaDirs([]string{dir}, dir)
	if err != nil {
		t.Fatalf("ParseSchemaDirs: %v", err)
	}

	var names []string
	for _, tbl := range sc.Tables {
		names = append(names, tbl.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "memberships,users" {
		t.Errorf("tables = %q, want %q", got, "memberships,users")
	}

	want := []string{
		"memberships.memberships_org_idx(org_id)/create-index/unique/partial",
		"memberships.memberships_pk(user_id,org_id)/primary-key/unique",
		"users.users_email_uk(email)/unique-constraint/unique",
		"users.users_pk(id)/primary-key/unique",
		"users.users_tenant_idx(tenant_id,created_at)/create-index",
	}
	got := indexSummary(sc)
	if len(got) != len(want) {
		t.Fatalf("indexes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if !sc.Known("users") || sc.Known("nope") {
		t.Errorf("Known() disagrees with the parsed table set")
	}
}

// writeSchema lays out one migration directory and parses it.
func writeSchema(t *testing.T, files map[string]string) Schema {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sc, err := ParseSchemaDirs([]string{dir}, dir)
	if err != nil {
		t.Fatalf("ParseSchemaDirs: %v", err)
	}
	return sc
}

func tableNamesOf(s Schema) string {
	out := make([]string, 0, len(s.Tables))
	for _, t := range s.Tables {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// Postgres' CONCURRENTLY is not a reserved word to this lexer, so it used to
// lex as the index name and the real name as the table -- recording the index
// against a table that does not exist while the real table went on looking
// unindexed.
func TestParseSchema_CreateIndexQualifiers(t *testing.T) {
	sc := writeSchema(t, map[string]string{"0001_init.up.sql": `
CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT, tenant_id INTEGER);
CREATE INDEX CONCURRENTLY users_tenant_idx ON users (tenant_id);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS users_email_idx ON users (email);
CREATE INDEX IF NOT EXISTS users_email2_idx ON users (email);
`})

	want := []string{
		"users.users_email2_idx(email)/create-index",
		"users.users_email_idx(email)/create-index/unique",
		"users.users_pk(id)/primary-key/unique",
		"users.users_tenant_idx(tenant_id)/create-index",
	}
	if got := indexSummary(sc); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("indexes =\n  %v\nwant\n  %v", got, want)
	}
	if got := tableNamesOf(sc); got != "users" {
		t.Errorf("tables = %q, want just users -- an index recorded against a phantom table means the real one reports unindexed", got)
	}
	if _, ok := sc.LeadingIndexFor("users", map[string]bool{"tenant_id": true}); !ok {
		t.Error("the CONCURRENTLY index must serve a tenant_id lookup")
	}
}

// An index declaration whose table atlas cannot anchor on the ON keyword is
// recorded as nothing at all. Guessing which identifier is the table is what
// produced the phantom-table rows in the first place.
func TestParseSchema_IndexWithoutOnIsNotGuessedAt(t *testing.T) {
	sc := writeSchema(t, map[string]string{"0001.up.sql": `
CREATE TABLE users (id INTEGER PRIMARY KEY);
CREATE INDEX users_weird_idx SOMETHINGELSE users (id);
`})
	for _, ix := range sc.Indexes {
		if ix.Origin == OriginCreateIndex {
			t.Errorf("an unreadable CREATE INDEX was recorded anyway: %+v", ix)
		}
	}
}

// The inventory is the schema as of the last migration. A down migration undoes
// its up sibling, and a DROP later in the sequence undoes an earlier CREATE;
// counting either as an addition overstates what the database actually holds.
func TestParseSchema_RollbacksAndDropsDoNotInflateTheInventory(t *testing.T) {
	sc := writeSchema(t, map[string]string{
		"0001_init.up.sql": `
CREATE TABLE users (id INTEGER PRIMARY KEY, tenant_id INTEGER);
CREATE TABLE legacy_jobs (id INTEGER PRIMARY KEY);
CREATE INDEX users_tenant_idx ON users (tenant_id);
CREATE INDEX legacy_jobs_id_idx ON legacy_jobs (id);
`,
		// The rollback must not be read at all: it drops tables the up
		// migration created and creates none of its own.
		"0001_init.down.sql": `
DROP TABLE users;
DROP TABLE legacy_jobs;
`,
		"0002_drop_legacy.up.sql": `
DROP INDEX CONCURRENTLY IF EXISTS users_tenant_idx;
DROP TABLE IF EXISTS legacy_jobs;
`,
		"0003_rename.up.sql": `
ALTER TABLE users RENAME TO accounts;
CREATE INDEX accounts_tenant_idx ON accounts (tenant_id);
`,
	})

	if got := tableNamesOf(sc); got != "accounts" {
		t.Errorf("tables = %q, want accounts: the dropped table and the rollback must not appear", got)
	}
	if !sc.Known("accounts") || sc.Known("users") || sc.Known("legacy_jobs") {
		t.Errorf("Known() disagrees with the applied migrations: %+v", sc.Tables)
	}
	want := []string{
		"accounts.accounts_tenant_idx(tenant_id)/create-index",
		"accounts.users_pk(id)/primary-key/unique",
	}
	if got := indexSummary(sc); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("indexes =\n  %v\nwant\n  %v", got, want)
	}
	for _, f := range sc.Files {
		if strings.Contains(f, ".down.") {
			t.Errorf("a rollback migration was read: %s", f)
		}
	}
}

func TestParseSchema_RecordsSourcePositions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.sql"), []byte(ddlFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := ParseSchemaDirs([]string{dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ix := range sc.Indexes {
		if ix.Origin != OriginCreateIndex {
			continue
		}
		if ix.Position.Path != "s.sql" || ix.Position.Line == 0 {
			t.Errorf("index %s has no usable position: %+v", ix.Name, ix.Position)
		}
	}
}

// LeadingIndexFor is what the missing-index advisory is built on: an index can
// serve a lookup only when the query filters on its FIRST column. A composite
// (tenant_id, created_at) does nothing for a query that filters created_at
// alone, and reporting otherwise would silently bless the slow query.
func TestSchema_LeadingIndexFor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.sql"), []byte(ddlFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := ParseSchemaDirs([]string{dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if ix, ok := sc.LeadingIndexFor("users", map[string]bool{"tenant_id": true}); !ok || ix.Name != "users_tenant_idx" {
		t.Errorf("tenant_id lookup should use users_tenant_idx, got %q ok=%v", ix.Name, ok)
	}
	if _, ok := sc.LeadingIndexFor("users", map[string]bool{"created_at": true}); ok {
		t.Errorf("created_at is a trailing index column; no index leads with it")
	}
	if _, ok := sc.LeadingIndexFor("users", map[string]bool{"id": true}); !ok {
		t.Errorf("the primary key must count as an index")
	}
}
