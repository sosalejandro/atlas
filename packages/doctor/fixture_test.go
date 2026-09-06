package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// fixture is a throwaway repo root + opened state DB. Every check test
// builds one, writes the working-tree files it cares about, seeds the
// store rows it cares about, and runs a single Check against it.
type fixture struct {
	root   string
	dbPath string
	store  *store.Store
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, ".atlas", "atlas.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &fixture{
		root:   root,
		dbPath: dbPath,
		store:  s,
		now:    time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
}

// env builds the Env a check under test receives, with the probe opened
// the way doctor.Run would open it.
func (f *fixture) env(t *testing.T) *Env {
	t.Helper()
	e := (&Env{
		Store:  f.store,
		DBPath: f.dbPath,
		Root:   f.root,
		Now:    func() time.Time { return f.now },
	}).withDefaults()
	e.probeErr = e.openProbe()
	t.Cleanup(e.closeProbe)
	return e
}

// writeFile creates relPath under the fixture root and returns its
// SHA-256, which is what a file_hashes row would record.
func (f *fixture) writeFile(t *testing.T, relPath, content string) string {
	t.Helper()
	abs := filepath.Join(f.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// indexFile writes the file AND records a matching file_hashes row: the
// "atlas has seen this exact content" state.
func (f *fixture) indexFile(t *testing.T, relPath, content string) {
	t.Helper()
	hash := f.writeFile(t, relPath, content)
	f.recordHash(t, relPath, hash, f.now.Add(-time.Hour))
}

func (f *fixture) recordHash(t *testing.T, relPath, hash string, scannedAt time.Time) {
	t.Helper()
	f.recordHashAt(t, relPath, hash, scannedAt, scannedAt)
}

// recordHashAt records a hash row with the file's mtime and the scan's
// last_scanned set independently. They come apart in exactly the state
// the coverage-freshness check has to get right: a re-scan that read
// nothing new bumps last_scanned on every row and leaves every mtime
// alone.
func (f *fixture) recordHashAt(t *testing.T, relPath, hash string, modTime, lastScanned time.Time) {
	t.Helper()
	err := f.store.FileHashes().Upsert(context.Background(), store.FileHashRow{
		FilePath:    relPath,
		ContentHash: hash,
		ModTime:     modTime,
		LastScanned: lastScanned,
	})
	if err != nil {
		t.Fatalf("record hash %s: %v", relPath, err)
	}
}

func (f *fixture) insertSymbol(t *testing.T, qn shared.SymbolID, relPath string, line int) int64 {
	t.Helper()
	id, err := f.store.Symbols().Insert(context.Background(), store.SymbolRow{
		QualifiedName: qn,
		Kind:          shared.KindFunc,
		FilePath:      relPath,
		Line:          line,
	})
	if err != nil {
		t.Fatalf("insert symbol %s: %v", qn, err)
	}
	return id
}

// runCheck executes one check and fails the test if it errored -- the
// error path is exercised separately, so a test that trips it by
// accident should say so loudly rather than assert on a fail Result.
func runCheck(t *testing.T, c Check, env *Env) Result {
	t.Helper()
	res, err := c.Run(context.Background(), env)
	if err != nil {
		t.Fatalf("%s returned an error: %v", c.Name(), err)
	}
	return res
}

func assertSeverity(t *testing.T, res Result, want Severity) {
	t.Helper()
	if res.Severity != want {
		t.Fatalf("severity = %q (finding %q), want %q", res.Severity, res.Finding, want)
	}
}
