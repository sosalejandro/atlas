package store

import "strings"

// bcPathFor returns the bounded-context path prefix for a repo-relative
// file path, or "" if the file does not live under src/contexts/<bc>/.
//
// The convention is fixed by docs/architecture.md §3.7 + schema-v1.md §5.4.
// Atlas treats anything matching `src/contexts/<bc>/` as living in that BC.
//
// This is a pure string-shape helper — no DB, no side effects. It lives in
// its own file (separate from ingest.go) so the SRP boundary between
// "transactional batch logic" and "path conventions" stays visible. If
// more BC-path helpers accumulate, they belong here.
func bcPathFor(relPath string) string {
	const prefix = "src/contexts/"
	if !strings.HasPrefix(relPath, prefix) {
		return ""
	}
	rest := relPath[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return ""
	}
	return prefix + rest[:slash]
}
