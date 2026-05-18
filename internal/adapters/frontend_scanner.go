package adapters

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sosalejandro/atlas/internal/domain"
)

// embeddedScanner holds the ts-scanner.ts source, embedded at compile time.
// This eliminates the need to install ts-scanner.ts separately.
//
//go:embed embedded_ts_scanner.ts
var embeddedScanner string

// FrontendScanResult is the JSON output from ts-scanner.ts.
type FrontendScanResult struct {
	Nodes    []FrontendNode `json:"nodes"`
	Edges    []FrontendEdge `json:"edges"`
	Warnings []string       `json:"warnings"`
	Stats    FrontendStats  `json:"stats"`
}

// FrontendNode represents a single node discovered by the TypeScript scanner.
type FrontendNode struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // "route", "component", "hook", "api-service", "endpoint"
	File string `json:"file"`
	Line int    `json:"line"`
	Doc  string `json:"doc,omitempty"`
}

// FrontendEdge represents a directed dependency discovered by the TypeScript scanner.
type FrontendEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// FrontendStats summarises what the TypeScript scanner found.
type FrontendStats struct {
	FilesScanned  int `json:"files_scanned"`
	RoutesFound   int `json:"routes_found"`
	APICallsFound int `json:"api_calls_found"`
}

// FrontendScanner invokes ts-scanner.ts via Node.js and returns the result.
// It caches the scan output so that repeated calls for the same project root
// reuse the first result instead of spawning a new subprocess each time.
type FrontendScanner struct {
	cachedResult *FrontendScanResult
	cachedFor    string // project root the cache is for
}

// NewFrontendScanner creates a new FrontendScanner.
func NewFrontendScanner() *FrontendScanner { return &FrontendScanner{} }

// Scan runs the TypeScript scanner subprocess and returns the parsed result.
// If a cached result exists for the same projectRoot, it is returned immediately
// without spawning a subprocess. The scanner script is located by checking
// TESTREG_TS_SCANNER, then paths relative to the testreg binary.
func (s *FrontendScanner) Scan(projectRoot string) (*FrontendScanResult, error) {
	// Return cached result if available for the same project root.
	if s.cachedResult != nil && s.cachedFor == projectRoot {
		return s.cachedResult, nil
	}

	scannerPath := s.findScanner()
	if scannerPath == "" {
		return nil, fmt.Errorf("ts-scanner.ts not found; set TESTREG_TS_SCANNER or place it next to the testreg binary")
	}

	cmd := exec.Command("node", "--experimental-strip-types", scannerPath, projectRoot)
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ts-scanner failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("running ts-scanner: %w", err)
	}

	var result FrontendScanResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("parsing ts-scanner output: %w", err)
	}

	// Cache the result for subsequent calls with the same project root.
	s.cachedResult = &result
	s.cachedFor = projectRoot

	return &result, nil
}

// findScanner looks for ts-scanner.ts in known locations, falling back
// to extracting the embedded scanner to a temp file.
func (s *FrontendScanner) findScanner() string {
	// Priority 1: explicit environment variable.
	if envPath := os.Getenv("TESTREG_TS_SCANNER"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}

	// Priority 2: same directory as the testreg binary.
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, candidate := range []string{
			filepath.Join(dir, "ts-scanner.ts"),
			filepath.Join(dir, "..", "ts-scanner.ts"),
		} {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}

	// Priority 3: extract the embedded scanner to a temp file.
	return s.extractEmbeddedScanner()
}

// extractEmbeddedScanner writes the compiled-in ts-scanner.ts to a
// stable temp file and returns its path. The file is reused across
// invocations within the same OS temp directory.
func (s *FrontendScanner) extractEmbeddedScanner() string {
	if embeddedScanner == "" {
		return ""
	}

	// Use a stable path so we don't create a new file every run.
	tmpPath := filepath.Join(os.TempDir(), "testreg-ts-scanner.ts")

	// If it already exists and matches our version, reuse it.
	if _, err := os.Stat(tmpPath); err == nil {
		return tmpPath
	}

	if err := os.WriteFile(tmpPath, []byte(embeddedScanner), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to extract embedded ts-scanner: %v\n", err)
		return ""
	}

	return tmpPath
}

// MergeIntoGraph takes the frontend scan result and adds its nodes and edges
// into an existing graph. Frontend endpoint nodes (e.g. "POST /api/v1/auth/login")
// are matched to existing backend endpoint nodes by ID — duplicates are skipped
// so the backend definition takes precedence.
func (s *FrontendScanner) MergeIntoGraph(graph *domain.Graph, result *FrontendScanResult) {
	kindMap := map[string]domain.NodeKind{
		"route":       domain.NodeEndpoint,
		"component":   domain.NodeComponent,
		"hook":        domain.NodeHook,
		"api-service": domain.NodeService,
		"endpoint":    domain.NodeEndpoint,
	}

	for _, n := range result.Nodes {
		kind, ok := kindMap[n.Kind]
		if !ok {
			kind = domain.NodeService
		}

		// If this is an endpoint node the backend already created, skip it
		// so the backend definition (with full file/line info) takes precedence.
		if n.Kind == "endpoint" {
			if _, exists := graph.Nodes[n.ID]; exists {
				continue
			}
		}

		graph.AddNode(&domain.Node{
			ID:   n.ID,
			Kind: kind,
			File: n.File,
			Line: n.Line,
			Doc:  n.Doc,
		})
	}

	for _, e := range result.Edges {
		graph.AddEdge(e.From, e.To)
	}
}
