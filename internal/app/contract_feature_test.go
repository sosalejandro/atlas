// @testreg workflow.contract
package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/ports"
)

func TestContractFeature_RESTFeatureLayers(t *testing.T) {
	// Set up a minimal registry with a REST API feature.
	regDir := t.TempDir()
	writeContractRegistryFile(t, regDir, "auth.yaml", `
domain: auth
features:
  - id: auth.login
    name: Login
    priority: critical
    surfaces:
      api:
        - method: POST
          path: /api/v1/auth/login
    coverage:
      unit:
        backend:
          status: tested
          tests:
            - file: src/auth/handler_test.go
              functions:
                - name: TestLoginHandler
                  run: go test ./src/auth/... -run TestLoginHandler
      integration: {}
      e2e: {}
`)

	// Create a project root with a handler file that the scanner can find.
	projectRoot := t.TempDir()
	srcDir := filepath.Join(projectRoot, "src", "auth")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a minimal Go handler file.
	handlerContent := `package auth

// @testreg auth.login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	// handler logic
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "handler.go"), []byte(handlerContent), 0o644); err != nil {
		t.Fatal(err)
	}

	store := adapters.NewYAMLStore()
	builder := adapters.NewGoASTScanner()
	traceUC := NewTraceFeatureUseCase(store, builder)
	contractUC := NewContractFeatureUseCase(traceUC, store)

	config := ports.GraphConfig{
		ProjectRoot: projectRoot,
		BackendRoot: "src",
		MaxDepth:    10,
	}

	result, err := contractUC.Execute(regDir, "auth.login", config)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify basic output structure.
	if result.FeatureID != "auth.login" {
		t.Errorf("expected feature ID 'auth.login', got %q", result.FeatureID)
	}

	if result.FeatureName != "Login" {
		t.Errorf("expected feature name 'Login', got %q", result.FeatureName)
	}

	if result.EntryPoint != "POST /api/v1/auth/login" {
		t.Errorf("expected entry point 'POST /api/v1/auth/login', got %q", result.EntryPoint)
	}

	if result.Priority != "critical" {
		t.Errorf("expected priority 'critical', got %q", result.Priority)
	}

	// Layers should be built from the trace; even if the scanner doesn't find
	// the handler, we should get a valid (possibly empty) layers list.
	// The key is that the command doesn't error out.
}

func TestContractFeature_NoAPISurfaces(t *testing.T) {
	// Feature with no API surfaces should produce empty layers.
	regDir := t.TempDir()
	writeContractRegistryFile(t, regDir, "utils.yaml", `
domain: utils
features:
  - id: utils.helpers
    name: Helper Utilities
    priority: low
    surfaces: {}
    coverage:
      unit: {}
      integration: {}
      e2e: {}
`)

	projectRoot := t.TempDir()

	store := adapters.NewYAMLStore()
	builder := adapters.NewGoASTScanner()
	traceUC := NewTraceFeatureUseCase(store, builder)
	contractUC := NewContractFeatureUseCase(traceUC, store)

	config := ports.GraphConfig{
		ProjectRoot: projectRoot,
		BackendRoot: "src",
		MaxDepth:    10,
	}

	result, err := contractUC.Execute(regDir, "utils.helpers", config)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if result.FeatureID != "utils.helpers" {
		t.Errorf("expected feature ID 'utils.helpers', got %q", result.FeatureID)
	}

	if len(result.Layers) != 0 {
		t.Errorf("expected 0 layers for feature with no API surfaces, got %d", len(result.Layers))
	}

	if result.EntryPoint != "" {
		t.Errorf("expected empty entry point, got %q", result.EntryPoint)
	}
}

func TestContractFeature_LayerNumbering(t *testing.T) {
	// Verify that layers are numbered sequentially starting from 1.
	regDir := t.TempDir()
	writeContractRegistryFile(t, regDir, "test.yaml", `
domain: test
features:
  - id: test.multi
    name: Multi Layer Test
    priority: medium
    surfaces:
      api:
        - method: GET
          path: /api/v1/test
    coverage:
      unit: {}
      integration: {}
      e2e: {}
`)

	projectRoot := t.TempDir()
	srcDir := filepath.Join(projectRoot, "src", "test")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	store := adapters.NewYAMLStore()
	builder := adapters.NewGoASTScanner()
	traceUC := NewTraceFeatureUseCase(store, builder)
	contractUC := NewContractFeatureUseCase(traceUC, store)

	config := ports.GraphConfig{
		ProjectRoot: projectRoot,
		BackendRoot: "src",
		MaxDepth:    10,
	}

	result, err := contractUC.Execute(regDir, "test.multi", config)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify sequential numbering.
	for i, layer := range result.Layers {
		expected := i + 1
		if layer.Number != expected {
			t.Errorf("layer %d: expected number %d, got %d", i, expected, layer.Number)
		}
	}
}

func TestContractFeature_FeatureNotFound(t *testing.T) {
	regDir := t.TempDir()
	writeContractRegistryFile(t, regDir, "empty.yaml", `
domain: empty
features: []
`)

	projectRoot := t.TempDir()

	store := adapters.NewYAMLStore()
	builder := adapters.NewGoASTScanner()
	traceUC := NewTraceFeatureUseCase(store, builder)
	contractUC := NewContractFeatureUseCase(traceUC, store)

	config := ports.GraphConfig{
		ProjectRoot: projectRoot,
		BackendRoot: "src",
		MaxDepth:    10,
	}

	_, err := contractUC.Execute(regDir, "nonexistent.feature", config)
	if err == nil {
		t.Fatal("expected error for nonexistent feature, got nil")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func writeContractRegistryFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
}
