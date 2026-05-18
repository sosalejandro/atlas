package adapters

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/atlas/internal/ports"
)

// PythonScanner discovers Python test files (pytest convention).
type PythonScanner struct{}

// NewPythonScanner creates a new Python test file scanner.
func NewPythonScanner() *PythonScanner {
	return &PythonScanner{}
}

// Name returns the scanner identifier.
func (s *PythonScanner) Name() string { return "pytest" }

// Scan walks the project tree for Python test files following pytest conventions:
//   - test_*.py (prefix convention)
//   - *_test.py (suffix convention)
//   - Files inside tests/ or test/ directories
func (s *PythonScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) {
	var tests []ports.DiscoveredTest

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			base := d.Name()
			if base == "node_modules" || base == ".venv" || base == "venv" ||
				base == "__pycache__" || base == ".git" || base == ".tox" ||
				base == "dist" || base == "build" || base == ".eggs" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(d.Name(), ".py") {
			return nil
		}

		name := d.Name()
		isTest := strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py")
		if !isTest {
			return nil
		}

		relPath, _ := filepath.Rel(rootDir, path)
		relPath = filepath.ToSlash(relPath)

		testType := classifyPythonTestType(relPath)

		tests = append(tests, ports.DiscoveredTest{
			FilePath:  relPath,
			TestType:  testType,
			Platform:  "backend",
			Framework: "pytest",
		})

		return nil
	})

	return tests, err
}

// classifyPythonTestType determines the test type from path conventions.
func classifyPythonTestType(relPath string) string {
	lower := strings.ToLower(relPath)
	if strings.Contains(lower, "e2e") || strings.Contains(lower, "end_to_end") {
		return "e2e"
	}
	if strings.Contains(lower, "integration") {
		return "integration"
	}
	return "unit"
}
