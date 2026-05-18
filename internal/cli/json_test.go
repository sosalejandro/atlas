package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestEmitJSON_StableEnvelope verifies the JSON envelope every subcommand
// emits has the required top-level keys (schema_version, command,
// generated_at, result) and that schema_version is pinned to "v1".
//
// This is the load-bearing contract from docs/architecture.md §6 — every
// consumer (bmad-cli, dashboards, CI integrations) pins on it.
func TestEmitJSON_StableEnvelope(t *testing.T) {
	var buf bytes.Buffer

	args := map[string]any{"foo": "bar", "n": 42}
	result := map[string]any{"items": []string{"a", "b", "c"}}
	warnings := []string{"one", "two"}

	if err := emitJSON(&buf, "test", args, result, warnings); err != nil {
		t.Fatalf("emitJSON: %v", err)
	}

	var got envelope
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw:\n%s", err, buf.String())
	}
	if got.SchemaVersion != "v1" {
		t.Fatalf("schema_version = %q, want v1", got.SchemaVersion)
	}
	if got.Command != "test" {
		t.Fatalf("command = %q, want test", got.Command)
	}
	if _, err := time.Parse(time.RFC3339, got.GeneratedAt); err != nil {
		t.Fatalf("generated_at %q not RFC3339: %v", got.GeneratedAt, err)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings len = %d, want 2", len(got.Warnings))
	}
	// Confirm we use 2-space indentation so the human-friendly fallback
	// (cat the JSON to a terminal) stays readable.
	if !strings.Contains(buf.String(), "  \"schema_version\"") {
		t.Fatalf("expected 2-space indentation in JSON output:\n%s", buf.String())
	}
}

// TestEmitJSON_NilWarnings confirms a nil warnings slice does NOT render
// an empty `"warnings": []` field — the JSON should omit it entirely so
// the on-the-wire payload stays tight.
func TestEmitJSON_NilWarnings(t *testing.T) {
	var buf bytes.Buffer
	if err := emitJSON(&buf, "test", nil, map[string]int{"n": 1}, nil); err != nil {
		t.Fatalf("emitJSON: %v", err)
	}
	if strings.Contains(buf.String(), "warnings") {
		t.Fatalf("expected `warnings` key to be omitted on nil slice; got:\n%s", buf.String())
	}
}
