//go:build !race

package annotations

// The race detector allocates its own shadow state on every memory access,
// which lands in runtime.MemStats.TotalAlloc alongside the parser's own
// allocations: the same parse measures ~5.5 KB without -race and 360,920 B
// with it. That is a measurement of the detector, not of ParseRelative, and
// CI runs `go test ./... -race`. Hence the build tag — the ceilings are
// checked by the ordinary `go test ./...` run, and skipped where the number
// would be meaningless rather than loosened until it fits.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TWO CEILINGS, ON TWO FILE SIZES, BECAUSE ONE CANNOT SEE THE OTHER'S
// REGRESSION.
//
// Both are CUMULATIVE BYTES ALLOCATED by one ParseRelative — a
// runtime.MemStats.TotalAlloc delta — not resident memory. The distinction is
// the one issue #152 was opened over, so it is stated here rather than left to
// the reader.
//
// There are two classes of regression this parser can suffer, they behave
// completely differently with file size, and a single fixture can only see
// one of them:
//
//   - A FIXED per-file overhead. The historical defect: a fresh
//     `scanner.Buffer(make([]byte, 0, 64*1024), ...)` per file, so a 551-byte
//     file cost 74,029 B before it had matched anything. Constant in the
//     file's size, so it is loudest on the smallest file and nearly invisible
//     on a large one.
//   - A SIZE-PROPORTIONAL copy. The other half of the same defect: every line
//     copied into a growing bytes.Buffer to reassemble the file the scanner
//     had just taken apart. It scales with the file, so on a 551-byte fixture
//     it is 551 bytes — noise — and on a 512 KB file it is 512 KB.
//
// Measured on this branch, linux/amd64, Intel Core i7-10750H, Go 1.26.4,
// 12 logical CPUs, by the two tests below, each mutation applied to
// ParseRelative and reverted after (medians of 3 processes):
//
//	mutation                    551 B fixture   vs 16 KB    512 KB fixture   ratio   vs 2.8
//	none                                5,484       pass          1,194,316   2.278    pass
//	+64 KB buffer per file             72,051       FAIL          1,266,056   2.415    pass
//	+1 whole-file copy                  6,009       pass          1,739,089   3.317    FAIL
//	+1 string copy per line             7,010       pass          1,921,849   3.665    FAIL
//
// Read the two pass columns, not just the failures. The 64 KB buffer — the
// regression that actually happened — is 4.4x the small-file ceiling and 6%
// on the large one. The whole-file copy is 46% on the large file and 10% of
// the small file's headroom. Neither fixture gates the other's regression,
// which is why there are two.

// maxTinyFileParseBytes bounds a FIXED per-file overhead.
//
// The 551-byte Go fixture, measured by TestParseRelative_TinyFileAllocation
// Ceiling in seven separate processes on the machine above: 5,481 / 5,481 /
// 5,484 / 5,484 / 5,503 / 5,518 / 5,521 B, median 5,484, spread 40 B (0.73%).
// It is not a deterministic number and this file no longer prints one: the
// map iteration inside the regexp engine's cache and the GC's own accounting
// move it by a few tens of bytes between processes.
//
// What is left at ~5.5 KB is proportional to what the parse KEEPS rather than
// to the file's size: the comment strings retained in logicalLine, the
// `make([]shared.Annotation, 0, 8)` in ParseBytes, the file itself, and the
// regexp submatch slices. That is what makes 5 KB the floor rather than a
// target to chase.
//
// 16 KB is roughly 3x that observation, so this ceiling fires on any fixed
// per-file overhead above about 10.9 KB and on nothing smaller. It does NOT
// fire on a reintroduced whole-file or per-line copy — on a 551-byte file
// that is 551 bytes — and the table above is the measurement of that limit.
// TestParseRelative_LargeFileAllocationRatio is what covers the other class.
// Adding a field to shared.Annotation moves this number and should not fail
// CI; raising the constant should require replacing the figures above with
// newly measured ones.
const maxTinyFileParseBytes = 16 << 10

// maxLargeFileParseBytesPerFileByte bounds a SIZE-PROPORTIONAL copy.
//
// A ratio rather than an absolute count, because the regression class it
// exists for scales with the file: one extra copy of the input costs exactly
// 1.0 on this scale whatever size the fixture is, so the ceiling states the
// thing it is guarding directly. (The gate in test/acceptance/memory_test.go
// argues the opposite way for the opposite reason: it scans a real repository
// that grows, so a ratio there would be satisfiable by adding files. Here the
// fixture is generated at a fixed size by genGoSource, so it cannot be.)
//
// Observed 2.278 (1,194,316 B over a 524,349-byte generated file), three
// processes spanning 2.277-2.280 — a 0.13% spread, tighter than the small
// file because a half-megabyte denominator swamps the per-process noise.
//
// 2.8 is the observation plus HALF a copy of the file. That is the whole
// derivation: any change that copies the input once more fails (measured at
// 3.317), and nothing that merely grows the retained annotations approaches
// it — the fixture holds about 1,090 comment lines against 512 KB of source,
// so doubling what a logicalLine keeps moves this ratio by under 0.1.
const maxLargeFileParseBytesPerFileByte = 2.8

// TestParseRelative_TinyFileAllocationCeiling is the ratchet on fixed
// per-file overhead. BenchmarkParseRelative reports the number; this is what
// makes a regression fail CI rather than sit in a benchmark nobody reran.
func TestParseRelative_TinyFileAllocationCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation measurement; -short")
	}

	src, err := os.ReadFile(filepath.Join("testdata", "login_test.go.fixture"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	path := writeParseFixture(t, "tiny.go", src)

	perParse := bytesPerParse(t, path, 2000)
	t.Logf("ParseRelative allocates %d B per parse of a %d-byte file (cumulative allocation, not RSS)",
		perParse, len(src))
	if perParse > maxTinyFileParseBytes {
		t.Errorf("ParseRelative allocated %d B for a %d-byte file, above the committed ceiling of %d B; "+
			"something is allocating per-file storage that does not depend on the file's size",
			perParse, len(src), maxTinyFileParseBytes)
	}
}

// TestParseRelative_LargeFileAllocationRatio is the ratchet on a copy of the
// input. It exists because the ceiling above cannot see one: on a 551-byte
// fixture a reintroduced whole-file copy is 551 bytes, which is inside the
// noise, and this test is where that regression is 46%.
//
// The fixture is genGoSource's 512 KB file — the same input
// BenchmarkParseRelative's large_512KB case prices, so the benchmark's B/op
// and this ceiling are two readings of one quantity.
func TestParseRelative_LargeFileAllocationRatio(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation measurement; -short")
	}

	src := genGoSource(512 << 10)
	path := writeParseFixture(t, "large.go", src)

	// 200 parses rather than the small file's 2,000: each one moves half a
	// megabyte, so the TotalAlloc delta is already six orders of magnitude
	// above the measurement's own overhead and more repeats only cost time.
	perParse := bytesPerParse(t, path, 200)
	ratio := float64(perParse) / float64(len(src))
	t.Logf("ParseRelative allocates %d B per parse of a %d-byte file — %.3f B per file byte "+
		"(cumulative allocation, not RSS)", perParse, len(src), ratio)
	if ratio > maxLargeFileParseBytesPerFileByte {
		t.Errorf("ParseRelative allocated %.3f B per byte of a %d-byte file, above the committed "+
			"ceiling of %.3f; a whole extra copy of the input is exactly 1.0 on this scale, so "+
			"look for a reintroduced whole-file buffer or a string copy per line",
			ratio, len(src), maxLargeFileParseBytesPerFileByte)
	}
}

// writeParseFixture puts content in a temp dir under a name whose extension
// selects the Go comment style, and returns the path. ParseRelative sniffs
// the extension, so *.fixture would parse as nothing.
func writeParseFixture(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// bytesPerParse is the measurement both ceilings are read from: cumulative
// bytes allocated per ParseRelative, as a TotalAlloc delta over runs parses.
//
// The warm-up is not decoration. The first parse compiles nothing — the
// regexps are package vars — but it does touch first-use paths in the
// runtime and the page cache, and charging those to the average would make
// the ceiling a measurement of the warm-up rather than of the parse.
func bytesPerParse(t *testing.T, path string, runs int) uint64 {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if _, err := ParseRelative(ctx, path, "fixture.go"); err != nil {
			t.Fatalf("warm-up parse: %v", err)
		}
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		if _, err := ParseRelative(ctx, path, "fixture.go"); err != nil {
			t.Fatalf("parse %d: %v", i, err)
		}
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}
