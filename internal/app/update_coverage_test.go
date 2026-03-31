// @testreg workflow.update
package app

import (
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

func TestUpdateCoverageExecute(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewUpdateCoverageUseCase(store, store)

	// Simulate passing test results for auth.login
	results := []ports.TestResult{
		{FeatureID: "auth.login", FilePath: "e2e/auth.spec.ts", Passed: true},
	}

	updateResult, err := uc.Execute(registryDir, results, "web", "e2e")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if updateResult.Processed != 1 {
		t.Errorf("Processed = %d, want 1", updateResult.Processed)
	}

	if updateResult.Updated != 1 {
		t.Errorf("Updated = %d, want 1", updateResult.Updated)
	}

	// Verify the feature was actually updated in the registry
	reg, err := store.LoadAll(registryDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	feature, err := reg.GetFeature("auth.login")
	if err != nil {
		t.Fatalf("GetFeature() error = %v", err)
	}

	if feature.Coverage.E2E.Web == nil {
		t.Fatal("Expected E2E.Web entry to exist after update")
	}

	if feature.Coverage.E2E.Web.Status != domain.StatusCovered {
		t.Errorf("E2E.Web.Status = %q, want %q", feature.Coverage.E2E.Web.Status, domain.StatusCovered)
	}

	if feature.Coverage.E2E.Web.PassRate != 1.0 {
		t.Errorf("E2E.Web.PassRate = %f, want 1.0", feature.Coverage.E2E.Web.PassRate)
	}
}

func TestUpdateCoverageFailingTests(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewUpdateCoverageUseCase(store, store)

	// Simulate mixed results: one pass, one fail
	results := []ports.TestResult{
		{FeatureID: "auth.login", FilePath: "e2e/auth-login.spec.ts", Passed: true},
		{FeatureID: "auth.login", FilePath: "e2e/auth-login-error.spec.ts", Passed: false, Error: "timeout"},
	}

	updateResult, err := uc.Execute(registryDir, results, "web", "e2e")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if updateResult.Failures != 1 {
		t.Errorf("Failures = %d, want 1", updateResult.Failures)
	}

	// Verify the feature was marked as failing
	reg, err := store.LoadAll(registryDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	feature, err := reg.GetFeature("auth.login")
	if err != nil {
		t.Fatalf("GetFeature() error = %v", err)
	}

	if feature.Coverage.E2E.Web.Status != domain.StatusFailing {
		t.Errorf("E2E.Web.Status = %q, want %q", feature.Coverage.E2E.Web.Status, domain.StatusFailing)
	}

	if feature.Coverage.E2E.Web.PassRate != 0.5 {
		t.Errorf("E2E.Web.PassRate = %f, want 0.5", feature.Coverage.E2E.Web.PassRate)
	}
}

func TestUpdateCoverageUnmappedResults(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewUpdateCoverageUseCase(store, store)

	results := []ports.TestResult{
		{FeatureID: "", FilePath: "unknown_test.go", Passed: true},
		{FeatureID: "nonexistent.feature", FilePath: "unknown_test.go", Passed: true},
	}

	updateResult, err := uc.Execute(registryDir, results, "backend", "unit")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// First result has empty feature ID -> unmapped
	// Second result has non-existent feature -> also unmapped
	if updateResult.Unmapped != 2 {
		t.Errorf("Unmapped = %d, want 2", updateResult.Unmapped)
	}

	if updateResult.Updated != 0 {
		t.Errorf("Updated = %d, want 0", updateResult.Updated)
	}
}

func TestUpdateCoverageIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewUpdateCoverageUseCase(store, store)

	results := []ports.TestResult{
		{FeatureID: "auth.login", FilePath: "e2e/auth.spec.ts", Passed: true},
	}

	// Run twice
	if _, err := uc.Execute(registryDir, results, "web", "e2e"); err != nil {
		t.Fatalf("First Execute() error = %v", err)
	}

	updateResult, err := uc.Execute(registryDir, results, "web", "e2e")
	if err != nil {
		t.Fatalf("Second Execute() error = %v", err)
	}

	// Second run should show no updates (status already covered)
	if updateResult.Updated != 0 {
		t.Errorf("Second run Updated = %d, want 0 (idempotent)", updateResult.Updated)
	}
}
