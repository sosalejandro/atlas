// @testreg scan.orchestration
package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
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

// ---------------------------------------------------------------------------
// Tests for auto-mapping by directory proximity
// ---------------------------------------------------------------------------

func TestBuildFeatureDirIndex(t *testing.T) {
	registry := &domain.Registry{
		Domains: []domain.DomainFile{
			{Domain: "auth", Features: []domain.Feature{
				{ID: "auth.login"}, {ID: "auth.register"},
			}},
			{Domain: "enroll", Features: []domain.Feature{
				{ID: "enroll.create"}, {ID: "enroll.list"},
			}},
		},
	}

	index := buildFeatureDirIndex(registry)

	if len(index) != 2 {
		t.Fatalf("expected 2 domains in index, got %d", len(index))
	}
	if len(index["auth"]) != 2 {
		t.Errorf("auth features = %d, want 2", len(index["auth"]))
	}
	if len(index["enroll"]) != 2 {
		t.Errorf("enroll features = %d, want 2", len(index["enroll"]))
	}
}

func TestBuildFeatureDirIndex_Empty(t *testing.T) {
	index := buildFeatureDirIndex(&domain.Registry{})
	if len(index) != 0 {
		t.Errorf("expected empty index, got %d entries", len(index))
	}
}

func TestAutoMapByProximity_DirectoryMatch(t *testing.T) {
	index := map[string][]string{
		"auth":   {"auth.login", "auth.register"},
		"enroll": {"enroll.create"},
	}

	tests := []struct {
		path     string
		wantIDs  []string
		wantNone bool
	}{
		// Strategy 1: exact directory segment match
		{"server/modules/auth/auth_test.go", []string{"auth.login", "auth.register"}, false},
		{"server/modules/interactions/enroll/enroll_test.go", []string{"enroll.create"}, false},
		{"internal/auth/service_test.go", []string{"auth.login", "auth.register"}, false},

		// Strategy 2: path substring match
		{"client/src/features/auth/Login.test.tsx", []string{"auth.login", "auth.register"}, false},

		// No match
		{"server/middlewares/ratelimit_test.go", nil, true},
		{"tests/something_test.go", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := autoMapByProximity(tt.path, index)
			if tt.wantNone {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.wantIDs) {
				t.Errorf("got %d features %v, want %d %v", len(got), got, len(tt.wantIDs), tt.wantIDs)
			}
		})
	}
}

func TestAutoMapByProximity_EmptyIndex(t *testing.T) {
	got := autoMapByProximity("server/auth/auth_test.go", nil)
	if got != nil {
		t.Errorf("expected nil for empty index, got %v", got)
	}

	got = autoMapByProximity("server/auth/auth_test.go", map[string][]string{})
	if got != nil {
		t.Errorf("expected nil for empty map, got %v", got)
	}
}

func TestAutoMapByProximity_NearestDirectoryWins(t *testing.T) {
	// If a path has multiple matching segments, the deepest (nearest) wins.
	index := map[string][]string{
		"modules": {"modules.something"},
		"auth":    {"auth.login"},
	}

	// "auth" is deeper than "modules" in this path
	got := autoMapByProximity("server/modules/auth/auth_test.go", index)
	if len(got) == 0 {
		t.Fatal("expected match, got nil")
	}
	if got[0] != "auth.login" {
		t.Errorf("expected nearest match 'auth.login', got %v", got)
	}
}

func TestAutoMapByProximity_CrossFramework(t *testing.T) {
	// Same domain matching works regardless of framework/language.
	index := map[string][]string{
		"auth": {"auth.login"},
	}

	paths := []string{
		"server/modules/auth/auth_test.go",           // Go/Echo
		"src/handlers/auth/auth_handler_test.go",      // Go/Chi
		"internal/auth/service_test.go",               // Go/stdlib
		"client/src/features/auth/Login.test.tsx",     // React/Vitest
		"tests/auth/test_login.py",                    // Python/pytest
		"apps/web/src/auth/auth.spec.ts",              // Playwright
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			got := autoMapByProximity(p, index)
			if len(got) == 0 {
				t.Errorf("expected auth match for %s, got nil", p)
			}
		})
	}
}
