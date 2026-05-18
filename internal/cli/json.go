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
)

// schemaVersion is the stable contract version every JSON envelope emits.
//
// Per docs/architecture.md §6, this is the additive-within-major contract
// across every subcommand: new fields can appear without bumping; removals
// or type changes bump the major.
const schemaVersion = "v1"

// envelope is the top-level JSON object every `--json` invocation emits.
//
// schema_version is pinned to "v1". `command` is the dotted verb path
// ("audit", "cov.sync", "codebase.find"). `args` carries the cobra-parsed
// flags + positional arguments — useful for the consumer to see what the
// CLI thought it was being asked to do without re-parsing argv.
type envelope struct {
	SchemaVersion string   `json:"schema_version"`
	Command       string   `json:"command"`
	Args          any      `json:"args,omitempty"`
	Result        any      `json:"result"`
	Warnings      []string `json:"warnings,omitempty"`
	GeneratedAt   string   `json:"generated_at"`
}

// emitJSON writes the standard envelope around `result` to `w` with the
// supplied command tag + arg payload. Warnings is nil-safe.
//
// Returns an error when JSON encoding fails — callers MUST propagate so
// the caller's RunE returns a non-zero exit.
func emitJSON(w io.Writer, command string, args any, result any, warnings []string) error {
	env := envelope{
		SchemaVersion: schemaVersion,
		Command:       command,
		Args:          args,
		Result:        result,
		Warnings:      warnings,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		return fmt.Errorf("emit json: %w", err)
	}
	return nil
}
