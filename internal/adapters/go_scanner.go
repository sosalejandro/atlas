package adapters

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/ports"
)

// GoScanner discovers Go test files by walking the project tree.
type GoScanner struct{}

// NewGoScanner creates a new GoScanner.
func NewGoScanner() *GoScanner {
	return &GoScanner{}
}

// Name returns the scanner's display name.
func (s *GoScanner) Name() string {
	return "Go Test Scanner"
}

// Scan walks rootDir looking for *_test.go files.
// Classifies them as unit, integration, or e2e based on naming conventions and build tags.
func (s *GoScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) {
	var tests []ports.DiscoveredTest

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if d.IsDir() {
			name := d.Name()
			// Skip vendor, node_modules, and hidden directories
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		testType := classifyGoTest(d.Name(), relPath)

		tests = append(tests, ports.DiscoveredTest{
			FilePath:  relPath,
			TestType:  testType,
			Platform:  "backend",
			Framework: "go",
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return tests, nil
}

// classifyGoTest determines whether a Go test file is unit, integration, or e2e.
func classifyGoTest(filename, relPath string) string {
	lower := strings.ToLower(filename)

	// E2E tests: explicitly named or in e2e directories
	if strings.Contains(lower, "_e2e_test.go") || strings.Contains(relPath, "/e2e/") {
		return "e2e"
	}

	// Integration tests: explicitly named or in integration directories
	if strings.Contains(lower, "_integration_test.go") ||
		strings.Contains(relPath, "/integration/") {
		return "integration"
	}

	// Default: unit test
	return "unit"
}
