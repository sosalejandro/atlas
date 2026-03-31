package app

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// Layer weights for health score calculation.
var layerWeights = map[string]float64{
	"handler":    0.30,
	"service":    0.30,
	"repository": 0.25,
	"query":      0.15,
}

// AuditFeatureUseCase generates a unified feature health report.
type AuditFeatureUseCase struct {
	traceUC  *TraceFeatureUseCase
	registry ports.RegistryReader
}

// NewAuditFeatureUseCase creates a new AuditFeatureUseCase.
func NewAuditFeatureUseCase(traceUC *TraceFeatureUseCase, registry ports.RegistryReader) *AuditFeatureUseCase {
	return &AuditFeatureUseCase{
		traceUC:  traceUC,
		registry: registry,
	}
}

// Execute generates the health report for a single feature.
func (uc *AuditFeatureUseCase) Execute(registryDir, featureID string, config ports.GraphConfig) (*domain.AuditOutput, error) {
	// Step 1: Trace the dependency graph.
	traceOutput, err := uc.traceUC.Execute(registryDir, featureID, config)
	if err != nil {
		return nil, fmt.Errorf("tracing feature %q: %w", featureID, err)
	}

	// Step 2: Load the feature from registry to get test file information.
	reg, err := uc.registry.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	feature, err := reg.GetFeature(featureID)
	if err != nil {
		return nil, fmt.Errorf("feature %q not found in registry: %w", featureID, err)
	}

	// Collect all known test files from the feature's coverage entries.
	knownTestFiles := collectTestFiles(feature)

	// Build a set of test file directories and basenames for matching.
	testFileDirs := buildTestFileIndex(knownTestFiles)

	// Step 3: Walk the trace tree and annotate each node.
	var allNodes []domain.AnnotatedNode
	for _, tr := range traceOutput.Traces {
		if tr == nil || tr.Root == nil {
			continue
		}
		walkAndAnnotate(tr.Root, testFileDirs, &allNodes, make(map[string]bool))
	}

	// Deduplicate annotated nodes by NodeID.
	allNodes = deduplicateNodes(allNodes)

	// Step 4: Group nodes by layer and calculate coverage.
	layerCoverage := computeLayerCoverage(allNodes)

	// Step 5: Identify gaps — untested nodes prioritized by severity.
	gaps := identifyGaps(allNodes)

	// Step 6: Generate recommended actions.
	actions := generateAuditActions(gaps, feature)

	// Step 7: Build E2E coverage summary.
	e2eWeb := buildE2ECoverageStatus(feature.Coverage.E2E.Web)
	e2eMobile := buildE2ECoverageStatus(feature.Coverage.E2E.Mobile)

	// Step 8: Calculate health score.
	// If graph tracing produced annotated nodes, use the weighted layer average.
	// If not (CLI tools, libraries, or features without API surfaces), fall back
	// to the registry's coverage entries — the same data that `testreg status` uses.
	var healthScore float64
	if len(allNodes) > 0 {
		healthScore = calculateHealthScore(layerCoverage)
	} else {
		healthScore = registryBasedHealth(feature)
	}

	// Step 9: Analyze performance testing gaps.
	// If graph tracing produced nodes, check each node's test file for benchmark/race patterns.
	// If not, fall back to scanning the feature's registered test files directly.
	var perfGaps []domain.PerfGap
	var perfScore *domain.PerfScore
	if len(allNodes) > 0 {
		perfGaps, perfScore = analyzePerformanceGaps(allNodes, config.ProjectRoot)
	} else {
		perfGaps, perfScore = registryBasedPerfAnalysis(feature, config.ProjectRoot)
	}

	return &domain.AuditOutput{
		FeatureID:      feature.ID,
		FeatureName:    feature.Name,
		Priority:       string(feature.Priority),
		TraceResults:   traceOutput.Traces,
		APISurfaces:    traceOutput.APISurfaces,
		TestFiles:      traceOutput.TestFiles,
		LayerCoverage:  layerCoverage,
		AnnotatedNodes: allNodes,
		Gaps:           gaps,
		Actions:        actions,
		PerfGaps:       perfGaps,
		PerfScore:      perfScore,
		E2EWeb:         e2eWeb,
		E2EMobile:      e2eMobile,
		HealthScore:    healthScore,
	}, nil
}

// registryBasedHealth computes a health score from the feature's coverage entries
// in the registry YAML. Used as a fallback when graph tracing produces no nodes
// (CLI tools, libraries, event-driven apps without API surfaces).
//
// The score is the fraction of non-nil coverage entries with status "covered" or
// "partial". This is the same data source that `testreg status` reads.
func registryBasedHealth(feature *domain.Feature) float64 {
	entries := feature.AllCoverageEntries()
	if len(entries) == 0 {
		return 0.0
	}

	covered := 0
	for _, status := range entries {
		if status.IsCovered() || status == domain.StatusPartial {
			covered++
		}
	}

	return float64(covered) / float64(len(entries))
}

// registryBasedPerfAnalysis scans the feature's registered test files for
// benchmark and race-test patterns. Used as a fallback when graph tracing
// produces no nodes (CLI tools, libraries, event-driven apps).
//
// Unlike analyzePerformanceGaps which checks per-node, this scans ALL test
// files for the feature and reports aggregate presence/absence.
func registryBasedPerfAnalysis(feature *domain.Feature, projectRoot string) ([]domain.PerfGap, *domain.PerfScore) {
	testFiles := collectTestFiles(feature)
	if len(testFiles) == 0 {
		return nil, &domain.PerfScore{}
	}

	totalFiles := len(testFiles)
	filesWithBenchmark := 0
	filesWithRace := 0

	for _, tf := range testFiles {
		absPath := tf
		if !filepath.IsAbs(tf) && projectRoot != "" {
			absPath = filepath.Join(projectRoot, tf)
		}
		hasBench, hasRace := scanTestFileForPerfPatterns(absPath)
		if hasBench {
			filesWithBenchmark++
		}
		if hasRace {
			filesWithRace++
		}
	}

	// Build gaps for files missing benchmarks or race tests.
	var gaps []domain.PerfGap
	for _, tf := range testFiles {
		absPath := tf
		if !filepath.IsAbs(tf) && projectRoot != "" {
			absPath = filepath.Join(projectRoot, tf)
		}
		hasBench, hasRace := scanTestFileForPerfPatterns(absPath)

		if !hasBench {
			gaps = append(gaps, domain.PerfGap{
				NodeID:     filepath.Base(tf),
				Kind:       "test-file",
				File:       tf,
				Severity:   "medium",
				GapType:    "no-benchmark",
				Reason:     "test file has no Benchmark* functions",
				Suggestion: fmt.Sprintf("Add Benchmark* functions to %s", filepath.Base(tf)),
			})
		}
		if !hasRace {
			gaps = append(gaps, domain.PerfGap{
				NodeID:     filepath.Base(tf),
				Kind:       "test-file",
				File:       tf,
				Severity:   "medium",
				GapType:    "no-race-test",
				Reason:     "test file has no t.Parallel() calls",
				Suggestion: fmt.Sprintf("Add t.Parallel() to tests in %s", filepath.Base(tf)),
			})
		}
	}

	score := &domain.PerfScore{
		BenchmarkedNodes:   filesWithBenchmark,
		BenchmarkableNodes: totalFiles,
		RaceTestedNodes:    filesWithRace,
		ConcurrentNodes:    totalFiles,
	}
	if totalFiles > 0 {
		score.BenchmarkCoverage = float64(filesWithBenchmark) / float64(totalFiles)
		score.RaceTestCoverage = float64(filesWithRace) / float64(totalFiles)
	}
	score.Overall = score.BenchmarkCoverage*0.6 + score.RaceTestCoverage*0.4

	return gaps, score
}

// ExecuteAll generates a summary report for all features.
func (uc *AuditFeatureUseCase) ExecuteAll(registryDir string, config ports.GraphConfig) ([]*domain.AuditOutput, error) {
	reg, err := uc.registry.LoadAll(registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry from %s: %w", registryDir, err)
	}

	features := reg.AllFeatures()
	if len(features) == 0 {
		return nil, nil
	}

	results := make([]*domain.AuditOutput, 0, len(features))
	for _, f := range features {
		output, err := uc.Execute(registryDir, f.ID, config)
		if err != nil {
			// Log the error but continue with other features.
			// Create a minimal output with zero health score.
			results = append(results, &domain.AuditOutput{
				FeatureID:   f.ID,
				FeatureName: f.Name,
				Priority:    string(f.Priority),
				HealthScore: 0.0,
			})
			continue
		}
		results = append(results, output)
	}

	// Sort by health score ascending (worst first).
	sort.Slice(results, func(i, j int) bool {
		return results[i].HealthScore < results[j].HealthScore
	})

	return results, nil
}

// testFileIndex maps directory paths and file basenames for efficient test file matching.
type testFileIndex struct {
	// dirSet contains normalized directory paths of known test files.
	dirSet map[string]bool
	// fileSet contains full relative paths of known test files.
	fileSet map[string]bool
	// baseNameSet maps base filenames (without _test.go suffix) to their test file paths.
	baseNameSet map[string][]string
}

// buildTestFileIndex creates a lookup index from known test file paths.
func buildTestFileIndex(testFiles []string) *testFileIndex {
	idx := &testFileIndex{
		dirSet:      make(map[string]bool),
		fileSet:     make(map[string]bool),
		baseNameSet: make(map[string][]string),
	}

	for _, tf := range testFiles {
		normalized := filepath.ToSlash(tf)
		idx.fileSet[normalized] = true
		idx.dirSet[filepath.ToSlash(filepath.Dir(tf))] = true

		// Extract the base name for matching: "auth_handler_test.go" -> "auth_handler"
		base := filepath.Base(tf)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		// Remove common test suffixes.
		for _, suffix := range []string{"_test", ".test", ".spec", "_e2e_test", "_integration_test"} {
			base = strings.TrimSuffix(base, suffix)
		}
		idx.baseNameSet[base] = append(idx.baseNameSet[base], tf)
	}

	return idx
}

// walkAndAnnotate recursively walks a trace tree and annotates each node with test status.
func walkAndAnnotate(tn *domain.TraceNode, idx *testFileIndex, nodes *[]domain.AnnotatedNode, visited map[string]bool) {
	if tn == nil || tn.Node == nil {
		return
	}

	if visited[tn.Node.ID] {
		return
	}
	visited[tn.Node.ID] = true

	// Determine test status for this node.
	status, matchedFiles := matchNodeToTests(tn.Node, idx)

	*nodes = append(*nodes, domain.AnnotatedNode{
		NodeID:     tn.Node.ID,
		Kind:       string(tn.Node.Kind),
		File:       tn.Node.File,
		Line:       tn.Node.Line,
		TestStatus: status,
		TestFiles:  matchedFiles,
	})

	if tn.IsCycle {
		return
	}

	for _, child := range tn.Children {
		walkAndAnnotate(child, idx, nodes, visited)
	}
}

// matchNodeToTests determines if a graph node is covered by known test files.
// Returns (status, matched_test_files).
func matchNodeToTests(node *domain.Node, idx *testFileIndex) (string, []string) {
	if node.File == "" {
		// Nodes without files (e.g., SQL queries, endpoints) can't be directly matched.
		// Check if any test file name references this node's ID.
		return matchNodeByID(node, idx)
	}

	normalizedFile := filepath.ToSlash(node.File)
	nodeDir := filepath.ToSlash(filepath.Dir(node.File))

	// Strategy 1: Check if there's a corresponding test file in the same directory.
	// For "handler/auth.go", check if "handler/auth_test.go" exists in known test files.
	baseNoExt := strings.TrimSuffix(filepath.Base(node.File), filepath.Ext(node.File))

	// Check Go test file convention: same_name_test.go
	goTestFile := filepath.ToSlash(filepath.Join(filepath.Dir(node.File), baseNoExt+"_test.go"))
	if idx.fileSet[goTestFile] {
		return "tested", []string{goTestFile}
	}

	// Check TypeScript test conventions: same_name.test.ts, same_name.test.tsx, same_name.spec.ts
	for _, ext := range []string{".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx"} {
		tsTestFile := filepath.ToSlash(filepath.Join(filepath.Dir(node.File), baseNoExt+ext))
		if idx.fileSet[tsTestFile] {
			return "tested", []string{tsTestFile}
		}
	}

	// Strategy 2: Check if any known test file is in the same directory as the node's file.
	var sameDir []string
	if idx.dirSet[nodeDir] {
		for tf := range idx.fileSet {
			if filepath.ToSlash(filepath.Dir(tf)) == nodeDir {
				sameDir = append(sameDir, tf)
			}
		}
	}
	if len(sameDir) > 0 {
		// Test file exists in same directory but doesn't match exact name —
		// the specific function may or may not be covered.
		return "partial", sameDir
	}

	// Strategy 3: Check by base name similarity.
	if matched, ok := idx.baseNameSet[baseNoExt]; ok && len(matched) > 0 {
		return "partial", matched
	}

	// Strategy 4: Check if the node's file path contains a package that matches
	// a test file's directory path (e.g., node in "auth/" matched by test in "auth/").
	var packageMatches []string
	for tf := range idx.fileSet {
		// If a test file's path shares a common package segment with the node's file
		if sharesPackage(normalizedFile, tf) {
			packageMatches = append(packageMatches, tf)
		}
	}
	if len(packageMatches) > 0 {
		return "partial", packageMatches
	}

	return "untested", nil
}

// matchNodeByID tries to match a node without a file path by checking if any
// test file name references the node's ID (e.g., SQL query names).
func matchNodeByID(node *domain.Node, idx *testFileIndex) (string, []string) {
	// For SQL queries like "sql:GetUserByEmail", extract the query name.
	queryName := node.ID
	if strings.HasPrefix(queryName, "sql:") {
		queryName = strings.TrimPrefix(queryName, "sql:")
	}

	// Check if any test file basename contains the query name (case-insensitive).
	lower := strings.ToLower(queryName)
	for base, files := range idx.baseNameSet {
		if strings.Contains(strings.ToLower(base), lower) {
			return "partial", files
		}
	}

	return "untested", nil
}

// sharesPackage checks if two file paths share a significant package directory segment.
func sharesPackage(file1, file2 string) bool {
	parts1 := strings.Split(filepath.ToSlash(file1), "/")
	parts2 := strings.Split(filepath.ToSlash(file2), "/")

	// Skip trivial directory names.
	skip := map[string]bool{
		"src": true, "internal": true, "pkg": true, "cmd": true,
		"tests": true, "test": true, "__tests__": true,
	}

	for _, p1 := range parts1 {
		if skip[p1] || p1 == "" {
			continue
		}
		for _, p2 := range parts2 {
			if skip[p2] || p2 == "" {
				continue
			}
			if p1 == p2 && len(p1) > 3 {
				return true
			}
		}
	}
	return false
}

// deduplicateNodes removes duplicate nodes by NodeID, keeping the first occurrence.
func deduplicateNodes(nodes []domain.AnnotatedNode) []domain.AnnotatedNode {
	seen := make(map[string]bool)
	var result []domain.AnnotatedNode
	for _, n := range nodes {
		if !seen[n.NodeID] {
			seen[n.NodeID] = true
			result = append(result, n)
		}
	}
	return result
}

// computeLayerCoverage groups annotated nodes by kind and calculates coverage percentages.
func computeLayerCoverage(nodes []domain.AnnotatedNode) []domain.LayerCoverage {
	type layerStats struct {
		tested int
		total  int
	}

	stats := make(map[string]*layerStats)

	// Define the layers we track (ordered).
	layerOrder := []string{"handler", "service", "repository", "query", "component", "hook"}
	for _, l := range layerOrder {
		stats[l] = &layerStats{}
	}

	for _, n := range nodes {
		kind := n.Kind
		s, ok := stats[kind]
		if !ok {
			// Unknown kind — skip.
			continue
		}
		s.total++
		if n.TestStatus == "tested" || n.TestStatus == "partial" {
			s.tested++
		}
	}

	var result []domain.LayerCoverage
	for _, layer := range layerOrder {
		s := stats[layer]
		if s.total == 0 {
			continue
		}
		pct := float64(s.tested) / float64(s.total) * 100
		result = append(result, domain.LayerCoverage{
			Layer:      layer,
			Tested:     s.tested,
			Total:      s.total,
			Percentage: pct,
		})
	}

	return result
}

// identifyGaps returns untested or partially-tested nodes as gaps, prioritized by severity.
func identifyGaps(nodes []domain.AnnotatedNode) []domain.AuditGap {
	var gaps []domain.AuditGap

	for _, n := range nodes {
		if n.TestStatus == "tested" {
			continue
		}

		severity := gapSeverity(n.Kind)
		reason := gapReason(n.Kind, n.TestStatus)

		gaps = append(gaps, domain.AuditGap{
			NodeID:   n.NodeID,
			Kind:     n.Kind,
			File:     n.File,
			Line:     n.Line,
			Severity: severity,
			Reason:   reason,
		})
	}

	// Sort by severity: critical > high > medium > low.
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.SliceStable(gaps, func(i, j int) bool {
		return severityOrder[gaps[i].Severity] < severityOrder[gaps[j].Severity]
	})

	return gaps
}

// gapSeverity returns the severity level for an untested node based on its kind.
func gapSeverity(kind string) string {
	switch kind {
	case "handler", "service":
		return "critical"
	case "repository":
		return "high"
	case "query":
		return "medium"
	default:
		return "low"
	}
}

// gapReason returns a human-readable reason for the coverage gap.
func gapReason(kind, testStatus string) string {
	label := "no"
	if testStatus == "partial" {
		label = "incomplete"
	}

	switch kind {
	case "handler":
		return fmt.Sprintf("%s unit test for handler", label)
	case "service":
		return fmt.Sprintf("%s unit test for service method", label)
	case "repository":
		return fmt.Sprintf("%s integration test for repository", label)
	case "query":
		return fmt.Sprintf("%s test coverage for SQL query", label)
	case "component":
		return fmt.Sprintf("%s test for component", label)
	case "hook":
		return fmt.Sprintf("%s test for hook", label)
	default:
		return fmt.Sprintf("%s test coverage", label)
	}
}

// generateAuditActions creates recommended actions from identified gaps.
func generateAuditActions(gaps []domain.AuditGap, feature *domain.Feature) []domain.AuditAction {
	var actions []domain.AuditAction
	priority := 1

	for _, gap := range gaps {
		action := domain.AuditAction{
			Priority: priority,
			File:     gap.File,
			Line:     gap.Line,
		}

		switch gap.Kind {
		case "handler":
			action.Description = fmt.Sprintf("Write unit test for %s", gap.NodeID)
			action.TestType = "unit"
			if gap.File != "" {
				action.File = suggestTestFile(gap.File)
			}
		case "service":
			action.Description = fmt.Sprintf("Write unit test for %s", gap.NodeID)
			action.TestType = "unit"
			if gap.File != "" {
				action.File = suggestTestFile(gap.File)
			}
		case "repository":
			action.Description = fmt.Sprintf("Write integration test for %s", gap.NodeID)
			action.TestType = "integration"
			if gap.File != "" {
				action.File = suggestIntegrationTestFile(gap.File)
			}
		case "query":
			action.Description = fmt.Sprintf("Write integration test covering %s", gap.NodeID)
			action.TestType = "integration"
		case "component":
			action.Description = fmt.Sprintf("Write component test for %s", gap.NodeID)
			action.TestType = "unit"
			if gap.File != "" {
				action.File = suggestTestFile(gap.File)
			}
		case "hook":
			action.Description = fmt.Sprintf("Write unit test for %s", gap.NodeID)
			action.TestType = "unit"
			if gap.File != "" {
				action.File = suggestTestFile(gap.File)
			}
		default:
			action.Description = fmt.Sprintf("Add test coverage for %s", gap.NodeID)
			action.TestType = "unit"
		}

		actions = append(actions, action)
		priority++
	}

	// Add E2E action if no E2E coverage exists.
	if feature.Coverage.E2E.Web == nil || feature.Coverage.E2E.Web.Status.IsMissing() {
		if feature.Surfaces.Web != nil {
			actions = append(actions, domain.AuditAction{
				Priority:    priority,
				Description: fmt.Sprintf("Add Playwright E2E test for %s route", feature.Surfaces.Web.Route),
				TestType:    "e2e",
			})
			priority++
		}
	}
	if feature.Coverage.E2E.Mobile == nil || feature.Coverage.E2E.Mobile.Status.IsMissing() {
		if feature.Surfaces.Mobile != nil {
			actions = append(actions, domain.AuditAction{
				Priority:    priority,
				Description: fmt.Sprintf("Add Maestro E2E flow for %s screen", feature.Surfaces.Mobile.Screen),
				TestType:    "e2e",
			})
		}
	}

	return actions
}

// suggestTestFile suggests the test file path for a given source file.
func suggestTestFile(file string) string {
	ext := filepath.Ext(file)
	base := strings.TrimSuffix(file, ext)

	switch ext {
	case ".go":
		return base + "_test.go"
	case ".ts", ".tsx":
		return base + ".test" + ext
	case ".js", ".jsx":
		return base + ".test" + ext
	default:
		return base + "_test" + ext
	}
}

// suggestIntegrationTestFile suggests an integration test file location.
func suggestIntegrationTestFile(file string) string {
	// For Go files, integration tests typically live in tests/integration/
	if strings.HasSuffix(file, ".go") {
		base := filepath.Base(file)
		base = strings.TrimSuffix(base, ".go")

		// Extract the package/directory name for context.
		dir := filepath.Dir(file)
		pkg := filepath.Base(dir)

		return filepath.ToSlash(filepath.Join("tests", "integration", "repositories", pkg+"_"+base+"_test.go"))
	}

	return suggestTestFile(file)
}

// buildE2ECoverageStatus creates an E2E coverage summary from a coverage entry.
func buildE2ECoverageStatus(entry *domain.E2ECoverageEntry) *domain.E2ECoverageStatus {
	if entry == nil {
		return nil
	}

	files := entry.AllFiles()
	testCount := 0
	for _, t := range entry.Tests {
		testCount += len(t.Functions)
		if len(t.Functions) == 0 {
			testCount++ // Count the file itself as a test if no functions listed.
		}
	}
	if testCount == 0 {
		testCount = len(files) // Fallback: count files as tests.
	}

	return &domain.E2ECoverageStatus{
		Covered:   entry.Status.IsCovered() || entry.Status == domain.StatusPartial,
		TestFiles: files,
		TestCount: testCount,
	}
}

// ---------------------------------------------------------------------------
// Performance gap analysis
// ---------------------------------------------------------------------------

// analyzePerformanceGaps checks each annotated node for performance test coverage.
// A node is a performance gap if:
//   - It's a handler on a hot path AND has no Benchmark* function in its test file
//   - It's concurrent (handler/service) AND has no race-test evidence (t.Parallel())
//   - It's a repository AND has no benchmark for query performance
//   - It's a query node (always suggest EXPLAIN ANALYZE)
func analyzePerformanceGaps(nodes []domain.AnnotatedNode, projectRoot string) ([]domain.PerfGap, *domain.PerfScore) {
	var gaps []domain.PerfGap

	// Counters for scoring.
	benchmarkableNodes := 0
	benchmarkedNodes := 0
	concurrentNodes := 0
	raceTestedNodes := 0

	for _, n := range nodes {
		kind := n.Kind

		// Determine if this node type is benchmarkable or concurrent.
		isBenchmarkable := kind == "handler" || kind == "service" || kind == "repository"
		isConcurrent := kind == "handler" || kind == "service"

		if !isBenchmarkable && kind != "query" {
			continue
		}

		if isBenchmarkable {
			benchmarkableNodes++
		}
		if isConcurrent {
			concurrentNodes++
		}

		// Find the corresponding test file to check for benchmark/race patterns.
		testFilePath := resolveTestFilePath(n.File, projectRoot)
		hasBenchmark := false
		hasRaceEvidence := false

		if testFilePath != "" {
			hasBenchmark, hasRaceEvidence = scanTestFileForPerfPatterns(testFilePath)
		}

		if hasBenchmark && isBenchmarkable {
			benchmarkedNodes++
		}
		if hasRaceEvidence && isConcurrent {
			raceTestedNodes++
		}

		// Extract a short function name from the node ID for suggestions.
		funcName := extractFuncName(n.NodeID)
		testFileName := suggestTestFileName(n.File)
		pkgDir := suggestPackageDir(n.File)

		// Check for benchmark gap.
		if isBenchmarkable && !hasBenchmark {
			severity := "high"
			if kind == "handler" {
				severity = "critical"
			}

			gap := domain.PerfGap{
				NodeID:     n.NodeID,
				Kind:       kind,
				File:       n.File,
				Line:       n.Line,
				Severity:   severity,
				GapType:    "no-benchmark",
				Reason:     perfBenchmarkReason(kind),
				Suggestion: fmt.Sprintf("Write Benchmark%s in %s", funcName, testFileName),
			}
			if pkgDir != "" {
				gap.Command = fmt.Sprintf("go test -benchmem -bench Benchmark%s ./%s/...", funcName, pkgDir)
			}
			gaps = append(gaps, gap)
		}

		// Check for race test gap.
		if isConcurrent && !hasRaceEvidence {
			gap := domain.PerfGap{
				NodeID:     n.NodeID,
				Kind:       kind,
				File:       n.File,
				Line:       n.Line,
				Severity:   "critical",
				GapType:    "no-race-test",
				Reason:     fmt.Sprintf("concurrent %s, no race test evidence", kind),
				Suggestion: fmt.Sprintf("Add t.Parallel() and run with -race flag for %s", funcName),
			}
			if pkgDir != "" {
				gap.Command = fmt.Sprintf("go test -race -run Test%s -count=10 ./%s/...", funcName, pkgDir)
			}
			gaps = append(gaps, gap)
		}

		// Query nodes always get a memory-baseline suggestion.
		if kind == "query" {
			gap := domain.PerfGap{
				NodeID:     n.NodeID,
				Kind:       kind,
				File:       n.File,
				Line:       n.Line,
				Severity:   "medium",
				GapType:    "no-memory-baseline",
				Reason:     "SQL query with no EXPLAIN ANALYZE baseline",
				Suggestion: fmt.Sprintf("Run EXPLAIN ANALYZE on %s and document expected plan", n.NodeID),
				Command:    fmt.Sprintf("EXPLAIN ANALYZE -- %s", n.NodeID),
			}
			gaps = append(gaps, gap)
		}
	}

	// Sort gaps by severity.
	severityOrder := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	sort.SliceStable(gaps, func(i, j int) bool {
		return severityOrder[gaps[i].Severity] < severityOrder[gaps[j].Severity]
	})

	// Calculate performance score.
	score := &domain.PerfScore{
		BenchmarkedNodes:   benchmarkedNodes,
		BenchmarkableNodes: benchmarkableNodes,
		RaceTestedNodes:    raceTestedNodes,
		ConcurrentNodes:    concurrentNodes,
	}

	if benchmarkableNodes > 0 {
		score.BenchmarkCoverage = float64(benchmarkedNodes) / float64(benchmarkableNodes)
	}
	if concurrentNodes > 0 {
		score.RaceTestCoverage = float64(raceTestedNodes) / float64(concurrentNodes)
	}
	score.Overall = score.BenchmarkCoverage*0.6 + score.RaceTestCoverage*0.4

	return gaps, score
}

// resolveTestFilePath finds the test file corresponding to a source file.
// For Go files at "some/path/foo.go", it checks for "some/path/foo_test.go".
// Returns the absolute path if found, empty string otherwise.
func resolveTestFilePath(sourceFile, projectRoot string) string {
	if sourceFile == "" {
		return ""
	}

	ext := filepath.Ext(sourceFile)
	if ext != ".go" {
		return ""
	}

	base := strings.TrimSuffix(sourceFile, ext)
	testFile := base + "_test.go"

	// Try absolute path first.
	absPath := testFile
	if !filepath.IsAbs(testFile) && projectRoot != "" {
		absPath = filepath.Join(projectRoot, testFile)
	}

	if _, err := os.Stat(absPath); err == nil {
		return absPath
	}

	return ""
}

// scanTestFileForPerfPatterns reads a test file and checks for benchmark
// functions (func Benchmark*) and race-test evidence (t.Parallel()).
func scanTestFileForPerfPatterns(testFilePath string) (hasBenchmark, hasRaceEvidence bool) {
	f, err := os.Open(testFilePath)
	if err != nil {
		return false, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "func Benchmark") {
			hasBenchmark = true
		}
		if strings.Contains(trimmed, "t.Parallel()") || strings.Contains(trimmed, ".Parallel()") {
			hasRaceEvidence = true
		}

		// Early exit if both found.
		if hasBenchmark && hasRaceEvidence {
			return true, true
		}
	}

	return hasBenchmark, hasRaceEvidence
}

// perfBenchmarkReason returns a human-readable reason for a benchmark gap.
func perfBenchmarkReason(kind string) string {
	switch kind {
	case "handler":
		return "handler on hot path, no benchmark"
	case "service":
		return "service method, no benchmark"
	case "repository":
		return "repository, no benchmark"
	default:
		return "no benchmark"
	}
}

// extractFuncName extracts a short function name from a node ID.
// Examples: "RecipeHandler.ListRecipes" -> "ListRecipes"
//           "handler:ListRecipes" -> "ListRecipes"
func extractFuncName(nodeID string) string {
	// Try "Type.Method" format.
	if idx := strings.LastIndex(nodeID, "."); idx >= 0 {
		return nodeID[idx+1:]
	}
	// Try "prefix:Name" format.
	if idx := strings.LastIndex(nodeID, ":"); idx >= 0 {
		return nodeID[idx+1:]
	}
	return nodeID
}

// suggestTestFileName returns the suggested test file name for a source file.
func suggestTestFileName(sourceFile string) string {
	if sourceFile == "" {
		return "<test_file>"
	}
	base := filepath.Base(sourceFile)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + "_test" + ext
}

// suggestPackageDir extracts a short package directory path for use in go test commands.
func suggestPackageDir(sourceFile string) string {
	if sourceFile == "" {
		return ""
	}
	dir := filepath.Dir(sourceFile)
	// Normalize to forward slashes for consistent output.
	return filepath.ToSlash(dir)
}

// calculateHealthScore computes a weighted health score from layer coverage.
func calculateHealthScore(layers []domain.LayerCoverage) float64 {
	totalWeight := 0.0
	weightedSum := 0.0

	for _, lc := range layers {
		weight, ok := layerWeights[lc.Layer]
		if !ok {
			weight = 0.05 // Small weight for unlisted layers.
		}
		totalWeight += weight
		weightedSum += weight * (lc.Percentage / 100.0)
	}

	if totalWeight == 0 {
		return 0.0
	}

	return weightedSum / totalWeight
}
