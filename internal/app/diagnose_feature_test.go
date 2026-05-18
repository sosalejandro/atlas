// @testreg workflow.diagnose
package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/internal/adapters"
	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// testGraphBuilder is a stub that returns a pre-built graph with nodes of
// various kinds so that extractCheckFiles has meaningful data to order.
type testGraphBuilder struct {
	graph *domain.Graph
}

func (b *testGraphBuilder) Build(_ string, _ ports.GraphConfig) (*domain.Graph, error) {
	return b.graph, nil
}

func (b *testGraphBuilder) BuildFrom(_ string, entryPoints []string, _ ports.GraphConfig) (*domain.Graph, error) {
	// Return the pre-built graph; entry point nodes are already present.
	return b.graph, nil
}

// buildTestGraph creates a minimal call graph:
//
//	handler (POST /api/v1/auth/login)
//	  ├─ service (AuthService.Login)
//	  │    └─ repository (UserRepo.FindByEmail)
//	  └─ external (TokenProvider.Issue)
func buildTestGraph() *domain.Graph {
	g := domain.NewGraph()

	handler := &domain.Node{ID: "POST /api/v1/auth/login", Kind: domain.NodeHandler, File: "internal/handler/auth.go"}
	svc := &domain.Node{ID: "AuthService.Login", Kind: domain.NodeService, File: "internal/service/auth_service.go"}
	repo := &domain.Node{ID: "UserRepo.FindByEmail", Kind: domain.NodeRepository, File: "internal/repo/user_repo.go"}
	ext := &domain.Node{ID: "TokenProvider.Issue", Kind: domain.NodeExternal, File: "internal/external/token.go"}

	g.AddNode(handler)
	g.AddNode(svc)
	g.AddNode(repo)
	g.AddNode(ext)

	g.AddEdge(handler.ID, svc.ID)
	g.AddEdge(svc.ID, repo.ID)
	g.AddEdge(handler.ID, ext.ID)

	return g
}

// setupDiagnoseRegistry creates a temporary registry directory with a single
// domain file containing the given features and returns the registry dir path.
func setupDiagnoseRegistry(t *testing.T, features []domain.Feature) string {
	t.Helper()

	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatalf("creating registry dir: %v", err)
	}

	store := adapters.NewYAMLStore()
	df := &domain.DomainFile{
		Domain:      "auth",
		Description: "Authentication features",
		Features:    features,
	}
	if err := store.SaveDomain(registryDir, df); err != nil {
		t.Fatalf("saving domain file: %v", err)
	}
	return registryDir
}

// authLoginFeature returns a feature with an API surface for POST /api/v1/auth/login.
func authLoginFeature() domain.Feature {
	return domain.Feature{
		ID:          "auth.login",
		Name:        "User Login",
		Description: "Email/password authentication",
		Priority:    domain.PriorityCritical,
		Surfaces: domain.Surfaces{
			Web: &domain.WebSurface{Route: "/login", Component: "LoginPage"},
			API: []domain.APISurface{{Method: "POST", Path: "/api/v1/auth/login"}},
		},
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
			},
		},
	}
}

// noAPISurfaceFeature returns a feature with no API surfaces (web-only).
func noAPISurfaceFeature() domain.Feature {
	return domain.Feature{
		ID:          "profile.view",
		Name:        "View Profile",
		Description: "Display user profile",
		Priority:    domain.PriorityMedium,
		Surfaces: domain.Surfaces{
			Web: &domain.WebSurface{Route: "/profile", Component: "ProfilePage"},
			// No API surfaces — trace should produce no entry points.
		},
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{Status: domain.StatusMissing},
			},
		},
	}
}

func TestDiagnoseExecute_401Symptom(t *testing.T) {
	graph := buildTestGraph()
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{authLoginFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	result, err := diagnoseUC.Execute(registryDir, "auth.login", "got 401 unauthorized response", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Rule should match the 401/unauthorized pattern.
	if result.Rule == nil {
		t.Fatal("expected a matched rule for 401 symptom, got nil")
	}
	if result.Rule.Layer != "backend-auth" {
		t.Errorf("Rule.Layer = %q, want %q", result.Rule.Layer, "backend-auth")
	}

	// CheckOrder for 401 is ["handler", "service", "external"], so handler files
	// should appear before service files, which appear before external files.
	if len(result.CheckFiles) == 0 {
		t.Fatal("expected non-empty CheckFiles")
	}

	// Verify ordering: handler file first, then service, then external.
	wantOrder := []string{
		"internal/handler/auth.go",
		"internal/service/auth_service.go",
		"internal/external/token.go",
	}
	if len(result.CheckFiles) < len(wantOrder) {
		t.Fatalf("CheckFiles has %d entries, want at least %d", len(result.CheckFiles), len(wantOrder))
	}
	for i, want := range wantOrder {
		if result.CheckFiles[i] != want {
			t.Errorf("CheckFiles[%d] = %q, want %q", i, result.CheckFiles[i], want)
		}
	}

	// FeatureID and Symptom should be passed through.
	if result.FeatureID != "auth.login" {
		t.Errorf("FeatureID = %q, want %q", result.FeatureID, "auth.login")
	}
	if result.Symptom != "got 401 unauthorized response" {
		t.Errorf("Symptom = %q, want %q", result.Symptom, "got 401 unauthorized response")
	}
}

func TestDiagnoseExecute_500Symptom(t *testing.T) {
	graph := buildTestGraph()
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{authLoginFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	result, err := diagnoseUC.Execute(registryDir, "auth.login", "500 internal server error", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Rule == nil {
		t.Fatal("expected a matched rule for 500 symptom, got nil")
	}
	if result.Rule.Layer != "backend-bug" {
		t.Errorf("Rule.Layer = %q, want %q", result.Rule.Layer, "backend-bug")
	}

	// CheckOrder for 500 is ["service", "repository", "handler"], so service files
	// should come first, then repository, then handler.
	if len(result.CheckFiles) == 0 {
		t.Fatal("expected non-empty CheckFiles")
	}

	wantOrder := []string{
		"internal/service/auth_service.go",
		"internal/repo/user_repo.go",
		"internal/handler/auth.go",
	}
	if len(result.CheckFiles) < len(wantOrder) {
		t.Fatalf("CheckFiles has %d entries, want at least %d", len(result.CheckFiles), len(wantOrder))
	}
	for i, want := range wantOrder {
		if result.CheckFiles[i] != want {
			t.Errorf("CheckFiles[%d] = %q, want %q", i, result.CheckFiles[i], want)
		}
	}
}

func TestDiagnoseExecute_TimeoutSymptom(t *testing.T) {
	graph := buildTestGraph()
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{authLoginFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	result, err := diagnoseUC.Execute(registryDir, "auth.login", "request timed out after 30s", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.Rule == nil {
		t.Fatal("expected a matched rule for timeout symptom, got nil")
	}
	if result.Rule.Layer != "infra" {
		t.Errorf("Rule.Layer = %q, want %q", result.Rule.Layer, "infra")
	}

	// CheckOrder for timeout is ["external", "repository", "service"], so external
	// files come first.
	if len(result.CheckFiles) == 0 {
		t.Fatal("expected non-empty CheckFiles")
	}

	wantOrder := []string{
		"internal/external/token.go",
		"internal/repo/user_repo.go",
		"internal/service/auth_service.go",
	}
	if len(result.CheckFiles) < len(wantOrder) {
		t.Fatalf("CheckFiles has %d entries, want at least %d", len(result.CheckFiles), len(wantOrder))
	}
	for i, want := range wantOrder {
		if result.CheckFiles[i] != want {
			t.Errorf("CheckFiles[%d] = %q, want %q", i, result.CheckFiles[i], want)
		}
	}
}

func TestDiagnoseExecute_NoMatchingSymptom(t *testing.T) {
	graph := buildTestGraph()
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{authLoginFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	result, err := diagnoseUC.Execute(registryDir, "auth.login", "something completely unrelated xyz", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// No rule should match.
	if result.Rule != nil {
		t.Errorf("expected nil Rule for unmatched symptom, got layer=%q", result.Rule.Layer)
	}

	// CheckFiles should be empty when no rule matches.
	if len(result.CheckFiles) != 0 {
		t.Errorf("expected empty CheckFiles, got %d entries: %v", len(result.CheckFiles), result.CheckFiles)
	}

	// Trace should still be populated even without a matching rule.
	if result.Trace == nil {
		t.Fatal("expected non-nil Trace even with no matching rule")
	}

	// FeatureID and Symptom should be passed through.
	if result.FeatureID != "auth.login" {
		t.Errorf("FeatureID = %q, want %q", result.FeatureID, "auth.login")
	}
	if result.Symptom != "something completely unrelated xyz" {
		t.Errorf("Symptom = %q, want input string", result.Symptom)
	}
}

func TestDiagnoseExecute_NoAPISurfaces(t *testing.T) {
	// A feature with no API surfaces produces no entry points, so the trace
	// returns early with empty traces. Diagnose should handle this gracefully.
	graph := domain.NewGraph() // empty graph — no nodes
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{noAPISurfaceFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	result, err := diagnoseUC.Execute(registryDir, "profile.view", "got 401 unauthorized", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Rule should still match (symptom matching is independent of the feature graph).
	if result.Rule == nil {
		t.Fatal("expected a matched rule for 401 symptom")
	}
	if result.Rule.Layer != "backend-auth" {
		t.Errorf("Rule.Layer = %q, want %q", result.Rule.Layer, "backend-auth")
	}

	// But CheckFiles should be empty because the trace has no nodes.
	if len(result.CheckFiles) != 0 {
		t.Errorf("expected empty CheckFiles for feature with no API surfaces, got %d: %v",
			len(result.CheckFiles), result.CheckFiles)
	}

	// Trace output should exist with the correct feature metadata.
	if result.Trace == nil {
		t.Fatal("expected non-nil Trace")
	}
	if result.Trace.FeatureID != "profile.view" {
		t.Errorf("Trace.FeatureID = %q, want %q", result.Trace.FeatureID, "profile.view")
	}
}

func TestDiagnoseExecute_NonExistentFeature(t *testing.T) {
	graph := domain.NewGraph()
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{authLoginFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	_, err := diagnoseUC.Execute(registryDir, "nonexistent.feature", "401 unauthorized", config)
	if err == nil {
		t.Fatal("expected error for non-existent feature, got nil")
	}
}

func TestDiagnoseExecute_CheckFilesRemainder(t *testing.T) {
	// Verify that files from node kinds NOT in the check order are appended
	// after the ordered files. The 401 rule checks ["handler", "service", "external"]
	// but our graph also has a "repository" node that isn't in that list.
	graph := buildTestGraph()
	registryDir := setupDiagnoseRegistry(t, []domain.Feature{authLoginFeature()})

	store := adapters.NewYAMLStore()
	builder := &testGraphBuilder{graph: graph}
	traceUC := NewTraceFeatureUseCase(store, builder)
	diagnoseUC := NewDiagnoseFeatureUseCase(traceUC)

	config := ports.GraphConfig{ProjectRoot: "/fake/project"}
	result, err := diagnoseUC.Execute(registryDir, "auth.login", "401 unauthorized", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// The graph has 4 nodes: handler, service, repository, external.
	// 401 check order is [handler, service, external], so repository should be appended last.
	if len(result.CheckFiles) != 4 {
		t.Fatalf("expected 4 CheckFiles (3 ordered + 1 remainder), got %d: %v",
			len(result.CheckFiles), result.CheckFiles)
	}

	// Last file should be the repository file (only kind not in check order).
	lastFile := result.CheckFiles[len(result.CheckFiles)-1]
	if lastFile != "internal/repo/user_repo.go" {
		t.Errorf("last CheckFile = %q, want repository file %q", lastFile, "internal/repo/user_repo.go")
	}
}
