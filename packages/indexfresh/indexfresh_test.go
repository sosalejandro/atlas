package indexfresh_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/indexfresh"
	"github.com/sosalejandro/atlas/packages/store"
)

// newStore returns an open store rooted in a fresh temp dir.
func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "atlas.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// write creates a file under root and returns its sha256, so a test can record
// the hash the scanner WOULD have seen without running the scanner.
func write(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func record(t *testing.T, s *store.Store, rel, hash string) {
	t.Helper()
	if err := s.FileHashes().Upsert(context.Background(), store.FileHashRow{
		FilePath: rel, ContentHash: hash,
	}); err != nil {
		t.Fatalf("FileHashes.Upsert(%s): %v", rel, err)
	}
}

// The case the package exists for: a file edited since the scan. Its stored
// spans describe a version that no longer exists, so joining diff line numbers
// against them silently attributes to the wrong symbol.
func TestClassify_EditedSinceScanIsStale(t *testing.T) {
	s, root := newStore(t)
	scanned := write(t, root, "billing/order.go", "package billing\n\nfunc Pay() {}\n")
	record(t, s, "billing/order.go", scanned)

	// Same path, different content: an edit landed after the scan.
	write(t, root, "billing/order.go", "package billing\n\n// a new comment\nfunc Pay() {}\n")

	rep, err := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"billing/order.go"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got := rep.States["billing/order.go"]; got != indexfresh.StateStale {
		t.Errorf("state = %q, want stale", got)
	}
	if rep.AllCurrent() {
		t.Error("AllCurrent() is true for a file edited since the scan")
	}
	if len(rep.Stale) != 1 || rep.Stale[0] != "billing/order.go" {
		t.Errorf("Stale = %v, want the edited path", rep.Stale)
	}
}

func TestClassify_UnchangedSinceScanIsCurrent(t *testing.T) {
	s, root := newStore(t)
	sum := write(t, root, "billing/order.go", "package billing\n\nfunc Pay() {}\n")
	record(t, s, "billing/order.go", sum)

	rep, err := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"billing/order.go"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got := rep.States["billing/order.go"]; got != indexfresh.StateCurrent {
		t.Errorf("state = %q, want current", got)
	}
	if !rep.AllCurrent() {
		t.Errorf("AllCurrent() is false with nothing stale: %+v", rep)
	}
	if !indexfresh.StateCurrent.Trustworthy() {
		t.Error("StateCurrent must be trustworthy")
	}
}

// Every non-current state must be untrustworthy. This is the property both
// callers branch on, so pin it directly rather than through them.
func TestState_OnlyCurrentIsTrustworthy(t *testing.T) {
	for _, st := range []indexfresh.State{
		indexfresh.StateStale, indexfresh.StateAbsent,
		indexfresh.StateDeleted, indexfresh.StateUnreadable,
	} {
		if st.Trustworthy() {
			t.Errorf("state %q reports itself trustworthy", st)
		}
	}
}

func TestClassify_NeverIndexedIsAbsentNotStale(t *testing.T) {
	s, root := newStore(t)
	write(t, root, "vendor/thing.go", "package vendor\n")

	rep, err := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"vendor/thing.go"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// Absent and stale call for different responses: absent means there are no
	// spans to be wrong about, stale means there are and they are.
	if got := rep.States["vendor/thing.go"]; got != indexfresh.StateAbsent {
		t.Errorf("state = %q, want absent", got)
	}
	if len(rep.Stale) != 0 {
		t.Errorf("an unindexed file was reported as stale: %v", rep.Stale)
	}
}

func TestClassify_IndexedButDeletedFromTree(t *testing.T) {
	s, root := newStore(t)
	sum := write(t, root, "old/gone.go", "package old\n")
	record(t, s, "old/gone.go", sum)
	if err := os.Remove(filepath.Join(root, "old/gone.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	rep, err := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"old/gone.go"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got := rep.States["old/gone.go"]; got != indexfresh.StateDeleted {
		t.Errorf("state = %q, want deleted", got)
	}
	if rep.AllCurrent() {
		t.Error("AllCurrent() is true when an indexed file is gone")
	}
}

// A scan run with --hash-files=false writes no hash rows at all, so its files
// classify absent rather than current. That is the safe direction: the callers
// degrade to their fallback instead of trusting spans nothing corroborates.
// This also pins the store invariant the classifier relies on -- an indexed
// file always has a digest, because the port refuses to write an empty one.
func TestClassify_HashlessScanDegradesRatherThanTrusts(t *testing.T) {
	s, root := newStore(t)
	write(t, root, "billing/order.go", "package billing\n")

	err := s.FileHashes().Upsert(context.Background(), store.FileHashRow{
		FilePath: "billing/order.go", ContentHash: "",
	})
	if err == nil {
		t.Fatal("the store accepted an empty content_hash; classifyOne assumes it cannot")
	}

	rep, cerr := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"billing/order.go"})
	if cerr != nil {
		t.Fatalf("Classify: %v", cerr)
	}
	if got := rep.States["billing/order.go"]; got != indexfresh.StateAbsent {
		t.Errorf("state = %q, want absent", got)
	}
	if rep.AllCurrent() && len(rep.Untrustworthy()) == 0 {
		t.Error("a file with no recorded hash was treated as safe to join against")
	}
}

// A diff is untrusted input. A path that climbs out of the repo must not cause
// a read outside it.
func TestClassify_RefusesPathsOutsideTheRoot(t *testing.T) {
	s, root := newStore(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	rep, err := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"../../etc/passwd", outside})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	for _, p := range []string{"../../etc/passwd", outside} {
		if got := rep.States[p]; got != indexfresh.StateUnreadable {
			t.Errorf("state for %q = %q, want unreadable", p, got)
		}
	}
}

func TestClassify_DeduplicatesAndSorts(t *testing.T) {
	s, root := newStore(t)
	aSum := write(t, root, "a.go", "package a\n")
	record(t, s, "a.go", aSum)
	write(t, root, "b.go", "package b\n")
	record(t, s, "b.go", "deadbeef")

	rep, err := indexfresh.Classify(context.Background(), s.FileHashes(), root,
		[]string{"b.go", "a.go", "b.go", ""})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(rep.States) != 2 {
		t.Errorf("States has %d entries, want 2 (duplicates and blanks dropped)", len(rep.States))
	}
	if got := rep.Untrustworthy(); len(got) != 1 || got[0] != "b.go" {
		t.Errorf("Untrustworthy() = %v, want [b.go]", got)
	}
}
