package tsscan

import (
	"runtime"
	"strings"
)

// Host conventions that are not the path SEPARATOR.
//
// The first pass at issue #143 fixed the separator, because that is the
// difference that threw an error loud enough to fail a test. The two
// helpers here cover the difference that does not: Windows compares
// environment-variable names and filenames case-insensitively, POSIX
// compares both exactly, and a comparison written for one host silently
// answers wrong on the other. Nothing errors — the child process just
// loses a NODE_PATH entry, or a legitimate node_modules directory is
// dropped with a warning about a rule it did not break.

// hostFoldsPathCase reports whether this host compares filenames and
// environment-variable names without regard to case.
func hostFoldsPathCase() bool { return runtime.GOOS == "windows" }

// takeEnv removes the assignment of name from a KEY=VALUE environment
// block and returns the remainder along with the value it carried.
//
// It exists because Windows' environment block is case-insensitive: a
// parent that exported `Node_Path` and a child that reads `NODE_PATH`
// are talking about one variable there and two on Linux. Matching
// case-sensitively on Windows meant buildScannerEnv left the parent's
// entry in the block AND appended its own — and since Go's exec
// de-duplicates the Windows environment case-insensitively, keeping the
// last, the parent's NODE_PATH was silently discarded instead of being
// appended after ours as the documented precedence order promises.
//
// The match is on the name up to the first '=', never a prefix of the
// whole entry: `NODE_PATH_EXTRA=...` starts with `NODE_PATH` and is a
// different variable.
func takeEnv(env []string, name string, fold bool) (rest []string, value string, found bool) {
	// Filtering in place is safe on the caller's slice here: os.Environ
	// returns a fresh one, and the only other caller is a test.
	rest = env[:0]
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			// Not an assignment at all. Pass it through rather than
			// dropping it: it is not ours to interpret.
			rest = append(rest, kv)
			continue
		}
		if !found && nameEquals(kv[:eq], name, fold) {
			value, found = kv[eq+1:], true
			continue
		}
		rest = append(rest, kv)
	}
	return rest, value, found
}

// baseNameIs reports whether the last element of p is want.
//
// It takes the path apart itself rather than calling filepath.Base so it
// accepts either separator regardless of host: a NodeModulesPaths entry
// is caller-supplied configuration, and a Windows user who wrote it with
// forward slashes has written a path Windows itself accepts.
func baseNameIs(p, want string, fold bool) bool {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return nameEquals(p, want, fold)
}

func nameEquals(a, b string, fold bool) bool {
	if fold {
		return strings.EqualFold(a, b)
	}
	return a == b
}
