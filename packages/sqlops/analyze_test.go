package sqlops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildProject lays out a repository with a sqlc config, a migration, a query
// file and Go sources, so Analyze is exercised through its discovery path
// rather than through hand-supplied directories.
func buildProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("sqlc.yaml", `version: "2"
sql:
  - engine: "postgresql"
    schema: "db/migrations"
    queries: "db/queries"
`)
	write("db/migrations/0001_init.sql", `
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  tenant_id INTEGER NOT NULL
);
CREATE INDEX users_tenant_idx ON users(tenant_id);
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY,
  actor TEXT NOT NULL
);
`)
	write("db/queries/users.sql", `-- name: ListUsers :many
SELECT id FROM users WHERE tenant_id = $1;
`)
	write("internal/repo/repo.go", `package repo

import (
	"context"
	"database/sql"
	"fmt"
)

type R struct{ db *sql.DB }

func (r *R) Bad(ctx context.Context, actor string) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM audit_log WHERE actor = '%s'", actor))
	return err
}

func (r *R) Wide(ctx context.Context) ([]int, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM audit_log")
	if err != nil {
		return nil, err
	}
	var out []int
	for rows.Next() {
		var n int
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}
`)
	return root
}

func TestAnalyze_DiscoversSchemaAndQueriesFromSQLCConfig(t *testing.T) {
	root := buildProject(t)
	rep, err := Analyze(Options{Root: root})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if got := strings.Join(rep.SchemaDirs, ","); got != "db/migrations" {
		t.Errorf("schema dirs = %q, want db/migrations", got)
	}
	if got := strings.Join(rep.QueryDirs, ","); got != "db/queries" {
		t.Errorf("query dirs = %q, want db/queries", got)
	}
	if !rep.Schema.Known("users") || !rep.Schema.Known("audit_log") {
		t.Errorf("schema tables = %+v", rep.Schema.Tables)
	}
	if len(rep.Operations) != 3 {
		t.Fatalf("operations = %d, want 3 (two Go call sites, one sqlc query)", len(rep.Operations))
	}
}

func TestAnalyze_ReportsResolvedFraction(t *testing.T) {
	root := buildProject(t)
	rep, err := Analyze(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Resolved != 2 || rep.Unresolved != 1 {
		t.Fatalf("resolved/unresolved = %d/%d, want 2/1", rep.Resolved, rep.Unresolved)
	}
	if got := rep.ResolvedFraction(); got < 0.66 || got > 0.67 {
		t.Errorf("resolved fraction = %v, want ~0.666", got)
	}

	// An empty repository is fully resolved, not zero-percent resolved.
	empty, err := Analyze(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if empty.ResolvedFraction() != 1 {
		t.Errorf("empty project fraction = %v, want 1", empty.ResolvedFraction())
	}
}

// buildSQLCProject lays out what sqlc actually produces: a .sql file holding
// the query definitions, and generated Go that passes the very same text --
// `-- name:` header and all -- to database/sql.
func buildSQLCProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("sqlc.yaml", `version: "2"
sql:
  - engine: "postgresql"
    schema: "db/migrations"
    queries: "db/queries"
`)
	write("db/migrations/0001_init.sql", `
CREATE TABLE users (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL);
CREATE INDEX users_tenant_idx ON users(tenant_id);
`)
	write("db/queries/users.sql", `-- name: ListUsers :many
SELECT id FROM users WHERE tenant_id = $1;

-- name: GetUser :one
SELECT id FROM users WHERE id = $1;
`)
	write("db/gen/users.sql.go", "package gen\n\n"+`
import (
	"context"
	"database/sql"
)

type Queries struct{ db *sql.DB }

const listUsers = ` + "`" + `-- name: ListUsers :many
SELECT id FROM users WHERE tenant_id = $1
` + "`" + `

func (q *Queries) ListUsers(ctx context.Context, tenantID int64) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, listUsers, tenantID)
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var n int64
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, nil
}

const getUser = ` + "`" + `-- name: GetUser :one
SELECT id FROM users WHERE id = $1
` + "`" + `

func (q *Queries) GetUser(ctx context.Context, id int64) (int64, error) {
	row := q.db.QueryRowContext(ctx, getUser, id)
	err := row.Scan(&id)
	return id, err
}
`)
	return root
}

// A sqlc query lives in the sources twice, and counting both counts the whole
// data layer twice: the operation count doubles and the resolved fraction --
// the number the honesty contract rests on and a CI gate reads -- moves with
// it.
func TestAnalyze_SQLCQueriesAreCountedOnce(t *testing.T) {
	rep, err := Analyze(Options{Root: buildSQLCProject(t)})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Operations) != 2 {
		var got []string
		for _, op := range rep.Operations {
			got = append(got, string(op.Source)+":"+op.Position.Path+":"+op.SymbolName)
		}
		t.Fatalf("operations = %d, want 2 (one per sqlc query): %v", len(rep.Operations), got)
	}
	if rep.Merged != 2 {
		t.Errorf("merged = %d, want 2 -- the count has to be reported, not applied silently", rep.Merged)
	}
	if rep.Resolved != 2 || rep.Unresolved != 0 {
		t.Errorf("resolution = %d/%d, want 2/0", rep.Resolved, rep.Unresolved)
	}

	// The surviving row is the .sql definition: it is where the query is
	// written, and its :one/:many annotation is better evidence of the row
	// shape than anything inferred from generated code.
	for _, op := range rep.Operations {
		if op.Source != SourceSQLFile {
			t.Errorf("%s survived from %s; the .sql definition is the one to keep", op.SymbolName, op.Source)
		}
		if op.Position.Path != "db/queries/users.sql" {
			t.Errorf("operation anchored at %s, want the .sql file", op.Position.Path)
		}
	}
	if rep.Advisories == nil {
		t.Log("no advisories, which is fine; the point of this case is the count")
	}
}

// A Go call site that is not a sqlc echo must never be merged away, however
// familiar its SQL looks.
func TestAnalyze_HandWrittenGoQueriesAreNotMerged(t *testing.T) {
	root := buildSQLCProject(t)
	body := "package repo\n\n" + `
import (
	"context"
	"database/sql"
)

type R struct{ db *sql.DB }

func (r *R) A(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, "SELECT id FROM users WHERE tenant_id = $1")
	return err
}

func (r *R) B(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, "SELECT id FROM users WHERE tenant_id = $1")
	return err
}
`
	if err := os.MkdirAll(filepath.Join(root, "internal", "repo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "repo", "repo.go"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Analyze(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Operations) != 4 {
		t.Fatalf("operations = %d, want 4 (2 sqlc queries + 2 hand-written call sites)", len(rep.Operations))
	}
	if rep.Merged != 2 {
		t.Errorf("merged = %d, want 2 -- only the generated echoes reconcile", rep.Merged)
	}
}

func TestAnalyze_EndToEndAdvisories(t *testing.T) {
	root := buildProject(t)
	rep, err := Analyze(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]Confidence{}
	for _, a := range rep.Advisories {
		codes[a.Code] = a.Confidence
	}
	if codes[CodePossibleInjection] != ConfidenceHigh {
		t.Errorf("the Sprintf'd actor must raise a high-confidence injection advisory; got %v", codes)
	}
	if codes[CodeUnboundedList] != ConfidenceHigh {
		t.Errorf("the LIMIT-less slice scan must raise an unbounded-list advisory; got %v", codes)
	}
	// One operation is unresolved, so the schema-wide checks must abstain.
	if _, found := codes[CodeOrphanTable]; found {
		t.Error("orphan-table ran on an inventory with an unresolved query")
	}
	var sawSkip bool
	for _, s := range rep.Skipped {
		if s.Code == CodeOrphanTable && strings.Contains(s.Reason, "could not be resolved") {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Errorf("the abstention was not reported: %+v", rep.Skipped)
	}
}
