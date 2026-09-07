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
