// Package store is Atlas's SQLite-backed registry + cache.
//
// It is the ONLY package in Atlas allowed to import database/sql and a
// SQLite driver (modernc.org/sqlite, pure-Go). Per docs/architecture.md
// §3.7, schema lives under store/schema/NNNN_*.up.sql and is embedded into
// the binary via //go:embed. Migrations are one-way (no .down.sql files —
// the per-developer cache DB is re-derivable; `rm atlas-state.db &&
// atlas init` is the canonical rollback path).
//
// The package's external surface is a *Store handle plus a set of narrow
// "port" interfaces (Features, Symbols, Edges, FeatureSymbols, FileHashes,
// Coverage, Audit, Annotations, Config). Each port is satisfied by a
// sqlite-backed implementation in this package; external consumers depend
// on the interfaces.
//
// Schema reference: docs/schema-v1.md.
// Reference implementation pattern: bmad-story-runner-cli's
// infrastructure/state/sqlite (Open + embedded migration runner).
package store
