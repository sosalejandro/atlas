package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/redact"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// leakedPassword is the credential the fixture hides inside stored query
// text. Its literal value is asserted against in both directions: it must be
// found, and it must never appear in anything the command prints.
const leakedPassword = "Kq9Xm2Vz7Pw4Rt6Y"

// newSecurityFixture builds a migrated store holding one clean query, one
// query with an inline credential, and a symbol, then chdirs into it.
func newSecurityFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".atlas", "atlas.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	ctx := context.Background()

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(
		`INSERT INTO symbols (qualified_name, kind, file_path, line, node_class)
		 VALUES ('orders.Repo.List', 'method', 'internal/orders/repo.go', 12, 'declaration')`); err != nil {
		t.Fatalf("seed symbol: %v", err)
	}
	stmts := []struct{ ref, text string }{
		{"clean", "SELECT id, total FROM orders WHERE tenant_id = $1 LIMIT 50"},
		{"leaky", "SELECT * FROM dblink('postgres://reporting:" + leakedPassword +
			"@warehouse.internal:5432/dw', 'SELECT 1')"},
	}
	for _, st := range stmts {
		if _, err := db.Exec(
			`INSERT INTO sql_operations (ref, source, name, file_path, line, symbol_name, kind, sql_text)
			 VALUES (?, 'go', ?, 'internal/orders/repo.go', 12, 'orders.Repo.List', 'select', ?)`,
			st.ref, st.ref, st.text); err != nil {
			t.Fatalf("seed sql_operations %s: %v", st.ref, err)
		}
	}
	t.Chdir(dir)
	return dbPath
}

func runSecurityCmd(t *testing.T, dbPath string, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"security", "--db-path", dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestSecurity_RegisteredOnRoot(t *testing.T) {
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "security" {
			return
		}
	}
	t.Fatal("atlas security is not registered on the root command")
}

func TestSecurity_JSONAnswersWhatAmISending(t *testing.T) {
	dbPath := newSecurityFixture(t)
	stdout, _, err := runSecurityCmd(t, dbPath, "--json")
	if err != nil {
		t.Fatalf("atlas security --json: %v", err)
	}

	var env struct {
		Command string `json:"command"`
		Result  struct {
			Store struct {
				Path          string `json:"path"`
				SizeBytes     int64  `json:"size_bytes"`
				SchemaVersion int    `json:"schema_version"`
				Tables        []struct {
					Name string `json:"name"`
					Rows int64  `json:"rows"`
				} `json:"tables"`
				Unclassified []string `json:"unclassified"`
			} `json:"store"`
			Egress struct {
				NetworkCalls bool   `json:"network_calls"`
				Telemetry    bool   `json:"telemetry"`
				Statement    string `json:"statement"`
			} `json:"egress"`
			Exports           []redact.Export `json:"exports"`
			SourceTextColumns []redact.Column `json:"source_text_columns"`
			Secrets           struct {
				Hits []struct {
					Table  string `json:"table"`
					Column string `json:"column"`
					Kind   string `json:"kind"`
				} `json:"hits"`
				ColumnsRead int `json:"columns_read"`
			} `json:"secrets"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	if env.Command != "security" {
		t.Errorf("command = %q, want %q", env.Command, "security")
	}
	if env.Result.Store.Path != dbPath {
		t.Errorf("store.path = %q, want %q", env.Result.Store.Path, dbPath)
	}
	if env.Result.Store.SizeBytes <= 0 || env.Result.Store.SchemaVersion <= 0 {
		t.Errorf("store size/schema not reported: %+v", env.Result.Store)
	}
	if len(env.Result.Store.Tables) == 0 {
		t.Error("no tables reported")
	}
	if len(env.Result.Store.Unclassified) != 0 {
		t.Errorf("unclassified = %v; the schema drifted from the registry",
			env.Result.Store.Unclassified)
	}
	if env.Result.Egress.NetworkCalls || env.Result.Egress.Telemetry {
		t.Errorf("egress statement claims atlas transmits: %+v", env.Result.Egress)
	}
	if env.Result.Egress.Statement == "" {
		t.Error("egress statement is empty")
	}
	if len(env.Result.Exports) == 0 {
		t.Error("no export surfaces reported")
	}
	if len(env.Result.SourceTextColumns) == 0 {
		t.Error("the report does not name the columns holding verbatim source text")
	}
	if env.Result.Secrets.ColumnsRead == 0 {
		t.Error("columns_read = 0; the sweep read nothing")
	}
	if len(env.Result.Secrets.Hits) != 1 {
		t.Fatalf("hits = %+v, want the one seeded credential", env.Result.Secrets.Hits)
	}
	h := env.Result.Secrets.Hits[0]
	if h.Table != "sql_operations" || h.Column != "sql_text" {
		t.Errorf("hit at %s.%s, want sql_operations.sql_text", h.Table, h.Column)
	}
}

// TestSecurity_NeverPrintsTheSecretItFound is the property that makes the
// command safe to run in CI and paste into a ticket. A report about a
// credential that quotes the credential is a second copy of the leak.
func TestSecurity_NeverPrintsTheSecretItFound(t *testing.T) {
	dbPath := newSecurityFixture(t)
	for _, args := range [][]string{{}, {"--json"}} {
		stdout, stderr, err := runSecurityCmd(t, dbPath, args...)
		if err != nil {
			t.Fatalf("atlas security %v: %v", args, err)
		}
		if strings.Contains(stdout+stderr, leakedPassword) {
			t.Errorf("atlas security %v printed the credential it found:\n%s%s",
				args, stdout, stderr)
		}
		if !strings.Contains(stdout, "warehouse.internal") {
			t.Errorf("atlas security %v does not say where the credential is: %s", args, stdout)
		}
	}
}

func TestSecurity_TextOutputNamesTheUncomfortableFacts(t *testing.T) {
	dbPath := newSecurityFixture(t)
	stdout, _, err := runSecurityCmd(t, dbPath)
	if err != nil {
		t.Fatalf("atlas security: %v", err)
	}
	// A reader must not be able to come away thinking the database holds
	// only structure. These are the specifics the statement turns on.
	for _, want := range []string{
		dbPath,
		"sql_operations.sql_text",
		"cfg_edges.condition",
		"snapshots.index_json",
		"doc comment",
		"nothing leaves this machine",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestSecurity_ExportFilterNarrowsTheCatalogue(t *testing.T) {
	dbPath := newSecurityFixture(t)
	stdout, _, err := runSecurityCmd(t, dbPath, "--export", "report", "--json")
	if err != nil {
		t.Fatalf("atlas security --export report: %v", err)
	}
	var env struct {
		Result struct {
			Exports []redact.Export `json:"exports"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env.Result.Exports) == 0 {
		t.Fatal("--export report matched nothing")
	}
	for _, e := range env.Result.Exports {
		if !strings.HasPrefix(e.Verb, "report ") {
			t.Errorf("--export report returned %q", e.Verb)
		}
	}
}

func TestSecurity_ExportFilterRejectsAVerbThatDoesNotExist(t *testing.T) {
	dbPath := newSecurityFixture(t)
	_, _, err := runSecurityCmd(t, dbPath, "--export", "nosuchverb")
	if err == nil {
		t.Fatal("--export nosuchverb was accepted; a typo must not read as 'discloses nothing'")
	}
}

// ---- redact subcommand -------------------------------------------------

func runSecurityRedactCLI(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"security", "redact", "--db-path", dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), err
}

func storedSQL(t *testing.T, dbPath, ref string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var got string
	if err := db.QueryRow(`SELECT sql_text FROM sql_operations WHERE ref = ?`, ref).Scan(&got); err != nil {
		t.Fatalf("read sql_text: %v", err)
	}
	return got
}

func TestSecurityRedact_DryRunReportsWithoutWriting(t *testing.T) {
	dbPath := newSecurityFixture(t)
	before := storedSQL(t, dbPath, "leaky")

	stdout, err := runSecurityRedactCLI(t, dbPath, "--dry-run")
	if err != nil {
		t.Fatalf("atlas security redact --dry-run: %v", err)
	}
	if strings.Contains(stdout, leakedPassword) {
		t.Errorf("dry run printed the credential:\n%s", stdout)
	}
	if got := storedSQL(t, dbPath, "leaky"); got != before {
		t.Errorf("--dry-run modified the store:\n got %q\nwant %q", got, before)
	}
}

func TestSecurityRedact_RewritesTheStoreAndKeepsTheQueryReadable(t *testing.T) {
	dbPath := newSecurityFixture(t)
	clean := storedSQL(t, dbPath, "clean")

	if _, err := runSecurityRedactCLI(t, dbPath); err != nil {
		t.Fatalf("atlas security redact: %v", err)
	}
	got := storedSQL(t, dbPath, "leaky")
	if strings.Contains(got, leakedPassword) {
		t.Fatalf("the credential survived redaction: %q", got)
	}
	for _, keep := range []string{"dblink", "postgres://", "reporting", "warehouse.internal", "dw"} {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction destroyed %q in the query: %q", keep, got)
		}
	}
	if after := storedSQL(t, dbPath, "clean"); after != clean {
		t.Errorf("redaction touched a query with no secret in it:\n got %q\nwant %q", after, clean)
	}

	// The whole point of running it is that `atlas security` then comes
	// back clean, so the answer to "what am I sending" changes.
	stdout, _, err := runSecurityCmd(t, dbPath, "--json")
	if err != nil {
		t.Fatalf("atlas security after redact: %v", err)
	}
	var env struct {
		Result struct {
			Secrets struct {
				Hits []json.RawMessage `json:"hits"`
			} `json:"secrets"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env.Result.Secrets.Hits) != 0 {
		t.Errorf("secrets remain after redact: %v", env.Result.Secrets.Hits)
	}
}

// ---- the catalogue must describe the real command tree -----------------

// verbsWithNoArtifact are the root verbs that emit no artifact beyond the
// --json envelope and the state database, both of which the catalogue
// already covers as cross-cutting surfaces.
//
// This list exists so that ADDING a command is a decision about disclosure
// rather than an omission. A new verb belongs either in
// redact.Exports() or here, and TestSecurity_EveryVerbIsAccountedFor fails
// until it is in one of them.
var verbsWithNoArtifact = map[string]bool{
	"init": true, "scan": true, "chain": true, "health": true, "sprint": true,
	"diff": true, "contract": true, "diagnose": true, "codebase": true,
	"doctor": true, "affected": true, "trend": true, "sql": true,
	"flow": true, "hotspots": true, "security": true,
	// resolve writes nothing: it scans the working tree in memory and
	// prints package counts, a tier histogram and timings. The one place
	// repository content reaches its output is the first type error of
	// each degraded package -- the same class of disclosure as a scan
	// warning, on the same --json envelope the catalogue already covers.
	"resolve": true,
	// version reports only the build stamps compiled into the binary --
	// no repository content reaches it at all.
	"version": true,
	// scip writes nothing: `scip inspect` reads an index the user names and
	// prints counts. No symbol name, path or source line reaches its output.
	//
	// One field does deserve naming, because it is the kind of thing this
	// list exists to make somebody state out loud: `project_root` is
	// reproduced VERBATIM from the index, and SCIP records it as an
	// absolute file:// URI. On a developer machine that is a home
	// directory, so a --json envelope pasted into a bug report can carry a
	// username. It is kept because it is the field that answers "was this
	// index built for THIS checkout", which is the question the command
	// exists to help with -- but it comes from the index, not from atlas
	// reading the tree, and a caller who cannot disclose it should drop it
	// rather than expect atlas to have removed it.
	"scip": true,
	"help": true, "completion": true,
	// cov has artifact-producing subcommands (cov run), which the catalogue
	// names individually; the verb itself writes only to the store.
	"cov": true,
}

func TestSecurity_EveryVerbIsAccountedFor(t *testing.T) {
	catalogued := map[string]bool{}
	for _, e := range redact.Exports() {
		if e.Verb == "" {
			continue
		}
		catalogued[strings.Fields(e.Verb)[0]] = true
	}
	for _, c := range NewRootCmd().Commands() {
		name := c.Name()
		if catalogued[name] || verbsWithNoArtifact[name] {
			continue
		}
		t.Errorf("`atlas %s` is in neither redact.Exports() nor verbsWithNoArtifact; "+
			"say what it discloses before shipping it", name)
	}
}

func TestSecurity_ExportCatalogueNamesRealCommands(t *testing.T) {
	root := NewRootCmd()
	for _, e := range redact.Exports() {
		if e.Verb == "" {
			continue
		}
		path := strings.Fields(e.Verb)
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Errorf("export %q names verb %q, which does not resolve: %v", e.Surface, e.Verb, err)
			continue
		}
		if !sameVerbPath(cmd, path) {
			t.Errorf("export %q names verb %q but cobra resolved to %q",
				e.Surface, e.Verb, cmd.CommandPath())
		}
	}
}

// sameVerbPath guards against cobra's Find falling back to a parent when the
// leaf does not exist -- `root.Find([]string{"report","sarrif"})` returns the
// `report` command and no error, which would let a typo pass the check above.
func sameVerbPath(cmd *cobra.Command, path []string) bool {
	return cmd.CommandPath() == "atlas "+strings.Join(path, " ")
}

// ---- redaction happens at ingest, not only in a later sweep ------------

// TestSecurity_IngestRedactsBeforeTheCredentialReachesTheStore is the check
// on issue #131's second deliverable.
//
// packages/redact used to be read-only in practice: nothing outside
// `atlas security` called it, so a hardcoded connection string landed in
// sql_operations.sql_text verbatim and stayed there until somebody remembered
// to run `atlas security redact`. The write path now runs the value through
// redact.Field, so the store never holds it in the first place, and
// `atlas security` on a freshly ingested store comes back clean rather than
// reporting the leak atlas itself just wrote.
func TestSecurity_IngestRedactsBeforeTheCredentialReachesTheStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "atlas.db")
	ctx := context.Background()

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	leaky := "SELECT * FROM dblink('postgres://reporting:" + leakedPassword +
		"@warehouse.internal:5432/dw', 'SELECT 1')"
	err = s.SQLOps().Replace(ctx, []store.SQLOperationRecord{
		{
			Ref: "orders.list", Source: "go", Name: "list",
			FilePath: "internal/orders/repo.go", Line: 12,
			SymbolName: "orders.Repo.List", Kind: "select",
			Resolved: true, SQLText: leaky,
		},
		{
			Ref: "orders.count", Source: "go", Name: "count",
			FilePath: "internal/orders/repo.go", Line: 40,
			SymbolName: "orders.Repo.Count", Kind: "select",
			Resolved: true, SQLText: "SELECT count(*) FROM orders WHERE tenant_id = $1",
		},
	})
	if err != nil {
		t.Fatalf("SQLOps().Replace: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
	t.Chdir(dir)

	stored := storedSQL(t, dbPath, "orders.list")
	if strings.Contains(stored, leakedPassword) {
		t.Fatalf("the credential reached the store verbatim: %q", stored)
	}
	if !strings.Contains(stored, "[redacted:connection-string:") {
		t.Errorf("the stored query carries no redaction placeholder: %q", stored)
	}
	// The query has to remain the query: `atlas sql` analyses this text.
	for _, keep := range []string{"dblink", "postgres://", "reporting", "warehouse.internal"} {
		if !strings.Contains(stored, keep) {
			t.Errorf("ingest-time redaction destroyed %q: %q", keep, stored)
		}
	}
	if clean := storedSQL(t, dbPath, "orders.count"); !strings.Contains(clean, "count(*)") {
		t.Errorf("a query with no secret in it was rewritten: %q", clean)
	}

	// And the whole point: the answer to "what am I sending" is now clean.
	stdout, _, err := runSecurityCmd(t, dbPath, "--json")
	if err != nil {
		t.Fatalf("atlas security --json: %v", err)
	}
	var env struct {
		Result struct {
			Secrets struct {
				Hits []json.RawMessage `json:"hits"`
			} `json:"secrets"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env.Result.Secrets.Hits) != 0 {
		t.Errorf("atlas security still reports %d hit(s) after an ingest that should have "+
			"redacted them: %v", len(env.Result.Secrets.Hits), env.Result.Secrets.Hits)
	}
}

// TestSecurity_IngestRecordsWhereItRedacted: a redaction the operator cannot
// locate is not much better than one that never happened. The credential is
// still in the source file, and rotating it is the one fix atlas cannot
// perform for them.
func TestSecurity_IngestRecordsWhereItRedacted(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "atlas.db")
	ctx := context.Background()

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	idx := &codeindex.Index{
		Annotations: []shared.Annotation{{
			Kind:     shared.AnnOwner,
			Raw:      `aws_secret_access_key = "` + leakedPassword + `"`,
			Position: shared.FilePosition{Path: "internal/orders/repo.go", Line: 7},
		}},
	}
	stats, err := s.Ingest(ctx, idx)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(stats.Redactions) != 1 {
		t.Fatalf("Redactions = %+v, want the one seeded credential", stats.Redactions)
	}
	r := stats.Redactions[0]
	if r.Table != "annotations" || r.Column != "value" {
		t.Errorf("redaction reported at %s.%s, want annotations.value", r.Table, r.Column)
	}
	if r.Where != "internal/orders/repo.go:7" {
		t.Errorf("Where = %q, want the file and line the credential is still sitting in", r.Where)
	}
	if r.Digest == "" || r.Kind == "" {
		t.Errorf("redaction reports neither rule nor digest: %+v", r)
	}
	// The record must not be a second copy of the leak.
	if strings.Contains(fmt.Sprintf("%+v", stats.Redactions), leakedPassword) {
		t.Errorf("the redaction record quotes the credential: %+v", stats.Redactions)
	}
	var storedValue string
	if err := queryOne(t, dbPath, `SELECT value FROM annotations WHERE line = 7`, &storedValue); err != nil {
		t.Fatalf("read annotation back: %v", err)
	}
	if strings.Contains(storedValue, leakedPassword) {
		t.Errorf("the credential reached annotations.value: %q", storedValue)
	}
}

// queryOne reads a single scalar out of the store on a fresh connection.
func queryOne(t *testing.T, dbPath, query string, into any) error {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.QueryRow(query).Scan(into)
}

// ---- `atlas security` must not modify the database it describes --------

// TestSecurity_DoesNotCreateAStoreThatIsNotThere.
//
// openStateDB used to go through store.Open, which creates the file and runs
// every pending migration before the command reads a row. Asking what a
// database contains is not permission to bring one into existence, and for a
// security review a store that appeared because someone inspected it is a
// worse answer than an error.
func TestSecurity_DoesNotCreateAStoreThatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	absent := filepath.Join(dir, "absent.db")

	if _, _, err := runSecurityCmd(t, absent); err == nil {
		t.Fatal("atlas security succeeded against a database that does not exist")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Errorf("atlas security created %s just by being asked what it holds", absent)
	}
}

// TestSecurity_DoesNotMigrateTheDatabaseItInspects.
//
// The fixture is rewound to schema version 1 with its tables left in place,
// standing in for a store captured from a machine running an older atlas.
// `atlas security` has to report the version it finds. Migrating it would
// mean the artifact you audited is not the artifact you now have -- and on a
// copy taken as evidence, that is the whole ballgame.
func TestSecurity_DoesNotMigrateTheDatabaseItInspects(t *testing.T) {
	dbPath := newSecurityFixture(t)
	rewindSchemaVersion(t, dbPath, 1)

	stdout, _, err := runSecurityCmd(t, dbPath, "--json")
	if err != nil {
		t.Fatalf("atlas security --json: %v", err)
	}
	var env struct {
		Result struct {
			Store struct {
				SchemaVersion int `json:"schema_version"`
			} `json:"store"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Result.Store.SchemaVersion != 1 {
		t.Errorf("reported schema_version = %d, want the 1 it was handed",
			env.Result.Store.SchemaVersion)
	}
	if got := storedSchemaVersion(t, dbPath); got != 1 {
		t.Errorf("atlas security migrated the store it was asked to describe: "+
			"schema_migrations is now %d, was 1", got)
	}
}

// rewindSchemaVersion rewrites schema_migrations to claim an older version
// without touching the tables, which is what a store written by an older
// atlas looks like to a newer binary.
func rewindSchemaVersion(t *testing.T, dbPath string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("clear schema_migrations: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_migrations (version, dirty) VALUES (?, 0)`, version); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
}

func storedSchemaVersion(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return v
}

// TestSecurityRedact_DryRunReportsTheCountItWouldChange.
//
// printSecurityRedact prints Sweep.ValuesRewritten, which Sweep used to set
// only inside the apply branch. A dry run therefore printed "0 distinct
// value(s)" over a store holding a credential: it told the operator that
// running the command for real would change nothing.
func TestSecurityRedact_DryRunReportsTheCountItWouldChange(t *testing.T) {
	dbPath := newSecurityFixture(t)

	stdout, err := runSecurityRedactCLI(t, dbPath, "--dry-run")
	if err != nil {
		t.Fatalf("atlas security redact --dry-run: %v", err)
	}
	if !strings.Contains(stdout, "1 distinct value(s) would be replaced") {
		t.Errorf("the dry run does not report what it would change:\n%s", stdout)
	}
	if got := storedSQL(t, dbPath, "leaky"); !strings.Contains(got, leakedPassword) {
		t.Errorf("--dry-run wrote to the database: %q", got)
	}
}
