package ports

// DiscoveredTest represents a test file found during scanning.
type DiscoveredTest struct {
	FilePath  string // relative to project root
	TestType  string // "unit", "integration", "e2e"
	Platform  string // "backend", "web", "mobile"
	Framework string // "go", "vitest", "playwright", "maestro", "jest"
}

// TestScanner discovers test files for a specific platform and framework.
type TestScanner interface {
	// Name returns the human-readable name of this scanner (e.g., "Go Test Scanner").
	Name() string

	// Scan walks the project tree rooted at rootDir and returns discovered test files.
	Scan(rootDir string) ([]DiscoveredTest, error)
}
