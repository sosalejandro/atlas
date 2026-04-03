package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sosalejandro/testreg/internal/adapters"
	"github.com/sosalejandro/testreg/internal/app"
	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// ─── View Models ────────────────────────────────────────────────────────────

// NavItem is a single sidebar navigation link.
type NavItem struct {
	Path   string
	Icon   string
	Label  string
	Active bool
}

// StatusVM is the status bar data.
type StatusVM struct {
	TotalFeatures int
	TotalTests    int
	AtTarget      int
	AtTargetPct   int
	DomainData    []app.DomainStatusRow
}

// OverviewVM holds dashboard page data.
type OverviewVM struct {
	PriorityRings []DonutRingVM
	CoverageBars  []ProgressBarVM
	SprintTop     []SprintItemVM
}

// DonutRingVM is data for a single donut chart.
type DonutRingVM struct {
	Pct         int
	AtTarget    int
	Total       int
	DashOffset  int
	StrokeClass string
	LabelClass  string
	Label       string
}

// ProgressBarVM is data for a single coverage bar.
type ProgressBarVM struct {
	Label    string
	Pct      int
	Count    int
	PctClass string
	BarClass string
}

// SprintItemVM is a single row in the sprint priorities list.
type SprintItemVM struct {
	ID           string
	Domain       string
	Priority     string
	Score        float64
	HealthPct    int
	TargetPct    int
	PriorityDot  string
	PriorityText string
	HealthBg     string
	TargetBg     string
}

// FeaturesVM holds the features page data.
type FeaturesVM struct {
	Rows          []FeatureRowVM
	Page          int
	TotalPages    int
	TotalFeatures int
	Pages         []int
}

// FeatureRowVM is a single row in the features table.
type FeatureRowVM struct {
	ID           string
	Domain       string
	Priority     string
	Status       string
	UnitCovered  bool
	IntegCovered bool
	E2ECovered   bool
	HasE2E       bool
	HasUnit      bool
	HealthPct    int
	PerfPct      int
	GapCount     int
	PriorityDot  string
	PriorityText string
	StatusClass  string
}

// ContractVM holds the contract page data.
type ContractVM struct {
	FeatureID  string
	EntryPoint string
	LayerCount int
	Layers     []ContractLayerVM
}

// ContractLayerVM is a single layer in the contract chain.
type ContractLayerVM struct {
	Kind         string
	NodeID       string
	FunctionName string
	Signature    string
	Calls        []string
}

// DiagnoseVM holds the diagnose page data.
type DiagnoseVM struct {
	FeatureID  string
	Symptom    string
	Rule       *domain.SymptomRule
	AllRules   []*domain.SymptomRule
	CheckFiles []string
}

// DiagnoseChipsVM holds quick-pick chip groups.
type DiagnoseChipsVM struct {
	HTTP     []string
	Infra    []string
	Frontend []string
}

// SprintVM holds the sprint page data.
type SprintVM struct {
	Items    []SprintItemVM
	Groups   []SprintGroup
	Limit    int
	Priority string
	GroupBy  string
}

// SprintGroup is a named group of sprint items.
type SprintGroup struct {
	Label string
	Items []SprintItemVM
}

// GraphVM holds the dependency graph page data.
type GraphVM struct {
	FeatureID   string
	FeatureName string
	Priority    string
	Traces      []*GraphTraceVM
	TotalNodes  int
	MaxDepth    int
	Confidence  float64
	ConfidencePct int
}

// GraphTraceVM is a single trace root (one per entry point).
type GraphTraceVM struct {
	Root         *GraphNodeVM
	TotalNodes   int
	MaxDepth     int
	MermaidGraph template.HTML
	SVG          *SVGGraphVM
}

// SVGNode represents a positioned node in the SVG tree layout.
type SVGNode struct {
	X        int
	Y        int
	Width    int
	Height   int
	Label    string
	Kind     string
	IsCycle  bool
	File     string
	Line     int
	ID       string
}

// SVGEdge represents a connector line between two nodes.
type SVGEdge struct {
	X1 int
	Y1 int
	X2 int
	Y2 int
}

// SVGGraphVM holds the complete SVG layout data.
type SVGGraphVM struct {
	Nodes      []SVGNode
	Edges      []SVGEdge
	ViewWidth  int
	ViewHeight int
}

// GraphNodeVM is a single node in the dependency tree.
type GraphNodeVM struct {
	ID          string
	Name        string
	Kind        string
	File        string
	Line        int
	Children    []*GraphNodeVM
	Depth       int
	IsCycle     bool
	KindBorder  string
	KindLabel   string
	KindBadge   string
}

// FeatureDetailVM holds the feature detail slide-over data.
type FeatureDetailVM struct {
	FeatureID     string
	FeatureName   string
	Priority      string
	Status        string
	HealthPct     int
	PriorityDot   string
	PriorityText  string
	StatusClass   string
	LayerCoverage []LayerCoverageVM
	Gaps          []GapVM
	TestFiles     []string
}

// LayerCoverageVM is a single layer in the feature detail panel.
type LayerCoverageVM struct {
	Layer  string
	Pct    int
	Tested int
	Total  int
	BarClass string
}

// GapVM is a single coverage gap in the feature detail panel.
type GapVM struct {
	NodeID string
	Kind   string
	File   string
	Line   int
	Reason string
	KindLabel string
}

// MetricsVM holds quality signals for the metrics page.
type MetricsVM struct {
	SlowestTests  []TestMetricVM
	FlakyTests    []TestMetricVM
	RaceTests     []TestMetricVM
	MemoryHogs    []TestMetricVM
	HasPerfData   bool
	HasSignals    bool
	Signals       *domain.QualitySignals
	TrendPoints   []TrendPointVM
	TrendChart    TrendChartVM
}

// TestMetricVM is a single test metric row.
type TestMetricVM struct {
	Name     string
	Duration string
	File     string
	Retries  int
	BytesMB  string
	Location string
}

// TrendPointVM is a single point on the health trend SVG chart.
type TrendPointVM struct {
	X     int
	Y     int
	Label string
	Pct   int
}

// TrendPoint is a data point for the SVG trend chart polyline.
type TrendPoint struct {
	X     int    // SVG x coordinate
	Y     int    // SVG y coordinate
	Label string // date label
	Value int    // percentage
}

// TrendLine is a single line on the trend chart.
type TrendLine struct {
	Points    []TrendPoint
	Color     string // stroke color
	DashArray string // "" for solid, "6,3" for dashed, "2,4" for dotted
	Label     string // "Overall", "Critical", "High"
}

// TrendChartVM holds data for the server-rendered SVG trend chart.
type TrendChartVM struct {
	Lines   []TrendLine
	XLabels []string // date labels for x-axis
	HasData bool
	Width   int // SVG viewBox width
	Height  int // SVG viewBox height
}

// DiffVM holds the diff page data.
type DiffVM struct {
	Snapshots []SnapshotVM
	Result    *DiffResultVM
	FromLabel string
	ToLabel   string
}

// SnapshotVM is a row in the snapshots table.
type SnapshotVM struct {
	Name     string
	Date     string
	Features int
	HealthPct int
}

// DiffResultVM holds the comparison output.
type DiffResultVM struct {
	Changed   int
	Improved  int
	Regressed int
	Added     int
	Unchanged int
	AvgDelta  string
	Rows      []DiffRowVM
}

// DiffRowVM is a single feature row in the diff results.
type DiffRowVM struct {
	ID          string
	Change      string
	Before      string
	After       string
	Delta       string
	ChangeClass string
	DeltaClass  string
}

// snapshotFile mirrors the cmd.Snapshot struct for JSON reading.
type snapshotFile struct {
	Timestamp time.Time          `json:"timestamp"`
	Features  map[string]float64 `json:"features"`
}

// PageData is the top-level template data passed to every page.
type PageData struct {
	ProjectName     string
	Version         string
	Nav             []NavItem
	Status          StatusVM
	Content         template.HTML // pre-rendered page content injected into base.html
	Overview        *OverviewVM
	Features        *FeaturesVM
	Contract        *ContractVM
	AllFeatures     []string
	DiagnoseFeature string
	DiagnoseSymptom string
	DiagnoseChips   DiagnoseChipsVM
	Diagnose        *DiagnoseVM
	Sprint          *SprintVM
	Graph           *GraphVM
	Metrics         *MetricsVM
	Diff            *DiffVM
}

// ─── Base page builder ───────────────────────────────────────────────────────

func (s *Server) buildBase(active string) (*PageData, error) {
	statusResult, err := s.statusUC.Execute(s.registryDir, app.StatusFilter{})
	if err != nil {
		return nil, fmt.Errorf("loading status: %w", err)
	}

	reg, err := s.store.LoadAll(s.registryDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry: %w", err)
	}

	var allIDs []string
	for _, f := range reg.AllFeatures() {
		allIDs = append(allIDs, f.ID)
	}
	sort.Strings(allIDs)

	nav := []NavItem{
		{Path: "/", Icon: "dashboard", Label: "Overview", Active: active == "overview"},
		{Path: "/features", Icon: "extension", Label: "Features", Active: active == "features"},
		{Path: "/graph", Icon: "account_tree", Label: "Graph", Active: active == "graph"},
		{Path: "/sprint", Icon: "reorder", Label: "Sprint", Active: active == "sprint"},
		{Path: "/contract", Icon: "description", Label: "Contract", Active: active == "contract"},
		{Path: "/metrics", Icon: "analytics", Label: "Metrics", Active: active == "metrics"},
		{Path: "/diff", Icon: "difference", Label: "Diff", Active: active == "diff"},
		{Path: "/diagnose", Icon: "troubleshoot", Label: "Diagnose", Active: active == "diagnose"},
	}

	return &PageData{
		ProjectName: s.projectName,
		Version:     "1.0.4",
		Nav:         nav,
		Status:      buildStatusVM(statusResult),
		AllFeatures: allIDs,
	}, nil
}

func buildStatusVM(r *app.StatusResult) StatusVM {
	total := r.Metrics.TotalFeatures
	atTarget := r.Metrics.CoveredUnit
	pct := 0
	if total > 0 {
		pct = (atTarget * 100) / total
	}
	totalTests := r.Metrics.CoveredUnit + r.Metrics.CoveredIntegration + r.Metrics.CoveredE2E
	return StatusVM{
		TotalFeatures: total,
		TotalTests:    totalTests,
		AtTarget:      atTarget,
		AtTargetPct:   pct,
		DomainData:    r.DomainData,
	}
}

// ─── Full page handlers (first load) ────────────────────────────────────────

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		s.handleOverviewPartial(w, r)
		return
	}
	data, err := s.buildOverviewData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "overview-content", data)
}

func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleFeaturesPartial(w, r)
		return
	}
	data, err := s.buildFeaturesData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "features-content", data)
}

func (s *Server) handleContract(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleContractPartial(w, r)
		return
	}
	data, err := s.buildContractData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "contract-content", data)
}

func (s *Server) handleDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleDiagnosePartial(w, r)
		return
	}
	data, err := s.buildDiagnoseData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "diagnose-content", data)
}

func (s *Server) handleSprint(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleSprintPartial(w, r)
		return
	}
	data, err := s.buildSprintData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "sprint-content", data)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleMetricsPartial(w, r)
		return
	}
	data, err := s.buildMetricsData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "metrics-content", data)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleDiffPartial(w, r)
		return
	}
	data, err := s.buildDiffData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "diff-content", data)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		s.handleGraphPartial(w, r)
		return
	}
	data, err := s.buildGraphData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFull(w, "graph-content", data)
}

// ─── Partial handlers (htmx swaps into #page-content) ────────────────────────

func (s *Server) handleOverviewPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildOverviewData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "overview-content", data)
}

func (s *Server) handleFeaturesPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildFeaturesData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// If htmx is targeting only the table body (filter/search).
	if r.Header.Get("HX-Target") == "feature-table-body" {
		s.renderPartial(w, "feature-rows", data.Features)
		return
	}
	s.renderPartial(w, "features-content", data)
}

func (s *Server) handleContractPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildContractData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "contract-content", data)
}

func (s *Server) handleDiagnosePartial(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		diag, err := s.runDiagnose(r)
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.renderPartial(w, "diagnose-result", diag)
		return
	}
	data, err := s.buildDiagnoseData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "diagnose-content", data)
}

func (s *Server) handleSprintPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildSprintData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "sprint-content", data)
}

func (s *Server) handleSprintAPI(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	items := s.buildSprintItems(0)

	w.Header().Set("Content-Type", "application/json")
	if format == "prompt" {
		// AI-friendly format
		type promptItem struct {
			Feature  string  `json:"feature"`
			Domain   string  `json:"domain"`
			Priority string  `json:"priority"`
			Score    float64 `json:"score"`
			Health   int     `json:"health_pct"`
			Target   int     `json:"target_pct"`
		}
		out := make([]promptItem, len(items))
		for i, it := range items {
			out[i] = promptItem{
				Feature:  it.ID,
				Domain:   it.Domain,
				Priority: it.Priority,
				Score:    it.Score,
				Health:   it.HealthPct,
				Target:   it.TargetPct,
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"instruction": "These are test coverage gaps sorted by priority. Write tests for the top items first.",
			"items":       out,
			"total":       len(out),
		})
		return
	}
	// Default JSON
	json.NewEncoder(w).Encode(items)
}

func (s *Server) handleMetricsPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildMetricsData()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "metrics-content", data)
}

func (s *Server) handleDiffPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildDiffData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "diff-content", data)
}

func (s *Server) handleGraphPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.buildGraphData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "graph-content", data)
}

func (s *Server) handleScanModal(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "scan-modal", nil)
}

func (s *Server) handleFeatureDetail(w http.ResponseWriter, r *http.Request) {
	featureID := strings.TrimPrefix(r.URL.Path, "/api/feature/")
	if featureID == "" {
		http.Error(w, "missing feature id", http.StatusBadRequest)
		return
	}
	vm, err := s.buildFeatureDetail(featureID)
	if err != nil {
		s.renderPartial(w, "feature-detail-empty", map[string]string{"FeatureID": featureID})
		return
	}
	s.renderPartial(w, "feature-detail", vm)
}

func (s *Server) handleDiffSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "snapshot-" + time.Now().Format("2006-01-02-150405")
	}
	if err := s.saveSnapshot(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Refresh the diff page partial.
	data, err := s.buildDiffData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPartial(w, "diff-content", data)
}

func (s *Server) handleDiffCompare(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	result, err := s.computeDiff(from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "diff-result", result)
}

// ─── API handlers ─────────────────────────────────────────────────────────────

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	scanners := []ports.TestScanner{
		adapters.NewGoScanner(),
		adapters.NewVitestScanner(),
		adapters.NewPlaywrightScanner(),
		adapters.NewMaestroScanner(),
		adapters.NewJestScanner(),
		adapters.NewPythonScanner(),
	}
	scanUC := app.NewScanTestsUseCase(s.store, s.store, scanners)
	if _, err := scanUC.Execute(s.projectRoot, s.registryDir); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<footer id="status-bar" class="fixed bottom-0 w-full h-8 z-[60] flex items-center px-4 bg-slate-900 font-mono text-[11px] text-red-400">
            <span class="material-symbols-outlined mr-2 text-sm">error</span>Scan failed: %s
        </footer>`, template.HTMLEscapeString(err.Error()))
		return
	}

	statusResult, _ := s.statusUC.Execute(s.registryDir, app.StatusFilter{})
	status := buildStatusVM(statusResult)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<footer id="status-bar" class="fixed bottom-0 w-full h-8 z-[60] flex items-center px-4 bg-slate-900 font-mono text-[11px] tabular-nums">
        <div class="flex items-center justify-between w-full">
            <div class="flex items-center gap-4">
                <div class="flex items-center gap-1.5">
                    <span class="w-1.5 h-1.5 rounded-full bg-emerald-500"></span>
                    <span class="text-slate-400">Scan complete</span>
                </div>
                <div class="h-3 w-[1px] bg-slate-800"></div>
                <div class="flex items-center gap-3 text-slate-300">
                    <span>%d features</span><span class="text-slate-600">•</span>
                    <span>%d tests</span><span class="text-slate-600">•</span>
                    <span class="text-emerald-400">%d%% at target</span>
                </div>
            </div>
        </div>
    </footer>`, status.TotalFeatures, status.TotalTests, status.AtTargetPct)
}

// ─── Data builders ────────────────────────────────────────────────────────────

func (s *Server) buildOverviewData() (*PageData, error) {
	data, err := s.buildBase("overview")
	if err != nil {
		return nil, err
	}

	statusResult, err := s.statusUC.Execute(s.registryDir, app.StatusFilter{})
	if err != nil {
		return nil, err
	}

	data.Overview = &OverviewVM{
		PriorityRings: buildPriorityRings(statusResult.Metrics),
		CoverageBars:  buildCoverageBars(statusResult.Metrics),
		SprintTop:     s.buildSprintItems(10),
	}
	return data, nil
}

func (s *Server) buildFeaturesData(r *http.Request) (*PageData, error) {
	data, err := s.buildBase("features")
	if err != nil {
		return nil, err
	}

	q := r.URL.Query().Get("q")
	priority := domain.Priority(r.URL.Query().Get("priority"))
	healthFilter := r.URL.Query().Get("health")

	// Load registry directly for domain info.
	reg, err := s.store.LoadAll(s.registryDir)
	if err != nil {
		return nil, err
	}

	var rows []FeatureRowVM
	for _, d := range reg.Domains {
		if d.Domain == "_unmapped" {
			continue
		}
		for _, f := range d.Features {
			if priority != "" && f.Priority != priority {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(f.ID), strings.ToLower(q)) &&
				!strings.Contains(strings.ToLower(string(f.Priority)), strings.ToLower(q)) {
				continue
			}
			vm := featureToVM(f)
			vm.Domain = d.Domain
			// Compute health per feature.
			h := featureHealth(f)
			vm.HealthPct = int(h * 100)
			// Compute gap count.
			vm.GapCount = len(f.Gaps())
			// PerfPct defaults to 0 (no metrics data available per feature).
			vm.PerfPct = 0
			// Boolean flags for icon display.
			vm.HasE2E = f.Coverage.E2E.Web != nil || f.Coverage.E2E.Mobile != nil
			vm.HasUnit = f.Coverage.Unit.Backend != nil || f.Coverage.Unit.Web != nil || f.Coverage.Unit.Mobile != nil

			// Apply health filter.
			if healthFilter == "healthy" && vm.HealthPct < 80 {
				continue
			}
			if healthFilter == "at-risk" && (vm.HealthPct < 50 || vm.HealthPct >= 80) {
				continue
			}
			if healthFilter == "critical" && vm.HealthPct >= 50 {
				continue
			}

			rows = append(rows, vm)
		}
	}

	// Pagination.
	const perPage = 15
	totalFeatures := len(rows)
	totalPages := (totalFeatures + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if pn, err := strconv.Atoi(p); err == nil && pn > 0 {
			page = pn
		}
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * perPage
	end := start + perPage
	if end > totalFeatures {
		end = totalFeatures
	}

	// Build page number list.
	var pages []int
	for i := 1; i <= totalPages; i++ {
		pages = append(pages, i)
	}

	pagedRows := rows[start:end]

	data.Features = &FeaturesVM{
		Rows:          pagedRows,
		Page:          page,
		TotalPages:    totalPages,
		TotalFeatures: totalFeatures,
		Pages:         pages,
	}
	return data, nil
}

func (s *Server) buildContractData(r *http.Request) (*PageData, error) {
	data, err := s.buildBase("contract")
	if err != nil {
		return nil, err
	}

	featureID := r.URL.Query().Get("feature")
	if featureID == "" && len(data.AllFeatures) > 0 {
		featureID = data.AllFeatures[0]
	}

	if featureID != "" {
		contract, cerr := s.contractUC.Execute(s.registryDir, featureID, s.config)
		if cerr == nil && contract != nil {
			data.Contract = contractToVM(contract)
		}
	}

	return data, nil
}

func (s *Server) buildDiagnoseData(r *http.Request) (*PageData, error) {
	data, err := s.buildBase("diagnose")
	if err != nil {
		return nil, err
	}

	data.DiagnoseFeature = r.URL.Query().Get("feature")
	if data.DiagnoseFeature == "" && len(data.AllFeatures) > 0 {
		data.DiagnoseFeature = data.AllFeatures[0]
	}
	data.DiagnoseSymptom = r.URL.Query().Get("symptom")
	data.DiagnoseChips = defaultDiagnoseChips()
	return data, nil
}

func (s *Server) runDiagnose(r *http.Request) (*DiagnoseVM, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	featureID := r.FormValue("feature")
	symptom := r.FormValue("symptom")

	out, err := s.diagnoseUC.Execute(s.registryDir, featureID, symptom, s.config)
	if err != nil {
		return &DiagnoseVM{FeatureID: featureID, Symptom: symptom}, nil
	}

	return &DiagnoseVM{
		FeatureID:  out.FeatureID,
		Symptom:    out.Symptom,
		Rule:       out.Rule,
		AllRules:   out.AllRules,
		CheckFiles: out.CheckFiles,
	}, nil
}

func (s *Server) buildSprintData(r *http.Request) (*PageData, error) {
	data, err := s.buildBase("sprint")
	if err != nil {
		return nil, err
	}

	// Read filter params
	limitStr := r.URL.Query().Get("limit")
	priority := r.URL.Query().Get("priority")
	groupBy := r.URL.Query().Get("group")

	limit := 20
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}

	items := s.buildSprintItems(0) // get all, then filter

	// Filter by priority
	if priority != "" {
		filtered := make([]SprintItemVM, 0)
		for _, it := range items {
			if it.Priority == priority {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	// Apply limit
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	// Build groups if requested
	var groups []SprintGroup
	if groupBy == "domain" || groupBy == "type" {
		groupMap := make(map[string][]SprintItemVM)
		var order []string
		for _, it := range items {
			key := it.Domain
			if groupBy == "type" {
				key = it.Priority
			}
			if _, exists := groupMap[key]; !exists {
				order = append(order, key)
			}
			groupMap[key] = append(groupMap[key], it)
		}
		for _, k := range order {
			groups = append(groups, SprintGroup{Label: k, Items: groupMap[k]})
		}
	}

	data.Sprint = &SprintVM{
		Items:    items,
		Groups:   groups,
		Limit:    limit,
		Priority: priority,
		GroupBy:  groupBy,
	}
	return data, nil
}

func (s *Server) buildMetricsData() (*PageData, error) {
	data, err := s.buildBase("metrics")
	if err != nil {
		return nil, err
	}

	statusResult, err := s.statusUC.Execute(s.registryDir, app.StatusFilter{})
	if err != nil {
		return nil, err
	}

	data.Overview = &OverviewVM{
		CoverageBars: buildCoverageBars(statusResult.Metrics),
	}
	data.Metrics = s.buildMetricsVM()
	return data, nil
}

func (s *Server) buildMetricsVM() *MetricsVM {
	// Read metrics history if available.
	historyFile := filepath.Join(s.projectRoot, ".testreg-cache", "metrics", "history.json")
	raw, err := os.ReadFile(historyFile)
	if err != nil {
		return &MetricsVM{HasPerfData: false, HasSignals: false}
	}
	var history domain.MetricsHistory
	if err := json.Unmarshal(raw, &history); err != nil || len(history.Runs) == 0 {
		return &MetricsVM{HasPerfData: false, HasSignals: false}
	}

	signals := computeQualitySignals(history)

	return &MetricsVM{
		HasPerfData:  true,
		HasSignals:   true,
		Signals:      &signals,
		SlowestTests: toTestMetricVMs(signals.SlowestTests),
		FlakyTests:   toTestMetricVMs(signals.FlakyTests),
		RaceTests:    toTestMetricVMs(signals.RaceConditions),
		MemoryHogs:   toTestMetricVMs(signals.MemoryHogs),
		TrendChart:   buildTrendChart(history),
	}
}

// buildTrendChart computes SVG polyline points from metrics history.
func buildTrendChart(h domain.MetricsHistory) TrendChartVM {
	if len(h.Runs) == 0 {
		return TrendChartVM{HasData: false}
	}

	const chartW, chartH = 800, 300
	const padLeft = 35 // space for y-axis labels
	const padRight = 10
	usableW := chartW - padLeft - padRight

	n := len(h.Runs)
	spacing := usableW
	if n > 1 {
		spacing = usableW / (n - 1)
	}

	var overallPts, critPts, highPts []TrendPoint
	var xLabels []string

	for i, run := range h.Runs {
		x := padLeft
		if n > 1 {
			x = padLeft + i*usableW/(n-1)
		}
		label := run.Timestamp.Format("Jan 2")

		// Overall health: pass rate across all tests.
		overallPct := 0
		if run.TotalTests > 0 {
			overallPct = run.Passed * 100 / run.TotalTests
		}
		overallY := chartH - (overallPct * chartH / 100)
		overallPts = append(overallPts, TrendPoint{X: x, Y: overallY, Label: label, Value: overallPct})

		// Critical: percentage of tests that are NOT failing (i.e. not outright failures).
		critPct := 100
		if run.TotalTests > 0 {
			critPct = (run.TotalTests - run.Failed) * 100 / run.TotalTests
		}
		critY := chartH - (critPct * chartH / 100)
		critPts = append(critPts, TrendPoint{X: x, Y: critY, Label: label, Value: critPct})

		// High: percentage excluding flaky (stable tests).
		highPct := 100
		if run.TotalTests > 0 {
			highPct = (run.Passed) * 100 / run.TotalTests
			if run.Flaky > 0 {
				// Penalise flaky tests: count them as half-passing.
				highPct = (run.Passed - run.Flaky/2) * 100 / run.TotalTests
				if highPct < 0 {
					highPct = 0
				}
			}
		}
		highY := chartH - (highPct * chartH / 100)
		highPts = append(highPts, TrendPoint{X: x, Y: highY, Label: label, Value: highPct})

		xLabels = append(xLabels, label)
	}

	_ = spacing // used via the formula above

	return TrendChartVM{
		HasData: true,
		Width:   chartW,
		Height:  chartH,
		XLabels: xLabels,
		Lines: []TrendLine{
			{Points: overallPts, Color: "#e2e8f0", DashArray: "", Label: "Overall"},
			{Points: critPts, Color: "#34d399", DashArray: "6,3", Label: "Critical"},
			{Points: highPts, Color: "#60a5fa", DashArray: "2,4", Label: "High"},
		},
	}
}

func computeQualitySignals(h domain.MetricsHistory) domain.QualitySignals {
	seen := map[string]bool{}
	var all []domain.TestMetric
	for i := len(h.Runs) - 1; i >= 0; i-- {
		for _, t := range h.Runs[i].TestMetrics {
			if !seen[t.Name] {
				seen[t.Name] = true
				all = append(all, t)
			}
		}
	}
	var slowest, flaky, races, hogs []domain.TestMetric
	for _, t := range all {
		if t.Retries > 0 {
			flaky = append(flaky, t)
		}
		if t.RaceDetected {
			races = append(races, t)
		}
		if t.BytesPerOp > 0 {
			hogs = append(hogs, t)
		}
		slowest = append(slowest, t)
	}
	sort.Slice(slowest, func(i, j int) bool { return slowest[i].Duration > slowest[j].Duration })
	sort.Slice(hogs, func(i, j int) bool { return hogs[i].BytesPerOp > hogs[j].BytesPerOp })
	if len(slowest) > 5 {
		slowest = slowest[:5]
	}
	if len(hogs) > 5 {
		hogs = hogs[:5]
	}
	return domain.QualitySignals{
		SlowestTests:   slowest,
		FlakyTests:     flaky,
		MemoryHogs:     hogs,
		RaceConditions: races,
	}
}

func toTestMetricVMs(tests []domain.TestMetric) []TestMetricVM {
	out := make([]TestMetricVM, 0, len(tests))
	for _, t := range tests {
		dur := ""
		if t.Duration > 0 {
			dur = fmt.Sprintf("%.2fs", t.Duration.Seconds())
		}
		bytesMB := ""
		if t.BytesPerOp > 0 {
			bytesMB = fmt.Sprintf("%.1f MB/op", float64(t.BytesPerOp)/(1024*1024))
		}
		file := t.File
		out = append(out, TestMetricVM{
			Name:     t.Name,
			Duration: dur,
			File:     file,
			Retries:  t.Retries,
			BytesMB:  bytesMB,
		})
	}
	return out
}

func (s *Server) buildGraphData(r *http.Request) (*PageData, error) {
	data, err := s.buildBase("graph")
	if err != nil {
		return nil, err
	}

	featureID := r.URL.Query().Get("feature")
	if featureID == "" && len(data.AllFeatures) > 0 {
		featureID = data.AllFeatures[0]
	}

	if featureID != "" {
		out, terr := s.traceUC.Execute(s.registryDir, featureID, s.config)
		if terr == nil && out != nil {
			data.Graph = buildGraphVM(out)
		}
	}
	return data, nil
}

func buildGraphVM(out *app.TraceOutput) *GraphVM {
	vm := &GraphVM{
		FeatureID:   out.FeatureID,
		FeatureName: out.FeatureName,
		Priority:    out.Priority,
	}
	total, maxDepth := 0, 0
	conf := 0.0

	for _, tr := range out.Traces {
		if tr == nil {
			continue
		}
		if tr.TotalNodes > total {
			total = tr.TotalNodes
		}
		if tr.MaxDepth > maxDepth {
			maxDepth = tr.MaxDepth
		}
		if tr.Confidence > conf {
			conf = tr.Confidence
		}
		root := traceNodeToVM(tr.Root, 0)
		vm.Traces = append(vm.Traces, &GraphTraceVM{
			Root:         root,
			TotalNodes:   tr.TotalNodes,
			MaxDepth:     tr.MaxDepth,
			MermaidGraph: buildMermaidGraph(root),
			SVG:          buildSVGGraph(root),
		})
	}
	vm.TotalNodes = total
	vm.MaxDepth = maxDepth
	vm.Confidence = conf
	vm.ConfidencePct = int(conf * 100)
	return vm
}

func traceNodeToVM(n *domain.TraceNode, depth int) *GraphNodeVM {
	if n == nil || n.Node == nil {
		return nil
	}
	// Derive short display name from node ID (last segment).
	name := n.Node.ID
	if parts := strings.Split(name, "."); len(parts) > 1 {
		name = parts[len(parts)-1]
	}
	vm := &GraphNodeVM{
		ID:         n.Node.ID,
		Name:       name,
		Kind:       string(n.Node.Kind),
		File:       n.Node.File,
		Line:       n.Node.Line,
		Depth:      depth,
		IsCycle:    n.IsCycle,
		KindBorder: layerBorderClass(string(n.Node.Kind)),
		KindLabel:  layerLabelClass(string(n.Node.Kind)),
		KindBadge:  kindBadgeClass(string(n.Node.Kind)),
	}
	for _, c := range n.Children {
		if child := traceNodeToVM(c, depth+1); child != nil {
			vm.Children = append(vm.Children, child)
		}
	}
	return vm
}

// buildMermaidGraph converts a GraphNodeVM tree into a Mermaid flowchart string.
func buildMermaidGraph(root *GraphNodeVM) template.HTML {
	if root == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("flowchart TB\n")

	counter := 0
	var walk func(n *GraphNodeVM, parentID int)
	walk = func(n *GraphNodeVM, parentID int) {
		if n == nil {
			return
		}
		id := counter
		counter++
		label := strings.ReplaceAll(n.Name, `"`, "#quot;")
		if n.Kind != "" {
			label += `\n` + n.Kind
		}
		if n.IsCycle {
			label += `\n⟳ cycle`
		}
		sb.WriteString(fmt.Sprintf("    n%d[\"%s\"]:::%s\n", id, label, mermaidNodeClass(n.Kind)))
		if parentID >= 0 {
			sb.WriteString(fmt.Sprintf("    n%d --> n%d\n", parentID, id))
		}
		for _, c := range n.Children {
			walk(c, id)
		}
	}
	walk(root, -1)

	sb.WriteString("    classDef handler fill:#1e3a5f,stroke:#3b82f6,color:#93c5fd\n")
	sb.WriteString("    classDef service fill:#14342a,stroke:#34d399,color:#6ee7b7\n")
	sb.WriteString("    classDef repository fill:#2a1a3a,stroke:#a78bfa,color:#c4b5fd\n")
	sb.WriteString("    classDef query fill:#332b14,stroke:#fbbf24,color:#fcd34d\n")
	sb.WriteString("    classDef component fill:#2d1433,stroke:#e879f9,color:#f0abfc\n")
	sb.WriteString("    classDef external fill:#1e2430,stroke:#64748b,color:#94a3b8\n")
	sb.WriteString("    classDef deflt fill:#1e293b,stroke:#475569,color:#94a3b8\n")
	return template.HTML(sb.String()) //nolint:gosec // Mermaid source is server-generated, not user input
}

func mermaidNodeClass(kind string) string {
	switch kind {
	case "handler", "endpoint":
		return "handler"
	case "service":
		return "service"
	case "repository":
		return "repository"
	case "query":
		return "query"
	case "component", "hook":
		return "component"
	case "external":
		return "external"
	default:
		return "deflt"
	}
}

// buildSVGGraph computes a top-down tree layout for SVG rendering.
// The algorithm assigns positions level by level: root at top center,
// children spread horizontally beneath their parent.
func buildSVGGraph(root *GraphNodeVM) *SVGGraphVM {
	if root == nil {
		return nil
	}

	const (
		nodeHeight   = 36
		levelSpacing = 80
		nodeGapX     = 20
		paddingX     = 40
		paddingY     = 30
		minWidth     = 120
		charWidth    = 8
		charPadding  = 24
	)

	// Phase 1: Flatten the tree into levels and compute subtree widths.
	// Each node gets a "subtreeWidth" that is the total horizontal space
	// its descendants require.
	type layoutNode struct {
		vm            *GraphNodeVM
		children      []*layoutNode
		subtreeWidth  int // total pixel width of this subtree
		nodeWidth     int
		x, y          int
	}

	var buildLayout func(n *GraphNodeVM, depth int) *layoutNode
	buildLayout = func(n *GraphNodeVM, depth int) *layoutNode {
		if n == nil {
			return nil
		}
		w := len(n.Name)*charWidth + charPadding
		if w < minWidth {
			w = minWidth
		}
		ln := &layoutNode{
			vm:        n,
			nodeWidth: w,
		}
		for _, c := range n.Children {
			if child := buildLayout(c, depth+1); child != nil {
				ln.children = append(ln.children, child)
			}
		}
		// Subtree width is the max of: own node width, or sum of children subtree widths + gaps.
		if len(ln.children) == 0 {
			ln.subtreeWidth = w
		} else {
			total := 0
			for _, c := range ln.children {
				total += c.subtreeWidth
			}
			total += (len(ln.children) - 1) * nodeGapX
			if total < w {
				total = w
			}
			ln.subtreeWidth = total
		}
		return ln
	}

	layoutRoot := buildLayout(root, 0)
	if layoutRoot == nil {
		return nil
	}

	// Phase 2: Assign (x, y) positions. Each node is centered within its
	// allocated subtree width. "leftX" is the left edge of the allocated space.
	var svgNodes []SVGNode
	var svgEdges []SVGEdge
	maxX, maxY := 0, 0

	var assignPositions func(ln *layoutNode, leftX, depth int)
	assignPositions = func(ln *layoutNode, leftX, depth int) {
		// Center this node within its subtree allocation.
		ln.x = leftX + (ln.subtreeWidth-ln.nodeWidth)/2
		ln.y = paddingY + depth*levelSpacing
		svgNodes = append(svgNodes, SVGNode{
			X:       ln.x,
			Y:       ln.y,
			Width:   ln.nodeWidth,
			Height:  nodeHeight,
			Label:   ln.vm.Name,
			Kind:    ln.vm.Kind,
			IsCycle: ln.vm.IsCycle,
			File:    ln.vm.File,
			Line:    ln.vm.Line,
			ID:      ln.vm.ID,
		})
		rightEdge := ln.x + ln.nodeWidth
		bottomEdge := ln.y + nodeHeight
		if rightEdge > maxX {
			maxX = rightEdge
		}
		if bottomEdge > maxY {
			maxY = bottomEdge
		}

		// Position children left-to-right within this node's subtree allocation.
		childLeft := leftX
		parentCenterX := ln.x + ln.nodeWidth/2
		parentBottomY := ln.y + nodeHeight

		for _, child := range ln.children {
			assignPositions(child, childLeft, depth+1)
			childCenterX := child.x + child.nodeWidth/2
			childTopY := child.y

			// Draw connector: vertical down from parent, horizontal to child column, vertical down to child.
			midY := parentBottomY + (childTopY-parentBottomY)/2
			// Parent vertical segment down to mid.
			svgEdges = append(svgEdges, SVGEdge{X1: parentCenterX, Y1: parentBottomY, X2: parentCenterX, Y2: midY})
			// Horizontal segment from parent column to child column.
			svgEdges = append(svgEdges, SVGEdge{X1: parentCenterX, Y1: midY, X2: childCenterX, Y2: midY})
			// Child vertical segment from mid down to child top.
			svgEdges = append(svgEdges, SVGEdge{X1: childCenterX, Y1: midY, X2: childCenterX, Y2: childTopY})

			childLeft += child.subtreeWidth + nodeGapX
		}
	}

	assignPositions(layoutRoot, paddingX, 0)

	viewWidth := maxX + paddingX
	viewHeight := maxY + paddingY
	if viewWidth < 400 {
		viewWidth = 400
	}
	if viewHeight < 200 {
		viewHeight = 200
	}

	return &SVGGraphVM{
		Nodes:      svgNodes,
		Edges:      svgEdges,
		ViewWidth:  viewWidth,
		ViewHeight: viewHeight,
	}
}

func kindBadgeClass(kind string) string {
	switch kind {
	case "handler", "endpoint":
		return "bg-primary/10 text-primary"
	case "service":
		return "bg-tertiary/10 text-tertiary"
	case "repository":
		return "bg-secondary/10 text-secondary"
	case "query":
		return "bg-yellow-500/10 text-yellow-400"
	case "component", "hook":
		return "bg-purple-500/10 text-purple-400"
	default:
		return "bg-surface-variant text-on-surface-variant"
	}
}

func (s *Server) buildFeatureDetail(featureID string) (*FeatureDetailVM, error) {
	out, err := s.auditUC.Execute(s.registryDir, featureID, s.config)
	if err != nil {
		return nil, err
	}

	health := int(out.HealthScore * 100)
	status := "missing"
	if health >= 70 {
		status = "covered"
	} else if health >= 20 {
		status = "partial"
	}

	vm := &FeatureDetailVM{
		FeatureID:    out.FeatureID,
		FeatureName:  out.FeatureName,
		Priority:     out.Priority,
		Status:       status,
		HealthPct:    health,
		PriorityDot:  priorityDot(domain.Priority(out.Priority)),
		PriorityText: priorityText(domain.Priority(out.Priority)),
		StatusClass:  statusBadgeClass(status),
		TestFiles:    out.TestFiles,
	}

	for _, lc := range out.LayerCoverage {
		pct := 0
		if lc.Total > 0 {
			pct = int(lc.Percentage)
		}
		barClass := "bg-emerald-500"
		if pct < 70 {
			barClass = "bg-yellow-500"
		}
		if pct < 30 {
			barClass = "bg-red-500"
		}
		vm.LayerCoverage = append(vm.LayerCoverage, LayerCoverageVM{
			Layer:    lc.Layer,
			Pct:      pct,
			Tested:   lc.Tested,
			Total:    lc.Total,
			BarClass: barClass,
		})
	}

	for _, g := range out.Gaps {
		vm.Gaps = append(vm.Gaps, GapVM{
			NodeID:    g.NodeID,
			Kind:      g.Kind,
			File:      g.File,
			Line:      g.Line,
			Reason:    g.Reason,
			KindLabel: layerLabelClass(g.Kind),
		})
	}

	return vm, nil
}

func (s *Server) snapshotDir() string {
	return filepath.Join(s.projectRoot, ".testreg-cache", "snapshots")
}

func (s *Server) saveSnapshot(name string) error {
	// Build current audit scores.
	reg, err := s.store.LoadAll(s.registryDir)
	if err != nil {
		return err
	}
	features := make(map[string]float64)
	for _, d := range reg.Domains {
		for _, f := range d.Features {
			features[f.ID] = featureHealth(f)
		}
	}
	snap := snapshotFile{Timestamp: time.Now(), Features: features}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := s.snapshotDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o644)
}

func (s *Server) loadSnapshots() ([]SnapshotVM, error) {
	dir := s.snapshotDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SnapshotVM
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var snap snapshotFile
		if err := json.Unmarshal(raw, &snap); err != nil {
			continue
		}
		// Compute average health.
		total := 0.0
		for _, v := range snap.Features {
			total += v
		}
		avgPct := 0
		if len(snap.Features) > 0 {
			avgPct = int((total / float64(len(snap.Features))) * 100)
		}
		out = append(out, SnapshotVM{
			Name:      strings.TrimSuffix(e.Name(), ".json"),
			Date:      snap.Timestamp.Format("Jan 02, 2006"),
			Features:  len(snap.Features),
			HealthPct: avgPct,
		})
	}
	// Newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

func (s *Server) computeDiff(fromName, toName string) (*DiffResultVM, error) {
	load := func(name string) (*snapshotFile, error) {
		if name == "" || name == "current" {
			// Build from live registry.
			reg, err := s.store.LoadAll(s.registryDir)
			if err != nil {
				return nil, err
			}
			features := make(map[string]float64)
			for _, d := range reg.Domains {
				for _, f := range d.Features {
					features[f.ID] = featureHealth(f)
				}
			}
			return &snapshotFile{Timestamp: time.Now(), Features: features}, nil
		}
		raw, err := os.ReadFile(filepath.Join(s.snapshotDir(), name+".json"))
		if err != nil {
			return nil, err
		}
		var snap snapshotFile
		return &snap, json.Unmarshal(raw, &snap)
	}

	from, err := load(fromName)
	if err != nil {
		return nil, fmt.Errorf("loading from snapshot: %w", err)
	}
	to, err := load(toName)
	if err != nil {
		return nil, fmt.Errorf("loading to snapshot: %w", err)
	}

	var rows []DiffRowVM
	improved, regressed, unchanged, added := 0, 0, 0, 0
	deltaSum := 0.0

	for id, toVal := range to.Features {
		fromVal, exists := from.Features[id]
		delta := toVal - fromVal
		deltaSum += delta

		change, changeClass, deltaClass := "unchanged", "bg-slate-700 text-slate-300", "text-slate-400"
		if !exists {
			change, changeClass = "new", "bg-primary/10 text-primary"
			added++
		} else if delta > 0.005 {
			change, changeClass, deltaClass = "improved", "bg-emerald-500/10 text-emerald-400", "text-emerald-400"
			improved++
		} else if delta < -0.005 {
			change, changeClass, deltaClass = "regressed", "bg-red-500/10 text-red-400", "text-red-400"
			regressed++
		} else {
			unchanged++
			continue // skip unchanged rows to keep table focused
		}

		deltaStr := fmt.Sprintf("%+.0f%%", delta*100)
		rows = append(rows, DiffRowVM{
			ID:          id,
			Change:      change,
			Before:      fmt.Sprintf("%.0f%%", fromVal*100),
			After:       fmt.Sprintf("%.0f%%", toVal*100),
			Delta:       deltaStr,
			ChangeClass: changeClass,
			DeltaClass:  deltaClass,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		order := map[string]int{"regressed": 0, "improved": 1, "new": 2}
		return order[rows[i].Change] < order[rows[j].Change]
	})

	avgDelta := ""
	total := len(to.Features)
	if total > 0 {
		avgDelta = fmt.Sprintf("%+.1f%%", (deltaSum/float64(total))*100)
	}

	return &DiffResultVM{
		Changed:   improved + regressed,
		Improved:  improved,
		Regressed: regressed,
		Added:     added,
		Unchanged: unchanged,
		AvgDelta:  avgDelta,
		Rows:      rows,
	}, nil
}

func (s *Server) buildDiffData(r *http.Request) (*PageData, error) {
	data, err := s.buildBase("diff")
	if err != nil {
		return nil, err
	}
	snaps, _ := s.loadSnapshots()
	vm := &DiffVM{Snapshots: snaps}

	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from != "" || to != "" {
		if to == "" {
			to = "current"
		}
		result, cerr := s.computeDiff(from, to)
		if cerr == nil {
			vm.Result = result
			vm.FromLabel = from
			vm.ToLabel = to
		}
	}
	data.Diff = vm
	return data, nil
}

// ─── Sprint helpers ───────────────────────────────────────────────────────────

// buildSprintItems computes priority-weighted gap scores. limit=0 means all items.
func (s *Server) buildSprintItems(limit int) []SprintItemVM {
	weights := map[domain.Priority]float64{
		domain.PriorityCritical: 4,
		domain.PriorityHigh:     3,
		domain.PriorityMedium:   2,
		domain.PriorityLow:      1,
	}
	targets := map[domain.Priority]float64{
		domain.PriorityCritical: 1.0,
		domain.PriorityHigh:     0.8,
		domain.PriorityMedium:   0.6,
		domain.PriorityLow:      0.4,
	}

	type scored struct {
		f      domain.Feature
		domain string
		score  float64
	}

	reg, _ := s.store.LoadAll(s.registryDir)
	if reg == nil {
		return nil
	}

	var items []scored
	for _, d := range reg.Domains {
		for _, f := range d.Features {
			target := targets[f.Priority]
			health := featureHealth(f)
			gap := target - health
			if gap <= 0 {
				continue
			}
			items = append(items, scored{f: f, domain: d.Domain, score: weights[f.Priority] * gap})
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].score > items[j].score })

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	out := make([]SprintItemVM, 0, len(items))
	for _, it := range items {
		target := targets[it.f.Priority]
		health := featureHealth(it.f)
		out = append(out, SprintItemVM{
			ID:           it.f.ID,
			Domain:       it.domain,
			Priority:     string(it.f.Priority),
			Score:        it.score,
			HealthPct:    int(health * 100),
			TargetPct:    int(target * 100),
			PriorityDot:  priorityDot(it.f.Priority),
			PriorityText: priorityText(it.f.Priority),
			HealthBg:     healthBarClass(health),
			TargetBg:     targetBgClass(it.f.Priority),
		})
	}
	return out
}

// ─── View model builders ──────────────────────────────────────────────────────

func buildPriorityRings(m domain.Metrics) []DonutRingVM {
	type ring struct {
		label       string
		priority    domain.Priority
		strokeClass string
		labelClass  string
	}
	rings := []ring{
		{"Critical", domain.PriorityCritical, "stroke-red-500", "text-red-500"},
		{"High", domain.PriorityHigh, "stroke-yellow-500", "text-yellow-500"},
		{"Medium", domain.PriorityMedium, "stroke-emerald-500", "text-emerald-500"},
		{"Low", domain.PriorityLow, "stroke-slate-500", "text-slate-400"},
	}

	out := make([]DonutRingVM, 0, 4)
	for _, r := range rings {
		pm := m.ByPriority[r.priority]
		pct := 0
		if pm.Total > 0 {
			pct = (pm.CoveredUnit * 100) / pm.Total
		}
		dashOffset := 88 - (88 * pct / 100)
		out = append(out, DonutRingVM{
			Pct:         pct,
			AtTarget:    pm.CoveredUnit,
			Total:       pm.Total,
			DashOffset:  dashOffset,
			StrokeClass: r.strokeClass,
			LabelClass:  r.labelClass,
			Label:       r.label,
		})
	}
	return out
}

func buildCoverageBars(m domain.Metrics) []ProgressBarVM {
	total := m.TotalFeatures
	pct := func(n int) int {
		if total == 0 {
			return 0
		}
		return (n * 100) / total
	}
	barClass := func(p int) string {
		if p >= 70 {
			return "bg-emerald-500"
		}
		if p >= 40 {
			return "bg-yellow-500"
		}
		return "bg-red-500"
	}
	pctClass := func(p int) string {
		if p >= 70 {
			return "text-emerald-500"
		}
		if p >= 40 {
			return "text-yellow-500"
		}
		return "text-red-500"
	}

	up := pct(m.CoveredUnit)
	ip := pct(m.CoveredIntegration)
	ep := pct(m.CoveredE2E)

	return []ProgressBarVM{
		{Label: "Unit Tests", Pct: up, Count: m.CoveredUnit, PctClass: pctClass(up), BarClass: barClass(up)},
		{Label: "Integration Tests", Pct: ip, Count: m.CoveredIntegration, PctClass: pctClass(ip), BarClass: barClass(ip)},
		{Label: "E2E Tests", Pct: ep, Count: m.CoveredE2E, PctClass: pctClass(ep), BarClass: barClass(ep)},
	}
}

func featureToVM(f domain.Feature) FeatureRowVM {
	unitCov := f.Coverage.Unit.Backend != nil || f.Coverage.Unit.Web != nil || f.Coverage.Unit.Mobile != nil
	integCov := f.Coverage.Integration.Backend != nil || f.Coverage.Integration.Mobile != nil
	e2eCov := f.Coverage.E2E.Web != nil || f.Coverage.E2E.Mobile != nil

	status := "missing"
	if unitCov && integCov {
		status = "covered"
	} else if unitCov || integCov || e2eCov {
		status = "partial"
	}

	return FeatureRowVM{
		ID:           f.ID,
		Priority:     string(f.Priority),
		Status:       status,
		UnitCovered:  unitCov,
		IntegCovered: integCov,
		E2ECovered:   e2eCov,
		HasE2E:       e2eCov,
		HasUnit:      unitCov,
		PriorityDot:  priorityDot(f.Priority),
		PriorityText: priorityText(f.Priority),
		StatusClass:  statusBadgeClass(status),
	}
}

func contractToVM(c *domain.ContractOutput) *ContractVM {
	vm := &ContractVM{
		FeatureID:  c.FeatureID,
		EntryPoint: c.EntryPoint,
		LayerCount: len(c.Layers),
	}
	for _, l := range c.Layers {
		var calls []string
		if l.DelegateTo != "" {
			calls = append(calls, l.DelegateTo)
		}
		vm.Layers = append(vm.Layers, ContractLayerVM{
			Kind:         l.Kind,
			NodeID:       l.NodeID,
			FunctionName: l.Name,
			Signature:    l.Signature,
			Calls:        calls,
		})
	}
	return vm
}

// ─── Priority / status helpers ────────────────────────────────────────────────

func priorityDot(p domain.Priority) string {
	switch p {
	case domain.PriorityCritical:
		return "bg-red-500"
	case domain.PriorityHigh:
		return "bg-yellow-500"
	case domain.PriorityMedium:
		return "bg-emerald-500"
	default:
		return "bg-slate-500"
	}
}

func priorityText(p domain.Priority) string {
	switch p {
	case domain.PriorityCritical:
		return "text-red-500"
	case domain.PriorityHigh:
		return "text-yellow-500"
	case domain.PriorityMedium:
		return "text-emerald-500"
	default:
		return "text-slate-400"
	}
}

func statusBadgeClass(s string) string {
	switch s {
	case "covered":
		return "bg-emerald-500/10 text-emerald-400"
	case "partial":
		return "bg-yellow-500/10 text-yellow-400"
	case "failing":
		return "bg-red-500/10 text-red-400"
	default:
		return "bg-slate-800 text-slate-500"
	}
}

func healthBarClass(h float64) string {
	if h >= 0.7 {
		return "bg-emerald-500"
	}
	if h >= 0.4 {
		return "bg-yellow-500"
	}
	return "bg-red-500"
}

func targetBgClass(p domain.Priority) string {
	switch p {
	case domain.PriorityCritical:
		return "bg-red-500/30"
	case domain.PriorityHigh:
		return "bg-yellow-500/30"
	case domain.PriorityMedium:
		return "bg-emerald-500/30"
	default:
		return "bg-slate-500/30"
	}
}

// featureHealth returns a 0..1 health score approximated from coverage entries.
func featureHealth(f domain.Feature) float64 {
	score, total := 0.0, 0.0
	checkEntry := func(e *domain.CoverageEntry) {
		if e == nil {
			return
		}
		total++
		if !e.Status.IsMissing() {
			score++
		}
	}
	checkE2E := func(e *domain.E2ECoverageEntry) {
		if e == nil {
			return
		}
		total++
		if !e.Status.IsMissing() {
			score++
		}
	}
	checkEntry(f.Coverage.Unit.Backend)
	checkEntry(f.Coverage.Unit.Web)
	checkEntry(f.Coverage.Integration.Backend)
	checkE2E(f.Coverage.E2E.Web)
	if total == 0 {
		return 0
	}
	return score / total
}

func defaultDiagnoseChips() DiagnoseChipsVM {
	return DiagnoseChipsVM{
		HTTP:     []string{"401", "403", "404", "500", "409", "422", "429", "502", "503"},
		Infra:    []string{"timeout", "connection refused", "unique constraint", "deadlock", "json unmarshal", "CORS", "EOF", "TLS"},
		Frontend: []string{"TypeError", "selector not found", "hydration mismatch"},
	}
}

// ─── Render helpers ───────────────────────────────────────────────────────────

// renderFull pre-renders contentTmpl into data.Content, then renders base.html.
// This prevents multiple {{define "page-content"}} conflicts in the template set.
func (s *Server) renderFull(w http.ResponseWriter, contentTmpl string, data *PageData) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, contentTmpl, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.Content = template.HTML(buf.String()) //nolint:gosec // our own template output
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "base.html", data); err != nil {
		// Headers already sent — log only.
		_ = err
	}
}

// renderPartial renders a named template directly to the response (htmx swaps).
func (s *Server) renderPartial(w http.ResponseWriter, tmplName string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
