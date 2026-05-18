// @testreg report.generate
package app

import (
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/internal/adapters"
)

func TestGenerateReportExecute(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	uc := NewGenerateReportUseCase(store)
	report, err := uc.Execute(registryDir, tmpDir)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if report.GeneratedAt == "" {
		t.Error("Expected non-empty GeneratedAt")
	}

	if report.ProjectRoot != tmpDir {
		t.Errorf("ProjectRoot = %q, want %q", report.ProjectRoot, tmpDir)
	}

	if report.Metrics.TotalFeatures == 0 {
		t.Error("Expected non-zero TotalFeatures in report metrics")
	}

	if len(report.Domains) == 0 {
		t.Error("Expected non-empty Domains in report")
	}

	// Verify each domain has features with status entries
	for _, d := range report.Domains {
		if d.Name == "" {
			t.Error("Domain with empty name")
		}
		for _, f := range d.Features {
			if f.ID == "" {
				t.Errorf("Feature with empty ID in domain %q", d.Name)
			}
			if len(f.Status) == 0 {
				t.Errorf("Feature %q has no status entries", f.ID)
			}
		}
	}
}

func TestGenerateReportEmptyRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "empty-registry")

	// Create empty registry dir
	store := adapters.NewYAMLStore()
	uc := NewGenerateReportUseCase(store)

	report, err := uc.Execute(registryDir, tmpDir)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if report.Metrics.TotalFeatures != 0 {
		t.Errorf("Expected 0 TotalFeatures for empty registry, got %d", report.Metrics.TotalFeatures)
	}
}
