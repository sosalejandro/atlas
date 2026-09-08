// Package cli implements the Atlas CLI command dispatch on top of cobra.
//
// Every subcommand lives in its own file (init.go, scan.go, trace.go, ...).
// The shared output envelope and config-loader live here so the per-verb
// files stay short.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sosalejandro/atlas/packages/envelope"
)

// schemaVersion is the stable contract version every JSON envelope emits.
//
// It is packages/envelope's, not a second copy: the HTTP API answers in the
// same envelope, and a version the two surfaces could disagree about would
// make the contract meaningless.
const schemaVersion = envelope.SchemaVersion

// emitJSON writes the standard envelope around `result` to `w` with the
// supplied command tag + arg payload. Warnings is nil-safe.
//
// Under --stable every field whose value depends on when or where the
// command ran is dropped, so two runs over the same code produce identical
// bytes. See docs/determinism-and-comparison.md.
//
// Returns an error when JSON encoding fails — callers MUST propagate so
// the caller's RunE returns a non-zero exit.
func emitJSON(w io.Writer, command string, args any, result any, warnings []string) error {
	env := envelope.New(command, args, result, warnings,
		time.Now().UTC().Format(time.RFC3339))
	if flags.Stable {
		stable, err := env.Stable()
		if err != nil {
			return fmt.Errorf("emit json: %w", err)
		}
		env = stable
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return fmt.Errorf("emit json: %w", err)
	}
	return nil
}
