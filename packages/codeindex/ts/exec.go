package tsscan

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
// allow-listing rules below are easy to audit. Three invariants:
//
//  1. The binary path used by newNodeCommand is always an absolute path
//     returned by exec.LookPath after running through validateNodeBin
//     (no shell metacharacters, must exist + be executable on the local
//     filesystem). The pre-LookPath form (relative names like "node")
//     is validated by the relaxed validateNodeBinName.
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

// fallbackNodeBin returns the binary name used in user-facing warnings
// (when LookPath fails). Centralised so the test helper can stay aligned.
func fallbackNodeBin(configured string) string {
	if configured == "" {
		return "node"
	}
	return configured
}

// resolveNodeBin returns the absolute path to the configured Node binary
// (or "node" by default) and a bool indicating whether it was found. The
// returned path always satisfies validateNodeBin (strict form: requires
// an absolute path with no shell metacharacters).
func resolveNodeBin(ctx context.Context, logger shared.Logger, configured string) (string, bool) {
	candidate := fallbackNodeBin(configured)
	// Pre-LookPath: allow relative names like "node" so PATH lookup can
	// run. Only reject obviously-unsafe input here.
	if err := validateNodeBinName(candidate); err != nil {
		logger.Warn(ctx, "rejected nodeBin", "nodeBin", candidate, "err", err.Error())
		return "", false
	}
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		logger.Warn(ctx, "node runtime not found; skipping TS scan",
			"nodeBin", candidate, "err", err.Error())
		return "", false
	}
	// LookPath may return a relative path on some platforms; force absolute
	// so the downstream exec.Command call never resolves via CWD.
	abs, err := filepath.Abs(resolved)
	if err != nil {
		logger.Warn(ctx, "abs nodeBin failed", "resolved", resolved, "err", err.Error())
		return "", false
	}
	// Post-LookPath: strict form. The path MUST be absolute now. This is
	// the invariant the package header advertises and downstream
	// newNodeCommand relies on.
	if err := validateNodeBin(abs); err != nil {
		logger.Warn(ctx, "resolved nodeBin failed validation",
			"resolved", abs, "err", err.Error())
		return "", false
	}
	return abs, true
}

// validateNodeBinName is the relaxed pre-LookPath validator. It rejects
// empty strings and shell metacharacters but allows relative names like
// "node" (which exec.LookPath will resolve against $PATH).
//
// Used only inside resolveNodeBin before the LookPath call. Every other
// call site MUST use validateNodeBin (the strict form) so the invariant
// "the binary path newNodeCommand sees is always absolute" holds.
func validateNodeBinName(s string) error {
	if s == "" {
		return errors.New("empty nodeBin")
	}
	if strings.ContainsAny(s, "\x00\n\r`$;|&<>\"'") {
		return fmt.Errorf("nodeBin %q contains forbidden character", s)
	}
	return nil
}

// validateNodeBin is the strict post-LookPath validator. In addition to
// the metacharacter checks performed by validateNodeBinName, it requires
// the path to be absolute — this is the contract newNodeCommand relies
// on so a future caller that bypassed LookPath would be caught here
// rather than silently spawning via $PATH resolution at the OS level.
func validateNodeBin(s string) error {
	if err := validateNodeBinName(s); err != nil {
		return err
	}
	if !filepath.IsAbs(s) {
		return fmt.Errorf("nodeBin %q must be absolute (post-LookPath invariant)", s)
	}
	return nil
}

// newNodeCommand is the ONLY place in the package that calls exec.Command*.
// All inputs are validated upstream (see invariants in the file header).
//
// We deliberately use os/exec to spawn the embedded TypeScript scanner; the
// binary name (validated by resolveNodeBin -> validateNodeBin, which now
// also enforces filepath.IsAbs) and args (validated by buildScannerArgs /
// validateScannerArg) are CLI-controlled config, never raw user input.
// The function-level block below documents WHY this is safe; the call-site
// suppressions below document WHICH specific scanner rule each line trips
// (the local semgrep MCP hook flags the exec.CommandContext line itself,
// so per-call-site suppressions are load-bearing — not redundant).
//
//nolint:gosec // bin + args validated by resolveNodeBin / buildScannerArgs; bin is filepath.IsAbs
// nosemgrep: go.lang.security.audit.dangerous-command-write
// nosemgrep: go.lang.security.audit.dangerous-exec-command
// nosemgrep: rules.dangerous-command-write
func newNodeCommand(ctx context.Context, bin string, args []string) (*exec.Cmd, error) {
	if err := validateNodeBin(bin); err != nil {
		return nil, fmt.Errorf("tsscan: invalid node binary: %w", err)
	}
	for i, a := range args {
		// Allow flag literals (start with --); everything else must have
		// passed validateScannerArg or be the absolute script path / root.
		if strings.HasPrefix(a, "--") {
			continue
		}
		if err := validateScannerArg(a); err != nil {
			return nil, fmt.Errorf("tsscan: arg %d failed validation: %w", i, err)
		}
	}
	// Call-site suppressions (specific scanner rule ids — see function
	// doc above for the "why"; these lines exist for the "which rule").
	// nosemgrep: go.lang.security.audit.dangerous-command-write
	// nosemgrep: go.lang.security.audit.dangerous-exec-command
	// nosemgrep: rules.dangerous-command-write
	return exec.CommandContext(ctx, bin, args...), nil //nolint:gosec // see function comment
}
