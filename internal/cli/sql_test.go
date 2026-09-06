package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
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
			for _, sub := range []string{"scan", "list", "advise", "capabilities"} {
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

// writeInto adds a file to an existing fixture.
func writeInto(t *testing.T, fix *sqlFixture, rel, body string) {
	t.Helper()
	p := filepath.Join(fix.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A sqlc query is in the sources twice -- the .sql definition and the
// generated Go call that passes the same text to database/sql. Counting both
// inflates the operation count and moves the resolved fraction, so scan
// reconciles them and has to SAY that it did: an unexplained gap between call
// sites read and operations reported is what makes a reader stop trusting the
// counters.
func TestSQL_ScanReportsMergedSQLCQueries(t *testing.T) {
	fix := newSQLFixture(t)
	writeInto(t, fix, "db/queries/audit.sql", `-- name: ListAudit :many
SELECT id FROM audit_log;
`)
	writeInto(t, fix, "repo/gen.go", "package repo\n\nimport \"context\"\n\n"+
		"const listAudit = `-- name: ListAudit :many\nSELECT id FROM audit_log\n`\n\n"+
		`type Queries struct{ db dbtx }

type dbtx interface{}

func (q *R) ListAudit(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, listAudit)
	return err
}
`)

	stdout, stderr, err := runSQLCmd(t, fix, "scan", fix.root, "--query-dir", "db/queries")
	if err != nil {
		t.Fatalf("sql scan: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "merged 1 generated call site") {
		t.Errorf("the merge is not reported in the output:\n%s", stdout)
	}

	stdout, _, err = runSQLCmd(t, fix, "scan", fix.root, "--query-dir", "db/queries", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result struct {
			Operations int `json:"operations"`
			Merged     int `json:"merged"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if env.Result.Merged != 1 {
		t.Errorf("merged = %d, want 1", env.Result.Merged)
	}
	// Four call sites from the base fixture, plus the one sqlc query. The
	// generated echo of that query is not a fifth operation.
	if env.Result.Operations != 5 {
		t.Errorf("operations = %d, want 5", env.Result.Operations)
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

// seedCapabilities links features to the symbols the fixture's queries live
// in, the way `atlas scan` does from annotations. It runs before `sql scan`
// because the operation rows resolve their symbol link by qualified name at
// write time.
func seedCapabilities(t *testing.T, fix *sqlFixture, links map[string]string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	for symbol, feature := range links {
		id, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(symbol), Kind: shared.KindMethod,
			FilePath: "repo/repo.go", Line: 1,
		})
		if err != nil {
			t.Fatalf("insert symbol %s: %v", symbol, err)
		}
		if err := s.Features().Upsert(ctx, store.Feature{
			ID: shared.FeatureID(feature), Title: feature,
		}); err != nil {
			t.Fatalf("upsert feature %s: %v", feature, err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: shared.FeatureID(feature), SymbolID: id,
			Role: store.RoleImpl, Source: store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("link %s -> %s: %v", feature, symbol, err)
		}
	}
}

// Issue #126 asks for a capability's read/write table set. This is the data
// side of it: the rollup a per-capability ERD would be drawn from, and the
// answer a privacy or migration review needs -- including the part where the
// answer is only a lower bound.
func TestSQL_CapabilitiesRollUpReadsAndWrites(t *testing.T) {
	fix := newSQLFixture(t)
	seedCapabilities(t, fix, map[string]string{
		"R.All":   "audit.read",
		"R.Built": "audit.dynamic",
	})
	if _, stderr, err := runSQLCmd(t, fix, "scan", fix.root); err != nil {
		t.Fatalf("sql scan: %v\n%s", err, stderr)
	}

	stdout, stderr, err := runSQLCmd(t, fix, "capabilities", "--json")
	if err != nil {
		t.Fatalf("sql capabilities: %v\n%s", err, stderr)
	}
	var env struct {
		Command string `json:"command"`
		Result  struct {
			Capabilities []struct {
				FeatureID  string   `json:"feature_id"`
				Reads      []string `json:"reads"`
				Writes     []string `json:"writes"`
				Operations int      `json:"operations"`
				Unresolved int      `json:"unresolved"`
			} `json:"capabilities"`
			Linked     int `json:"linked_operations"`
			Operations int `json:"operations"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if env.Command != "sql.capabilities" {
		t.Errorf("command = %q, want sql.capabilities", env.Command)
	}

	byID := map[string]int{}
	for i, c := range env.Result.Capabilities {
		byID[c.FeatureID] = i
	}
	read, ok := byID["audit.read"]
	if !ok {
		t.Fatalf("audit.read is missing from the rollup: %+v", env.Result.Capabilities)
	}
	if strings.Join(env.Result.Capabilities[read].Reads, ",") != "audit_log" {
		t.Errorf("audit.read reads = %v, want audit_log", env.Result.Capabilities[read].Reads)
	}
	if n := env.Result.Capabilities[read].Unresolved; n != 0 {
		t.Errorf("audit.read unresolved = %d, want 0", n)
	}

	// The capability whose only query is builder-assembled keeps its row with
	// an empty footprint and the count that explains it. Reporting "touches no
	// tables" for it would be the confident wrong answer.
	dyn, ok := byID["audit.dynamic"]
	if !ok {
		t.Fatalf("audit.dynamic was dropped from the rollup: %+v", env.Result.Capabilities)
	}
	if len(env.Result.Capabilities[dyn].Reads) != 0 || len(env.Result.Capabilities[dyn].Writes) != 0 {
		t.Errorf("audit.dynamic footprint = %+v, want empty", env.Result.Capabilities[dyn])
	}
	if env.Result.Capabilities[dyn].Unresolved != 1 {
		t.Errorf("audit.dynamic unresolved = %d, want 1", env.Result.Capabilities[dyn].Unresolved)
	}
	if env.Result.Linked != 2 || env.Result.Operations != 4 {
		t.Errorf("linked/total = %d/%d, want 2/4", env.Result.Linked, env.Result.Operations)
	}

	// The human rendering has to carry the caveat too.
	stdout, _, err = runSQLCmd(t, fix, "capabilities")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "audit_log") {
		t.Errorf("the table footprint is missing from the output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "PARTIAL") {
		t.Errorf("a capability with an unresolved query must be flagged partial:\n%s", stdout)
	}
}

func TestSQL_CapabilitiesBeforeAnyLinkSaysSo(t *testing.T) {
	fix := newSQLFixture(t)
	if _, _, err := runSQLCmd(t, fix, "scan", fix.root); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runSQLCmd(t, fix, "capabilities")
	if err != nil {
		t.Fatalf("capabilities with no links should not error: %v", err)
	}
	if !strings.Contains(stdout, "no capability owns any recorded query") {
		t.Errorf("expected the empty rollup to explain itself; got:\n%s", stdout)
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
