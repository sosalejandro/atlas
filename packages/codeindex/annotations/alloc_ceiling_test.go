//go:build !race

package annotations

// The race detector allocates its own shadow state on every memory access,
// which lands in runtime.MemStats.TotalAlloc alongside the parser's own
// allocations: the same parse measures 5,463 B without -race and 360,920 B
// with it. That is a measurement of the detector, not of ParseRelative, and
// CI runs `go test ./... -race`. Hence the build tag — the ceiling is
// checked by the ordinary `go test ./...` run, and skipped where the number
// would be meaningless rather than loosened until it fits.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// maxTinyFileParseBytes is a ceiling on the CUMULATIVE BYTES ALLOCATED by
// one ParseRelative of the 551-byte fixture — a runtime.MemStats.TotalAlloc
// delta, not resident memory. The distinction is the one issue #152 was
// opened over, so it is stated here rather than left to the reader.
//
// The number that made this test necessary is 64 KB: ParseRelative used to
// hand every file a fresh `scanner.Buffer(make([]byte, 0, 64*1024), ...)`,
// so a 551-byte file cost 74,029 B — a hundred and thirty times its own
// size — before it had matched anything. After the fix the same parse
// allocates 5,463 B (measured by this test on the machine and by the method
// recorded in docs/performance.md §Annotation parsing): the file itself, the
// comment lines kept from it, the `make([]shared.Annotation, 0, 8)` in
// ParseBytes, and the regexp submatch slices. Every one of those is
// proportional to what the parse KEEPS, which is what makes 5 KB the floor
// rather than a target to chase.
//
// 16 KB is roughly 3x that observation. The gap is deliberate: this is a
// trap for a reintroduced per-file buffer or whole-file copy, which are tens
// of KB, not a budget that ordinary parser growth has to negotiate — adding
// a field to shared.Annotation moves this number and should not fail CI.
// Raising the constant should require replacing the figures above with newly
// measured ones.
const maxTinyFileParseBytes = 16 << 10

// TestParseRelative_TinyFileAllocationCeiling is the ratchet. The benchmark
// above reports the number; this is what makes a regression fail CI rather
// than sit in a benchmark nobody reran.
func TestParseRelative_TinyFileAllocationCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation measurement; -short")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.go")
	src, err := os.ReadFile(filepath.Join("testdata", "login_test.go.fixture"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx := context.Background()
	// Warm-up: the first parse compiles nothing (the regexps are package
	// vars) but does touch first-use paths in the runtime and the page
	// cache, and charging those to the average would make the ceiling a
	// measurement of the warm-up rather than of the parse.
	for i := 0; i < 100; i++ {
		if _, err := ParseRelative(ctx, path, "tiny.go"); err != nil {
			t.Fatalf("warm-up parse: %v", err)
		}
	}

	const runs = 2000
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		if _, err := ParseRelative(ctx, path, "tiny.go"); err != nil {
			t.Fatalf("parse %d: %v", i, err)
		}
	}
	runtime.ReadMemStats(&after)

	perParse := (after.TotalAlloc - before.TotalAlloc) / runs
	t.Logf("ParseRelative allocates %d B per parse of a %d-byte file (cumulative allocation, not RSS)",
		perParse, len(src))
	if perParse > maxTinyFileParseBytes {
		t.Errorf("ParseRelative allocated %d B for a %d-byte file, above the committed ceiling of %d B; "+
			"something is allocating per-file storage that does not depend on the file's size",
			perParse, len(src), maxTinyFileParseBytes)
	}
}
