// @testreg scan.vitest-scanner
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVitestScannerDiscoversTsAndTsxFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create web directory structure that passes isWebPath
	dirs := []string{
		"src/components",
		"src/hooks",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"src/components/button.test.ts":  "import { describe } from 'vitest'",
		"src/hooks/useAuth.test.tsx":     "import { describe } from 'vitest'",
		"src/components/button.tsx":      "export const Button = () => {}",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewVitestScanner()
	if scanner.Name() != "Vitest Scanner" {
		t.Errorf("Name() = %q, want %q", scanner.Name(), "Vitest Scanner")
	}

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Should find both .test.ts and .test.tsx
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

	for _, want := range []string{"button.test.ts", "useAuth.test.tsx"} {
		if !found[want] {
			t.Errorf("Expected to find %s in scan results", want)
		}
	}
}

func TestVitestScannerClassifiesTestTypes(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"src/components",
		"src/services",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"src/components/button.test.ts":            "import { describe } from 'vitest'",
		"src/services/api.integration.test.ts":     "import { describe } from 'vitest'",
		"src/services/db.integration.test.tsx":     "import { describe } from 'vitest'",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewVitestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	typeMap := make(map[string]string)
	for _, test := range tests {
		typeMap[filepath.Base(test.FilePath)] = test.TestType
	}

	expectations := map[string]string{
		"button.test.ts":            "unit",
		"api.integration.test.ts":   "integration",
		"db.integration.test.tsx":   "integration",
	}

	for file, expectedType := range expectations {
		if got, ok := typeMap[file]; !ok {
			t.Errorf("Expected to find %s in scan results", file)
		} else if got != expectedType {
			t.Errorf("%s classified as %q, want %q", file, got, expectedType)
		}
	}
}

func TestVitestScannerSetsPlatformAndFramework(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "src/utils"), 0o755)

	files := map[string]string{
		"src/utils/format.test.ts": "import { describe } from 'vitest'",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewVitestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 1 {
		t.Fatalf("Scan() found %d tests, want 1", len(tests))
	}

	test := tests[0]
	if test.Platform != "web" {
		t.Errorf("Platform = %q, want %q", test.Platform, "web")
	}
	if test.Framework != "vitest" {
		t.Errorf("Framework = %q, want %q", test.Framework, "vitest")
	}
}

func TestVitestScannerIgnoresExcludedDirs(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"src/components",
		"node_modules/vitest/tests",
		"dist/tests",
		"e2e/specs",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"src/components/button.test.ts":       "import { describe } from 'vitest'",
		"node_modules/vitest/tests/core.test.ts": "import { describe } from 'vitest'",
		"dist/tests/compiled.test.ts":         "import { describe } from 'vitest'",
		"e2e/specs/login.test.ts":             "import { describe } from 'vitest'",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewVitestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Only src/components/button.test.ts should be found
	if len(tests) != 1 {
		t.Errorf("Scan() found %d tests, want 1 (only src/)", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}

	if len(tests) == 1 && tests[0].FilePath != "src/components/button.test.ts" {
		t.Errorf("Expected src/components/button.test.ts, got %s", tests[0].FilePath)
	}
}

func TestVitestScannerEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewVitestScanner()

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests in empty dir, got %d", len(tests))
	}
}

func TestVitestScannerNoMatchingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"src/services",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"src/services/auth_test.go":   "package services",
		"src/services/auth.go":        "package services",
		"src/services/handler.py":     "def handler():",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewVitestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 vitest tests with only .go/.py files, got %d", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}
}
