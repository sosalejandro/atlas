package annotations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Line endings are a behavioural contract, not an implementation detail.
//
// ParseRelative used to read files through a bufio.Scanner and re-join the
// lines with "\n". bufio.ScanLines strips a trailing "\r" as well as the
// "\n", so a CRLF file arrived at the parser already normalised to LF —
// nobody chose that, it fell out of how the file was read. Replacing the
// read with os.ReadFile hands the parser the CRs, so the normalisation had
// to move somewhere it is written down and tested rather than disappear.
//
// It now lives in splitLines (comments.go), which is the one place lines are
// formed. These tests are what stops it drifting back out: they pin CRLF and
// LF to IDENTICAL annotations — same kinds, same ids, same tags, same LINE
// NUMBERS — through both entry points, the in-memory one and the one that
// touches the disk.

// crlfFixtures pairs each committed LF fixture with its byte-for-byte CRLF
// twin and the extension that selects the comment style. All four styles are
// covered because each unwrapper forms its own lines.
var crlfFixtures = []struct {
	name string // LF fixture; the CRLF twin is name + ".crlf.fixture"
	base string // basename to write into a temp dir, ext selects the style
}{
	{"login_test.go", "sample.go"},
	{"dashboard.test.tsx", "sample.tsx"},
	{"rollup.py", "sample.py"},
	{"runbook.md", "sample.md"},
}

// TestCRLFFixturesAreTrueTwins checks the fixtures before anything relies on
// them. A CRLF fixture that a checkout rewrote to LF would make every test
// below pass while proving nothing, so the property is asserted rather than
// assumed: the twin must actually contain CRLF, and deleting its CRs must
// reproduce the LF fixture exactly. testdata/.gitattributes is what keeps
// that true across platforms; this is the check that it worked.
func TestCRLFFixturesAreTrueTwins(t *testing.T) {
	t.Parallel()

	for _, fx := range crlfFixtures {
		lf := readFixture(t, fx.name+".fixture")
		crlf := readFixture(t, fx.name+".crlf.fixture")

		if !bytes.Contains(crlf, []byte("\r\n")) {
			t.Fatalf("%s.crlf.fixture contains no CRLF; the checkout normalised it "+
				"and every CRLF assertion in this file is vacuous", fx.name)
		}
		if bytes.Contains(lf, []byte("\r")) {
			t.Fatalf("%s.fixture contains a CR; it is supposed to be the LF side", fx.name)
		}
		if stripped := bytes.ReplaceAll(crlf, []byte("\r"), nil); !bytes.Equal(stripped, lf) {
			t.Fatalf("%s: CRLF fixture is not the LF fixture plus CRs; the two have "+
				"diverged and comparing their annotations no longer isolates line endings",
				fx.name)
		}
	}
}

// TestParseBytes_CRLFEqualsLF is the in-memory half: the same content in the
// two line endings must produce annotations that are equal field for field.
func TestParseBytes_CRLFEqualsLF(t *testing.T) {
	t.Parallel()

	for _, fx := range crlfFixtures {
		t.Run(fx.base, func(t *testing.T) {
			t.Parallel()

			style := commentStyleFor(filepath.Ext(fx.base))
			if style == styleUnsupported {
				t.Fatalf("%s: unsupported extension; the case tests nothing", fx.base)
			}

			relPath := "pkg/" + fx.base
			wantLF := ParseBytes(relPath, readFixture(t, fx.name+".fixture"), style)
			gotCRLF := ParseBytes(relPath, readFixture(t, fx.name+".crlf.fixture"), style)

			if len(wantLF) == 0 {
				t.Fatalf("%s: LF fixture produced no annotations; nothing is being compared", fx.name)
			}
			if !reflect.DeepEqual(gotCRLF, wantLF) {
				t.Fatalf("%s: CRLF annotations differ from LF\n CRLF: %+v\n   LF: %+v",
					fx.name, gotCRLF, wantLF)
			}
		})
	}
}

// TestParseRelative_CRLFFixtureMatchesLFTwin is the same assertion through
// the filesystem path, which is the one that changed. Both fixtures are
// written to a temp dir under a name whose extension selects the style,
// because ParseRelative sniffs the extension and *.fixture is unsupported.
func TestParseRelative_CRLFFixtureMatchesLFTwin(t *testing.T) {
	t.Parallel()

	for _, fx := range crlfFixtures {
		t.Run(fx.base, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			lfPath := filepath.Join(dir, "lf_"+fx.base)
			crlfPath := filepath.Join(dir, "crlf_"+fx.base)
			if err := os.WriteFile(lfPath, readFixture(t, fx.name+".fixture"), 0o644); err != nil {
				t.Fatalf("write LF: %v", err)
			}
			if err := os.WriteFile(crlfPath, readFixture(t, fx.name+".crlf.fixture"), 0o644); err != nil {
				t.Fatalf("write CRLF: %v", err)
			}

			// Same relPath for both: FilePosition.Path must not be what
			// distinguishes them, the content must be what does not.
			const relPath = "pkg/sample"
			ctx := context.Background()
			wantLF, err := ParseRelative(ctx, lfPath, relPath)
			if err != nil {
				t.Fatalf("ParseRelative LF: %v", err)
			}
			gotCRLF, err := ParseRelative(ctx, crlfPath, relPath)
			if err != nil {
				t.Fatalf("ParseRelative CRLF: %v", err)
			}

			if len(wantLF) == 0 {
				t.Fatalf("%s: LF fixture produced no annotations; nothing is being compared", fx.name)
			}
			if !reflect.DeepEqual(gotCRLF, wantLF) {
				t.Fatalf("%s: CRLF annotations differ from LF\n CRLF: %+v\n   LF: %+v",
					fx.name, gotCRLF, wantLF)
			}
		})
	}
}

// TestParseBytes_CRLFDoesNotLeakIntoPayloads is the assertion the DeepEqual
// above cannot make on its own: if both sides carried a stray CR they would
// still be equal to each other. A CR that survives into Raw, an id or a tag
// reaches the feature registry and the annotation is silently a different
// annotation, so it is checked directly.
func TestParseBytes_CRLFDoesNotLeakIntoPayloads(t *testing.T) {
	t.Parallel()

	content := []byte("// @atlas:feature auth.login #real\r\n" +
		"/* @atlas:owner platform-team */\r\n" +
		"// @api POST /api/v1/auth/login\r\n")
	got := ParseBytes("auth/login.go", content, styleGoTS)
	if len(got) != 3 {
		t.Fatalf("expected 3 annotations from the CRLF source, got %d: %+v", len(got), got)
	}
	for _, ann := range got {
		fields := append([]string{ann.Raw, ann.Method, ann.Path}, ann.IDs...)
		fields = append(fields, ann.Tags...)
		for _, f := range fields {
			if bytes.ContainsRune([]byte(f), '\r') {
				t.Fatalf("CR survived into an annotation payload: %+v", ann)
			}
		}
	}
	if got[2].Path != "/api/v1/auth/login" {
		t.Fatalf("@api path = %q; want the path with no trailing CR", got[2].Path)
	}
}

// TestParseRelative_LoneCROnItsOwnIsNotALineBreak pins the OTHER half of the
// old reader's behaviour. bufio.ScanLines strips a CR only when it precedes
// an LF, so a classic-Mac file (CR-only endings) was one enormous line and
// the comment on it was one comment. splitLines does the same, deliberately:
// treating a bare CR as a line break would newly find annotations in files
// where atlas has never reported any.
func TestParseRelative_LoneCROnItsOwnIsNotALineBreak(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "mac.go")
	// One physical line as far as any LF-based reader is concerned: the
	// first `//` opens a comment that swallows the rest, so both ids land on
	// ONE annotation at line 1 rather than on two annotations at lines 1
	// and 2. Ugly, and exactly what atlas has always reported for such a
	// file — which is the point of pinning it.
	if err := os.WriteFile(path, []byte("// @atlas:feature a.a\r// @atlas:feature b.b\r"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ParseRelative(context.Background(), path, "mac.go")
	if err != nil {
		t.Fatalf("ParseRelative: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 annotation from a CR-only file, got %d: %+v", len(got), got)
	}
	if got[0].Position.Line != 1 {
		t.Fatalf("annotation line = %d; want 1 — a bare CR must not advance the line counter",
			got[0].Position.Line)
	}
	if !reflect.DeepEqual(got[0].IDs, []string{"a.a", "b.b"}) {
		t.Fatalf("IDs = %v; want both ids on the single logical line", got[0].IDs)
	}
}

// TestSplitLines_ContractIsWhatBufioScanLinesDid is the guard on dropCR, and
// it has to sit on splitLines rather than on ParseBytes because — measured,
// not assumed — nothing above splitLines can see whether the "\r" was removed.
//
// The differential: dropCR's body was replaced with `return line`, and 4,000
// randomly assembled CR-laden inputs (CRLF, LF, lone-CR and CR-CR-LF endings,
// across all three comment styles) were parsed through ParseBytes. Every
// annotation field of every result hashed identically to the same corpus
// parsed with dropCR intact, and the whole annotations suite stayed green.
// The reason is structural: each unwrapper bytes.TrimSpaces the text it takes
// off a line before that text becomes a logicalLine, and TrimSpace eats "\r"
// — so does the `\s*$` in the @atlas and @testreg patterns, and `(\S+)` in
// the @api one.
//
// So dropCR is not what makes CRLF and LF agree TODAY, and the tests above
// would keep passing without it. What it makes true is splitLines' documented
// contract, which is what the unwrappers are entitled to rely on: an
// unwrapper that stopped trimming, or a matcher anchored more tightly than
// `\s*$`, would inherit the "\r" the day it was written. Pinning the line
// former is how that stays a decision rather than an accident — the same
// reason the normalisation was moved here out of bufio.ScanLines to begin
// with.
func TestSplitLines_ContractIsWhatBufioScanLinesDid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			// The behaviour dropCR exists for.
			name:    "CRLF loses the CR with the LF",
			content: "a\r\nb\r\n",
			want:    []string{"a", "b", ""},
		},
		{
			// bufio.ScanLines' asymmetry, preserved deliberately: a CR
			// that does not precede an LF is ordinary content, so a
			// classic-Mac file stays one line. Treating it as a break
			// would newly find annotations in files atlas has never
			// reported any for.
			name:    "a lone CR is content, not a line break",
			content: "a\rb\n",
			want:    []string{"a\rb", ""},
		},
		{
			// Only ONE CR goes. ScanLines drops a single trailing CR and
			// leaves anything before it, so "x\r\r\n" is the line "x\r".
			name:    "only the CR adjacent to the LF is dropped",
			content: "x\r\r\n",
			want:    []string{"x\r", ""},
		},
		{
			// ScanLines strips a trailing CR from the final token too,
			// with no LF after it. The final line is where an
			// implementation that only handled "\r\n" pairs would differ.
			name:    "the final line without an LF is normalised too",
			content: "tail\r",
			want:    []string{"tail"},
		},
		{
			name:    "LF-only content is untouched",
			content: "a\nb",
			want:    []string{"a", "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, line := range splitLines([]byte(tc.content)) {
				got = append(got, string(line))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitLines(%q) = %q; want %q", tc.content, got, tc.want)
			}
		})
	}
}

// TestSplitLines_YieldsSubslicesNotCopies pins the other half of splitLines'
// contract, the half issue #152 was about. bytes.Split allocates a header per
// line for the whole file and the callers then copied each one to a string;
// walking subslices is why only the handful of comment lines a file has are
// ever copied, and it is most of the -66% on the 512 KB row in
// docs/performance.md §5.
//
// Aliasing is asserted by writing through the input after the lines have been
// collected: a subslice sees the change, a copy cannot.
func TestSplitLines_YieldsSubslicesNotCopies(t *testing.T) {
	t.Parallel()

	content := []byte("first\nsecond\nthird")
	var lines [][]byte
	for _, line := range splitLines(content) {
		lines = append(lines, line)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	content[0] = 'X'
	content[len(content)-1] = 'X'
	if got := string(lines[0]); got != "Xirst" {
		t.Fatalf("first line = %q after writing through the input; splitLines copied it", got)
	}
	if got := string(lines[2]); got != "thirX" {
		t.Fatalf("last line = %q after writing through the input; splitLines copied it", got)
	}
}
