package server

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/sosalejandro/atlas/internal/domain"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/app"
	"github.com/sosalejandro/atlas/internal/ports"
)

//go:embed templates/* static/*
var assets embed.FS

// Server holds the HTTP server and its dependencies.
type Server struct {
	mux         *http.ServeMux
	tmpl        *template.Template
	registryDir string
	projectRoot string
	projectName string
	config      ports.GraphConfig
	store       *adapters.YAMLStore
	statusUC    *app.GetStatusUseCase
	traceUC     *app.TraceFeatureUseCase
	diagnoseUC  *app.DiagnoseFeatureUseCase
	contractUC  *app.ContractFeatureUseCase
	auditUC     *app.AuditFeatureUseCase
}

// New creates and configures the HTTP server.
func New(registryDir, projectRoot, projectName string) (*Server, error) {
	graphSection, _ := adapters.LoadGraphConfig(projectRoot)
	config := graphSection.ToPortsConfig()
	config.ProjectRoot = projectRoot
	return newWithBuilder(registryDir, projectRoot, projectName, config, adapters.NewGraphBuilder(config))
}

// NewForTesting creates a Server with an injected GraphBuilder.
// Use adapters.NewStubGraphBuilder() to avoid real AST scanning in tests.
func NewForTesting(registryDir, projectRoot, projectName string, builder ports.GraphBuilder) (*Server, error) {
	return newWithBuilder(registryDir, projectRoot, projectName, ports.GraphConfig{ProjectRoot: projectRoot}, builder)
}

func newWithBuilder(registryDir, projectRoot, projectName string, config ports.GraphConfig, builder ports.GraphBuilder) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}

	store := adapters.NewYAMLStore()
	traceUC := app.NewTraceFeatureUseCase(store, builder)

	s := &Server{
		mux:         http.NewServeMux(),
		tmpl:        tmpl,
		registryDir: registryDir,
		projectRoot: projectRoot,
		projectName: projectName,
		config:      config,
		store:       store,
		statusUC:    app.NewGetStatusUseCase(store),
		traceUC:     traceUC,
		diagnoseUC:  app.NewDiagnoseFeatureUseCase(traceUC),
		contractUC:  app.NewContractFeatureUseCase(traceUC, store),
		auditUC:     app.NewAuditFeatureUseCase(traceUC, store),
	}

	s.routes()
	return s, nil
}

// Handler returns the HTTP handler for use with http.ListenAndServe.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// routes registers all HTTP routes.
func (s *Server) routes() {
	// Static assets.
	staticFS, _ := fs.Sub(assets, "static")
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Full page routes (first load).
	s.mux.HandleFunc("/", s.handleOverview)
	s.mux.HandleFunc("/features", s.handleFeatures)
	s.mux.HandleFunc("/contract", s.handleContract)
	s.mux.HandleFunc("/diagnose", s.handleDiagnose)
	s.mux.HandleFunc("/sprint", s.handleSprint)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc("/diff", s.handleDiff)
	s.mux.HandleFunc("/graph", s.handleGraph)

	// htmx partial routes (navigation swaps into #page-content).
	s.mux.HandleFunc("/pages/overview", s.handleOverviewPartial)
	s.mux.HandleFunc("/pages/features", s.handleFeaturesPartial)
	s.mux.HandleFunc("/pages/contract", s.handleContractPartial)
	s.mux.HandleFunc("/pages/diagnose", s.handleDiagnosePartial)
	s.mux.HandleFunc("/pages/sprint", s.handleSprintPartial)
	s.mux.HandleFunc("/pages/metrics", s.handleMetricsPartial)
	s.mux.HandleFunc("/pages/diff", s.handleDiffPartial)
	s.mux.HandleFunc("/pages/graph", s.handleGraphPartial)
	s.mux.HandleFunc("/pages/scan-modal", s.handleScanModal)

	// API endpoints.
	s.mux.HandleFunc("/api/scan", s.handleScan)
	s.mux.HandleFunc("/api/feature/", s.handleFeatureDetail)
	s.mux.HandleFunc("/api/diff/snapshot", s.handleDiffSnapshot)
	s.mux.HandleFunc("/api/diff/compare", s.handleDiffCompare)
	s.mux.HandleFunc("/api/sprint", s.handleSprintAPI)
}

// parseTemplates loads and parses all HTML templates.
func parseTemplates() (*template.Template, error) {
	funcMap := template.FuncMap{
		"add":     func(a, b int) int { return a + b },
		"mul":     func(a, b float64) float64 { return a * b },
		"float64": func(a int) float64 { return float64(a) },
		"slice": func(rules []*domain.SymptomRule, start, end int) []*domain.SymptomRule {
			if start < 0 {
				start = 0
			}
			if end > len(rules) {
				end = len(rules)
			}
			if start >= end {
				return nil
			}
			return rules[start:end]
		},
		"layerBorderClass": layerBorderClass,
		"layerLabelClass":  layerLabelClass,
		"dec":              func(a int) int { return a - 1 },
		"inc":              func(a int) int { return a + 1 },
		"div":              func(a, b int) int { if b == 0 { return 0 }; return a / b },
	}

	tmpl := template.New("").Funcs(funcMap)

	// Read all template files from the embed.
	entries, err := fs.ReadDir(assets, "templates")
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := assets.ReadFile("templates/" + e.Name())
		if err != nil {
			return nil, err
		}
		if _, err := tmpl.New(e.Name()).Parse(string(data)); err != nil {
			return nil, fmt.Errorf("template %s: %w", e.Name(), err)
		}
	}

	return tmpl, nil
}

func layerBorderClass(kind string) string {
	switch kind {
	case "handler", "endpoint":
		return "border-primary"
	case "service":
		return "border-tertiary"
	case "repository":
		return "border-secondary"
	case "query":
		return "border-yellow-500"
	default:
		return "border-outline-variant"
	}
}

func layerLabelClass(kind string) string {
	switch kind {
	case "handler", "endpoint":
		return "text-primary"
	case "service":
		return "text-tertiary"
	case "repository":
		return "text-secondary"
	case "query":
		return "text-yellow-500"
	default:
		return "text-on-surface-variant"
	}
}
