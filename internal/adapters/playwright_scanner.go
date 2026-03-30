package adapters

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/ports"
)

// PlaywrightScanner discovers Playwright E2E spec files (*.spec.ts).
type PlaywrightScanner struct{}

// NewPlaywrightScanner creates a new PlaywrightScanner.
func NewPlaywrightScanner() *PlaywrightScanner {
	return &PlaywrightScanner{}
}

// Name returns the scanner's display name.
func (s *PlaywrightScanner) Name() string {
	return "Playwright E2E Scanner"
}

// Scan walks rootDir looking for *.spec.ts files in e2e directories.
func (s *PlaywrightScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) {
	var tests []ports.DiscoveredTest

	err := filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "dist" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		if !strings.HasSuffix(name, ".spec.ts") {
			return nil
		}

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		// Playwright specs are web E2E tests
		tests = append(tests, ports.DiscoveredTest{
			FilePath:  relPath,
			TestType:  "e2e",
			Platform:  "web",
			Framework: "playwright",
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return tests, nil
}
