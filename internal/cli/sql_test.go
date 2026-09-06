package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqlFixture lays out a repository whose data layer contains one of each
// interesting shape: a resolvable bounded query, an unbounded slice scan, and
// a query built with Sprintf from a caller-supplied value.
type sqlFixture struct {
	root   string
	dbPath string
}

func newSQLFixture(t *testing.T) *sqlFixture {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("db/migrations/0001_init.sql", `
CREATE TABLE users (id INTEGER PRIMARY KEY, tenant_id INTEGER NOT NULL);
CREATE TABLE audit_log (id INTEGER PRIMARY KEY, actor TEXT NOT NULL);
CREATE INDEX users_tenant_idx ON users(tenant_id);
`)
	write("repo/repo.go", `package repo

import (
	"context"
	"database/sql"
	"fmt"
)

type R struct{ db *sql.DB }

func (r *R) One(ctx context.Context, id int64) error {
	return r.db.QueryRowContext(ctx, "SELECT id FROM users WHERE id = $1", id).Scan(&id)
}

func (r *R) All(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM audit_log")
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

func (r *R) Spliced(ctx context.Context, actor string) error {
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM audit_log WHERE actor = '%s'", actor))
	return err
}

func (r *R) Built(ctx context.Context, b *builder) error {
	_, err := r.db.ExecContext(ctx, b.SQL())
	return err
}

type builder struct{}

func (b *builder) SQL() string { return "" }
`)
	f := &sqlFixture{root: dir, dbPath: filepath.Join(dir, ".atlas", "atlas.db")}
	loaded = Config{repoRoot: dir, DBPath: f.dbPath}
	flags = globalFlags{DBPath: f.dbPath}
	return f
}

func runSQLCmd(t *testing.T, fix *sqlFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"sql"}, append(args, "--db-path", fix.dbPath)...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestSQL_CommandIsRegistered(t *testing.T) {
	var found bool
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "sql" {
			found = true
			for _, sub := range []string{"scan", "list", "advise"} {
				var haveSub bool
				for _, s := range c.Commands() {
					if s.Name() == sub {
						haveSub = true
					}
				}
				if !haveSub {
					t.Errorf("atlas sql is missing the %q verb", sub)
				}
			}
		}
	}
	if !found {
		t.Fatal("atlas sql is not registered on the root command")
	}
}

func TestSQL_ScanReportsResolvedFraction(t *testing.T) {
	fix := newSQLFixture(t)
	stdout, stderr, err := runSQLCmd(t, fix, "scan", fix.root)
	if err != nil {
		t.Fatalf("sql scan: %v\nstderr:\n%s", err, stderr)
	}
	// Half the operations resolve: the Sprintf-assembled DELETE and the
	// builder-assembled Exec do not, and the command must say so rather than
	// reporting a clean sweep over the half it could read.
	if !strings.Contains(stdout, "4 operations") {
		t.Errorf("expected the operation count in the output; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "unresolved") {
		t.Errorf("expected the unresolved count to be reported; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "50% of the data layer analysed") {
		t.Errorf("expected the resolved fraction (50%%); got:\n%s", stdout)
	}
}

func TestSQL_ScanPersistsAndListReadsBack(t *testing.T) {
	fix := newSQLFixture(t)
	if _, stderr, err := runSQLCmd(t, fix, "scan", fix.root); err != nil {
		t.Fatalf("sql scan: %v\n%s", err, stderr)
	}

	stdout, stderr, err := runSQLCmd(t, fix, "list", "--json")
	if err != nil {
		t.Fatalf("sql list: %v\n%s", err, stderr)
	}
	var env struct {
		Command string `json:"command"`
		Result  struct {
			Operations []struct {
				Symbol     string `json:"symbol_name"`
				Kind       string `json:"kind"`
				Resolved   bool   `json:"resolved"`
				Reason     string `json:"unresolved_reason"`
				RowScan    string `json:"row_scan"`
				Tables     []struct{ Table, Access string }
				Predicates []struct {
					Column string `json:"column"`
				} `json:"predicates"`
			} `json:"operations"`
			Resolved         int     `json:"resolved"`
			Unresolved       int     `json:"unresolved"`
			ResolvedFraction float64 `json:"resolved_fraction"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if env.Command != "sql.list" {
		t.Errorf("command = %q, want sql.list", env.Command)
	}
	if len(env.Result.Operations) != 4 {
		t.Fatalf("operations = %d, want 4", len(env.Result.Operations))
	}
	if env.Result.Resolved != 2 || env.Result.Unresolved != 2 {
		t.Errorf("resolution = %d/%d, want 2/2", env.Result.Resolved, env.Result.Unresolved)
	}

	byName := map[string]int{}
	for i, op := range env.Result.Operations {
		byName[op.Symbol] = i
	}
	one := env.Result.Operations[byName["R.One"]]
	if !one.Resolved || one.Kind != "select" || one.RowScan != "single" {
		t.Errorf("R.One round-tripped as %+v", one)
	}
	if len(one.Predicates) != 1 || one.Predicates[0].Column != "id" {
		t.Errorf("R.One predicates = %+v", one.Predicates)
	}
	built := env.Result.Operations[byName["R.Built"]]
	if built.Resolved || built.Reason == "" {
		t.Errorf("the builder-assembled query must persist as unresolved with a reason: %+v", built)
	}
}

func TestSQL_AdviseFromStoredInventory(t *testing.T) {
	fix := newSQLFixture(t)
	if _, stderr, err := runSQLCmd(t, fix, "scan", fix.root); err != nil {
		t.Fatalf("sql scan: %v\n%s", err, stderr)
	}

	stdout, stderr, err := runSQLCmd(t, fix, "advise", "--json")
	if err != nil {
		t.Fatalf("sql advise: %v\n%s", err, stderr)
	}
	var env struct {
		Result struct {
			Advisories []struct {
				Code       string `json:"code"`
				Confidence string `json:"confidence"`
				Position   struct {
					Path string `json:"path"`
					Line int    `json:"line"`
				} `json:"position"`
			} `json:"advisories"`
			Skipped []struct {
				Code   string `json:"code"`
				Reason string `json:"reason"`
			} `json:"skipped_checks"`
			ResolvedFraction float64 `json:"resolved_fraction"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}

	codes := map[string]string{}
	for _, a := range env.Result.Advisories {
		codes[a.Code] = a.Confidence
		if a.Position.Path == "" || a.Position.Line == 0 {
			t.Errorf("advisory %s has no file:line anchor", a.Code)
		}
	}
	if codes["sql.unbounded-list"] != "high" {
		t.Errorf("expected a high-confidence unbounded-list advisory, got %v", codes)
	}
	if codes["sql.possible-injection"] != "high" {
		t.Errorf("expected a high-confidence injection advisory, got %v", codes)
	}
	if len(env.Result.Skipped) == 0 {
		t.Error("the schema-wide checks must be reported as skipped on a partial inventory")
	}
	if env.Result.ResolvedFraction >= 1 {
		t.Errorf("resolved fraction = %v, want < 1 with one unresolved query", env.Result.ResolvedFraction)
	}
}

func TestSQL_AdviseRespectsSuppressAndMinConfidence(t *testing.T) {
	fix := newSQLFixture(t)
	if _, _, err := runSQLCmd(t, fix, "scan", fix.root); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runSQLCmd(t, fix, "advise", "--suppress", "sql.unbounded-list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "sql.unbounded-list") {
		t.Errorf("--suppress did not silence the code:\n%s", stdout)
	}

	stdout, _, err = runSQLCmd(t, fix, "advise", "--min-confidence", "high")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "sql.select-star") {
		t.Errorf("--min-confidence high let a low-confidence advisory through:\n%s", stdout)
	}

	if _, _, err := runSQLCmd(t, fix, "advise", "--min-confidence", "nonsense"); err == nil {
		t.Error("an unknown --min-confidence value must be rejected, not silently ignored")
	}
}

func TestSQL_ListFiltersUnresolved(t *testing.T) {
	fix := newSQLFixture(t)
	if _, _, err := runSQLCmd(t, fix, "scan", fix.root); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runSQLCmd(t, fix, "list", "--unresolved")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "R.Built") {
		t.Errorf("--unresolved dropped the one unresolved query:\n%s", stdout)
	}
	if strings.Contains(stdout, "R.One") {
		t.Errorf("--unresolved listed a resolved query:\n%s", stdout)
	}
}

// The store is the source of truth for `list` and `advise`. Running them
// before a scan must say so, not report an empty clean bill of health.
func TestSQL_AdviseBeforeScanSaysSo(t *testing.T) {
	fix := newSQLFixture(t)
	stdout, _, err := runSQLCmd(t, fix, "advise")
	if err != nil {
		t.Fatalf("advise on an empty store should not error: %v", err)
	}
	if !strings.Contains(stdout, "atlas sql scan") {
		t.Errorf("expected the output to point at `atlas sql scan`; got:\n%s", stdout)
	}
}
