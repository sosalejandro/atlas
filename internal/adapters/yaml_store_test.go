// @testreg registry.yaml-store
package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/domain"
)

func TestYAMLStoreRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewYAMLStore()

	original := &domain.DomainFile{
		Domain:      "test",
		Description: "Test domain",
		Features: []domain.Feature{
			{
				ID:       "test.feature1",
				Name:     "Feature One",
				Priority: domain.PriorityCritical,
				Surfaces: domain.Surfaces{
					Web: &domain.WebSurface{Route: "/test", Component: "TestPage"},
					API: []domain.APISurface{{Method: "GET", Path: "/api/test"}},
				},
				Coverage: domain.Coverage{
					Unit: domain.UnitCoverage{
						Backend: &domain.CoverageEntry{
							Status: domain.StatusCovered,
							Files:  []string{"test_file.go"},
							Mocked: true,
						},
					},
					E2E: domain.E2ECoverage{
						Web: &domain.E2ECoverageEntry{
							Status:   domain.StatusFailing,
							Files:    []string{"test.spec.ts"},
							LastRun:  "2026-03-30",
							PassRate: 0.75,
						},
					},
				},
			},
		},
	}

	// Save
	if err := store.SaveDomain(tmpDir, original); err != nil {
		t.Fatalf("SaveDomain() error = %v", err)
	}

	// Verify file exists
	filePath := filepath.Join(tmpDir, "test.yaml")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("Expected test.yaml to exist")
	}

	// Load back
	loaded, err := store.LoadDomain(tmpDir, "test")
	if err != nil {
		t.Fatalf("LoadDomain() error = %v", err)
	}

	if loaded.Domain != original.Domain {
		t.Errorf("Domain = %q, want %q", loaded.Domain, original.Domain)
	}

	if len(loaded.Features) != 1 {
		t.Fatalf("Features count = %d, want 1", len(loaded.Features))
	}

	f := loaded.Features[0]
	if f.ID != "test.feature1" {
		t.Errorf("Feature.ID = %q, want %q", f.ID, "test.feature1")
	}

	if f.Coverage.Unit.Backend == nil {
		t.Fatal("Expected Unit.Backend to be non-nil")
	}

	if f.Coverage.Unit.Backend.Status != domain.StatusCovered {
		t.Errorf("Unit.Backend.Status = %q, want %q", f.Coverage.Unit.Backend.Status, domain.StatusCovered)
	}

	if !f.Coverage.Unit.Backend.Mocked {
		t.Error("Expected Unit.Backend.Mocked to be true")
	}

	if f.Coverage.E2E.Web == nil {
		t.Fatal("Expected E2E.Web to be non-nil")
	}

	if f.Coverage.E2E.Web.PassRate != 0.75 {
		t.Errorf("E2E.Web.PassRate = %f, want 0.75", f.Coverage.E2E.Web.PassRate)
	}
}

func TestYAMLStoreLoadAllMultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewYAMLStore()

	// Save two domains
	domain1 := &domain.DomainFile{
		Domain:      "auth",
		Description: "Auth",
		Features: []domain.Feature{
			{ID: "auth.login", Name: "Login", Priority: domain.PriorityCritical},
		},
	}
	domain2 := &domain.DomainFile{
		Domain:      "meals",
		Description: "Meals",
		Features: []domain.Feature{
			{ID: "meals.log", Name: "Log Meal", Priority: domain.PriorityHigh},
		},
	}

	if err := store.SaveDomain(tmpDir, domain1); err != nil {
		t.Fatalf("SaveDomain(auth) error = %v", err)
	}
	if err := store.SaveDomain(tmpDir, domain2); err != nil {
		t.Fatalf("SaveDomain(meals) error = %v", err)
	}

	// Load all
	reg, err := store.LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(reg.Domains) != 2 {
		t.Errorf("Domains count = %d, want 2", len(reg.Domains))
	}
}

func TestYAMLStoreLoadAllEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewYAMLStore()

	reg, err := store.LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(reg.Domains) != 0 {
		t.Errorf("Expected 0 domains for empty dir, got %d", len(reg.Domains))
	}
}

func TestYAMLStoreLoadAllNonExistentDir(t *testing.T) {
	store := NewYAMLStore()

	reg, err := store.LoadAll("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("LoadAll() should not error for nonexistent dir, got: %v", err)
	}

	if len(reg.Domains) != 0 {
		t.Errorf("Expected 0 domains for nonexistent dir, got %d", len(reg.Domains))
	}
}

func TestYAMLStoreValidationRejectsMissingDomain(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a YAML file missing the domain field
	content := []byte("description: Bad file\nfeatures: []\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "bad.yaml"), content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewYAMLStore()
	_, err := store.LoadAll(tmpDir)
	if err == nil {
		t.Fatal("Expected error for YAML file missing 'domain' field")
	}
}

func TestYAMLStoreValidationRejectsInvalidPriority(t *testing.T) {
	tmpDir := t.TempDir()

	content := []byte(`domain: test
description: Test
features:
  - id: test.feature
    name: Test Feature
    priority: invalid_priority
`)
	if err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewYAMLStore()
	_, err := store.LoadAll(tmpDir)
	if err == nil {
		t.Fatal("Expected error for invalid priority value")
	}
}

func TestYAMLStoreSaveAllAndLoadAll(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewYAMLStore()

	reg := &domain.Registry{
		Domains: []domain.DomainFile{
			{Domain: "a", Description: "A", Features: []domain.Feature{
				{ID: "a.1", Name: "A1", Priority: domain.PriorityLow},
			}},
			{Domain: "b", Description: "B", Features: []domain.Feature{
				{ID: "b.1", Name: "B1", Priority: domain.PriorityMedium},
			}},
		},
	}

	if err := store.SaveAll(tmpDir, reg); err != nil {
		t.Fatalf("SaveAll() error = %v", err)
	}

	loaded, err := store.LoadAll(tmpDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(loaded.Domains) != 2 {
		t.Errorf("Expected 2 domains after SaveAll/LoadAll, got %d", len(loaded.Domains))
	}
}
