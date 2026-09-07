package codeindex

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	goscan "github.com/sosalejandro/atlas/packages/codeindex/go"
)

// ONE READ PER FILE PER PASS — AND WHY THE NUMBER IS NOT ONE.
//
// Issue #156 asks that a .go file be read from disk once per scan. What is
// implementable, and what this test pins, is once per PASS. The difference
// is worth writing down, because "3" looks like a failure to reach "1" and
// is in fact the deliberate answer.
//
// IndexProject makes three sequential passes over the tree, and each has a
// recorded reason it cannot consume the previous one's product:
//
//   - Phase A, the Go sub-scanner. Its funcInfo cache is unexported and its
//     ASTs are adopted from the type checker, so they are keyed by node
//     pointers no other pass holds (syntaxFor's godoc).
//   - Phase A.5, the pattern recognisers. They walk different AST shapes —
//     struct embeds, closures — than the call-graph builder
//     (runPatternRecognizers' godoc).
//   - Phase B, the annotation walk. It covers every language atlas reads,
//     not just Go, and its output ORDER is load-bearing for feature
//     attribution (walkAnnotations' godoc).
//
// Collapsing the three reads into one would mean caching every file's bytes
// from phase A until phase B finished with them — the whole tree's source
// resident for the length of a scan. That is the opposite trade from the
// one #156 is making: three passes each holding one file per worker is
// bounded by `jobs × MaxSourceBytes`, while one cache is bounded by the
// repository. On a tree the size of this one that is ~9 MB and would look
// free; on the trees atlas is meant to scan it is not, and a ceiling that
// scales with the input is what issue #152 was opened about.
//
// So: three passes, three reads, and the number to attack is the number of
// passes — not the number of reads within one. Before #156 the same scan
// read a non-test .go file FIVE times.
//
// The counter is the kernel's, for the reason spelled out in
// packages/codeindex/go/readonce_test.go: a seam a test can swap only sees
// the readers that went through it.
const indexReadOnceFixtureBytes = 4 << 20

// passesThatReadEachGoFile is the assertion's whole content. Raising it
// requires adding a pass, and adding a pass requires the argument above.
const passesThatReadEachGoFile = 3

func procReadBytes(t *testing.T) int64 {
	t.Helper()
	f, err := os.Open("/proc/self/io")
	if err != nil {
		t.Skipf("/proc/self/io unavailable: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		field, value, ok := strings.Cut(sc.Text(), ":")
		if !ok || field != "rchar" {
			continue
		}
		n, perr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if perr == nil {
			return n
		}
	}
	t.Skip("/proc/self/io carried no rchar field")
	return 0
}

func writeIndexReadOnceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	var b strings.Builder
	b.Grow(indexReadOnceFixtureBytes + 1024)
	b.WriteString("package big\n\n// @atlas:feature big.padded #real\n/*\n")
	const filler = "// padding so the file is worth measuring the read of\n"
	for b.Len() < indexReadOnceFixtureBytes {
		b.WriteString(filler)
	}
	b.WriteString("*/\n\nfunc Big() {}\n")
	if err := os.WriteFile(filepath.Join(root, "big.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return root
}

// TestIndexProject_ReadsEachFileOncePerPass counts the bytes a whole
// IndexProject reads and divides by the fixture's size. Every pass that
// reads the file shows up as one more copy.
func TestIndexProject_ReadsEachFileOncePerPass(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read accounting comes from /proc/self/io")
	}
	root := writeIndexReadOnceFixture(t)
	opts := Options{
		HashFiles: true,
		SkipTS:    true,
		SkipPY:    true,
		Jobs:      1,
		GoOptions: goscan.Options{SkipTypedResolution: true},
	}
	ctx := context.Background()

	// Warm-up: the first IndexProject in a process reads things a scan
	// does not — lazily compiled regexps, the resolver's package data.
	if _, err := IndexProject(ctx, root, opts); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	before := procReadBytes(t)
	idx, err := IndexProject(ctx, root, opts)
	after := procReadBytes(t)
	if err != nil {
		t.Fatalf("IndexProject: %v", err)
	}
	if len(idx.Symbols) == 0 || len(idx.Annotations) == 0 || len(idx.FileHashes) == 0 {
		t.Fatalf("fixture produced symbols=%d annotations=%d hashes=%d; "+
			"a pass that did no work reads nothing and would pass this test for free",
			len(idx.Symbols), len(idx.Annotations), len(idx.FileHashes))
	}

	read := after - before
	const slackBytes = 256 << 10
	max := int64(indexReadOnceFixtureBytes)*passesThatReadEachGoFile + slackBytes
	if read > max {
		t.Errorf("IndexProject read %d bytes, %.2f copies of the %d-byte fixture; want at most %d "+
			"(one per pass: Go scan, pattern recognisers, annotation walk).\n"+
			"Somewhere a file is being read twice within one pass — see annotations.ReadSource.",
			read, float64(read)/indexReadOnceFixtureBytes, indexReadOnceFixtureBytes, passesThatReadEachGoFile)
	}
	if read < indexReadOnceFixtureBytes {
		t.Errorf("IndexProject read only %d bytes of a %d-byte fixture; the measurement is not seeing the scan's I/O",
			read, indexReadOnceFixtureBytes)
	}
}
