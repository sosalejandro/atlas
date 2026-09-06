package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/store"
)

// doctorFixture is a throwaway repo root plus its state DB, driven
// through the real cobra tree the way a user would.
type doctorFixture struct {
	root   string
	dbPath string
}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".atlas", "atlas.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	return &doctorFixture{root: dir, dbPath: dbPath}
}

// seedCleanIndex writes one Go file and the file_hashes row that matches
// it: the state a doctor should call healthy.
func (f *doctorFixture) seedCleanIndex(t *testing.T) {
	t.Helper()
	const body = "package pkg\n\nfunc A() {}\n"
	abs := filepath.Join(f.root, "pkg", "a.go")
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	sum := sha256.Sum256([]byte(body))

	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	err = s.FileHashes().Upsert(ctx, store.FileHashRow{
		FilePath:    "pkg/a.go",
		ContentHash: hex.EncodeToString(sum[:]),
		ModTime:     time.Now().UTC(),
		LastScanned: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("upsert hash: %v", err)
	}
}

// breakIndex edits the indexed file without refreshing its hash -- the
// silent staleness doctor exists to surface.
func (f *doctorFixture) breakIndex(t *testing.T) {
	t.Helper()
	abs := filepath.Join(f.root, "pkg", "a.go")
	if err := os.WriteFile(abs, []byte("package pkg\n\nfunc A() { println(1) }\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.go: %v", err)
	}
}

func runDoctorCmd(t *testing.T, f *doctorFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{
		"doctor", "--db-path", f.dbPath, "--root", f.root,
	}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestDoctor_FlagsWired(t *testing.T) {
	c := newDoctorCmd()
	for _, name := range []string{"fail-on", "root"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("atlas doctor is missing --%s", name)
		}
	}
}

func TestDoctor_RegisteredOnRoot(t *testing.T) {
	var found bool
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "doctor" {
			found = true
		}
	}
	if !found {
		t.Error("atlas doctor is not registered on the root command")
	}
}

// A healthy store exits zero and still prints every check: the user has
// to be able to tell "checked and fine" from "never looked".
func TestDoctor_HealthyStoreReportsEveryCheckAndExitsZero(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)

	stdout, stderr, err := runDoctorCmd(t, f)
	if err != nil {
		t.Fatalf("doctor: %v\nstderr:\n%s", err, stderr)
	}
	for _, name := range []string{
		"index.freshness", "coverage.freshness", "coverage.attribution",
		"feature.linkage", "store.schema",
	} {
		if !strings.Contains(stdout, name) {
			t.Errorf("check %q missing from output:\n%s", name, stdout)
		}
	}
}

// The CI contract: a fail-severity check must make the process exit
// non-zero, or nobody can gate on this.
func TestDoctor_FailSeverityExitsNonZero(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)
	f.breakIndex(t)

	stdout, _, err := runDoctorCmd(t, f)
	if err == nil {
		t.Fatalf("doctor with a stale index returned nil error; output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fail") {
		t.Errorf("expected a fail verdict in the output:\n%s", stdout)
	}
}

// --fail-on warn tightens the gate without changing the check set.
func TestDoctor_FailOnWarnTightensTheGate(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)
	// A second Go file nothing has indexed: warn, not fail.
	if err := os.WriteFile(filepath.Join(f.root, "pkg", "b.go"),
		[]byte("package pkg\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}

	if _, _, err := runDoctorCmd(t, f); err != nil {
		t.Fatalf("default --fail-on=fail must not trip on a warn: %v", err)
	}
	if _, _, err := runDoctorCmd(t, f, "--fail-on", "warn"); err == nil {
		t.Error("--fail-on warn did not trip on a warn-severity check")
	}
}

func TestDoctor_RejectsUnknownFailOn(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)

	if _, _, err := runDoctorCmd(t, f, "--fail-on", "banana"); err == nil {
		t.Error("--fail-on banana was accepted")
	}
	// "ok" as a gate would fail every healthy repo, so it is not a
	// severity the flag accepts.
	if _, _, err := runDoctorCmd(t, f, "--fail-on", "ok"); err == nil {
		t.Error("--fail-on ok was accepted")
	}
}

// The JSON envelope carries EVERY check, not just the failures -- a
// consumer that only received failures could not distinguish a clean run
// from a run that never happened.
func TestDoctor_JSONEnvelopeCarriesEveryCheck(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)

	stdout, _, err := runDoctorCmd(t, f, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Worst  string `json:"worst"`
			Counts struct {
				OK            int `json:"ok"`
				Warn          int `json:"warn"`
				Fail          int `json:"fail"`
				NotApplicable int `json:"n/a"`
			} `json:"counts"`
			Checks []struct {
				Name        string `json:"name"`
				Examines    string `json:"examines"`
				Severity    string `json:"severity"`
				Finding     string `json:"finding"`
				Remediation string `json:"remediation"`
			} `json:"checks"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\nstdout:\n%s", err, stdout)
	}
	if env.SchemaVersion != "v1" {
		t.Errorf("schema_version = %q, want v1", env.SchemaVersion)
	}
	if env.Command != "doctor" {
		t.Errorf("command = %q, want doctor", env.Command)
	}
	if len(env.Result.Checks) != 5 {
		t.Fatalf("got %d checks, want 5", len(env.Result.Checks))
	}
	total := env.Result.Counts.OK + env.Result.Counts.Warn +
		env.Result.Counts.Fail + env.Result.Counts.NotApplicable
	if total != len(env.Result.Checks) {
		t.Errorf("counts sum to %d but %d checks were reported", total, len(env.Result.Checks))
	}
	for _, c := range env.Result.Checks {
		if c.Name == "" || c.Examines == "" || c.Severity == "" || c.Finding == "" {
			t.Errorf("check %+v is under-populated", c)
		}
		if c.Severity != "ok" && c.Remediation == "" {
			t.Errorf("check %q is %q with no remediation", c.Name, c.Severity)
		}
	}
}

// Nothing has been ingested here beyond an index. The coverage checks
// must say "not applicable" with a reason, never "ok" -- an empty store
// reported as healthy is the exact lie doctor exists to prevent.
func TestDoctor_UningestedCoverageIsNotApplicableNotOK(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)

	stdout, _, err := runDoctorCmd(t, f, "--json")
	if err != nil {
		t.Fatalf("doctor --json: %v", err)
	}
	var env struct {
		Result struct {
			Checks []struct {
				Name     string `json:"name"`
				Severity string `json:"severity"`
				Finding  string `json:"finding"`
			} `json:"checks"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	for _, c := range env.Result.Checks {
		if !strings.HasPrefix(c.Name, "coverage.") {
			continue
		}
		if c.Severity != "n/a" {
			t.Errorf("%s severity = %q with no coverage ingested, want n/a", c.Name, c.Severity)
		}
		if c.Finding == "" {
			t.Errorf("%s reported n/a with no reason", c.Name)
		}
	}
}

// The most useful moment for doctor is when the store will not open at
// all -- that is when every other atlas command dies in migrate output.
func TestDoctor_StoreThatWillNotOpenStillReports(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedCleanIndex(t)
	// A file that is not a SQLite database at all.
	if err := os.WriteFile(f.dbPath, []byte("not a database"), 0o644); err != nil {
		t.Fatalf("clobber db: %v", err)
	}

	stdout, _, err := runDoctorCmd(t, f)
	if err == nil {
		t.Error("an unopenable store must exit non-zero")
	}
	if !strings.Contains(stdout, "store.schema") {
		t.Errorf("the schema check must still be reported:\n%s", stdout)
	}
	if !strings.Contains(stdout, "index.freshness") {
		t.Errorf("every check must still be listed:\n%s", stdout)
	}
}

// The stated contract is that a diagnostic must not conjure the state it
// was asked to inspect -- and "the file exists" is not the same thing as
// "the file is an atlas store". A 0-byte placeholder (an interrupted
// init, a stray `touch`) passed the old os.Stat guard, went straight to
// store.Open, and had the embedded migrations written into it by the
// command asked whether it was initialised.
func TestDoctor_PlaceholderFileIsNotMigratedIntoAStore(t *testing.T) {
	f := newDoctorFixture(t)
	if err := os.WriteFile(f.dbPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}

	stdout, _, err := runDoctorCmd(t, f)
	if err == nil {
		t.Errorf("a placeholder that is not a store must exit non-zero; output:\n%s", stdout)
	}
	info, statErr := os.Stat(f.dbPath)
	if statErr != nil {
		t.Fatalf("stat after doctor: %v", statErr)
	}
	if info.Size() != 0 {
		t.Errorf("doctor migrated the file it was asked to inspect: it is now %d bytes", info.Size())
	}
	if !strings.Contains(stdout, "store.schema") {
		t.Errorf("the schema check must still report on it:\n%s", stdout)
	}
}

// A repo that has never been scanned has no state database. doctor must
// report that and exit non-zero -- and must NOT create the database on
// the way, or it would report the repo as initialised for the sole
// reason that someone asked whether it was.
func TestDoctor_NeverInitializedDoesNotCreateTheStore(t *testing.T) {
	f := newDoctorFixture(t)

	stdout, _, err := runDoctorCmd(t, f)
	if err == nil {
		t.Errorf("a repo with no state database must exit non-zero; output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "never been scanned") {
		t.Errorf("expected the uninitialised diagnosis:\n%s", stdout)
	}
	if _, statErr := os.Stat(f.dbPath); statErr == nil {
		t.Error("doctor created the state database it was asked to inspect")
	}
}
