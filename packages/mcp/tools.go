package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Limits caps every result set this server can return.
//
// A cap is not a nicety here. `callers()` on a hot utility in a real monorepo
// returns thousands of rows; unbounded, one call evicts the agent's working
// context and the session gets worse at the task it was doing. The cap is the
// server's, not the caller's: a per-call `limit` may only narrow it (see
// clamp), so a client cannot ask its way out of the bound.
type Limits struct {
	MaxFeatures int `json:"max_features"`
	MaxSymbols  int `json:"max_symbols"`
	MaxEdges    int `json:"max_edges"`
	MaxTests    int `json:"max_tests"`
}

// Default caps. They are sized so a full result still leaves room for the
// agent to think: a hundred call edges is already more than a model will
// meaningfully reason over in one step, and a surface bigger than 200 symbols
// is a signal to narrow the question, not to read further.
func (l Limits) withDefaults() Limits {
	if l.MaxFeatures <= 0 {
		l.MaxFeatures = 50
	}
	if l.MaxSymbols <= 0 {
		l.MaxSymbols = 200
	}
	if l.MaxEdges <= 0 {
		l.MaxEdges = 100
	}
	if l.MaxTests <= 0 {
		l.MaxTests = 100
	}
	return l
}

// tool is one entry in tools/list plus the handler tools/call dispatches to.
type tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Handle      func(ctx context.Context, a *toolArgs) (any, error)
}

// descriptor is the tools/list projection — everything except the handler.
//
// annotations declare what this whole surface is: readOnlyHint tells a client
// it never needs a write confirmation, and openWorldHint false says every
// answer comes from the local index rather than the network, which is what
// makes the results reproducible.
func (t tool) descriptor() map[string]any {
	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": t.Description,
		"inputSchema": t.InputSchema,
		"annotations": map[string]any{
			"title":           t.Title,
			"readOnlyHint":    true,
			"destructiveHint": false,
			"idempotentHint":  true,
			"openWorldHint":   false,
		},
	}
}

// argError marks a caller mistake in the ARGUMENTS — a missing required
// field, a string where a number belongs. These become JSON-RPC -32602, not a
// tool result, because the model must change the CALL rather than its
// question. Everything else a handler returns becomes an isError result.
type argError struct{ msg string }

func (e argError) Error() string { return e.msg }

func badArg(format string, a ...any) error { return argError{msg: fmt.Sprintf(format, a...)} }

// toolArgs is the decoded `arguments` object of a tools/call.
//
// Decoding is per-field rather than into a struct so a wrong TYPE is reported
// as the specific argument that was wrong. json.Unmarshal into a struct
// reports "cannot unmarshal string into field of type int", which tells an
// agent nothing about which of its arguments to fix.
type toolArgs struct {
	tool   string
	fields map[string]json.RawMessage
}

func parseToolArgs(name string, raw json.RawMessage) (*toolArgs, error) {
	a := &toolArgs{tool: name, fields: map[string]json.RawMessage{}}
	if len(raw) == 0 || string(raw) == "null" {
		return a, nil
	}
	if err := json.Unmarshal(raw, &a.fields); err != nil {
		return nil, badArg("%s: `arguments` must be a JSON object of named arguments", name)
	}
	return a, nil
}

func (a *toolArgs) requiredString(name string) (string, error) {
	raw, ok := a.fields[name]
	if !ok {
		return "", badArg("%s: required argument %q is missing", a.tool, name)
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", badArg("%s: argument %q must be a string, got %s", a.tool, name, raw)
	}
	if out == "" {
		return "", badArg("%s: argument %q must not be empty", a.tool, name)
	}
	return out, nil
}

// optionalLimit reads a caller-supplied `limit` and clamps it to the server
// cap. A caller asking for a million rows gets `capValue` — and, because the
// result set is then cut, a Truncation block telling it so.
func (a *toolArgs) optionalLimit(capValue int) (int, error) {
	raw, ok := a.fields["limit"]
	if !ok {
		return capValue, nil
	}
	var out int
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, badArg("%s: argument \"limit\" must be a positive integer, got %s", a.tool, raw)
	}
	if out <= 0 {
		return 0, badArg("%s: argument \"limit\" must be a positive integer, got %d", a.tool, out)
	}
	if out > capValue {
		return capValue, nil
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Schemas
//
// Every tool declares a real inputSchema. An agent cannot use a tool whose
// arguments it has to guess: with no schema it invents plausible field names,
// gets -32602 back, and burns the turn. Every property carries a description
// for the same reason — `feature_id` is not self-explanatory to a model that
// has never seen an atlas annotation.
// ---------------------------------------------------------------------------

func objectSchema(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringProp(description string, examples ...string) map[string]any {
	p := map[string]any{"type": "string", "description": description}
	if len(examples) > 0 {
		p["examples"] = examples
	}
	return p
}

func limitProp(capValue int, what string) map[string]any {
	return map[string]any{
		"type":    "integer",
		"minimum": 1,
		"maximum": capValue,
		"default": capValue,
		"description": fmt.Sprintf(
			"Maximum %s to return. Values above the server cap of %d are clamped to it. "+
				"Whenever the answer is cut, the response carries a `truncated` block saying by how much.",
			what, capValue),
	}
}
