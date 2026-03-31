// @testreg scan.playwright-scanner
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlaywrightScannerDiscoversSpecTsFiles(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"e2e/auth",
		"e2e/dashboard",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"e2e/auth/login.spec.ts":           "import { test } from '@playwright/test'",
		"e2e/dashboard/overview.spec.ts":   "import { test } from '@playwright/test'",
		"e2e/auth/login.ts":                "export const loginPage = {}",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewPlaywrightScanner()
	if scanner.Name() != "Playwright E2E Scanner" {
		t.Errorf("Name() = %q, want %q", scanner.Name(), "Playwright E2E Scanner")
	}

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Should find both .spec.ts files, not the plain .ts file
	if len(tests) != 2 {
		t.Errorf("Scan() found %d tests, want 2", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}

	found := make(map[string]bool)
	for _, test := range tests {
		found[filepath.Base(test.FilePath)] = true
	}

	for _, want := range []string{"login.spec.ts", "overview.spec.ts"} {
		if !found[want] {
			t.Errorf("Expected to find %s in scan results", want)
		}
	}
}

func TestPlaywrightScannerSetsTypeAndPlatformAndFramework(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "e2e"), 0o755)

	files := map[string]string{
		"e2e/checkout.spec.ts": "import { test } from '@playwright/test'",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewPlaywrightScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 1 {
		t.Fatalf("Scan() found %d tests, want 1", len(tests))
	}

	test := tests[0]
	if test.TestType != "e2e" {
		t.Errorf("TestType = %q, want %q", test.TestType, "e2e")
	}
	if test.Platform != "web" {
		t.Errorf("Platform = %q, want %q", test.Platform, "web")
	}
	if test.Framework != "playwright" {
		t.Errorf("Framework = %q, want %q", test.Framework, "playwright")
	}
}

func TestPlaywrightScannerIgnoresNodeModules(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"e2e",
		"node_modules/@playwright/test/specs",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"e2e/login.spec.ts":                               "import { test } from '@playwright/test'",
		"node_modules/@playwright/test/specs/sample.spec.ts": "import { test } from '@playwright/test'",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewPlaywrightScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Only e2e/login.spec.ts should be found, not node_modules
	if len(tests) != 1 {
		t.Errorf("Scan() found %d tests, want 1", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}

	if len(tests) == 1 && tests[0].FilePath != "e2e/login.spec.ts" {
		t.Errorf("Expected e2e/login.spec.ts, got %s", tests[0].FilePath)
	}
}

func TestPlaywrightScannerEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewPlaywrightScanner()

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests in empty dir, got %d", len(tests))
	}
}

func TestPlaywrightScannerNoSpecTsFiles(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"src/components",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"src/components/button.test.ts": "import { describe } from 'vitest'",
		"src/components/button.tsx":     "export const Button = () => {}",
		"src/components/index.ts":       "export * from './button'",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewPlaywrightScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 playwright tests with no .spec.ts files, got %d", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}
}
