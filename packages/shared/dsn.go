package shared

import "strings"

// EscapeSQLitePath makes a filesystem path safe to embed in a `file:` SQLite
// DSN.
//
// modernc.org/sqlite parses a `file:` DSN as a URI, so '?' begins the query
// string and '#' begins a fragment. A path containing either was silently
// TRUNCATED at that character: the driver opened a different database, created
// it on demand, and reported success -- so every command answered confidently
// from an empty store, or worse from a store shared with every other path that
// truncates to the same prefix.
//
// Found by FuzzSymbols_RoundTrip (#122): Go names a fuzz seed's temp directory
// ".../<Target>seed#<n>...", so all three seeds opened one file and read each
// other's rows. Nothing about that looked like a path bug from the inside --
// the symptom was a symbol coming back as a different symbol.
//
// It lives here rather than in packages/store because three packages build
// such a DSN -- the store itself, doctor's read-only probe and the security
// verb -- and a second copy is how one of them silently keeps the bug.
//
// Order matters: '%' must be encoded first or it would re-encode the escapes
// introduced for the other two. See https://sqlite.org/uri.html.
func EscapeSQLitePath(path string) string {
	path = strings.ReplaceAll(path, "%", "%25")
	path = strings.ReplaceAll(path, "?", "%3f")
	return strings.ReplaceAll(path, "#", "%23")
}
