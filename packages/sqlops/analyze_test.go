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
