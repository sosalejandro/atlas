package adapters

import "strings"

// slashPath rewrites a path to forward slashes no matter which OS wrote it.
//
// filepath.ToSlash is not the right tool here and using it was the bug
// issue #143 found. ToSlash rewrites the *host's* separator, so on Linux
// it is a no-op: a Playwright JSON report or a Go test log produced on a
// Windows runner arrives as `e2e\meals\log.spec.ts` and sails through
// untouched. The separator in these strings is a property of the machine
// that WROTE the artifact, not of the machine reading it, so the
// conversion has to be unconditional.
//
// The cost is that a POSIX file whose name genuinely contains a backslash
// is read as two segments. That trade is deliberate: `\` in a filename is
// legal on Linux but has never appeared in a test report or a scan
// result, whereas a report crossing OS boundaries is routine — and the
// alternative is feature ids that silently differ by which runner
// produced the report.
func slashPath(p string) string {
	return strings.ReplaceAll(p, `\`, "/")
}
