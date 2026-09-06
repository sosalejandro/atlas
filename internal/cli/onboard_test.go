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
		"atlas audit", // the CI snippet turns the map into a gate
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
