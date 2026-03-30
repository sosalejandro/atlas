package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// ScanTestsUseCase discovers test files and maps them to features in the registry.
type ScanTestsUseCase struct {
	reader   ports.RegistryReader
	writer   ports.RegistryWriter
	scanners []ports.TestScanner
}

// ScanResult summarizes the outcome of a scan operation.
type ScanResult struct {
	TotalTests    int
	MappedTests   int
	UnmappedTests int
	Mapped        []MappedTest
	Unmapped      []ports.DiscoveredTest
	UpdatedFiles  int
}

// MappedTest links a discovered test to its feature.
type MappedTest struct {
	Test      ports.DiscoveredTest
	FeatureID string
}

// NewScanTestsUseCase creates a new ScanTestsUseCase.
func NewScanTestsUseCase(reader ports.RegistryReader, writer ports.RegistryWriter, scanners []ports.TestScanner) *ScanTestsUseCase {
	return &ScanTestsUseCase{reader: reader, writer: writer, scanners: scanners}
}

// Execute runs all scanners against the project root and updates the registry.
func (uc *ScanTestsUseCase) Execute(projectRoot, registryDir string) (*ScanResult, error) {
	registry, err := uc.reader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	// Gather all discovered tests from all scanners
	var allTests []ports.DiscoveredTest
	for _, scanner := range uc.scanners {
		tests, scanErr := scanner.Scan(projectRoot)
		if scanErr != nil {
			return nil, fmt.Errorf("scanner %s failed: %w", scanner.Name(), scanErr)
		}
		allTests = append(allTests, tests...)
	}

	result := &ScanResult{TotalTests: len(allTests)}

	// Build lookup index: file path -> feature pointer
	fileIndex := buildFileIndex(registry)

	// Build keyword index for fuzzy matching
	keywordIndex := buildKeywordIndex(registry)

	for _, test := range allTests {
		featureID := matchTestToFeature(test, fileIndex, keywordIndex)
		if featureID != "" {
			result.Mapped = append(result.Mapped, MappedTest{Test: test, FeatureID: featureID})
			result.MappedTests++

			// Update the feature's coverage
			updateFeatureCoverage(registry, featureID, test)
		} else {
			result.Unmapped = append(result.Unmapped, test)
			result.UnmappedTests++
		}
	}

	// Save updated registry
	if err := uc.writer.SaveAll(registryDir, registry); err != nil {
		return nil, fmt.Errorf("saving updated registry: %w", err)
	}

	// Save unmapped tests for manual review
	if len(result.Unmapped) > 0 {
		if err := saveUnmappedTests(uc.writer, registryDir, result.Unmapped); err != nil {
			return nil, fmt.Errorf("saving unmapped tests: %w", err)
		}
	}

	return result, nil
}

// buildFileIndex maps every file path already in the registry to its feature ID.
func buildFileIndex(reg *domain.Registry) map[string]string {
	index := make(map[string]string)
	for _, d := range reg.Domains {
		for _, f := range d.Features {
			for _, file := range collectAllFiles(&f) {
				index[file] = f.ID
			}
		}
	}
	return index
}

// collectAllFiles gathers every file path listed in a feature's coverage entries.
func collectAllFiles(f *domain.Feature) []string {
	var files []string
	appendFiles := func(entry *domain.CoverageEntry) {
		if entry != nil {
			files = append(files, entry.Files...)
		}
	}
	appendE2EFiles := func(entry *domain.E2ECoverageEntry) {
		if entry != nil {
			files = append(files, entry.Files...)
		}
	}

	appendFiles(f.Coverage.Unit.Backend)
	appendFiles(f.Coverage.Unit.Web)
	appendFiles(f.Coverage.Unit.Mobile)
	appendFiles(f.Coverage.Integration.Backend)
	appendFiles(f.Coverage.Integration.Mobile)
	appendE2EFiles(f.Coverage.E2E.Web)
	appendE2EFiles(f.Coverage.E2E.Mobile)

	return files
}

// buildKeywordIndex maps domain keywords to feature IDs for fuzzy matching.
func buildKeywordIndex(reg *domain.Registry) map[string]string {
	index := make(map[string]string)
	for _, d := range reg.Domains {
		for _, f := range d.Features {
			// Extract keywords from feature ID (e.g., "auth.login" -> "auth", "login")
			parts := strings.Split(f.ID, ".")
			for _, part := range parts {
				// Map keyword to feature, preferring more specific matches
				index[strings.ToLower(part)] = f.ID
			}

			// Add name-based keywords
			nameWords := strings.Fields(strings.ToLower(f.Name))
			for _, w := range nameWords {
				index[w] = f.ID
			}
		}
	}
	return index
}

// matchTestToFeature attempts to map a discovered test to a feature.
// Returns the feature ID or empty string if no match found.
func matchTestToFeature(test ports.DiscoveredTest, fileIndex map[string]string, keywordIndex map[string]string) string {
	// Strategy 1: Exact file path match in existing registry
	if id, ok := fileIndex[test.FilePath]; ok {
		return id
	}

	// Strategy 2: Pattern matching on file path
	base := filepath.Base(test.FilePath)
	base = strings.TrimSuffix(base, filepath.Ext(base))

	// Remove common test suffixes
	for _, suffix := range []string{"_test", "_e2e_test", ".test", ".spec", ".integration.test", "_integration_test"} {
		base = strings.TrimSuffix(base, suffix)
	}

	// Normalize separators
	normalized := strings.ToLower(base)
	normalized = strings.ReplaceAll(normalized, "-", ".")
	normalized = strings.ReplaceAll(normalized, "_", ".")

	// Try direct keyword match
	if id, ok := keywordIndex[normalized]; ok {
		return id
	}

	// Try individual words from the normalized name
	words := strings.Split(normalized, ".")
	for _, word := range words {
		if id, ok := keywordIndex[word]; ok {
			return id
		}
	}

	// Strategy 3: Path component matching (e.g., "src/auth/..." -> auth domain)
	pathParts := strings.Split(filepath.ToSlash(test.FilePath), "/")
	for _, part := range pathParts {
		lower := strings.ToLower(part)
		if id, ok := keywordIndex[lower]; ok {
			return id
		}
	}

	return ""
}

// updateFeatureCoverage adds the test file to the appropriate coverage entry
// and updates the status from missing to covered.
func updateFeatureCoverage(reg *domain.Registry, featureID string, test ports.DiscoveredTest) {
	feature, err := reg.GetFeature(featureID)
	if err != nil {
		return
	}

	switch {
	case test.TestType == "unit" && test.Platform == "backend":
		if feature.Coverage.Unit.Backend == nil {
			feature.Coverage.Unit.Backend = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateStatus(feature.Coverage.Unit.Backend, test.FilePath)

	case test.TestType == "unit" && test.Platform == "web":
		if feature.Coverage.Unit.Web == nil {
			feature.Coverage.Unit.Web = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateStatus(feature.Coverage.Unit.Web, test.FilePath)

	case test.TestType == "unit" && test.Platform == "mobile":
		if feature.Coverage.Unit.Mobile == nil {
			feature.Coverage.Unit.Mobile = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateStatus(feature.Coverage.Unit.Mobile, test.FilePath)

	case test.TestType == "integration" && test.Platform == "backend":
		if feature.Coverage.Integration.Backend == nil {
			feature.Coverage.Integration.Backend = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateStatus(feature.Coverage.Integration.Backend, test.FilePath)

	case test.TestType == "integration" && test.Platform == "mobile":
		if feature.Coverage.Integration.Mobile == nil {
			feature.Coverage.Integration.Mobile = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateStatus(feature.Coverage.Integration.Mobile, test.FilePath)

	case test.TestType == "e2e" && test.Platform == "web":
		if feature.Coverage.E2E.Web == nil {
			feature.Coverage.E2E.Web = &domain.E2ECoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateE2EStatus(feature.Coverage.E2E.Web, test.FilePath)

	case test.TestType == "e2e" && test.Platform == "mobile":
		if feature.Coverage.E2E.Mobile == nil {
			feature.Coverage.E2E.Mobile = &domain.E2ECoverageEntry{Status: domain.StatusMissing}
		}
		addFileAndUpdateE2EStatus(feature.Coverage.E2E.Mobile, test.FilePath)
	}
}

func addFileAndUpdateStatus(entry *domain.CoverageEntry, filePath string) {
	// Deduplicate files
	for _, f := range entry.Files {
		if f == filePath {
			return
		}
	}
	entry.Files = append(entry.Files, filePath)

	if entry.Status == domain.StatusMissing {
		entry.Status = domain.StatusCovered
	}
}

func addFileAndUpdateE2EStatus(entry *domain.E2ECoverageEntry, filePath string) {
	for _, f := range entry.Files {
		if f == filePath {
			return
		}
	}
	entry.Files = append(entry.Files, filePath)

	if entry.Status == domain.StatusMissing {
		entry.Status = domain.StatusCovered
	}
}

// saveUnmappedTests writes unmapped tests to a special _unmapped.yaml file
// for manual review and categorization.
func saveUnmappedTests(writer ports.RegistryWriter, registryDir string, unmapped []ports.DiscoveredTest) error {
	features := make([]domain.Feature, 0, len(unmapped))
	for _, t := range unmapped {
		features = append(features, domain.Feature{
			ID:          fmt.Sprintf("unmapped.%s", filepath.Base(t.FilePath)),
			Name:        fmt.Sprintf("Unmapped: %s", t.FilePath),
			Description: fmt.Sprintf("Discovered by %s scanner, needs manual mapping", t.Framework),
			Priority:    domain.PriorityLow,
			Notes:       fmt.Sprintf("type=%s platform=%s framework=%s", t.TestType, t.Platform, t.Framework),
		})
	}

	df := &domain.DomainFile{
		Domain:      "_unmapped",
		Description: "Tests discovered by scan that could not be automatically mapped to features. Review and move to appropriate domain files.",
		Features:    features,
	}

	return writer.SaveDomain(registryDir, df)
}
