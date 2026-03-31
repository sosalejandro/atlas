// @testreg scan.maestro-scanner
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaestroScannerDiscoversFlowFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directories matching maestro/e2e/flows patterns
	dirs := []string{
		"mobile/e2e/flows",
		"apps/mobile/maestro/flows",
		"apps/mobile/e2e/maestro",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	// Create .yaml and .yml flow files
	files := map[string]string{
		"mobile/e2e/flows/login.yaml":             "appId: com.app\n---\n- launchApp",
		"mobile/e2e/flows/signup.yml":              "appId: com.app\n---\n- launchApp",
		"apps/mobile/maestro/flows/checkout.yaml":  "appId: com.app\n---\n- launchApp",
		"apps/mobile/e2e/maestro/navigation.yml":   "appId: com.app\n---\n- launchApp",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewMaestroScanner()

	if scanner.Name() != "Maestro E2E Scanner" {
		t.Errorf("Name() = %q, want %q", scanner.Name(), "Maestro E2E Scanner")
	}

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Should find all 4 flow files
	if len(tests) != 4 {
		t.Errorf("Scan() found %d tests, want 4", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s [%s/%s/%s]", test.FilePath, test.Platform, test.Framework, test.TestType)
		}
	}

	// Verify both .yaml and .yml are discovered
	hasYAML := false
	hasYML := false
	for _, test := range tests {
		base := filepath.Base(test.FilePath)
		if filepath.Ext(base) == ".yaml" {
			hasYAML = true
		}
		if filepath.Ext(base) == ".yml" {
			hasYML = true
		}
	}
	if !hasYAML {
		t.Error("Expected at least one .yaml file to be discovered")
	}
	if !hasYML {
		t.Error("Expected at least one .yml file to be discovered")
	}
}

func TestMaestroScannerSetsFieldsCorrectly(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "mobile/e2e/flows"), 0o755)

	files := map[string]string{
		"mobile/e2e/flows/login.yaml":  "appId: com.app\n---\n- launchApp",
		"mobile/e2e/flows/signup.yaml": "appId: com.app\n---\n- launchApp",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewMaestroScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	for _, test := range tests {
		if test.TestType != "e2e" {
			t.Errorf("Maestro test %s should have TestType=e2e, got %q", test.FilePath, test.TestType)
		}
		if test.Platform != "mobile" {
			t.Errorf("Maestro test %s should have Platform=mobile, got %q", test.FilePath, test.Platform)
		}
		if test.Framework != "maestro" {
			t.Errorf("Maestro test %s should have Framework=maestro, got %q", test.FilePath, test.Framework)
		}
	}
}

func TestMaestroScannerIgnoresNonMaestroPaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directories that should NOT match (no mobile/e2e/flows/maestro keywords in combination)
	dirs := []string{
		"config",
		"backend/config",
		"web/src/config",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	// YAML files that are NOT in maestro/e2e/flows directories
	files := map[string]string{
		"config/app.yaml":            "key: value",
		"backend/config/db.yml":      "host: localhost",
		"web/src/config/routes.yaml": "routes: []",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewMaestroScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests for non-maestro YAML files, got %d", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}
}

func TestMaestroScannerEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewMaestroScanner()

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests in empty dir, got %d", len(tests))
	}
}

func TestMaestroScannerNoMatchingYAMLInMatchingDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create matching directory structure but with no YAML files
	dirs := []string{
		"mobile/e2e/flows",
		"apps/mobile/maestro",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	// Only non-YAML files in the matching directories
	files := map[string]string{
		"mobile/e2e/flows/README.md":        "# Flows",
		"mobile/e2e/flows/helper.ts":        "export const helper = () => {}",
		"apps/mobile/maestro/config.json":   "{}",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewMaestroScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests when no YAML files exist in matching dirs, got %d", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}
}
