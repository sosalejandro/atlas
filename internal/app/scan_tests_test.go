package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/ports"
)

// stubScanner returns a fixed list of tests.
type stubScanner struct {
	name  string
	tests []ports.DiscoveredTest
}

func (s *stubScanner) Name() string                                        { return s.name }
func (s *stubScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) { return s.tests, nil }

func TestScanTestsExecute(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	// Initialize registry first
	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	// Create a stub scanner that returns tests matching registry features
	scanner := &stubScanner{
		name: "test scanner",
		tests: []ports.DiscoveredTest{
			{FilePath: "src/services/auth_test.go", TestType: "unit", Platform: "backend", Framework: "go"},
			{FilePath: "apps/web/tests/login.test.ts", TestType: "unit", Platform: "web", Framework: "vitest"},
			{FilePath: "random/unrelated_test.go", TestType: "unit", Platform: "backend", Framework: "go"},
		},
	}

	uc := NewScanTestsUseCase(store, store, []ports.TestScanner{scanner})
	result, err := uc.Execute(tmpDir, registryDir)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.TotalTests != 3 {
		t.Errorf("TotalTests = %d, want 3", result.TotalTests)
	}

	// At least some should be mapped (auth and login keywords match auth domain features)
	if result.MappedTests == 0 {
		t.Error("Expected at least some mapped tests")
	}
}

func TestScanTestsSavesUnmapped(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	// Scanner with only unmappable tests
	scanner := &stubScanner{
		name: "test scanner",
		tests: []ports.DiscoveredTest{
			{FilePath: "random/totally_unrelated_xyz_test.go", TestType: "unit", Platform: "backend", Framework: "go"},
		},
	}

	uc := NewScanTestsUseCase(store, store, []ports.TestScanner{scanner})
	result, err := uc.Execute(tmpDir, registryDir)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.UnmappedTests == 0 {
		t.Error("Expected at least one unmapped test")
	}

	// Check that _unmapped.yaml was created
	unmappedPath := filepath.Join(registryDir, "_unmapped.yaml")
	if _, err := os.Stat(unmappedPath); os.IsNotExist(err) {
		t.Error("Expected _unmapped.yaml to be created")
	}
}
