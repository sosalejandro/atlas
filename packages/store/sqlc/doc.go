// Package sqlc holds the sqlc-generated query layer for packages/store.
//
// Every *.sql.go file in this package is produced by `sqlc generate`
// (config: ../sqlc.yaml; queries: ../queries/*.sql; schema: ../schema/*.sql).
// Do not edit those files by hand — re-run `sqlc generate` instead.
//
// External callers should NOT import this package directly. The narrow
// port interfaces in packages/store (Features, Symbols, Edges, …) are the
// supported API surface; this package is an internal implementation
// detail kept exported only because sqlc requires generated code to be a
// real Go package.
package sqlc
