package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	tsscan "github.com/sosalejandro/atlas/packages/codeindex/ts"
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
	if env.SchemaVersion != "v1.1" {
		t.Fatalf("schema_version = %q; want v1.1", env.SchemaVersion)
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

// TestRunTrace_ReactRouter_LangTagging confirms the Phase-2 addition: trace
// over a TS-only fixture emits chain entries with `lang: "ts"`. This is the
// dispatch's acceptance criterion #3.
func TestRunTrace_ReactRouter_LangTagging(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not on PATH: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	atlasNM, err := filepath.Abs(filepath.Join(wd, "..", "..", "node_modules"))
	if err != nil {
		t.Fatalf("abs node_modules: %v", err)
	}
	if _, err := os.Stat(filepath.Join(atlasNM, "typescript")); err != nil {
		t.Skipf("atlas node_modules/typescript not installed: %v", err)
	}

	root := filepath.Join("..", "..", "packages", "codeindex", "ts", "testdata", "react-router")
	var buf bytes.Buffer
	err = runTraceWithOpts(context.Background(), &buf, root, "route:/login", 10, codeindex.Options{
		TSOptions: tsscan.Options{NodeModulesPaths: []string{atlasNM}},
	})
	if err != nil {
		t.Fatalf("runTraceWithOpts: %v", err)
	}

	var env traceEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if env.SchemaVersion != "v1.1" {
		t.Fatalf("schema_version = %q; want v1.1", env.SchemaVersion)
	}
	// Expect the root to be a route node tagged ts.
	if env.Data.Root != "route:/login" {
		t.Fatalf("root = %q; want route:/login", env.Data.Root)
	}
	hasTS := false
	for _, e := range env.Data.Chain {
		if e.Lang == "ts" {
			hasTS = true
			break
		}
	}
	if !hasTS {
		t.Fatalf("expected at least one chain entry with lang=ts; got chain=%+v", env.Data.Chain)
	}
}
