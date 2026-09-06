package resolver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This file exists because of issue #143: the Windows CI leg has never
// been green, and packages/resolver was the one suspect nobody had
// examined. The suspicion was specific — Program.byPath INSERTS the path
// `go list` reports and QUERIES with the path filepath.WalkDir produced,
// and on Windows those two sources do not always spell the same file the
// same way.
//
// Every case below therefore states the Windows shape LITERALLY and
// passes `windows` as a parameter rather than reading runtime.GOOS. A
// table that derived the separator from the host would exercise the POSIX
// branch on the machine CI gates hardest on and the Windows branch
// nowhere, which is exactly how this rotted for months.

func TestPathKeyOn_WindowsUnifiesEverySpellingOfOnePath(t *testing.T) {
	t.Parallel()

	// The one thing that matters: every row is a way some tool on
	// Windows can spell the SAME file, and every row must collapse to
	// one key, or the byPath lookup misses and the whole tree silently
	// degrades out of `typed`.
	const want = `c:\src\atlas\packages\resolver\load.go`
	spellings := []struct {
		name string
		in   string
	}{
		{"go list: native separators", `C:\src\atlas\packages\resolver\load.go`},
		{"WalkDir over a slash-spelled root", "C:/src/atlas/packages/resolver/load.go"},
		{"mixed, as a joined config value arrives", `C:/src\atlas/packages\resolver/load.go`},
		{"lower-case drive letter", `c:\src\atlas\packages\resolver\load.go`},
		{"upper-cased component", `C:\SRC\atlas\packages\resolver\load.go`},
		{"uncleaned", `C:\src\atlas\packages\store\..\resolver\.\load.go`},
		{"doubled separator", `C:\src\atlas\\packages\resolver\load.go`},
	}
	for _, s := range spellings {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			if got := pathKeyOn(s.in, true); got != want {
				t.Errorf("pathKeyOn(%q, windows) = %q, want %q", s.in, got, want)
			}
		})
	}
}

func TestPathKeyOn_WindowsVolumeForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"drive root", `C:\`, `c:\`},
		{"bare drive", `C:`, `c:`},
		{"trailing separator is not a component", `C:\src\atlas\`, `c:\src\atlas`},
		// A UNC share is a volume, not two directories: cleaning must
		// not collapse the leading pair into one separator, or every
		// path on a network drive keys to the wrong file.
		{"unc share", `\\build01\share\atlas\load.go`, `\\build01\share\atlas\load.go`},
		{"unc share, uncleaned", `\\build01\share\atlas\..\atlas\load.go`, `\\build01\share\atlas\load.go`},
		{"unc share root", `\\build01\share`, `\\build01\share`},
		// The 8.3 short name is a DIFFERENT spelling that no amount of
		// string work can reconcile with the long one -- only the
		// filesystem knows they are the same file. The key function must
		// not pretend otherwise; that case is closed by the EvalSymlinks
		// fallback in Program.lookup, tested below.
		{"8.3 short name is preserved verbatim",
			`C:\Users\RUNNER~1\AppData\Local\Temp\atlas\a.go`,
			`c:\users\runner~1\appdata\local\temp\atlas\a.go`},
		{"empty", "", ""},
		{"relative", `packages\resolver`, `packages\resolver`},
		{"dot", ".", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pathKeyOn(tt.in, true); got != tt.want {
				t.Errorf("pathKeyOn(%q, windows) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The POSIX rules are the opposite ones, and stating them is the point:
// folding case or treating '\' as a separator on Linux would make two
// genuinely different files share a key.
func TestPathKeyOn_PosixKeepsCaseAndTreatsBackslashAsAName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"case is significant", "/src/Atlas/Load.go", "/src/Atlas/Load.go"},
		{"backslash is a legal filename character", `/src/a\b.go`, `/src/a\b.go`},
		{"uncleaned", "/src/atlas/packages/store/../resolver/./load.go", "/src/atlas/packages/resolver/load.go"},
		{"trailing separator", "/src/atlas/", "/src/atlas"},
		{"root", "/", "/"},
		{"empty", "", ""},
		{"dot", ".", "."},
		// A drive letter is not a volume here; it is a directory named
		// "C:" and must not be lower-cased.
		{"drive letter is just a name", "C:/src/load.go", "C:/src/load.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pathKeyOn(tt.in, false); got != tt.want {
				t.Errorf("pathKeyOn(%q, posix) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// pathKey is the host-bound wrapper production calls. Pinning it to
// pathKeyOn keeps the seam the tables above exercise from drifting away
// from the code path that actually runs.
func TestPathKey_UsesTheHostRules(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		`C:\src\atlas\load.go`,
		"/src/atlas/load.go",
		`/src/a\b.go`,
		"",
	} {
		want := pathKeyOn(in, runtime.GOOS == "windows")
		if got := pathKey(in); got != want {
			t.Errorf("pathKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoad_EveryIndexKeyIsCanonical is the wiring assertion for the
// INSERT side, which is the half a Linux run cannot otherwise reach: on
// Linux `go list` already reports clean, correctly-cased paths, so an
// index built by raw insertion looks identical to one built through
// pathKey. On Windows it does not — a raw insert would put
// `C:\Users\...` in the map with its original case — so this is the test
// that fails there if the canonicalisation is ever unwired.
func TestLoad_EveryIndexKeyIsCanonical(t *testing.T) {
	t.Parallel()
	p := load(t, goldenCorpus, Options{})
	if len(p.byPath) == 0 {
		t.Fatal("golden corpus indexed no files; the rest of this test proves nothing")
	}
	for key := range p.byPath {
		if got := pathKey(key); got != key {
			t.Errorf("byPath key %q is not canonical: pathKey(%q) = %q", key, key, got)
		}
	}
}

// TestSyntax_ResolvesAThirdSpellingThroughTheFilesystem is the Windows
// 8.3 short-name class (`C:\Users\RUNNER~1\...` versus
// `C:\Users\runneradmin\...`) reproduced on a host that has no 8.3 names.
//
// Both are real paths to one file, and no string rule reconciles them —
// only the filesystem does. A symlink is the POSIX instance of exactly
// that relationship, so this test fails on Linux for the same reason a
// scan fails on a GitHub Windows runner, and passes for the same reason.
func TestSyntax_ResolvesAThirdSpellingThroughTheFilesystem(t *testing.T) {
	t.Parallel()
	corpus, err := filepath.Abs(goldenCorpus)
	if err != nil {
		t.Fatal(err)
	}
	p := load(t, corpus, Options{})

	// Any file the corpus type-checked will do; the property under test
	// is about spelling, not about which file.
	var indexed string
	for key := range p.byPath {
		indexed = key
		break
	}
	if indexed == "" {
		t.Fatal("golden corpus indexed no files")
	}

	link := filepath.Join(t.TempDir(), "corpus")
	if err := os.Symlink(corpus, link); err != nil {
		t.Skipf("this filesystem has no symlinks: %v", err)
	}
	rel, err := filepath.Rel(pathKey(corpus), indexed)
	if err != nil {
		t.Fatalf("indexed file %q is not under %q: %v", indexed, corpus, err)
	}
	viaLink := filepath.Join(link, rel)

	if _, err := os.Stat(viaLink); err != nil {
		t.Fatalf("%s does not name a real file: %v", viaLink, err)
	}
	if !p.TypeChecked(viaLink) {
		t.Errorf("TypeChecked(%q) = false; it is the same file as %q, "+
			"reachable under a second spelling only the filesystem can reconcile",
			viaLink, indexed)
	}
	if _, ok := p.Syntax(viaLink); !ok {
		t.Errorf("Syntax(%q) missed; every alternate spelling of an indexed "+
			"file must return the SAME type-checked tree, or types.Info "+
			"lookups against a re-parsed tree silently resolve nothing", viaLink)
	}
}

// TestStripRoots_FoldsCaseOnlyWhereTheFilesystemDoes covers relativise's
// worker. Absolute paths in a type-checker message end up in JSON output
// and in golden files, so a root that fails to strip is a determinism
// break (docs/testing/determinism.md) and a leak of the operator's home
// directory — and on Windows the two spellings of the root routinely
// differ only in case.
func TestStripRoots_FoldsCaseOnlyWhereTheFilesystemDoes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		msg   string
		roots []string
		sep   rune
		fold  bool
		want  string
	}{
		{
			name:  "posix root strips exactly",
			msg:   "/home/u/atlas/pkg/a.go:3:1: undefined: X",
			roots: []string{"/home/u/atlas"},
			sep:   '/',
			want:  "pkg/a.go:3:1: undefined: X",
		},
		{
			name:  "posix is case sensitive: a different case is a different directory",
			msg:   "/home/u/Atlas/pkg/a.go:3:1: undefined: X",
			roots: []string{"/home/u/atlas"},
			sep:   '/',
			want:  "/home/u/Atlas/pkg/a.go:3:1: undefined: X",
		},
		{
			name:  "windows drive-letter case must not defeat the strip",
			msg:   `C:\src\atlas\pkg\a.go:3:1: undefined: X`,
			roots: []string{`c:\src\atlas`},
			sep:   '\\',
			fold:  true,
			want:  `pkg\a.go:3:1: undefined: X`,
		},
		{
			name:  "windows: the second root spelling wins when the first misses",
			msg:   `C:\Users\runneradmin\T\atlas\pkg\a.go:3: oops`,
			roots: []string{`C:\Users\RUNNER~1\T\atlas`, `C:\Users\runneradmin\T\atlas`},
			sep:   '\\',
			fold:  true,
			want:  `pkg\a.go:3: oops`,
		},
		{
			name:  "every occurrence, not just the first",
			msg:   "/r/a.go:1: cannot use /r/b.go",
			roots: []string{"/r"},
			sep:   '/',
			want:  "a.go:1: cannot use b.go",
		},
		{
			name:  "empty root is a no-op, not a strip of everything",
			msg:   "pkg/a.go:3: undefined: X",
			roots: []string{""},
			sep:   '/',
			want:  "pkg/a.go:3: undefined: X",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := stripRoots(tt.msg, tt.roots, tt.sep, tt.fold); got != tt.want {
				t.Errorf("stripRoots(%q, %q, fold=%v) = %q, want %q",
					tt.msg, tt.roots, tt.fold, got, tt.want)
			}
		})
	}
}
