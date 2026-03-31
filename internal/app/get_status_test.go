// @testreg report.status
package app

import (
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/domain"
)

func setupRegistryForStatusTests(t *testing.T) (string, *adapters.YAMLStore) {
	t.Helper()
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	return registryDir, store
}

func TestGetStatusExecuteNoFilter(t *testing.T) {
	registryDir, store := setupRegistryForStatusTests(t)

	uc := NewGetStatusUseCase(store)
	result, err := uc.Execute(registryDir, StatusFilter{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Metrics.TotalFeatures == 0 {
		t.Error("Expected non-zero TotalFeatures")
	}

	if len(result.DomainData) == 0 {
		t.Error("Expected non-empty DomainData")
	}
}

func TestGetStatusFilterByDomain(t *testing.T) {
	registryDir, store := setupRegistryForStatusTests(t)

	uc := NewGetStatusUseCase(store)
	result, err := uc.Execute(registryDir, StatusFilter{Domain: "auth"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(result.DomainData) != 1 {
		t.Errorf("Expected 1 domain row, got %d", len(result.DomainData))
	}

	if result.DomainData[0].Domain != "auth" {
		t.Errorf("Expected domain 'auth', got %q", result.DomainData[0].Domain)
	}
}

func TestGetStatusFilterByPriority(t *testing.T) {
	registryDir, store := setupRegistryForStatusTests(t)

	uc := NewGetStatusUseCase(store)
	result, err := uc.Execute(registryDir, StatusFilter{Priority: domain.PriorityCritical})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	for _, f := range result.Features {
		if f.Priority != domain.PriorityCritical {
			t.Errorf("Feature %q has priority %q, expected critical", f.ID, f.Priority)
		}
	}
}

func TestGetStatusNonExistentDomain(t *testing.T) {
	registryDir, store := setupRegistryForStatusTests(t)

	uc := NewGetStatusUseCase(store)
	result, err := uc.Execute(registryDir, StatusFilter{Domain: "nonexistent"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(result.DomainData) != 0 {
		t.Errorf("Expected 0 domain rows for non-existent domain, got %d", len(result.DomainData))
	}
}
