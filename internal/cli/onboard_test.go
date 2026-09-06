package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/onboard"
	"github.com/sosalejandro/atlas/packages/store"
)

// onboardFixture is a real, scannable Go project with no annotations in it.
// The zero-annotation state is the entire subject of this command, so the
// fixture must not be seeded with any.
type onboardFixture struct {
	root   string
	dbPath string
}

func newOnboardFixture(t *testing.T) *onboardFixture {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	write := func(rel, body string) {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.25\n")
	write("internal/orders/place.go", `package orders

import "database/sql"

// Place records an order.
func Place(db *sql.DB, id string) error {
	_, err := db.Exec("INSERT INTO orders (id) VALUES (?)", id)
	return err
}

// List returns every order.
func List(db *sql.DB) error {
	_, err := db.Query("SELECT id FROM orders")
	return err
}
`)
	write("internal/billing/settle.go", `package billing

import "database/sql"

// Settle closes out an order.
func Settle(db *sql.DB, id string) error {
	_, err := db.Exec("UPDATE orders SET settled = 1 WHERE id = ?", id)
	return err
}
`)
	return &onboardFixture{root: dir, dbPath: filepath.Join(dir, ".atlas", "atlas.db")}
}

func execOnboard(t *testing.T, fix *onboardFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	full := append([]string{"onboard"}, args...)
	full = append(full, "--db-path", fix.dbPath, "--root", fix.root)
	root.SetArgs(full)
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestOnboard_RegisteredAndFlagged(t *testing.T) {
	var found bool
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "onboard" {
			found = true
			for _, f := range []string{"root", "skip-sql", "skip-churn", "top"} {
				if c.Flags().Lookup(f) == nil {
					t.Errorf("atlas onboard is missing --%s", f)
				}
			}
		}
	}
	if !found {
		t.Fatal("atlas onboard is not registered on the root command")
	}
}

// The load-bearing test. A first run on a repository with no annotations
// must produce a map and MUST NOT put any of it in the features table --
// the registry's whole value is that a human wrote every row in it.
func TestOnboard_WritesNoFeatureRows(t *testing.T) {
	fix := newOnboardFixture(t)
	stdout, stderr, err := execOnboard(t, fix)
	if err != nil {
		t.Fatalf("onboard: %v\nstderr:\n%s", err, stderr)
	}

	s, err := store.Open(context.Background(), fix.dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()
	feats, err := s.Features().List(context.Background(), store.FeatureFilter{})
	if err != nil {
		t.Fatalf("list features: %v", err)
	}
	if len(feats) != 0 {
		t.Fatalf("onboard wrote %d rows into the features table: %+v", len(feats), feats)
	}
	if !strings.Contains(stdout, onboard.ProvisionalPrefix) {
		t.Errorf("output does not namespace its proposals:\n%s", stdout)
	}
	if !strings.Contains(strings.ToUpper(stdout), "PROVISIONAL") {
		t.Errorf("output never says the map is provisional:\n%s", stdout)
	}
}

// The run has to end with something to read and an honest account of what
// it could not see -- that pair is the whole acceptance criterion.
func TestOnboard_ReportHasFindingsLimitsAndCISnippet(t *testing.T) {
	fix := newOnboardFixture(t)
	stdout, stderr, err := execOnboard(t, fix)
	if err != nil {
		t.Fatalf("onboard: %v\nstderr:\n%s", err, stderr)
	}
	for _, want := range []string{
		"WHAT ATLAS CANNOT SEE",
		"atlas health", // the CI snippet turns the map into a gate
		"atlas onboard promote",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output is missing %q:\n%s", want, stdout)
		}
	}
	// Tables written by two different capabilities is the finding this
	// fixture is built to produce: orders is written from both packages.
	if !strings.Contains(stdout, "orders") {
		t.Errorf("output never mentions the shared table:\n%s", stdout)
	}
}

// With --skip-sql the SQL pass never runs, so there is no operation count
// and no unresolved count. Printing "0 operations, 0 unresolved   0.0s" put
// an unknown on the header wearing a measurement's clothes -- complete with
// the time it supposedly took to find it out -- and a reader has no way to
// tell that line from a project that genuinely has no queries.
func TestOnboard_SkipSQLReportsSkippedRatherThanAMeasuredZero(t *testing.T) {
	fix := newOnboardFixture(t)
	stdout, stderr, err := execOnboard(t, fix, "--skip-sql")
	if err != nil {
		t.Fatalf("onboard --skip-sql: %v\nstderr:\n%s", err, stderr)
	}
	sqlLine := ""
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "sql ") {
			sqlLine = line
			break
		}
	}
	if sqlLine == "" {
		t.Fatalf("no sql line in the header:\n%s", stdout)
	}
	if !strings.Contains(sqlLine, "skipped") {
		t.Errorf("sql line does not say the pass was skipped: %q", sqlLine)
	}
	for _, unwanted := range []string{"0 operations", "0 unresolved", "0.0s"} {
		if strings.Contains(sqlLine, unwanted) {
			t.Errorf("sql line prints %q for a pass that never ran: %q", unwanted, sqlLine)
		}
	}

	// The same distinction has to survive into the JSON, where a consumer
	// reading stats.sql_operations otherwise cannot tell "no queries" from
	// "nobody looked".
	jsonOut, stderr, err := execOnboard(t, fix, "--skip-sql", "--json")
	if err != nil {
		t.Fatalf("onboard --skip-sql --json: %v\nstderr:\n%s", err, stderr)
	}
	var env struct {
		Result struct {
			SQLScanned bool `json:"sql_scanned"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, jsonOut)
	}
	if env.Result.SQLScanned {
		t.Error("JSON reports sql_scanned=true for a run that skipped the SQL pass")
	}

	// And a run that DID scan still prints its counts.
	scanned, stderr, err := execOnboard(t, fix)
	if err != nil {
		t.Fatalf("onboard: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(scanned, "operations,") {
		t.Errorf("a run that scanned SQL printed no operation count:\n%s", scanned)
	}
}

func TestOnboard_JSONEnvelopeIsLabelled(t *testing.T) {
	fix := newOnboardFixture(t)
	stdout, stderr, err := execOnboard(t, fix, "--json")
	if err != nil {
		t.Fatalf("onboard --json: %v\nstderr:\n%s", err, stderr)
	}
	var env struct {
		Command string `json:"command"`
		Result  struct {
			Provisional  bool `json:"provisional"`
			Capabilities []struct {
				ID          string `json:"id"`
				Provisional bool   `json:"provisional"`
			} `json:"provisional_capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	if env.Command != "onboard" {
		t.Errorf("command = %q, want onboard", env.Command)
	}
	if !env.Result.Provisional {
		t.Error("the JSON result does not declare itself provisional")
	}
	if len(env.Result.Capabilities) == 0 {
		t.Fatal("no capabilities in the JSON result")
	}
	for _, c := range env.Result.Capabilities {
		if !c.Provisional {
			t.Errorf("capability %s is not flagged provisional in JSON", c.ID)
		}
	}
}

func TestOnboard_PersistsMapOutsideTheRegistry(t *testing.T) {
	fix := newOnboardFixture(t)
	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	doc, err := onboard.Load(fix.root)
	if err != nil {
		t.Fatalf("load provisional map: %v", err)
	}
	if !doc.Provisional || len(doc.Capabilities) == 0 {
		t.Fatalf("provisional map is empty or unlabelled: %+v", doc.Stats)
	}
}

// Promotion is the explicit user action, and its default is a dry run:
// nothing edits a user's source because they typed a verb.
func TestOnboardPromote_DryRunByDefaultThenApplies(t *testing.T) {
	fix := newOnboardFixture(t)
	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	src := filepath.Join(fix.root, "internal", "orders", "place.go")
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	runPromote := func(args ...string) (string, error) {
		root := NewRootCmd()
		loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
		flags = globalFlags{DBPath: fix.dbPath}
		var out, errBuf bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errBuf)
		root.SetArgs(append([]string{"onboard", "promote", "--root", fix.root, "--db-path", fix.dbPath}, args...))
		// Execute first: reading the buffers in the return expression would
		// snapshot them before the command had written anything.
		execErr := root.ExecuteContext(context.Background())
		return out.String() + errBuf.String(), execErr
	}

	out, err := runPromote("--all")
	if err != nil {
		t.Fatalf("promote --all: %v\n%s", err, out)
	}
	if !strings.Contains(out, "@atlas:feature") {
		t.Errorf("dry run did not show the annotations it would write:\n%s", out)
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(before, after) {
		t.Fatalf("promote edited source without --apply:\n%s", after)
	}

	if out, err = runPromote("--all", "--apply"); err != nil {
		t.Fatalf("promote --all --apply: %v\n%s", err, out)
	}
	applied, _ := os.ReadFile(src)
	if !strings.Contains(string(applied), "@atlas:feature") {
		t.Fatalf("promote --apply wrote no annotation:\n%s", applied)
	}

	// The promoted annotation must reach the registry through the normal
	// ingest -- that is the claim the whole design rests on.
	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("re-scan: %v\n%s", err, stderr)
	}
	s, err := store.Open(context.Background(), fix.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	feats, err := s.Features().List(context.Background(), store.FeatureFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feats) == 0 {
		t.Fatal("promoted annotations did not materialise any feature on re-scan")
	}
}

// `promote --all` regularly lands two annotations in one file, and that is
// where this used to corrupt the user's source: each insertion shifts every
// line below it, and the second annotation was written against line numbers
// computed before the first edit. One line off is enough to detach an
// annotation from its declaration -- the scanner then reads nothing, and
// the user is left with a stray comment in a file they did not expect to be
// edited at all.
func TestOnboardPromote_TwoAnnotationsInOneFileBothLandOnTheirDeclaration(t *testing.T) {
	fix := newOnboardFixture(t)
	// Two capabilities anchored in one file: a test-name cluster claims
	// Ship, and the directory fallback claims Quote beside it. No doc
	// comments and single blank lines between declarations, so a one-line
	// slip is unambiguous rather than merely untidy.
	shipping := filepath.Join(fix.root, "internal", "shipping")
	if err := os.MkdirAll(shipping, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(shipping, "ship.go")
	if err := os.WriteFile(src, []byte(`package shipping

func Ship(id string) error {
	return nil
}

func Quote(id string) (int, error) {
	return 0, nil
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shipping, "ship_test.go"), []byte(`package shipping

import "testing"

func TestShipOnce(t *testing.T)  { _ = Ship }
func TestShipTwice(t *testing.T) { _ = Ship }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	rootCmd := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{
		"onboard", "promote", "--root", fix.root, "--db-path", fix.dbPath, "--all", "--apply",
	})
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("promote --all --apply: %v\n%s", err, out.String())
	}

	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	annotated := 0
	for i, line := range lines {
		if !strings.Contains(line, "@atlas:feature") {
			continue
		}
		annotated++
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "func ") {
			next := "<end of file>"
			if i+1 < len(lines) {
				next = lines[i+1]
			}
			t.Errorf("annotation %q is not attached to a declaration; the next line is %q\nfile:\n%s",
				strings.TrimSpace(line), next, body)
		}
	}
	if annotated != 2 {
		t.Errorf("ship.go carries %d annotations, want 2 (one per capability anchored in it)\nfile:\n%s",
			annotated, body)
	}
}

// Promoting an id that does not exist must fail loudly rather than silently
// do nothing -- a typo'd id in a script is otherwise a green no-op.
func TestOnboardPromote_UnknownIDFails(t *testing.T) {
	fix := newOnboardFixture(t)
	if _, stderr, err := execOnboard(t, fix); err != nil {
		t.Fatalf("onboard: %v\n%s", err, stderr)
	}
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"onboard", "promote", "--root", fix.root, "--db-path", fix.dbPath, "--id", "no.such-capability"})
	if err := root.ExecuteContext(context.Background()); err == nil {
		t.Fatal("promoting an unknown id succeeded")
	}
}

// Running promote before onboard must say so, not report an empty map.
func TestOnboardPromote_WithoutAMapExplainsItself(t *testing.T) {
	fix := newOnboardFixture(t)
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"onboard", "promote", "--root", fix.root, "--db-path", fix.dbPath, "--all"})
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("promote succeeded with no provisional map")
	}
	if !strings.Contains(err.Error(), "atlas onboard") {
		t.Errorf("error does not point at the command that fixes it: %v", err)
	}
}
