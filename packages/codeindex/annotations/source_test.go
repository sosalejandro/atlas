package annotations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReadSource_BoundIsCheckedBeforeAnythingIsAllocated is the whole point
// of ReadSource stat-ing separately from os.ReadFile.
//
// A bound applied after the read has nothing left to bound: os.ReadFile
// stats the file and allocates all of it before it returns, so by the time a
// caller could measure len(content) the memory the bound exists to refuse
// has already been handed out. Returning nil is therefore not enough
// evidence on its own — an implementation that read first and checked
// second would return nil too, having allocated 16 MB to do it. So the
// bytes allocated across the call are measured, and the fixture is sparse
// so that reading it is cheap in every way EXCEPT the allocation this is
// watching for.
//
// TotalAlloc is cumulative bytes handed out, not resident memory — the
// distinction issue #152 was opened over. The threshold is deliberately far
// below MaxSourceBytes and far above the few hundred bytes a refusal costs;
// there is nothing in between for it to be sensitive to.
func TestReadSource_BoundIsCheckedBeforeAnythingIsAllocated(t *testing.T) {
	// Not parallel: it reads a process-wide allocation counter.

	path := filepath.Join(t.TempDir(), "huge.go")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(MaxSourceBytes + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	content, info, err := ReadSource(path)
	runtime.ReadMemStats(&after)

	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Errorf("the refusal allocated %d bytes (CUMULATIVE ALLOCATION, not resident); "+
			"the size check is running after the read instead of before it", allocated)
	}
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("ReadSource on a %d-byte file: err = %v; want ErrSourceTooLarge", MaxSourceBytes+1, err)
	}
	if content != nil {
		t.Errorf("ReadSource returned %d bytes alongside the refusal; the bound bought nothing", len(content))
	}
	// The stat is returned even on refusal: the caller reporting the file is
	// the one that wants to say how big it was.
	if info == nil || info.Size() != MaxSourceBytes+1 {
		t.Errorf("info = %v; want the stat of the refused file", info)
	}
	if !strings.Contains(err.Error(), "bound is") {
		t.Errorf("error %q does not say what the bound was", err)
	}
}

// TestReadSource_AtTheBound pins that the bound is inclusive — a file of
// exactly MaxSourceBytes is read, not refused. An off-by-one here is
// invisible in every other test and would only ever be discovered by
// whoever owns the one file in the world that is exactly 16 MiB.
func TestReadSource_AtTheBound(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exact.go")
	if err := os.WriteFile(path, make([]byte, MaxSourceBytes), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	content, _, err := ReadSource(path)
	if err != nil {
		t.Fatalf("ReadSource at exactly the bound: %v", err)
	}
	if len(content) != MaxSourceBytes {
		t.Errorf("read %d bytes, want %d", len(content), MaxSourceBytes)
	}
}

// TestParseRelative_RefusesOverTheBound checks that the wrapper propagates
// the refusal rather than degrading to an empty annotation list. An empty
// list is indistinguishable from "this file has no annotations", and a scan
// that silently drops a file's features is the failure mode issue #152's
// notes describe from the other direction.
func TestParseRelative_RefusesOverTheBound(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "huge.js")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(MaxSourceBytes + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	anns, err := ParseRelative(context.Background(), path, "web/huge.js")
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("ParseRelative err = %v; want ErrSourceTooLarge", err)
	}
	if len(anns) != 0 {
		t.Errorf("got %d annotations from a file that was never read", len(anns))
	}
}

// TestSupported keeps the extension gate honest: it is what lets a caller
// holding only a path decide not to read a file no parser will look at, so
// it has to agree with ParseBytes about which files those are.
func TestSupported(t *testing.T) {
	t.Parallel()

	for _, ext := range []string{".go", ".ts", ".tsx", ".js", ".py", ".md"} {
		if !Supported(ext) {
			t.Errorf("Supported(%q) = false; ParseBytes has a dialect for it", ext)
		}
	}
	for _, ext := range []string{".png", ".lock", ""} {
		if Supported(ext) {
			t.Errorf("Supported(%q) = true; ParseBytes returns nil for it, so reading it buys nothing", ext)
		}
	}
}

// TestReadSource_MatchesOsReadFile is the equivalence check the hand-written
// read loop owes for not calling os.ReadFile.
//
// The corpus is this package's own source and testdata, which covers the
// cases a size-then-ReadFull loop can get wrong: an empty file, a file with
// no trailing newline, and the CRLF twins pinned by testdata/.gitattributes.
// A divergence here would be silent rather than loud — a digest computed
// from different bytes classifies every file stale forever
// (indexfresh.hashFile's godoc) instead of failing.
func TestReadSource_MatchesOsReadFile(t *testing.T) {
	t.Parallel()

	var checked int
	err := filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is not this test's subject
		}
		want, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		got, info, gerr := ReadSource(p)
		if gerr != nil {
			t.Errorf("ReadSource(%s): %v", p, gerr)
			return nil
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ReadSource(%s) returned %d bytes, os.ReadFile %d", p, len(got), len(want))
		}
		if info.Size() != int64(len(want)) {
			t.Errorf("ReadSource(%s) info.Size() = %d, content is %d bytes", p, info.Size(), len(want))
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked < 5 {
		t.Fatalf("only %d files compared; the walk found nothing to check", checked)
	}
}

// TestReadSource_EmptyFile is called out separately because a zero-length
// buffer handed to io.ReadFull is the one shape where the loop could
// plausibly return an error for a perfectly ordinary file.
func TestReadSource_EmptyFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "empty.go")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	content, info, err := ReadSource(path)
	if err != nil {
		t.Fatalf("ReadSource on an empty file: %v", err)
	}
	if len(content) != 0 || info.Size() != 0 {
		t.Errorf("content = %d bytes, info.Size() = %d; want both zero", len(content), info.Size())
	}
}
