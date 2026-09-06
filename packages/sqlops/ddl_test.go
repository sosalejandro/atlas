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
