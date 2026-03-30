package adapters

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/ports"
)

// VitestScanner discovers Vitest test files (*.test.ts, *.test.tsx).
type VitestScanner struct{}

// NewVitestScanner creates a new VitestScanner.
func NewVitestScanner() *VitestScanner {
	return &VitestScanner{}
}

// Name returns the scanner's display name.
func (s *VitestScanner) Name() string {
	return "Vitest Scanner"
}

// Scan walks rootDir looking for *.test.ts and *.test.tsx files,
// excluding node_modules and e2e directories (those are Playwright).
func (s *VitestScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) {
	var tests []ports.DiscoveredTest

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "e2e" || name == "dist" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".test.ts") && !strings.HasSuffix(name, ".test.tsx") {
			return nil
		}

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		// Only match files in web-related directories
		if !isWebPath(relPath) {
			return nil
		}

		testType := classifyVitestFile(name, relPath)

		tests = append(tests, ports.DiscoveredTest{
			FilePath:  relPath,
			TestType:  testType,
			Platform:  "web",
			Framework: "vitest",
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return tests, nil
}

func isWebPath(relPath string) bool {
	return strings.HasPrefix(relPath, "web/") ||
		strings.HasPrefix(relPath, "apps/web/") ||
		strings.Contains(relPath, "/web/") ||
		strings.HasPrefix(relPath, "src/") // standalone web projects
}

func classifyVitestFile(filename, relPath string) string {
	lower := strings.ToLower(filename)

	if strings.Contains(lower, ".integration.test.") {
		return "integration"
	}

	return "unit"
}
