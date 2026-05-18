// Package shared is the Atlas kernel: value types and sentinel errors that
// every other package depends on.
//
// Per docs/architecture.md §3.1, this package MUST NOT import any other
// in-repo package. Stdlib only. If something in shared/ grows a dependency
// on parsing, file I/O beyond os.FileInfo, or a third-party data store, it
// is a layering violation — move it to a tier-2 package instead.
//
// What lives here:
//   - FilePosition, SymbolID, SymbolKind, Symbol — value types
//   - Annotation + AnnotationKind — the cross-language annotation record
//   - Sentinel errors (ErrNotFound, ErrAnnotationInvalid, ...)
//   - Logger interface + NopLogger
//
// What does NOT live here:
//   - Anything that reads files, opens sockets, or touches SQLite
//   - Anything that knows the shape of a call graph (that's packages/graph)
//   - Parsers of any kind (those live in packages/codeindex/*)
package shared
