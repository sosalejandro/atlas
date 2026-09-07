// Package envelope is the JSON contract every atlas surface answers in.
//
// It exists because atlas is about to have two of them. The CLI's `--json`
// and the HTTP API (#101) return the same shapes computed by the same
// packages, and the only durable way to keep them from drifting is for the
// envelope to be one type that both import rather than two that happen to
// match today.
//
// The alternative was tried by the surface this replaces: the testreg-era
// dashboard in internal/server/ read registry YAML and never touched
// packages/store, so it answered a different question from the CLI using
// different code, and drifted until it could no longer see features,
// statement coverage, edges or snapshots at all. Three thousand lines that
// still compile and cannot tell you anything true.
package envelope

// SchemaVersion is the stable contract version every envelope emits.
//
// Additive within a major: new fields can appear without bumping; removals
// or type changes bump it. Per docs/architecture.md section 6.
const SchemaVersion = "v1"

// Envelope is the top-level object every JSON answer is wrapped in.
//
// Command is the dotted verb path ("health", "cov.sync", "codebase.find")
// for the CLI, and the route ("api.features") for the HTTP surface -- one
// vocabulary, so a consumer reading a recorded envelope can tell what
// produced it without being told.
type Envelope struct {
	SchemaVersion string   `json:"schema_version"`
	Command       string   `json:"command"`
	Args          any      `json:"args,omitempty"`
	Result        any      `json:"result"`
	Warnings      []string `json:"warnings,omitempty"`
	// GeneratedAt is omitted in the stable form: it is the single field
	// that makes every envelope differ from itself between runs, so a
	// digest or a checked-in artifact containing it certifies nothing.
	// See Stable and docs/determinism-and-comparison.md.
	GeneratedAt string `json:"generated_at,omitempty"`
}

// New builds an envelope. generatedAt is passed in rather than read from the
// clock here, so a caller that wants the stable form never has to produce a
// timestamp only to discard it.
func New(command string, args, result any, warnings []string, generatedAt string) Envelope {
	return Envelope{
		SchemaVersion: SchemaVersion,
		Command:       command,
		Args:          args,
		Result:        result,
		Warnings:      warnings,
		GeneratedAt:   generatedAt,
	}
}

// Stable returns the envelope with every field whose value depends on WHEN
// or WHERE it was produced removed, so two runs over the same code produce
// identical bytes.
func (e Envelope) Stable() (Envelope, error) {
	out := e
	out.GeneratedAt = ""
	stripped, err := StripVolatile(struct {
		Args   any `json:"args,omitempty"`
		Result any `json:"result"`
	}{Args: e.Args, Result: e.Result})
	if err != nil {
		return Envelope{}, err
	}
	m, ok := stripped.(map[string]any)
	if !ok {
		return Envelope{}, errNotAnObject
	}
	out.Args = m["args"]
	out.Result = m["result"]
	return out, nil
}
