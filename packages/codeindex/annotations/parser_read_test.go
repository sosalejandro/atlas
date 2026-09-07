package annotations

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseRelative_LineLongerThanTheOldScannerCap pins the ONE behaviour
// that reading the file whole deliberately does not preserve.
//
// The old reader capped a bufio.Scanner token at 1 MB. A file with a longer
// single line — a minified bundle, a generated lookup table, a vendored
// dist/*.js — failed with bufio.ErrTooLong, and because the error aborted
// the whole parse, walkAnnotations logged "annotation parse failed" and
// dropped EVERY annotation in that file, not just the long line's.
// Reproduced against the old loop before removing it: a 2,097,182-byte
// single-line file gave `err=bufio.Scanner: token too long, bytes
// recovered=0`.
//
// os.ReadFile has no such cap, so the file now parses. In annotation terms
// that is a widening and not a narrowing — no input that used to yield
// annotations stops doing so — and it is asserted rather than assumed because
// "we quietly started indexing minified bundles" is the kind of change that
// should be someone's decision.
//
// In MEMORY terms it is a trade, not a free win, and the test that pins the
// widening is the right place to say so. The 1 MB token cap was the only
// per-file bound on how much this parser could hold at once; os.ReadFile has
// none, so one parse now peaks at roughly twice the file's size in RESIDENT
// memory (63.5 MB for a 32 MB single-line file, measured — see the figures
// and the reasoning in ParseRelative). The 2 MB fixture below is deliberately
// just over the old cap rather than the tens of megabytes a real generated
// bundle reaches: this test exists to pin the behaviour, and making it
// allocate 128 MB to do so would be a poor trade of its own.
func TestParseRelative_LineLongerThanTheOldScannerCap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.js")
	// One line, comfortably past the old 1 MB token cap, carrying an
	// annotation the way a banner comment on a minified file does.
	content := "// @atlas:feature big.bundle #real " + strings.Repeat("x", 2<<20) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ParseRelative(context.Background(), path, "web/bundle.js")
	if err != nil {
		t.Fatalf("ParseRelative on a %d-byte single-line file: %v", len(content), err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the banner annotation, got %d: %+v", len(got), got)
	}
	if len(got[0].IDs) != 1 || got[0].IDs[0] != "big.bundle" {
		t.Fatalf("IDs = %v; want [big.bundle]", got[0].IDs)
	}
}

// TestParseRelative_MissingFileErrorNamesTheFile keeps the I/O failure
// legible after the switch from os.Open to os.ReadFile. walkAnnotations logs
// this error against a path it already knows, but Parse's other callers do
// not, and an unwrapped "no such file or directory" names nothing.
func TestParseRelative_MissingFileErrorNamesTheFile(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "gone.go")
	_, err := ParseRelative(context.Background(), missing, "gone.go")
	if err == nil {
		t.Fatal("expected an error for a file that does not exist")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error does not unwrap to fs.ErrNotExist: %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not name the file it failed on", err)
	}
}
