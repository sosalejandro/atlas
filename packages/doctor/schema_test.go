package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// rawExec opens a normal read-write handle on the fixture DB so a test
// can forge the states golang-migrate would otherwise refuse to create.
func (f *fixture) rawExec(t *testing.T, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", f.dbPath))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("raw exec %q: %v", query, err)
	}
}

// A store this binary just migrated is by definition at the version this
// binary expects.
func TestSchema_FreshStore_OK(t *testing.T) {
	f := newFixture(t)

	res := runCheck(t, schemaVersion{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
	applied, _ := res.Details["applied"].(int)
	expected, _ := res.Details["expected"].(int)
	if applied == 0 || applied != expected {
		t.Errorf("applied = %d, expected = %d; want equal and non-zero", applied, expected)
	}
}

// A half-applied migration leaves the schema in a state golang-migrate
// will not advance past. Every other atlas command dies on open, so this
// is the check that has to survive without a Store.
func TestSchema_DirtyFlag_FailsWithoutAStore(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	f.rawExec(t, `UPDATE schema_migrations SET dirty = 1`)

	env := f.env(t)
	env.Store = nil
	env.StoreErr = fmt.Errorf("store: migrate: dirty")
	res := runCheck(t, schemaVersion{}, env)

	assertSeverity(t, res, SeverityFail)
	if res.Details["dirty"] != true {
		t.Errorf("dirty = %v, want true", res.Details["dirty"])
	}
	if res.Remediation == "" {
		t.Error("a dirty schema must name the way out")
	}
}

// A store written by a newer atlas is the dangerous direction: Up() is a
// no-op so the open succeeds, and this binary then reads a schema it was
// never built against.
func TestSchema_StoreAheadOfBinary_Fails(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	f.rawExec(t, `UPDATE schema_migrations SET version = version + 5`)

	env := f.env(t)
	env.Store = nil
	res := runCheck(t, schemaVersion{}, env)

	assertSeverity(t, res, SeverityFail)
}

// No database file at all, and no store either: nothing in atlas can
// read this path. Reporting "cannot check" would let an absent state
// file exit zero, so this is a failure with a way out.
func TestSchema_MissingDatabase_Fails(t *testing.T) {
	f := newFixture(t)
	env := (&Env{DBPath: filepath.Join(f.root, "nope", "atlas.db"), Root: f.root}).withDefaults()
	env.probeErr = env.openProbe()
	defer env.closeProbe()

	res := runCheck(t, schemaVersion{}, env)

	assertSeverity(t, res, SeverityFail)
	if res.Remediation == "" {
		t.Error("an unreadable state database must name the way out")
	}
}

// The mirror case: atlas opened the store fine and only doctor's own
// second handle failed. That is a limit of the diagnostic, not a finding
// about the repo, so it must not red-light a build.
func TestSchema_ProbeFailedButStoreOpened_NotApplicable(t *testing.T) {
	f := newFixture(t)
	env := (&Env{
		Store:  f.store,
		DBPath: filepath.Join(f.root, "nope", "atlas.db"),
		Root:   f.root,
	}).withDefaults()
	env.probeErr = env.openProbe()
	defer env.closeProbe()

	res := runCheck(t, schemaVersion{}, env)

	assertSeverity(t, res, SeverityNotApplicable)
}

// The expectation is derived from what this binary's own migrations
// produce, never from a hand-maintained constant -- a constant would be
// one more thing that goes stale silently, which is the bug class doctor
// exists to close.
func TestExpectedSchemaVersion_MatchesAFreshStore(t *testing.T) {
	v, err := expectedSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("expectedSchemaVersion: %v", err)
	}
	if v <= 0 {
		t.Fatalf("expectedSchemaVersion = %d, want > 0", v)
	}

	f := newFixture(t)
	applied, err := f.store.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if applied != v {
		t.Errorf("fresh store is at %d, expectation says %d", applied, v)
	}
}
