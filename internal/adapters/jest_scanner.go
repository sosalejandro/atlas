package adapters

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/ports"
)

// JestScanner discovers Jest test files in mobile __tests__/ directories.
type JestScanner struct{}

// NewJestScanner creates a new JestScanner.
func NewJestScanner() *JestScanner {
	return &JestScanner{}
}

// Name returns the scanner's display name.
func (s *JestScanner) Name() string {
	return "Jest Mobile Scanner"
}

// Scan walks rootDir looking for test files in __tests__/ directories
// within mobile app paths.
func (s *JestScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) {
	var tests []ports.DiscoveredTest

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "dist" || name == "build" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		isTestFile := strings.HasSuffix(name, ".test.ts") ||
			strings.HasSuffix(name, ".test.tsx") ||
			strings.HasSuffix(name, ".test.js") ||
			strings.HasSuffix(name, ".test.jsx")

		if !isTestFile {
			return nil
		}

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		// Only match files in mobile directories
		if !isMobilePath(relPath) {
			return nil
		}

		testType := classifyJestFile(name, relPath)

		tests = append(tests, ports.DiscoveredTest{
			FilePath:  relPath,
			TestType:  testType,
			Platform:  "mobile",
			Framework: "jest",
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return tests, nil
}

func isMobilePath(relPath string) bool {
	return strings.HasPrefix(relPath, "apps/mobile/") ||
		strings.HasPrefix(relPath, "mobile/") ||
		strings.Contains(relPath, "__tests__/")
}

func classifyJestFile(filename, relPath string) string {
	lower := strings.ToLower(relPath)

	if strings.Contains(lower, "integration") {
		return "integration"
	}

	if strings.Contains(lower, "e2e") {
		return "e2e"
	}

	return "unit"
}
