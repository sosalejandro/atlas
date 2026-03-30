package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoScannerScan(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test directory structure
	dirs := []string{
		"src/services",
		"src/handlers",
		"src/domain",
		"src/e2e",
		"vendor/lib",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	// Create test files
	files := map[string]string{
		"src/services/auth_test.go":             "package services",
		"src/handlers/auth_handler_test.go":     "package handlers",
		"src/domain/user_test.go":               "package domain",
		"src/e2e/auth_e2e_test.go":              "package e2e",
		"src/services/auth_integration_test.go": "package services",
		"src/services/auth.go":                  "package services", // not a test file
		"vendor/lib/vendor_test.go":             "package lib",      // should be skipped
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewGoScanner()

	if scanner.Name() != "Go Test Scanner" {
		t.Errorf("Name() = %q, want %q", scanner.Name(), "Go Test Scanner")
	}

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Should find: auth_test.go, auth_handler_test.go, user_test.go, auth_e2e_test.go, auth_integration_test.go
	// Should NOT find: auth.go, vendor_test.go
	if len(tests) != 5 {
		t.Errorf("Scan() found %d tests, want 5", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s [%s/%s]", test.FilePath, test.Platform, test.TestType)
		}
	}

	// Verify classification
	typeMap := make(map[string]string)
	for _, test := range tests {
		typeMap[filepath.Base(test.FilePath)] = test.TestType
		if test.Platform != "backend" {
			t.Errorf("Go test %s should have platform=backend, got %q", test.FilePath, test.Platform)
		}
		if test.Framework != "go" {
			t.Errorf("Go test %s should have framework=go, got %q", test.FilePath, test.Framework)
		}
	}

	expectations := map[string]string{
		"auth_test.go":             "unit",
		"user_test.go":             "unit",
		"auth_handler_test.go":     "integration",
		"auth_e2e_test.go":         "e2e",
		"auth_integration_test.go": "integration",
	}

	for file, expectedType := range expectations {
		if got, ok := typeMap[file]; !ok {
			t.Errorf("Expected to find %s in scan results", file)
		} else if got != expectedType {
			t.Errorf("%s classified as %q, want %q", file, got, expectedType)
		}
	}
}

func TestGoScannerEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewGoScanner()

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests in empty dir, got %d", len(tests))
	}
}
