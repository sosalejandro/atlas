// Package redact answers, in code, the question every security review asks
// of a tool that reads a whole proprietary codebase: if I hand you this
// database, what am I handing you?
//
// Atlas is pitched at regulated industries and at a hosted tier. "Trust us"
// is not an answer either audience accepts, and a wiki page is not an
// answer either -- documents drift from the schema they describe within one
// migration. So the data-handling statement in docs/security.md is written
// FROM this package rather than alongside it:
//
//   - schema.go enumerates every table and every TEXT column of the state
//     database and says what each holds. schema_test.go compares that list
//     against a freshly migrated store, so a migration that adds a column
//     fails the build until somebody classifies it.
//
//   - exports.go enumerates every surface through which indexed content
//     leaves the database, and internal/cli/security_test.go checks the
//     list against the real command tree.
//
//   - egress_test.go walks the import graph of the atlas binary and fails
//     if any first-party package reaches network code, which is what turns
//     "nothing leaves the machine" from a promise into a check.
//
// The uncomfortable fact this package exists to state plainly is that the
// state database is not metadata. It holds file paths and symbol names, and
// it also holds SQL query text verbatim, the source text of every branch
// condition, and -- inside a snapshot blob -- the doc comment and signature
// of every symbol. That is proprietary source content, and anything that
// treats the database as safe to circulate because "it is only structure"
// is wrong.
//
// # Secret detection
//
// Source contains credentials more often than anyone admits, and a
// hardcoded connection string lands in sql_operations.sql_text as query
// text. Scan and Text find the high-signal shapes (PEM private keys, AWS
// access key ids, credentials inline in a connection URL, and high-entropy
// literals assigned to names that say "credential") and replace them with a
// placeholder that names the rule and a digest of what was removed.
//
// The detector is tuned to under-report, and the reason is that the two
// errors do not cost the same. A missed weak password is a credential an
// operator could have found by reading the file. A false positive silently
// rewrites a legitimate query that `atlas sql` then analyses and reports on
// as though it were the code -- a wrong answer that looks authoritative,
// which is the failure mode this repository rejects everywhere else.
// Everything redacted is therefore recorded and shown, never merely logged.
//
// # What this package does not do
//
// It does not encrypt anything, it does not implement egress controls for a
// service that does not exist, and it does not sync. The honest statement
// today is that nothing leaves the machine, and the job here is to make
// that checkable.
package redact
