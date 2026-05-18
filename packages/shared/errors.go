package shared

import "errors"

// Sentinel errors. Callers test with errors.Is(err, shared.ErrXxx).
//
// Per docs/architecture.md §8 we deliberately keep this list short and flat
// (no typed hierarchy). New sentinels land here only when a caller actually
// needs to branch on the failure mode.
var (
	// ErrNotFound is returned by lookup-style APIs when the requested key
	// doesn't exist (e.g. graph.Graph lookup by SymbolID with no match).
	ErrNotFound = errors.New("not found")

	// ErrFeatureNotFound — a FeatureID was referenced but Atlas could not
	// find any annotation declaring it. Surfaced by `atlas trace`,
	// `atlas audit`.
	ErrFeatureNotFound = errors.New("feature not found")

	// ErrSymbolNotFound — a SymbolID was referenced but no scanner has
	// produced a record for it.
	ErrSymbolNotFound = errors.New("symbol not found")

	// ErrAnnotationInvalid — the annotation parser hit a line whose
	// grammar it could not accept (e.g. `@atlas:feature` with no IDs).
	// The error wraps a per-position diagnostic so the CLI can print
	// `file:line: <message>`.
	ErrAnnotationInvalid = errors.New("annotation invalid")

	// ErrUnsupportedFile — a parser was asked to handle an extension it
	// does not recognise. The codeindex orchestrator uses this to decide
	// whether to skip silently or warn.
	ErrUnsupportedFile = errors.New("unsupported file type")
)
