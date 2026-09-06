package resolver

import (
	"path"
	"runtime"
	"strings"
)

// This file is the answer to one question: when two tools name the same
// source file, how does this package decide they mean the same file?
//
// It has to decide, because Program indexes files by absolute path and
// the two sides of that index come from different places. The INSERT
// side is whatever `go list` reported through
// token.Position.Filename. The QUERY side is whatever the scanner's
// filepath.WalkDir produced from the root the operator typed. On Linux
// those two strings are reliably identical and the question never comes
// up — which is precisely why it went unasked until the Windows CI leg
// (issue #143) was added and nothing was green.
//
// On Windows the same file has several equally correct spellings:
//
//	C:\src\atlas\load.go        go list, native separators
//	c:/src/atlas/load.go        a root the operator typed with slashes
//	C:\Users\RUNNER~1\...       an 8.3 short name, as %TEMP% is on CI
//	C:\Users\runneradmin\...    the long name for that same directory
//
// The first three lines of defence are textual and live here: a path key
// that normalises separators, cleans the path, and folds case where the
// filesystem does. The fourth cannot be done with strings at all — only
// the filesystem knows RUNNER~1 and runneradmin are one directory — so it
// lives in Program.lookup, which asks the filesystem on a miss.
//
// A miss is silent. Nothing errors; the file simply is not type-checked,
// every call in it falls back to name matching, and the only symptom is a
// resolution tier histogram that shifts. That is why this is a keying
// rule with its own tests rather than a filepath.Clean at each call site.

// pathKey returns the identity under which a file is indexed on THIS
// host.
func pathKey(p string) string { return pathKeyOn(p, runtime.GOOS == "windows") }

// pathKeyOn is pathKey with the platform as an argument.
//
// The parameter is the whole point: it is what lets the Windows rules be
// asserted from a Linux test run, on the host CI gates hardest on. Read
// from runtime.GOOS instead and the Windows branch would only ever
// execute on the platform whose results nobody was allowed to block on.
func pathKeyOn(p string, windows bool) string {
	if p == "" {
		return ""
	}
	if !windows {
		// path.Clean, not filepath.Clean: on a POSIX target the two are
		// the same function, and naming the pure one keeps this branch
		// from silently becoming the Windows one when the test host is
		// Windows.
		return path.Clean(p)
	}

	s := strings.ReplaceAll(p, `\`, "/")
	vol := windowsVolume(s)
	rest := s[len(vol):]
	if rest != "" {
		rest = path.Clean(rest)
		// path.Clean("") is ".", and "C:." is not how anyone spells the
		// current directory of a drive.
		if rest == "." && vol != "" {
			rest = ""
		}
	}
	// Lower-casing the whole path, not only the drive letter: Windows
	// compares filenames case-insensitively, so `C:\Src\a.go` and
	// `C:\src\a.go` ARE the same file and must key alike. The known cost
	// is a directory with the per-directory case-sensitivity flag set
	// (WSL sets it), where two files could differ only in case and would
	// collide here — one of them would resolve to the other's syntax
	// tree. That is rare, needs an explicit opt-in to create, and is a
	// far smaller failure than the drive-letter mismatch that made the
	// index miss on every file.
	return asciiLower(strings.ReplaceAll(vol+rest, "/", `\`))
}

// windowsVolume returns the leading part of a slash-spelled Windows path
// that is a VOLUME rather than a directory: "C:", or the host and share
// of a UNC path.
//
// It exists so cleaning cannot eat the structure. `\\build01\share\x`
// spelled with slashes is `//build01/share/x`, and path.Clean collapses
// the leading pair to one — which turns a network share into an absolute
// path on the local drive. Splitting the volume off first keeps the two
// separators and still cleans everything after them.
//
// The `\\?\` extended-length and `\\.\` device prefixes are deliberately
// not special-cased: the go tool does not emit them and a scan root
// spelled that way is not a shape this repository claims to support.
func windowsVolume(s string) string {
	if len(s) >= 2 && s[1] == ':' && isASCIILetter(s[0]) {
		return s[:2]
	}
	if !strings.HasPrefix(s, "//") {
		return ""
	}
	rest := s[2:]
	host := strings.IndexByte(rest, '/')
	if host < 0 {
		return s // `\\build01` alone: no share, nothing after it to clean
	}
	share := strings.IndexByte(rest[host+1:], '/')
	if share < 0 {
		return s // `\\build01\share`
	}
	return s[:2+host+1+share]
}

func isASCIILetter(b byte) bool {
	return ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}

// asciiLower lower-cases A-Z and nothing else.
//
// strings.ToLower would be wrong here for a reason that matters: it is
// Unicode-aware, so it can change a string's LENGTH ('İ' is two bytes and
// lower-cases to three). Path keys are compared and sliced by byte
// offset, and stripRoots below indexes a folded copy of a string to cut
// the original — an operation that is only correct while folding
// preserves offsets. Windows' own filename comparison is a fixed
// case-folding table, not a locale-aware one, so restricting this to
// ASCII also matches what the filesystem actually does with the drive
// letters and directory names this is here to reconcile.
func asciiLower(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			if b == nil {
				b = []byte(s)
			}
			b[i] = c + ('a' - 'A')
		}
	}
	if b == nil {
		return s
	}
	return string(b)
}

// stripRoots removes every occurrence of "<root><sep>" from a
// type-checker message, for each spelling of the root it was given.
//
// Absolute paths must not survive into a message: these end up on a scan
// Result that is serialised to JSON, stored, and compared in golden files
// (docs/testing/determinism.md), so an unstripped root makes two scans of
// one commit from two checkouts disagree — and leaks the operator's home
// directory into whatever the report is pasted into.
//
// fold says whether the host compares filenames case-insensitively. On
// Windows it does, and it matters: the root this process computed and the
// root the go tool echoed back routinely differ only in the case of the
// drive letter, and a case-sensitive strip would leave every path in the
// message absolute.
func stripRoots(msg string, roots []string, sep rune, fold bool) string {
	for _, root := range roots {
		if root == "" {
			continue
		}
		msg = replaceAll(msg, root+string(sep), fold)
	}
	return msg
}

// replaceAll deletes every occurrence of old from s, comparing with ASCII
// case folding when fold is set. asciiLower is length-preserving, so an
// offset found in the folded copy is valid in the original.
func replaceAll(s, old string, fold bool) string {
	if old == "" {
		return s
	}
	if !fold {
		return strings.ReplaceAll(s, old, "")
	}
	needle := asciiLower(old)
	var b strings.Builder
	for {
		i := strings.Index(asciiLower(s), needle)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+len(old):]
	}
}
