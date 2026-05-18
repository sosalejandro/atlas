package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
	"gopkg.in/yaml.v3"
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

// Execute runs annotation-based scanning against the project root and updates the registry.
// Files with @testreg annotations are mapped to features; files without are written to _unmapped.yaml.
func (uc *ScanTestsUseCase) Execute(projectRoot, registryDir string) (*ScanResult, error) {
	registry, err := uc.reader.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	// Phase 1: Discover all test files using scanners (parallel).
	type scannerResult struct {
		tests []ports.DiscoveredTest
		err   error
		name  string
	}

	results := make([]scannerResult, len(uc.scanners))
	var wg sync.WaitGroup
	for i, scanner := range uc.scanners {
		wg.Add(1)
		go func(idx int, sc ports.TestScanner) {
			defer wg.Done()
			tests, err := sc.Scan(projectRoot)
			results[idx] = scannerResult{tests: tests, err: err, name: sc.Name()}
		}(i, scanner)
	}
	wg.Wait()

	var allTests []ports.DiscoveredTest
	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("scanner %s failed: %w", r.name, r.err)
		}
		allTests = append(allTests, r.tests...)
	}

	result := &ScanResult{TotalTests: len(allTests)}

	// Build a feature-to-handler-path index for auto-mapping.
	// Maps handler file directory → list of feature IDs.
	featureByDir := buildFeatureDirIndex(registry)

	// Phase 2: Parse annotations for each discovered test file.
	// Priority: explicit @testreg annotations > auto-mapped by directory proximity.
	var mappedAnnotations []*adapters.AnnotatedTest
	var unmappedAnnotations []*adapters.AnnotatedTest

	for _, test := range allTests {
		absPath := filepath.Join(projectRoot, test.FilePath)

		// Try to parse annotations first (explicit always wins).
		annotated, parseErr := adapters.ParseAnnotatedFile(absPath, test.FilePath)
		if parseErr != nil {
			continue
		}

		if annotated != nil && len(annotated.FeatureIDs) > 0 {
			// File has @testreg annotations — it's mapped.
			mappedAnnotations = append(mappedAnnotations, annotated)
			for _, featureID := range annotated.FeatureIDs {
				result.Mapped = append(result.Mapped, MappedTest{
					Test:      test,
					FeatureID: featureID,
				})
			}
			result.MappedTests++
		} else {
			// No annotations — try auto-mapping by directory proximity.
			autoFeatures := autoMapByProximity(test.FilePath, featureByDir)
			if len(autoFeatures) > 0 {
				// Auto-mapped: create a synthetic annotation.
				unmapped, _ := adapters.ParseAnnotatedFileForUnmapped(absPath, test.FilePath)
				if unmapped != nil {
					unmapped.FeatureIDs = autoFeatures
					mappedAnnotations = append(mappedAnnotations, unmapped)
				}
				for _, featureID := range autoFeatures {
					result.Mapped = append(result.Mapped, MappedTest{
						Test:      test,
						FeatureID: featureID,
					})
				}
				result.MappedTests++
			} else {
				// Truly unmapped — no annotations, no proximity match.
				unmapped, unmapErr := adapters.ParseAnnotatedFileForUnmapped(absPath, test.FilePath)
				if unmapErr != nil {
					continue
				}
				if unmapped != nil {
					unmappedAnnotations = append(unmappedAnnotations, unmapped)
				}
				result.Unmapped = append(result.Unmapped, test)
				result.UnmappedTests++
			}
		}
	}

	// Phase 3: Update registry with mapped annotations
	for _, ann := range mappedAnnotations {
		updateRegistryFromAnnotation(registry, ann)
	}

	// Phase 4: Save updated registry
	if err := uc.writer.SaveAll(registryDir, registry); err != nil {
		return nil, fmt.Errorf("saving updated registry: %w", err)
	}

	// Phase 5: Save unmapped tests
	if len(unmappedAnnotations) > 0 {
		if err := saveAnnotatedUnmapped(registryDir, unmappedAnnotations); err != nil {
			return nil, fmt.Errorf("saving unmapped tests: %w", err)
		}
	} else if len(result.Unmapped) > 0 {
		// Fallback: save basic unmapped info
		if err := saveUnmappedTests(uc.writer, registryDir, result.Unmapped); err != nil {
			return nil, fmt.Errorf("saving unmapped tests: %w", err)
		}
	}

	return result, nil
}

// updateRegistryFromAnnotation applies an annotated test's data to the registry.
func updateRegistryFromAnnotation(reg *domain.Registry, ann *adapters.AnnotatedTest) {
	for _, featureID := range ann.FeatureIDs {
		feature, err := reg.GetFeature(featureID)
		if err != nil {
			continue // Feature not in registry
		}

		// Build TestEntry from the annotation
		testEntry := domain.TestEntry{
			File: ann.FilePath,
		}
		for _, fn := range ann.Functions {
			runCmd := domain.GenerateRunCommand(ann.Framework, ann.TestType, ann.FilePath, fn.Name)
			testEntry.Functions = append(testEntry.Functions, domain.FunctionEntry{
				Name: fn.Name,
				Run:  runCmd,
			})
		}

		// Determine mocked status from flags
		isMocked := domain.HasFlag(ann.Flags, "#mocked")

		// Apply to the appropriate coverage entry
		applyCoverageFromAnnotation(feature, ann.TestType, ann.Platform, testEntry, isMocked)
	}

	// Also handle function-level feature overrides
	for _, fn := range ann.Functions {
		for _, featureID := range fn.FeatureIDs {
			// Skip if already covered by file-level annotation
			alreadyCovered := false
			for _, fileID := range ann.FeatureIDs {
				if fileID == featureID {
					alreadyCovered = true
					break
				}
			}
			if alreadyCovered {
				continue
			}

			feature, err := reg.GetFeature(featureID)
			if err != nil {
				continue
			}

			testEntry := domain.TestEntry{
				File: ann.FilePath,
				Functions: []domain.FunctionEntry{
					{
						Name: fn.Name,
						Run:  domain.GenerateRunCommand(ann.Framework, ann.TestType, ann.FilePath, fn.Name),
					},
				},
			}

			isMocked := domain.HasFlag(fn.Flags, "#mocked")
			applyCoverageFromAnnotation(feature, ann.TestType, ann.Platform, testEntry, isMocked)
		}
	}
}

// applyCoverageFromAnnotation adds a test entry to the right coverage slot on a feature.
func applyCoverageFromAnnotation(feature *domain.Feature, testType, platform string, testEntry domain.TestEntry, isMocked bool) {
	switch {
	case testType == "unit" && platform == "backend":
		if feature.Coverage.Unit.Backend == nil {
			feature.Coverage.Unit.Backend = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdate(feature.Coverage.Unit.Backend, testEntry, isMocked)

	case testType == "unit" && platform == "web":
		if feature.Coverage.Unit.Web == nil {
			feature.Coverage.Unit.Web = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdate(feature.Coverage.Unit.Web, testEntry, isMocked)

	case testType == "unit" && platform == "mobile":
		if feature.Coverage.Unit.Mobile == nil {
			feature.Coverage.Unit.Mobile = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdate(feature.Coverage.Unit.Mobile, testEntry, isMocked)

	case testType == "integration" && platform == "backend":
		if feature.Coverage.Integration.Backend == nil {
			feature.Coverage.Integration.Backend = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdate(feature.Coverage.Integration.Backend, testEntry, isMocked)

	case testType == "integration" && platform == "mobile":
		if feature.Coverage.Integration.Mobile == nil {
			feature.Coverage.Integration.Mobile = &domain.CoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdate(feature.Coverage.Integration.Mobile, testEntry, isMocked)

	case testType == "e2e" && platform == "web":
		if feature.Coverage.E2E.Web == nil {
			feature.Coverage.E2E.Web = &domain.E2ECoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdateE2E(feature.Coverage.E2E.Web, testEntry)

	case testType == "e2e" && platform == "mobile":
		if feature.Coverage.E2E.Mobile == nil {
			feature.Coverage.E2E.Mobile = &domain.E2ECoverageEntry{Status: domain.StatusMissing}
		}
		addTestEntryAndUpdateE2E(feature.Coverage.E2E.Mobile, testEntry)
	}
}

// addTestEntryAndUpdate adds a test entry to a coverage entry, deduplicating by file,
// and updates status from missing to covered.
func addTestEntryAndUpdate(entry *domain.CoverageEntry, testEntry domain.TestEntry, isMocked bool) {
	// Deduplicate by file path
	for i, existing := range entry.Tests {
		if existing.File == testEntry.File {
			// Merge functions
			entry.Tests[i].Functions = mergeFunctions(existing.Functions, testEntry.Functions)
			entry.Mocked = isMocked
			if entry.Status == domain.StatusMissing {
				entry.Status = domain.StatusCovered
			}
			return
		}
	}

	entry.Tests = append(entry.Tests, testEntry)
	entry.Mocked = isMocked

	if entry.Status == domain.StatusMissing {
		entry.Status = domain.StatusCovered
	}
}

// addTestEntryAndUpdateE2E adds a test entry to an E2E coverage entry.
func addTestEntryAndUpdateE2E(entry *domain.E2ECoverageEntry, testEntry domain.TestEntry) {
	for i, existing := range entry.Tests {
		if existing.File == testEntry.File {
			entry.Tests[i].Functions = mergeFunctions(existing.Functions, testEntry.Functions)
			if entry.Status == domain.StatusMissing {
				entry.Status = domain.StatusCovered
			}
			return
		}
	}

	entry.Tests = append(entry.Tests, testEntry)

	if entry.Status == domain.StatusMissing {
		entry.Status = domain.StatusCovered
	}
}

// mergeFunctions combines two function slices, deduplicating by name.
func mergeFunctions(existing, newFns []domain.FunctionEntry) []domain.FunctionEntry {
	seen := make(map[string]bool, len(existing))
	for _, fn := range existing {
		seen[fn.Name] = true
	}
	result := make([]domain.FunctionEntry, len(existing))
	copy(result, existing)
	for _, fn := range newFns {
		if !seen[fn.Name] {
			result = append(result, fn)
			seen[fn.Name] = true
		}
	}
	return result
}

// unmappedEntry represents a single entry in the _unmapped.yaml file.
type unmappedEntry struct {
	File      string   `yaml:"file"`
	Type      string   `yaml:"type"`
	Platform  string   `yaml:"platform"`
	Framework string   `yaml:"framework"`
	Functions []string `yaml:"functions,omitempty"`
}

// unmappedFile represents the structure of _unmapped.yaml.
type unmappedFile struct {
	Unmapped []unmappedEntry `yaml:"unmapped"`
}

// saveAnnotatedUnmapped writes unmapped tests with function detail to _unmapped.yaml.
func saveAnnotatedUnmapped(registryDir string, unmapped []*adapters.AnnotatedTest) error {
	data := unmappedFile{}

	for _, ann := range unmapped {
		entry := unmappedEntry{
			File:      ann.FilePath,
			Type:      ann.TestType,
			Platform:  ann.Platform,
			Framework: ann.Framework,
		}
		for _, fn := range ann.Functions {
			entry.Functions = append(entry.Functions, fn.Name)
		}
		data.Unmapped = append(data.Unmapped, entry)
	}

	content, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshaling unmapped data: %w", err)
	}

	header := "# Files without @testreg annotations\n# Add annotations to these files to include them in the registry\n"
	filePath := filepath.Join(registryDir, "_unmapped.yaml")

	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", registryDir, err)
	}

	if err := os.WriteFile(filePath, []byte(header+string(content)), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", filePath, err)
	}

	return nil
}

// saveUnmappedTests writes unmapped tests to a _unmapped.yaml file using the legacy format.
// This is the backward-compatible fallback when annotation parsing is not available.
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
			files = append(files, entry.AllFiles()...)
		}
	}
	appendE2EFiles := func(entry *domain.E2ECoverageEntry) {
		if entry != nil {
			files = append(files, entry.AllFiles()...)
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

// ---------------------------------------------------------------------------
// Auto-mapping: match test files to features by directory proximity
// ---------------------------------------------------------------------------

// buildFeatureDirIndex creates a lookup from domain names to feature IDs.
// Used by autoMapByProximity to match test file paths to features without annotations.
func buildFeatureDirIndex(registry *domain.Registry) map[string][]string {
	index := make(map[string][]string)
	for _, df := range registry.Domains {
		for _, f := range df.Features {
			found := false
			for _, existing := range index[df.Domain] {
				if existing == f.ID {
					found = true
					break
				}
			}
			if !found {
				index[df.Domain] = append(index[df.Domain], f.ID)
			}
		}
	}
	return index
}

// autoMapByProximity matches a test file path to features by comparing
// path segments against domain names in the feature registry.
//
// Strategy (priority order, nearest directory wins):
//  1. Directory segment match: any directory in the test file's path matches a domain name
//     "server/modules/auth/auth_test.go" → "auth" directory → auth.* features
//  2. Path substring match: test file's path contains a domain name as a segment
//     "client/src/features/grafo/EnrollDialog.test.tsx" → "enroll" in path → enroll.* features
func autoMapByProximity(testFilePath string, featureByDir map[string][]string) []string {
	if len(featureByDir) == 0 {
		return nil
	}

	normalized := filepath.ToSlash(testFilePath)
	parts := strings.Split(normalized, "/")

	// Strategy 1: Check each directory segment (deepest first → nearest match wins).
	for i := len(parts) - 2; i >= 0; i-- {
		dir := strings.ToLower(parts[i])
		if features, ok := featureByDir[dir]; ok {
			return features
		}
	}

	// Strategy 2: Check if any path segment contains a domain name as substring.
	lowerPath := strings.ToLower(normalized)
	for domain, features := range featureByDir {
		if strings.Contains(lowerPath, "/"+domain+"/") || strings.Contains(lowerPath, "/"+domain+".") {
			return features
		}
	}

	return nil
}
