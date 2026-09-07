// Exit codes are a contract, not a detail.
//
// Until this file existed, `os.Exit` appeared exactly once in the whole tree
// -- cmd/atlas/main.go, with a literal 1 -- so every command was binary: it
// worked, or it did not. A caller could not distinguish "I checked, and the
// thing you asked me to gate on does not hold" from "I could not check."
//
// That distinction is not academic in this repository. It is the specific bug
// atlas has shipped, twice:
//
//   - .gitleaks.toml's worktree allowlist was unanchored, so it matched the
//     ABSOLUTE path and any scan rooted inside .claude/worktrees allowlisted
//     its own tree, exiting 0 having read nothing. Eight batches of
//     "secrets: ok" were vacuous.
//   - A scan run with --hash-files=false writes no hash rows, so every path
//     classifies indexfresh.StateAbsent and the diff-joining commands degrade
//     to their fallback. Correct behaviour -- and indistinguishable, from the
//     exit code, from having checked everything and found it clean.
//
// .github/scripts/secret-scan.sh already encodes the fix for the shell half,
// and says why: "a caller must be able to tell 'I found a secret' from 'I
// could not look'. A test that treats any non-zero exit as detection passes
// when the scanner is simply absent." The binary never got it. packages/
// indexfresh models five states precisely so callers can tell stale from
// unknowable, and then the CLI collapsed all five into 1.
//
// The rule this file encodes: a command returns ExitUndetermined only when it
// was asked to DECIDE something it cannot decide. Reporting a caveat is not
// undetermined -- printing "3 files are stale" beside a number nobody is
// gating on is information, and exiting non-zero for it would train users to
// ignore the code. It is the gate that turns a caveat into a refusal.
package cli

import (
	"errors"
	"fmt"
)

// The process status contract. A CI job that treats any non-zero as failure
// still fails closed; a job that wants to tell "atlas found untested code"
// from "atlas could not tell" now can.
const (
	// ExitOK means the command ran and whatever it gates on holds.
	ExitOK = 0

	// ExitFinding means the command checked, and the finding is real: the
	// coverage is below the floor, the check tripped, the drift exists.
	// This is also the default for an unclassified error, so a command that
	// has not been taught the contract keeps its old behaviour.
	ExitFinding = 1

	// ExitUsage means the invocation was wrong -- a missing required flag, an
	// unparseable value, an impossible combination. Nothing was measured, and
	// nothing about the codebase is implied.
	ExitUsage = 2

	// ExitUndetermined means the command could not reach a verdict: the index
	// is stale or absent, an input could not be read, a dependency was
	// missing. The distinction from ExitFinding is the entire point of this
	// file -- a gate that cannot run must not look like a gate that passed,
	// and must not look like one that failed either.
	ExitUndetermined = 3
)

// ExitError carries a process status alongside an error. Commands return it
// from RunE like any other error; cmd/atlas/main.go asks ExitCodeFor what to
// exit with. Wrapping is preserved, so errors.Is and errors.As still reach
// whatever the command wrapped.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// undeterminedf builds an ExitUndetermined error. The message should say what
// could not be established and what would establish it -- "the index is
// stale" is a diagnosis the user cannot act on, "run atlas scan" is one they
// can.
func undeterminedf(format string, a ...any) error {
	return &ExitError{Code: ExitUndetermined, Err: fmt.Errorf(format, a...)}
}

// usagef builds an ExitUsage error, for a command rejecting its own flags.
// Cobra's own parse failures are classified by the FlagErrorFunc set in
// NewRootCmd, which wraps them the same way.
func usagef(format string, a ...any) error {
	return &ExitError{Code: ExitUsage, Err: fmt.Errorf(format, a...)}
}

// ExitCodeFor maps an error from Execute onto the contract above.
//
// The default is ExitFinding rather than a distinct "internal error" code, on
// purpose: an unclassified failure is more safely read as "something is
// wrong" than as "nothing was determined", and every command that has not yet
// been taught the contract keeps exactly the behaviour it had.
func ExitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitFinding
}
