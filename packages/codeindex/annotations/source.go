package annotations

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// THE ONE PLACE A SCAN READS A SOURCE FILE, AND THE ONE PLACE IT IS BOUNDED.
//
// Reading a source file whole is not an annotations concept, and this is an
// odd house for it. It lives here because it is the only package both the Go
// sub-scanner and the orchestrator already import — a new one under
// packages/codeindex/ would be a third — and because the argument the bound
// below settles was already written down here, in ParseRelative, when issue
// #152 removed the last thing that bounded a per-file read.
//
// # Why one reader at all (issue #156)
//
// A non-test .go file used to be opened six times in one IndexProject, each
// read allocating its own copy of the bytes: the generated-header probe,
// go/parser (a nil `src` argument is what makes the parser open the file
// itself) and the @api pass in the Go sub-scanner; a second go/parser call in
// the pattern recognisers; and, in the annotation walk, one read for the
// annotations and another for the SHA-256. Two of the six were 8% of the
// scan's cumulative allocation under names the profile could see
// (`io.copyBuffer` 28.4 MB, `os.readFileContents` 25.9 MB of 717 MB); the
// other four do not surface under their own symbols and cost more.
//
// Each pass now reads once, through here, and fans the bytes out. What that
// buys and what it costs is in docs/performance.md §6.
//
// # The bound
//
// Holding a file's bytes for the whole of its processing is what makes one
// read enough. It also puts "one file's size" into the momentary footprint,
// next to the AST built from it, where before the parser's own copy was
// released the moment the parse returned. That term wants a ceiling, and
// until now the pipeline had none anywhere:
//
//   - The 1 MB bufio token cap in the old annotation reader was the last
//     per-file bound in the scan, and #152 removed it — for a good reason.
//     It fired on ORDINARY inputs (a minified bundle, a generated lookup
//     table: any file with one long line) and, because the error aborted the
//     parse, cost every annotation in the file rather than the long line's.
//     TestParseRelative_LineLongerThanTheOldScannerCap pins that widening.
//   - `os.ReadFile` stats the file and allocates all of it. It has no bound
//     of its own and never did.
//
// So the bound is here, once, rather than in each reader — and it is a WHOLE
// FILE bound rather than a line bound, which is the distinction that makes it
// a different decision from the one #152 reversed. A line cap fires on files
// that are perfectly ordinary apart from their formatting. A 16 MiB file cap
// fires on files that are pathological by any measure: the largest source file
// in this repository is 106 KB, 154 times under it, and a .go file at the
// bound would cost go/parser several hundred megabytes of AST — far more than
// the bytes this refuses — before it produced a single symbol.
//
// 16 MiB is also a number that can be said out loud in terms of the worker
// count issue #109 made configurable. `--jobs × 16 MiB` is the SOURCE BYTES a
// scan can have in flight — 192 MB at `--jobs=12` — and the two parallel
// passes are the annotation walk and the pattern recognisers; the Go
// sub-scanner's walk is serial, so it contributes one file rather than one
// per worker. RESIDENT is roughly twice that, because each pass derives
// something file-sized from the bytes: logicalLine strings here, an AST
// there. docs/performance.md §5 measured one parse at 63.5 MB resident for a
// 32 MB file, and §6 carries the ceiling. An unbounded read has no such
// sentence, which is the point: a ceiling nobody can state is a ceiling
// nobody can size a container against.
//
// A file over the bound is NOT read, and callers report it the way they
// already report a file they cannot parse: a warning naming the file, and the
// scan carries on. That is the graceful degradation the 1 MB cap could not
// offer — it turned an ordinary file into an error; this turns a pathological
// one into a ledger entry — and it is why the bound could not live in
// ParseBytes, which by then has already been handed the bytes.
//
// The one consequence worth stating: an over-bound file gets no content hash
// either, so packages/indexfresh classifies it StateAbsent and every caller
// falls back to its non-incremental path for it. That is the safe direction —
// "rescan this every time" rather than "trust a digest nobody computed".
const MaxSourceBytes = 16 << 20

// ErrSourceTooLarge is returned by ReadSource for a file over
// MaxSourceBytes. Callers match it with errors.Is to tell "too big to be
// worth reading" apart from "could not be read", because only the second is
// a symptom of something wrong with the machine.
var ErrSourceTooLarge = errors.New("source file exceeds the scan's per-file size bound")

// ReadSource reads absPath whole and returns its bytes alongside the stat
// the read had to do anyway — the caller that needs a mod time (the
// incremental cache's FileHash) would otherwise ask for it a second time.
//
// # Why this is not four lines around os.ReadFile
//
// The size has to be known BEFORE any bytes are allocated, or the bound
// above has nothing left to bound: os.ReadFile stats and then allocates the
// whole file, so a caller checking len(content) is checking after the
// damage. Wrapping it (os.Stat, compare, os.ReadFile) works and is shorter,
// and it stats every file twice, because os.ReadFile stats again for its own
// size hint. That second stat allocates an os.fileStat per file, and this
// function runs about 1,850 times per scan of this repository.
//
// Measured, medians of three, `BenchmarkPatternRecognizers` on a fixed
// 681-file corpus — the pass where the cost is visible on its own because
// it is the one pass that gained a stat rather than losing a read:
//
//	before #156 (parser reads the file itself)   34,610,728 B/op
//	os.Stat + os.ReadFile                        34,764,688 B/op   +0.44%
//	open + fstat + ReadFull (this)               34,615,072 B/op   +0.01%
//
// 154 KB against a 683 MB scan is not why the loop is written out; it is
// written out because a pass that gets slightly WORSE is a pass someone will
// later be right to question, and "+0.44% for a stat we did not need" is a
// worse answer than ten lines.
//
// # The short-read case
//
// content is sized from the stat, so a file that GREW between the stat and
// the read is returned as its first size bytes rather than in full, where
// os.ReadFile would have appended the rest. That is a file being written
// while a scan reads it, and neither answer is the file; what matters is
// which way it fails. The digest computed from a truncated snapshot will not
// match the file on disk, so packages/indexfresh classifies it stale and the
// next scan reads it again — the safe direction. A file that SHRANK returns
// io.ErrUnexpectedEOF, which is why that error is a truncation and not a
// failure below.
//
// A stat is not a read: it moves no bytes and shows up in neither of the
// counters packages/codeindex/go/readonce_test.go asserts on.
func ReadSource(absPath string) ([]byte, fs.FileInfo, error) {
	f, err := os.Open(absPath) //nolint:gosec // absPath comes from the scan's own walk
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", absPath, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", absPath, err)
	}
	size := info.Size()
	if size > MaxSourceBytes {
		return nil, info, fmt.Errorf("%w: %s is %d bytes, bound is %d",
			ErrSourceTooLarge, absPath, size, MaxSourceBytes)
	}
	content := make([]byte, size)
	n, err := io.ReadFull(f, content)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, info, fmt.Errorf("read %s: %w", absPath, err)
	}
	return content[:n], info, nil
}

// Supported reports whether ext names a comment dialect ParseBytes
// understands, so a caller that already holds a path can decide not to
// read a file no parser will look at.
func Supported(ext string) bool {
	return commentStyleFor(ext) != styleUnsupported
}
