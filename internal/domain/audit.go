package domain

// AuditOutput holds the complete feature health report.
type AuditOutput struct {
	FeatureID   string
	FeatureName string
	Priority    string

	// From trace
	TraceResults []*TraceResult
	APISurfaces  []APISurface
	TestFiles    []string

	// Coverage summary per layer
	LayerCoverage []LayerCoverage

	// Nodes annotated with test status
	AnnotatedNodes []AnnotatedNode

	// Gaps — nodes with no test coverage
	Gaps []AuditGap

	// Recommended actions
	Actions []AuditAction

	// Performance gap analysis
	PerfGaps  []PerfGap  `json:"perf_gaps,omitempty" yaml:"perf_gaps,omitempty"`
	PerfScore *PerfScore `json:"perf_score,omitempty" yaml:"perf_score,omitempty"`

	// E2E coverage summary
	E2EWeb    *E2ECoverageStatus
	E2EMobile *E2ECoverageStatus

	// Overall health score (0.0 - 1.0)
	HealthScore float64
}

// E2ECoverageStatus summarizes E2E coverage for a platform.
type E2ECoverageStatus struct {
	Covered   bool
	TestFiles []string
	TestCount int
}

// LayerCoverage holds coverage statistics for a single architectural layer.
type LayerCoverage struct {
	Layer      string  // "handler", "service", "repository", "query", "component", "hook"
	Tested     int
	Total      int
	Percentage float64
}

// AnnotatedNode is a graph node annotated with its test coverage status.
type AnnotatedNode struct {
	NodeID     string
	Kind       string
	File       string
	Line       int
	TestStatus string   // "tested", "untested", "partial"
	TestFiles  []string // test files that cover this node
}

// AuditGap represents a node with no or insufficient test coverage.
type AuditGap struct {
	NodeID   string
	Kind     string
	File     string
	Line     int
	Severity string // "critical", "high", "medium", "low"
	Reason   string
}

// AuditAction is a recommended step to improve coverage.
type AuditAction struct {
	Priority    int
	Description string
	File        string
	Line        int
	TestType    string // "unit", "integration", "e2e"
}

// PerfGap represents a performance testing gap for a node.
type PerfGap struct {
	NodeID     string `json:"node_id" yaml:"node_id"`
	Kind       string `json:"kind" yaml:"kind"`
	File       string `json:"file" yaml:"file"`
	Line       int    `json:"line" yaml:"line"`
	Severity   string `json:"severity" yaml:"severity"`              // critical, high, medium, low
	GapType    string `json:"gap_type" yaml:"gap_type"`              // no-benchmark, no-race-test, no-load-test, no-memory-baseline
	Reason     string `json:"reason" yaml:"reason"`
	Suggestion string `json:"suggestion" yaml:"suggestion"`          // specific action
	Command    string `json:"command,omitempty" yaml:"command,omitempty"` // command to run
}

// PerfScore is the performance coverage score (0.0-1.0).
type PerfScore struct {
	BenchmarkCoverage float64 `json:"benchmark_coverage" yaml:"benchmark_coverage"` // fraction of hot-path nodes with benchmarks
	RaceTestCoverage  float64 `json:"race_test_coverage" yaml:"race_test_coverage"` // fraction of concurrent nodes race-tested
	Overall           float64 `json:"overall" yaml:"overall"`

	// Raw counts for rendering.
	BenchmarkedNodes    int `json:"benchmarked_nodes" yaml:"benchmarked_nodes"`
	BenchmarkableNodes  int `json:"benchmarkable_nodes" yaml:"benchmarkable_nodes"`
	RaceTestedNodes     int `json:"race_tested_nodes" yaml:"race_tested_nodes"`
	ConcurrentNodes     int `json:"concurrent_nodes" yaml:"concurrent_nodes"`
}
