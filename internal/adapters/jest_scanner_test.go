// @testreg scan.jest-scanner
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJestScannerDiscoversMobileTestFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mobile directory structures with __tests__ dirs
	dirs := []string{
		"apps/mobile/src/__tests__",
		"apps/mobile/src/components/__tests__",
		"mobile/screens/__tests__",
		"apps/mobile/src/__tests__/integration",
		"apps/mobile/src/__tests__/e2e",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	// Create test files covering all four extensions
	files := map[string]string{
		"apps/mobile/src/__tests__/auth.test.ts":                  "// ts test",
		"apps/mobile/src/components/__tests__/Button.test.tsx":    "// tsx test",
		"mobile/screens/__tests__/Home.test.js":                   "// js test",
		"mobile/screens/__tests__/Profile.test.jsx":               "// jsx test",
		"apps/mobile/src/__tests__/integration/api.test.ts":       "// integration test",
		"apps/mobile/src/__tests__/e2e/login.test.ts":             "// e2e test",
		"apps/mobile/src/__tests__/utils.ts":                      "// NOT a test file",
		"apps/mobile/src/components/__tests__/Button.stories.tsx": "// NOT a test file",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewJestScanner()

	if scanner.Name() != "Jest Mobile Scanner" {
		t.Errorf("Name() = %q, want %q", scanner.Name(), "Jest Mobile Scanner")
	}

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Should find 6 test files (.test.ts, .test.tsx, .test.js, .test.jsx, integration, e2e)
	// Should NOT find utils.ts or Button.stories.tsx
	if len(tests) != 6 {
		t.Errorf("Scan() found %d tests, want 6", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s [%s/%s/%s]", test.FilePath, test.Platform, test.Framework, test.TestType)
		}
	}

	// Verify all discovered files have correct extensions
	validExts := map[string]bool{
		".test.ts": true, ".test.tsx": true,
		".test.js": true, ".test.jsx": true,
	}
	for _, test := range tests {
		base := filepath.Base(test.FilePath)
		found := false
		for ext := range validExts {
			if len(base) > len(ext) && base[len(base)-len(ext):] == ext {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Unexpected file discovered: %s", test.FilePath)
		}
	}
}

func TestJestScannerClassifiesTestType(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"apps/mobile/src/__tests__",
		"apps/mobile/src/__tests__/integration",
		"apps/mobile/src/__tests__/e2e",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"apps/mobile/src/__tests__/unit.test.ts":            "// unit",
		"apps/mobile/src/__tests__/integration/api.test.ts": "// integration",
		"apps/mobile/src/__tests__/e2e/login.test.ts":       "// e2e",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewJestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	typeMap := make(map[string]string)
	for _, test := range tests {
		typeMap[filepath.Base(test.FilePath)] = test.TestType
	}

	expectations := map[string]string{
		"unit.test.ts":  "unit",
		"api.test.ts":   "integration",
		"login.test.ts": "e2e",
	}

	for file, expectedType := range expectations {
		if got, ok := typeMap[file]; !ok {
			t.Errorf("Expected to find %s in scan results", file)
		} else if got != expectedType {
			t.Errorf("%s classified as %q, want %q", file, got, expectedType)
		}
	}
}

func TestJestScannerSetsPlatformAndFramework(t *testing.T) {
	tmpDir := t.TempDir()

	os.MkdirAll(filepath.Join(tmpDir, "apps/mobile/src/__tests__"), 0o755)

	files := map[string]string{
		"apps/mobile/src/__tests__/auth.test.ts":       "// test",
		"apps/mobile/src/__tests__/dashboard.test.tsx": "// test",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewJestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	for _, test := range tests {
		if test.Platform != "mobile" {
			t.Errorf("Jest test %s should have platform=mobile, got %q", test.FilePath, test.Platform)
		}
		if test.Framework != "jest" {
			t.Errorf("Jest test %s should have framework=jest, got %q", test.FilePath, test.Framework)
		}
	}
}

func TestJestScannerIgnoresNodeModules(t *testing.T) {
	tmpDir := t.TempDir()

	dirs := []string{
		"apps/mobile/src/__tests__",
		"node_modules/some-lib/__tests__",
		"apps/mobile/node_modules/lib/__tests__",
	}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(tmpDir, d), 0o755)
	}

	files := map[string]string{
		"apps/mobile/src/__tests__/valid.test.ts":              "// should be found",
		"node_modules/some-lib/__tests__/internal.test.ts":     "// should be skipped",
		"apps/mobile/node_modules/lib/__tests__/vendor.test.ts": "// should be skipped",
	}
	for path, content := range files {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	scanner := NewJestScanner()
	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Only the valid.test.ts should be discovered
	if len(tests) != 1 {
		t.Errorf("Scan() found %d tests, want 1 (node_modules should be skipped)", len(tests))
		for _, test := range tests {
			t.Logf("  found: %s", test.FilePath)
		}
	}

	if len(tests) == 1 && filepath.Base(tests[0].FilePath) != "valid.test.ts" {
		t.Errorf("Expected valid.test.ts, got %s", tests[0].FilePath)
	}
}

func TestJestScannerEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewJestScanner()

	tests, err := scanner.Scan(tmpDir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(tests) != 0 {
		t.Errorf("Expected 0 tests in empty dir, got %d", len(tests))
	}
}
