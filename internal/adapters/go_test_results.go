package adapters

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sosalejandro/testreg/internal/ports"
)

// GoTestResultParser parses `go test -json` output (line-delimited JSON).
type GoTestResultParser struct{}

// NewGoTestResultParser creates a new GoTestResultParser.
func NewGoTestResultParser() *GoTestResultParser {
	return &GoTestResultParser{}
}

// Name returns the parser's display name.
func (p *GoTestResultParser) Name() string {
	return "Go Test JSON Parser"
}

// goTestEvent represents a single line from `go test -json` output.
type goTestEvent struct {
	Time    string  `json:"Time"`
	Action  string  `json:"Action"` // "run", "output", "pass", "fail", "skip", "bench"
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"` // seconds
	Output  string  `json:"Output"`
}

// Parse reads a file containing `go test -json` output and returns test results.
// Each line in the file is a JSON object representing a test event.
func (p *GoTestResultParser) Parse(resultPath string) ([]ports.TestResult, error) {
	f, err := os.Open(resultPath)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", resultPath, err)
	}
	defer f.Close()

	// Track test outcomes by package+test name
	type testKey struct {
		pkg  string
		name string
	}
	outcomes := make(map[testKey]*ports.TestResult)

	scanner := bufio.NewScanner(f)
	// Increase buffer for potentially long output lines
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event goTestEvent
		if err := json.Unmarshal(line, &event); err != nil {
			// Skip non-JSON lines (build output, etc.)
			continue
		}

		// Only track test-level events (not package-level)
		if event.Test == "" {
			continue
		}

		key := testKey{pkg: event.Package, name: event.Test}

		switch event.Action {
		case "run":
			outcomes[key] = &ports.TestResult{
				FilePath:  packageToFilePath(event.Package),
				FeatureID: inferFeatureFromGoPackage(event.Package, event.Test),
			}

		case "pass":
			if r, ok := outcomes[key]; ok {
				r.Passed = true
				r.Duration = time.Duration(event.Elapsed * float64(time.Second))
			}

		case "fail":
			if r, ok := outcomes[key]; ok {
				r.Passed = false
				r.Duration = time.Duration(event.Elapsed * float64(time.Second))
			}

		case "output":
			if r, ok := outcomes[key]; ok {
				// Capture error output for failing tests
				if strings.Contains(event.Output, "FAIL") || strings.Contains(event.Output, "Error") {
					if r.Error == "" {
						r.Error = strings.TrimSpace(event.Output)
					}
				}
			}

		case "skip":
			// Remove skipped tests from results
			delete(outcomes, key)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", resultPath, err)
	}

	// Collect all completed test results
	var results []ports.TestResult
	for _, r := range outcomes {
		results = append(results, *r)
	}

	return results, nil
}

// packageToFilePath converts a Go package path to a relative file path.
// e.g., "github.com/user/project/src/services" -> "src/services"
func packageToFilePath(pkg string) string {
	parts := strings.Split(pkg, "/")
	// Find the first segment that looks like a local directory
	for i, p := range parts {
		if p == "src" || p == "internal" || p == "cmd" || p == "pkg" {
			return strings.Join(parts[i:], "/")
		}
	}
	// Fallback: return last two segments
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return pkg
}

// inferFeatureFromGoPackage attempts to extract a feature ID from the Go package and test name.
func inferFeatureFromGoPackage(pkg, testName string) string {
	// Extract meaningful segments from the package path
	parts := strings.Split(pkg, "/")
	var meaningful []string
	skip := map[string]bool{
		"src": true, "internal": true, "pkg": true, "cmd": true,
		"application": true, "infrastructure": true, "domain": true,
		"services": true, "handlers": true, "repositories": true,
	}

	for _, p := range parts {
		if !skip[p] && !strings.Contains(p, ".") {
			meaningful = append(meaningful, strings.ToLower(p))
		}
	}

	if len(meaningful) == 0 {
		return ""
	}

	// Use last meaningful segment as domain hint
	domain := meaningful[len(meaningful)-1]

	// Try to extract feature hint from test name
	// e.g., "TestLoginHandler" -> "login"
	testLower := strings.ToLower(testName)
	testLower = strings.TrimPrefix(testLower, "test")
	testLower = strings.TrimPrefix(testLower, "_")

	// Split camelCase
	feature := splitCamelToLower(testName)

	if feature != "" && feature != domain {
		return domain + "." + feature
	}

	return domain
}

// splitCamelToLower extracts the first meaningful word from a CamelCase test name.
func splitCamelToLower(name string) string {
	name = strings.TrimPrefix(name, "Test")
	name = strings.TrimPrefix(name, "_")

	if name == "" {
		return ""
	}

	// Find the first word boundary (lowercase followed by uppercase)
	var word []byte
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if i > 0 && ch >= 'A' && ch <= 'Z' {
			break
		}
		if ch >= 'A' && ch <= 'Z' {
			ch = ch + 32 // toLower
		}
		word = append(word, ch)
	}

	result := string(word)

	// Filter out common noise words
	noise := map[string]bool{
		"handler": true, "service": true, "repository": true,
		"test": true, "suite": true, "all": true,
	}
	if noise[result] {
		return ""
	}

	return result
}

// MaestroResultParser parses Maestro test output.
// Maestro outputs pass/fail per flow file, typically as exit codes and log files.
type MaestroResultParser struct{}

// NewMaestroResultParser creates a new MaestroResultParser.
func NewMaestroResultParser() *MaestroResultParser {
	return &MaestroResultParser{}
}

// Name returns the parser's display name.
func (p *MaestroResultParser) Name() string {
	return "Maestro Result Parser"
}

// Parse reads a Maestro output directory. It looks for flow result files
// and determines pass/fail based on file naming convention or content.
func (p *MaestroResultParser) Parse(resultPath string) ([]ports.TestResult, error) {
	info, err := os.Stat(resultPath)
	if err != nil {
		return nil, fmt.Errorf("accessing %s: %w", resultPath, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory — Maestro results should be a directory", resultPath)
	}

	var results []ports.TestResult

	entries, err := os.ReadDir(resultPath)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", resultPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".log") {
			continue
		}

		// Determine pass/fail from filename convention
		// Maestro typically outputs: flow-name.{passed|failed}.xml
		passed := !strings.Contains(strings.ToLower(name), "failed")

		flowName := strings.TrimSuffix(name, filepath.Ext(name))
		flowName = strings.TrimSuffix(flowName, ".passed")
		flowName = strings.TrimSuffix(flowName, ".failed")

		results = append(results, ports.TestResult{
			FeatureID: inferFeatureFromMaestroFlow(flowName),
			FilePath:  filepath.Join(resultPath, name),
			Passed:    passed,
		})
	}

	return results, nil
}

// inferFeatureFromMaestroFlow extracts a feature ID from a Maestro flow name.
// e.g., "login-valid" -> "auth.login", "meal-log-food" -> "meals.log"
func inferFeatureFromMaestroFlow(flowName string) string {
	lower := strings.ToLower(flowName)
	lower = strings.ReplaceAll(lower, "-", ".")
	lower = strings.ReplaceAll(lower, "_", ".")

	parts := strings.Split(lower, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return ""
}
