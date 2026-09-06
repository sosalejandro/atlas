package pyscan

import (
	"runtime"
	"strings"
)

// takeEnv removes the assignment of name from a KEY=VALUE environment
// block and returns the remainder along with the value it carried.
//
// It is a near-copy of the identically-named helper in
// packages/codeindex/ts, and deliberately so: the two scanner packages
// share no internal package, and inventing one to hold twenty lines
// would couple them for less than it costs. Both exist because Windows
// compares environment-variable names case-insensitively and POSIX does
// not, so `strings.HasPrefix(kv, "PYTHONIOENCODING=")` fails to find a
// parent's `PythonIOEncoding` on the one platform where they are the
// same variable.
//
// Here the consequence is narrower than in the TS scanner — this package
// appends its values afterwards, and os/exec de-duplicates the Windows
// environment case-insensitively keeping the last — but "our values win"
// should be a property of this function, not of a rule in another
// package's implementation.
//
// The match is on the name up to the first '=', never a prefix of the
// whole entry: `PYTHONPATH_EXTRA=...` starts with `PYTHONPATH` and is a
// different variable.
func takeEnv(env []string, names []string, fold bool) []string {
	out := env[:0]
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq >= 0 && matchesAny(kv[:eq], names, fold) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// hostFoldsEnvCase reports whether this host's environment block is
// case-insensitive.
func hostFoldsEnvCase() bool { return runtime.GOOS == "windows" }

func matchesAny(name string, names []string, fold bool) bool {
	for _, want := range names {
		if fold {
			if strings.EqualFold(name, want) {
				return true
			}
			continue
		}
		if name == want {
			return true
		}
	}
	return false
}
