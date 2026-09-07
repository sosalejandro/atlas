package goscan

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
)

// THE READ COUNTER, AND WHY IT COUNTS BYTES RATHER THAN CALLS.
//
// Issue #156: a .go file used to be read from disk three times by this
// package alone — the generated-header probe (bufio over an os.Open), the
// annotation pass (os.ReadFile), and go/parser, which reads the file itself
// whenever ParseFile is handed a nil src. Nothing asserted that, so nothing
// stopped a fourth from being added.
//
// The obvious test — a package-level `var readFile = os.ReadFile` the test
// swaps for a counter — only sees readers that go through the seam, which
// is exactly the reader nobody is worried about. A reader added the natural
// way (`os.Open`, `os.ReadFile`, `parser.ParseFile(..., nil, ...)`) would
// walk straight past it and the test would still pass.
//
// So the counter is the kernel's. /proc/self/io `rchar` is the number of
// bytes this process has obtained from read(2), page cache included, and
// `syscr` the number of read calls. Neither can be bypassed from inside
// Go, so a new reader shows up whatever API it was written against. The
// price is that the test is Linux-only; it skips elsewhere, and the same
// regression would still be caught on any CI runner that is Linux, which
// ours is.
//
// The fixture is one large file because the assertion is a ratio. A 4 MB
// file read once moves rchar by 4 MB; read twice, by 8 MB. Everything else
// the scan touches in a two-file temp tree — directory entries, the second
// tiny file — is kilobytes, so it disappears into the tolerance instead of
// having to be modelled.
const readOnceFixtureBytes = 4 << 20

// procIOCounters reads rchar and syscr from /proc/self/io.
func procIOCounters(t *testing.T) (rchar, syscr int64) {
	t.Helper()
	f, err := os.Open("/proc/self/io")
	if err != nil {
		t.Skipf("/proc/self/io unavailable: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		field, value, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		n, perr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if perr != nil {
			continue
		}
		switch field {
		case "rchar":
			rchar = n
		case "syscr":
			syscr = n
		}
	}
	return rchar, syscr
}

// writeReadOnceFixture lays down a module-free tree with one big .go file
// and one small one. The big file is padded with a block comment rather
// than with declarations so the parse stays cheap: this test is measuring
// I/O, and a 4 MB AST would make it measure the allocator instead.
//
// The padding deliberately carries an `@atlas:feature` annotation and the
// file deliberately does NOT carry a generated header, so both of the
// readers this test is about are exercised: a file that tripped the
// generated rule would never reach the parse.
func writeReadOnceFixture(t *testing.T) (root, bigRel string) {
	t.Helper()
	root = t.TempDir()

	var b strings.Builder
	b.Grow(readOnceFixtureBytes + 1024)
	b.WriteString("package big\n\n// @atlas:feature big.padded #real\n/*\n")
	const filler = "// padding so the file is worth measuring the read of\n"
	for b.Len() < readOnceFixtureBytes {
		b.WriteString(filler)
	}
	b.WriteString("*/\n\nfunc Big() {}\n")
	bigRel = "big.go"
	if err := os.WriteFile(filepath.Join(root, bigRel), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.go"),
		[]byte("package big\n\nfunc Small() { Big() }\n"), 0o644); err != nil {
		t.Fatalf("write small: %v", err)
	}
	return root, bigRel
}

// TestScan_ReadsEachFileOnce is the acceptance criterion of issue #156 for
// this package: one Scan, one read of each .go file.
//
// SkipTypedResolution is set because go/packages reads every file again
// inside x/tools, where atlas has no say. That read is real and the issue
// records it; it is not what this test can hold anyone to.
func TestScan_ReadsEachFileOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read accounting comes from /proc/self/io")
	}
	root, _ := writeReadOnceFixture(t)

	// One warm-up scan. The first one pays for lazily-initialised
	// regexps, the resolver's own package data and whatever the runtime
	// faults in, and some of that is read from disk.
	if _, err := Scan(context.Background(), root, Options{SkipTypedResolution: true}); err != nil {
		t.Fatalf("warm-up scan: %v", err)
	}

	beforeR, beforeS := procIOCounters(t)
	res, err := Scan(context.Background(), root, Options{SkipTypedResolution: true})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	afterR, afterS := procIOCounters(t)

	if len(res.Symbols) == 0 {
		t.Fatalf("fixture produced no symbols; the scan did not do the work being measured")
	}

	read := afterR - beforeR
	// One read of the fixture, plus a generous allowance for the tree's
	// other kilobytes. Two reads would be 8 MB and cannot hide in this.
	const slackBytes = 256 << 10
	if max := int64(readOnceFixtureBytes) + slackBytes; read > max {
		t.Errorf("Scan read %d bytes (%.2f copies of the %d-byte fixture); want one copy, at most %d bytes.\n"+
			"read syscalls: %d.\n"+
			"A .go file is meant to be read once per scan and fanned out — see readSourceFile in scanner.go.",
			read, float64(read)/readOnceFixtureBytes, readOnceFixtureBytes, max, afterS-beforeS)
	}
	if read < readOnceFixtureBytes/2 {
		t.Errorf("Scan read only %d bytes of a %d-byte fixture; the measurement is not seeing the scan's I/O",
			read, readOnceFixtureBytes)
	}
}

// TestScan_OversizeFileIsSkippedWithAWarning pins the other half of the
// read-once decision: the pipeline's single per-file size bound.
//
// Holding a file's bytes for the whole of its processing is what makes one
// read enough, and it is also what puts "one file's size" into the peak
// alongside its AST. annotations.MaxSourceBytes is where that is bounded,
// once, for every reader in the scan. A file over it is not read at all —
// it is reported the way a file that fails to parse is reported, as a
// warning naming the file, and the scan carries on.
func TestScan_OversizeFileIsSkippedWithAWarning(t *testing.T) {
	root := t.TempDir()
	// Just over the bound, and sparse: the assertion is about the size the
	// reader refuses, and writing 16 MB of real bytes to prove it would be
	// the same trade the fixture above declines.
	path := filepath.Join(root, "huge.go")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString("package huge\n\nfunc Huge() {}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Truncate(int64(annotations.MaxSourceBytes) + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.go"),
		[]byte("package huge\n\nfunc Ok() {}\n"), 0o644); err != nil {
		t.Fatalf("write ok.go: %v", err)
	}

	res, err := Scan(context.Background(), root, Options{SkipTypedResolution: true})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// The scan survives, and the file it refused is named — with the
	// reason, not merely as one more file that would not parse. The
	// distinction is the whole test: a 16 MB file of NUL padding fails
	// go/parser too, so an assertion that only checks "there was a
	// warning" would pass without the bound existing at all.
	var named bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "huge.go") && strings.Contains(w, "size bound") {
			named = true
		}
	}
	if !named {
		t.Errorf("no warning attributed huge.go to the per-file size bound; warnings = %v", res.Warnings)
	}
	var sawOK bool
	for _, s := range res.Symbols {
		if strings.HasSuffix(string(s.ID), "Ok") {
			sawOK = true
		}
		if strings.HasSuffix(string(s.ID), "Huge") {
			t.Errorf("the oversize file was indexed anyway: %s", s.ID)
		}
	}
	if !sawOK {
		t.Errorf("the sibling file was not indexed; one refused file stopped the scan")
	}
}

// TestHasGeneratedHeader_OverBytes pins the three behaviours the move from
// bufio.Scanner to a byte walk could have lost silently (issue #156).
//
// None of them would have failed a build, and only the first would have
// failed a test on a Linux checkout — which is the reason the CRLF case is
// written as bytes here rather than as a fixture file. A repo-root
// `* text=auto eol=lf` attribute rewrites a committed CRLF fixture on
// checkout and turns the assertion into LF-compared-to-LF; the annotations
// package pins its own CRLF fixtures with a testdata/.gitattributes rule
// for exactly that reason, and a literal is cheaper here than a fourth.
func TestHasGeneratedHeader_OverBytes(t *testing.T) {
	t.Parallel()

	const marker = "// Code generated by sqlc. DO NOT EDIT.\n"
	var longPreamble strings.Builder
	for i := 0; i < maxHeaderLines+4; i++ {
		longPreamble.WriteString("// licence line\n")
	}
	longPreamble.WriteString(marker)

	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"marker on the first line", marker + "\npackage db\n", true},
		{"marker after a licence preamble", "// Copyright\n//\n" + marker + "\npackage db\n", true},
		{
			// bufio.ScanLines dropped the CR before the regexp ever saw
			// the line; the anchored `DO NOT EDIT\.$` does not match with
			// one attached. Every Windows checkout depends on this.
			"CRLF line endings",
			strings.ReplaceAll(marker+"\npackage db\n", "\n", "\r\n"),
			true,
		},
		{
			// A lone CR is content, not a line ending — the same asymmetry
			// splitLines preserves in the annotations package.
			"a lone CR does not end the line",
			"// Code generated by x. DO NOT EDIT.\rtrailing\npackage db\n",
			false,
		},
		{"no marker", "// an ordinary file\n\npackage db\n", false},
		{
			// The rule is "ahead of the package clause". A file that
			// merely discusses the convention below its own declarations
			// is not claiming to be generated.
			"marker after the package clause",
			"package db\n\n" + marker,
			false,
		},
		{"marker past maxHeaderLines", longPreamble.String() + "\npackage db\n", false},
		{"empty file", "", false},
		{"no trailing newline", strings.TrimSuffix(marker, "\n"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasGeneratedHeader([]byte(tc.src)); got != tc.want {
				t.Errorf("hasGeneratedHeader = %v, want %v", got, tc.want)
			}
		})
	}
}
