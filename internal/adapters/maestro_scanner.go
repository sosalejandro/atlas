package adapters

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/ports"
)

// MaestroScanner discovers Maestro mobile E2E flow files (*.yaml in e2e/flows).
type MaestroScanner struct{}

// NewMaestroScanner creates a new MaestroScanner.
func NewMaestroScanner() *MaestroScanner {
	return &MaestroScanner{}
}

// Name returns the scanner's display name.
func (s *MaestroScanner) Name() string {
	return "Maestro E2E Scanner"
}

// Scan walks rootDir looking for Maestro flow YAML files in mobile e2e directories.
func (s *MaestroScanner) Scan(rootDir string) ([]ports.DiscoveredTest, error) {
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
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		// Only match Maestro flows in mobile/e2e directories
		if !isMaestroPath(relPath) {
			return nil
		}

		tests = append(tests, ports.DiscoveredTest{
			FilePath:  relPath,
			TestType:  "e2e",
			Platform:  "mobile",
			Framework: "maestro",
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return tests, nil
}

func isMaestroPath(relPath string) bool {
	lower := strings.ToLower(relPath)
	return (strings.Contains(lower, "mobile") || strings.Contains(lower, "apps/mobile")) &&
		(strings.Contains(lower, "e2e") || strings.Contains(lower, "flows") || strings.Contains(lower, "maestro"))
}
