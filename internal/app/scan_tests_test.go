// @testreg scan.orchestration
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

	// Create actual test files in the project root for the annotation parser
	createTestFile(t, tmpDir, "src/services/auth_test.go", `package services

// @testreg auth.login #mocked

func TestLoginSuccess(t *testing.T) {}
func TestLoginFailure(t *testing.T) {}
`)

	createTestFile(t, tmpDir, "apps/web/tests/login.test.ts", `// @testreg auth.login

test('should render login form', async () => {});
`)

	createTestFile(t, tmpDir, "random/unrelated_test.go", `package random

func TestSomething(t *testing.T) {}
`)

	// Create a stub scanner that returns these test files
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

	// Two files have @testreg annotations, one does not
	if result.MappedTests != 2 {
		t.Errorf("MappedTests = %d, want 2", result.MappedTests)
	}

	if result.UnmappedTests != 1 {
		t.Errorf("UnmappedTests = %d, want 1", result.UnmappedTests)
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

	// Create test file without annotations
	createTestFile(t, tmpDir, "random/totally_unrelated_xyz_test.go", `package random

func TestSomething(t *testing.T) {}
`)

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

func TestScanTestsUpdatesCoverageFromAnnotations(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	store := adapters.NewYAMLStore()
	initUC := NewInitRegistryUseCase(store, store)
	if err := initUC.Execute(registryDir); err != nil {
		t.Fatalf("init error = %v", err)
	}

	// Create annotated test file
	createTestFile(t, tmpDir, "src/services/auth_test.go", `package services

// @testreg auth.login #mocked

func TestLoginSuccess(t *testing.T) {}
`)

	scanner := &stubScanner{
		name: "test scanner",
		tests: []ports.DiscoveredTest{
			{FilePath: "src/services/auth_test.go", TestType: "unit", Platform: "backend", Framework: "go"},
		},
	}

	uc := NewScanTestsUseCase(store, store, []ports.TestScanner{scanner})
	if _, err := uc.Execute(tmpDir, registryDir); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Verify registry was updated
	reg, err := store.LoadAll(registryDir)
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	feature, err := reg.GetFeature("auth.login")
	if err != nil {
		t.Fatalf("GetFeature() error = %v", err)
	}

	if feature.Coverage.Unit.Backend == nil {
		t.Fatal("Expected Unit.Backend to be non-nil after scan")
	}

	if feature.Coverage.Unit.Backend.Status != "covered" {
		t.Errorf("Unit.Backend.Status = %q, want covered", feature.Coverage.Unit.Backend.Status)
	}

	if !feature.Coverage.Unit.Backend.Mocked {
		t.Error("Expected Unit.Backend.Mocked to be true")
	}

	if len(feature.Coverage.Unit.Backend.Tests) == 0 {
		t.Fatal("Expected Tests to be populated")
	}

	te := feature.Coverage.Unit.Backend.Tests[0]
	if te.File != "src/services/auth_test.go" {
		t.Errorf("Tests[0].File = %q, want src/services/auth_test.go", te.File)
	}

	if len(te.Functions) == 0 {
		t.Fatal("Expected Functions to be populated")
	}

	if te.Functions[0].Name != "TestLoginSuccess" {
		t.Errorf("Functions[0].Name = %q, want TestLoginSuccess", te.Functions[0].Name)
	}

	if te.Functions[0].Run == "" {
		t.Error("Expected Functions[0].Run to be non-empty")
	}
}

// createTestFile creates a test file at the given relative path under the root directory.
func createTestFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", fullPath, err)
	}
}
