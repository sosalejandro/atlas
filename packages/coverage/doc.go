// Package coverage ingests test-framework result reports and persists them
// through the store.Coverage port.
//
// Each supported framework lives in its own sub-package and exports a
// stable Parse(io.Reader) (Run, []Result, error) function:
//
//	coverage/gotest      go test -json line-delimited output
//	coverage/playwright  Playwright JSON reporter
//	coverage/vitest      Vitest --reporter=json
//	coverage/jest        Jest --json (Jest reporter)
//	coverage/maestro     Maestro JUnit/JSON output
//
// The orchestrator at the package root (Ingest) wires a parser plus a
// store.Store and writes one coverage_runs row + N coverage_results rows
// atomically via store.Coverage.InsertRunWithResults.
//
// Feature ID resolution:
//
//   - When a test name (or extracted annotation) carries an `@atlas:feature
//     <id>` token (canonical) or `@testreg <id>` (legacy), the parser maps
//     that to CoverageResult.FeatureID. Both forms round-trip to the same
//     FeatureID.
//
//   - Otherwise FeatureID stays nil — the result row is recorded but not
//     attributed to a feature (a perfectly normal state for legacy tests).
//
// Symbol resolution (mapping a Go test function to a symbols row) is done
// by the orchestrator via store.Symbols.FindByQualifiedName at ingest
// time, not by the parser. Parsers stay framework-pure: input bytes →
// raw rows, no DB.
package coverage
