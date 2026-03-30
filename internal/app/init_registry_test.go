package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
)

func TestInitRegistryExecute(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	uc := NewInitRegistryUseCase(store, store)

	// First run: should create files
	if err := uc.Execute(registryDir); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Verify files were created
	entries, err := os.ReadDir(registryDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("Expected YAML files to be created, got none")
	}

	// Verify we can load the registry back
	reg, err := store.LoadAll(registryDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(reg.Domains) == 0 {
		t.Fatal("Expected non-empty registry after init")
	}

	// Check that template domains exist
	domainNames := make(map[string]bool)
	for _, d := range reg.Domains {
		domainNames[d.Domain] = true
	}

	for _, expected := range []string{"auth", "meals", "profile"} {
		if !domainNames[expected] {
			t.Errorf("Expected domain %q not found in registry", expected)
		}
	}
}

func TestInitRegistryIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	uc := NewInitRegistryUseCase(store, store)

	// First run
	if err := uc.Execute(registryDir); err != nil {
		t.Fatalf("First Execute() error = %v", err)
	}

	reg1, err := store.LoadAll(registryDir)
	if err != nil {
		t.Fatalf("LoadAll() after first init error = %v", err)
	}
	count1 := len(reg1.AllFeatures())

	// Second run — should not duplicate features
	if err := uc.Execute(registryDir); err != nil {
		t.Fatalf("Second Execute() error = %v", err)
	}

	reg2, err := store.LoadAll(registryDir)
	if err != nil {
		t.Fatalf("LoadAll() after second init error = %v", err)
	}
	count2 := len(reg2.AllFeatures())

	if count1 != count2 {
		t.Errorf("Feature count changed after second init: %d -> %d", count1, count2)
	}
}
