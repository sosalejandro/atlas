package pyscan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// This file isolates every os/exec call site so the validation and
// allow-listing rules below are easy to audit. The same three invariants
// that tsscan/exec.go documents apply verbatim here:
//
//  1. The binary path used by newPythonCommand is always an absolute path
//     returned by exec.LookPath after running through validatePythonBin
//     (no shell metacharacters, must exist + be executable on the local
//     filesystem). The pre-LookPath form (relative names like "python3")
//     is validated by the relaxed validatePythonBinName.
//  2. The args slice is the output of buildScannerArgs (see scanner.go) —
//     every entry was either a constant flag literal or a value that
//     passed validateScannerArg.
//  3. exec.CommandContext is called variadically (argv-style), never via
//     a shell wrapper, so command-substitution / pipe / redirection are
//     not interpreted by the runtime.
//
// Together these eliminate the OS-command-injection vector. The
// nosemgrep / gosec directives below document that this is the single
// reviewed call site.

// fallbackPythonBin returns the binary name used in user-facing warnings
// (when LookPath fails). Centralised so the test helper can stay aligned.
func fallbackPythonBin(configured string) string {
	if configured == "" {
		return "python3"
	}
	return configured
}

// resolvePythonBin returns the absolute path to the configured Python
// binary (or "python3" by default) and a bool indicating whether it was
// found. The returned path always satisfies validatePythonBin (strict
// form: requires an absolute path with no shell metacharacters).
func resolvePythonBin(ctx context.Context, logger shared.Logger, configured string) (string, bool) {
	candidate := fallbackPythonBin(configured)
	// Pre-LookPath: allow relative names like "python3" so PATH lookup
	// can run. Only reject obviously-unsafe input here.
	if err := validatePythonBinName(candidate); err != nil {
		logger.Warn(ctx, "rejected pythonBin", "pythonBin", candidate, "err", err.Error())
		return "", false
	}
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		logger.Warn(ctx, "python3 runtime not found; skipping Python scan",
			"pythonBin", candidate, "err", err.Error())
		return "", false
	}
	// LookPath may return a relative path on some platforms; force absolute
	// so the downstream exec.Command call never resolves via CWD.
	abs, err := filepath.Abs(resolved)
	if err != nil {
		logger.Warn(ctx, "abs pythonBin failed", "resolved", resolved, "err", err.Error())
		return "", false
	}
	// Post-LookPath: strict form. The path MUST be absolute now. This is
	// the invariant the package header advertises and downstream
	// newPythonCommand relies on.
	if err := validatePythonBin(abs); err != nil {
		logger.Warn(ctx, "resolved pythonBin failed validation",
			"resolved", abs, "err", err.Error())
		return "", false
	}
	return abs, true
}

// validatePythonBinName is the relaxed pre-LookPath validator. It rejects
// empty strings and shell metacharacters but allows relative names like
// "python3" (which exec.LookPath will resolve against $PATH).
//
// Used only inside resolvePythonBin before the LookPath call. Every other
// call site MUST use validatePythonBin (the strict form) so the invariant
// "the binary path newPythonCommand sees is always absolute" holds.
func validatePythonBinName(s string) error {
	if s == "" {
		return errors.New("empty pythonBin")
	}
	if strings.ContainsAny(s, "\x00\n\r`$;|&<>\"'") {
		return fmt.Errorf("pythonBin %q contains forbidden character", s)
	}
	return nil
}

// validatePythonBin is the strict post-LookPath validator. In addition to
// the metacharacter checks performed by validatePythonBinName, it requires
// the path to be absolute — this is the contract newPythonCommand relies
// on so a future caller that bypassed LookPath would be caught here
// rather than silently spawning via $PATH resolution at the OS level.
func validatePythonBin(s string) error {
	if err := validatePythonBinName(s); err != nil {
		return err
	}
	if !filepath.IsAbs(s) {
		return fmt.Errorf("pythonBin %q must be absolute (post-LookPath invariant)", s)
	}
	return nil
}

// newPythonCommand is the ONLY place in the package that calls exec.Command*.
// All inputs are validated upstream (see invariants in the file header).
//
// We deliberately use os/exec to spawn the embedded Python scanner; the
// binary name (validated by resolvePythonBin -> validatePythonBin, which
// also enforces filepath.IsAbs) and args (validated by buildScannerArgs /
// validateScannerArg) are CLI-controlled config, never raw user input.
//
//nolint:gosec // bin + args validated by resolvePythonBin / buildScannerArgs; bin is filepath.IsAbs
// nosemgrep: go.lang.security.audit.dangerous-command-write
// nosemgrep: go.lang.security.audit.dangerous-exec-command
// nosemgrep: rules.dangerous-command-write
// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
func newPythonCommand(ctx context.Context, bin string, args []string) (*exec.Cmd, error) {
	if err := validatePythonBin(bin); err != nil {
		return nil, fmt.Errorf("pyscan: invalid python binary: %w", err)
	}
	for i, a := range args {
		// Allow flag literals (start with --); everything else must have
		// passed validateScannerArg or be the absolute script path / root.
		if strings.HasPrefix(a, "--") {
			continue
		}
		if err := validateScannerArg(a); err != nil {
			return nil, fmt.Errorf("pyscan: arg %d failed validation: %w", i, err)
		}
	}
	// nosemgrep: go.lang.security.audit.dangerous-command-write
	// nosemgrep: go.lang.security.audit.dangerous-exec-command
	// nosemgrep: rules.dangerous-command-write
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	return exec.CommandContext(ctx, bin, args...), nil //nolint:gosec // see function comment
}
