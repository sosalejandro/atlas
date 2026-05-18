package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRunTrace_SampleProject(t *testing.T) {
	t.Parallel()

	// Use the goscan testdata project as a self-contained fixture.
	root := filepath.Join("..", "..", "packages", "codeindex", "go", "testdata", "sampleproject")
	var buf bytes.Buffer
	if err := runTrace(context.Background(), &buf, root, "AuthHandler.Login", 10); err != nil {
		t.Fatalf("runTrace: %v", err)
	}

	var env traceEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if env.SchemaVersion != "v1" {
		t.Fatalf("schema_version = %q; want v1", env.SchemaVersion)
	}
	if env.Command != "trace" {
		t.Fatalf("command = %q; want trace", env.Command)
	}
	if env.Data.Root != "AuthHandler.Login" {
		t.Fatalf("root = %q; want AuthHandler.Login", env.Data.Root)
	}
	if len(env.Data.Chain) < 3 {
		t.Fatalf("expected chain to include 3 hops (handler→service→repo); got %d:\n%s", len(env.Data.Chain), buf.String())
	}
}

func TestRunTrace_MissingFeature_Error(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "packages", "codeindex", "go", "testdata", "sampleproject")
	var buf bytes.Buffer
	err := runTrace(context.Background(), &buf, root, "does.not.exist", 10)
	if err == nil {
		t.Fatal("expected error for unknown feature")
	}
}
