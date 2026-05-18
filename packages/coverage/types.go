package coverage

import (
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
)

// Status mirrors store.CoverageStatus but lives in the parser layer so
// framework packages do not have to import store. The orchestrator maps
// Status → store.CoverageStatus on persistence.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Framework is the closed enum of supported parsers. It matches
// store.Framework so a Run is trivially persistable.
type Framework string

const (
	FrameworkGoTest     Framework = "go-test"
	FrameworkPlaywright Framework = "playwright"
	FrameworkVitest     Framework = "vitest"
	FrameworkJest       Framework = "jest"
	FrameworkMaestro    Framework = "maestro"
)

// Run is the parser-level summary of a single test execution.
//
// StartedAt / FinishedAt are best-effort: not every framework reports
// wall-clock times so parsers may leave them zero — the orchestrator fills
// them with time.Now().UTC() before persisting.
type Run struct {
	Framework   Framework
	StartedAt   time.Time
	FinishedAt  time.Time
	SummaryJSON string
}

// Result is one test row as emitted by a parser, before the orchestrator
// resolves it against the symbols + features tables.
//
// TestName is the framework-native identifier (e.g. "TestLogin" for go
// test, "auth › login › valid input" for Playwright). It is preserved on
// the row so failures can be looked up by name without a JOIN.
//
// QualifiedSymbol is the Go-style "pkg.Func" / "Receiver.Method" form when
// the parser can derive it (gotest only); the orchestrator passes this to
// store.Symbols.FindByQualifiedName to populate the symbol_id FK.
//
// FilePath is the repo-relative source file the test lives in (best-effort
// per framework). Empty for go-test because `go test -json` reports a
// package, not a file.
//
// FeatureID is the dot-namespaced feature this test attests, derived
// either from an @atlas:feature / @testreg annotation embedded in the
// test name, or from heuristics on the path/test name. Nil when no
// signal is available.
type Result struct {
	TestName        string
	QualifiedSymbol shared.SymbolID
	FilePath        string
	FeatureID       *shared.FeatureID
	Status          Status
	Duration        time.Duration
	Message         string
}
