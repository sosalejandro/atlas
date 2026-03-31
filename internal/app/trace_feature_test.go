// @testreg trace.dependency-graph
package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Helper: write a YAML registry file in a temp directory.
// NOTE: mockGraphBuilder is defined in audit_feature_test.go (same package).
// ---------------------------------------------------------------------------

func writeRegistryFile(t *testing.T, dir string, df domain.DomainFile) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	data, err := yaml.Marshal(df)
	if err != nil {
		t.Fatalf("marshal domain file: %v", err)
	}
	path := filepath.Join(dir, df.Domain+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Helper: build a small graph for testing.
//
// Layout (backend):
//   POST /api/v1/auth/login  (handler)
//     → AuthService.Login    (service)
//       → UserRepo.FindByEmail (repository)
//         → find_user_by_email (query)
//
// Layout (frontend chain → backend):
//   route:/login  → LoginPage → useAuth → authApi → POST /api/v1/auth/login
// ---------------------------------------------------------------------------

func buildTraceTestGraph() *domain.Graph {
	g := domain.NewGraph()

	// Backend nodes
	g.AddNode(&domain.Node{ID: "POST /api/v1/auth/login", Kind: domain.NodeHandler, File: "handler/auth.go", Line: 10})
	g.AddNode(&domain.Node{ID: "AuthService.Login", Kind: domain.NodeService, File: "service/auth.go", Line: 25})
	g.AddNode(&domain.Node{ID: "UserRepo.FindByEmail", Kind: domain.NodeRepository, File: "repo/user.go", Line: 40})
	g.AddNode(&domain.Node{ID: "find_user_by_email", Kind: domain.NodeQuery, File: "queries/user.sql", Line: 1})

	// Backend edges (handler → service → repo → query)
	g.AddEdge("POST /api/v1/auth/login", "AuthService.Login")
	g.AddEdge("AuthService.Login", "UserRepo.FindByEmail")
	g.AddEdge("UserRepo.FindByEmail", "find_user_by_email")

	// Frontend nodes
	g.AddNode(&domain.Node{ID: "route:/login", Kind: domain.NodeComponent, File: "src/routes.tsx", Line: 5})
	g.AddNode(&domain.Node{ID: "LoginPage", Kind: domain.NodeComponent, File: "src/pages/Login.tsx", Line: 1})
	g.AddNode(&domain.Node{ID: "useAuth", Kind: domain.NodeHook, File: "src/hooks/useAuth.ts", Line: 1})
	g.AddNode(&domain.Node{ID: "authApi", Kind: domain.NodeEndpoint, File: "src/api/auth.ts", Line: 1})

	// Frontend edges (route → component → hook → apiService → endpoint)
	g.AddEdge("route:/login", "LoginPage")
	g.AddEdge("LoginPage", "useAuth")
	g.AddEdge("useAuth", "authApi")
	g.AddEdge("authApi", "POST /api/v1/auth/login")

	return g
}

// ---------------------------------------------------------------------------
// Helper: build a domain file with a feature that has API surfaces.
// ---------------------------------------------------------------------------

func featureWithAPISurfaces() domain.DomainFile {
	return domain.DomainFile{
		Domain:      "auth",
		Description: "Authentication domain",
		Features: []domain.Feature{
			{
				ID:       "auth.login",
				Name:     "User Login",
				Priority: domain.PriorityCritical,
				Surfaces: domain.Surfaces{
					Web: &domain.WebSurface{Route: "/login", Component: "LoginPage"},
					API: []domain.APISurface{
						{Method: "POST", Path: "/api/v1/auth/login"},
					},
				},
				Coverage: domain.Coverage{
					Unit: domain.UnitCoverage{
						Backend: &domain.CoverageEntry{
							Status: domain.StatusCovered,
							Tests: []domain.TestEntry{
								{File: "service/auth_test.go", Functions: []domain.FunctionEntry{{Name: "TestLogin", Run: "go test ./service/ -run TestLogin"}}},
							},
						},
						Web: &domain.CoverageEntry{
							Status: domain.StatusCovered,
							Tests: []domain.TestEntry{
								{File: "src/pages/__tests__/Login.test.tsx"},
							},
						},
					},
					Integration: domain.IntegrationCoverage{
						Backend: &domain.CoverageEntry{
							Status: domain.StatusCovered,
							Tests: []domain.TestEntry{
								{File: "integration/auth_test.go"},
							},
						},
					},
					E2E: domain.E2ECoverage{
						Web: &domain.E2ECoverageEntry{
							Status: domain.StatusCovered,
							Tests: []domain.TestEntry{
								{File: "e2e/login.spec.ts"},
							},
						},
					},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Helper: build a domain file with a feature that has no API surfaces.
// ---------------------------------------------------------------------------

func featureWithoutAPISurfaces() domain.DomainFile {
	return domain.DomainFile{
		Domain:      "settings",
		Description: "Settings domain",
		Features: []domain.Feature{
			{
				ID:       "settings.theme",
				Name:     "Theme Toggle",
				Priority: domain.PriorityLow,
				Surfaces: domain.Surfaces{
					Web: &domain.WebSurface{Route: "/settings", Component: "ThemePicker"},
					// No API surfaces
				},
				Coverage: domain.Coverage{
					Unit: domain.UnitCoverage{
						Web: &domain.CoverageEntry{
							Status: domain.StatusCovered,
							Tests: []domain.TestEntry{
								{File: "src/components/__tests__/ThemePicker.test.tsx"},
							},
						},
					},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Helper: build a domain file with multiple API surfaces, including gRPC.
// ---------------------------------------------------------------------------

func featureWithMixedAPISurfaces() domain.DomainFile {
	return domain.DomainFile{
		Domain:      "products",
		Description: "Products domain",
		Features: []domain.Feature{
			{
				ID:       "products.create",
				Name:     "Create Product",
				Priority: domain.PriorityHigh,
				Surfaces: domain.Surfaces{
					API: []domain.APISurface{
						{Method: "POST", Path: "/api/v1/products"},
						{Method: "GRPC", Path: "ProductsServer.CreateProduct"},
					},
				},
				Coverage: domain.Coverage{},
			},
		},
	}
}

// ===========================================================================
// Tests
// ===========================================================================

func TestTraceExecute_WithAPISurfaces(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	writeRegistryFile(t, registryDir, featureWithAPISurfaces())

	graph := buildTraceTestGraph()
	builder := &mockGraphBuilder{graph: graph}
	store := newYAMLReader(t, registryDir)

	uc := NewTraceFeatureUseCase(store, builder)
	config := ports.GraphConfig{ProjectRoot: tmpDir, MaxDepth: 10}

	out, err := uc.Execute(registryDir, "auth.login", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if out.FeatureID != "auth.login" {
		t.Errorf("FeatureID = %q, want %q", out.FeatureID, "auth.login")
	}
	if out.FeatureName != "User Login" {
		t.Errorf("FeatureName = %q, want %q", out.FeatureName, "User Login")
	}
	if out.Priority != "critical" {
		t.Errorf("Priority = %q, want %q", out.Priority, "critical")
	}
	if len(out.Traces) == 0 {
		t.Fatal("Expected at least one trace, got 0")
	}

	// The single trace should have a root node.
	trace := out.Traces[0]
	if trace.Root == nil {
		t.Fatal("Expected non-nil Root in trace")
	}

	// The root should be the frontend route (FindPathTo returns a chain starting
	// at route:/login) or the handler if no frontend chain exists.
	// With our graph the frontend chain is: route:/login → LoginPage → useAuth → authApi → POST /api/v1/auth/login
	// So the root of the full-stack trace should be route:/login.
	rootID := trace.Root.Node.ID
	if rootID != "route:/login" {
		t.Errorf("trace root = %q, want %q", rootID, "route:/login")
	}

	// Walk down to verify the linear chain stitches correctly.
	node := trace.Root
	expectedChain := []string{"route:/login", "LoginPage", "useAuth", "authApi", "POST /api/v1/auth/login", "AuthService.Login", "UserRepo.FindByEmail", "find_user_by_email"}
	for i, wantID := range expectedChain {
		if node == nil {
			t.Fatalf("chain broke at index %d: expected %q but node is nil", i, wantID)
		}
		if node.Node.ID != wantID {
			t.Errorf("chain[%d] = %q, want %q", i, node.Node.ID, wantID)
		}
		if i < len(expectedChain)-1 {
			if len(node.Children) == 0 {
				t.Fatalf("chain[%d] (%q) has no children, expected %q", i, node.Node.ID, expectedChain[i+1])
			}
			node = node.Children[0]
		}
	}

	// TotalNodes should count all unique nodes in the stitched trace.
	if trace.TotalNodes < 4 {
		t.Errorf("TotalNodes = %d, want >= 4 (backend portion)", trace.TotalNodes)
	}
}

func TestTraceExecute_WithoutAPISurfaces(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	writeRegistryFile(t, registryDir, featureWithoutAPISurfaces())

	graph := domain.NewGraph()
	builder := &mockGraphBuilder{graph: graph}
	store := newYAMLReader(t, registryDir)

	uc := NewTraceFeatureUseCase(store, builder)
	config := ports.GraphConfig{ProjectRoot: tmpDir, MaxDepth: 10}

	out, err := uc.Execute(registryDir, "settings.theme", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if out.FeatureID != "settings.theme" {
		t.Errorf("FeatureID = %q, want %q", out.FeatureID, "settings.theme")
	}
	if len(out.Traces) != 0 {
		t.Errorf("Expected 0 traces for feature with no API surfaces, got %d", len(out.Traces))
	}
	// TestFiles should still be collected even without traces.
	if len(out.TestFiles) != 1 || out.TestFiles[0] != "src/components/__tests__/ThemePicker.test.tsx" {
		t.Errorf("TestFiles = %v, want [src/components/__tests__/ThemePicker.test.tsx]", out.TestFiles)
	}
}

func TestTraceExecute_FeatureNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	writeRegistryFile(t, registryDir, featureWithAPISurfaces())

	builder := &mockGraphBuilder{graph: domain.NewGraph()}
	store := newYAMLReader(t, registryDir)

	uc := NewTraceFeatureUseCase(store, builder)
	config := ports.GraphConfig{ProjectRoot: tmpDir}

	_, err := uc.Execute(registryDir, "nonexistent.feature", config)
	if err == nil {
		t.Fatal("Expected error for non-existent feature, got nil")
	}
}

func TestTraceExecute_DefaultMaxDepth(t *testing.T) {
	tmpDir := t.TempDir()
	registryDir := filepath.Join(tmpDir, "registry")

	writeRegistryFile(t, registryDir, featureWithAPISurfaces())

	graph := buildTraceTestGraph()
	builder := &mockGraphBuilder{graph: graph}
	store := newYAMLReader(t, registryDir)

	uc := NewTraceFeatureUseCase(store, builder)
	// MaxDepth = 0 → should default to 10 inside Execute.
	config := ports.GraphConfig{ProjectRoot: tmpDir, MaxDepth: 0}

	out, err := uc.Execute(registryDir, "auth.login", config)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(out.Traces) == 0 {
		t.Fatal("Expected traces even with MaxDepth=0 (should default to 10)")
	}
}

// ===========================================================================
// deriveEntryPoints
// ===========================================================================

func TestDeriveEntryPoints_HTTPMethods(t *testing.T) {
	f := &domain.Feature{
		Surfaces: domain.Surfaces{
			API: []domain.APISurface{
				{Method: "GET", Path: "/api/v1/users"},
				{Method: "POST", Path: "/api/v1/users"},
				{Method: "PUT", Path: "/api/v1/users/1"},
				{Method: "DELETE", Path: "/api/v1/users/1"},
				{Method: "PATCH", Path: "/api/v1/users/1"},
			},
		},
	}

	points := deriveEntryPoints(f)

	expected := []string{
		"GET /api/v1/users",
		"POST /api/v1/users",
		"PUT /api/v1/users/1",
		"DELETE /api/v1/users/1",
		"PATCH /api/v1/users/1",
	}

	if len(points) != len(expected) {
		t.Fatalf("len(points) = %d, want %d", len(points), len(expected))
	}
	for i, want := range expected {
		if points[i] != want {
			t.Errorf("points[%d] = %q, want %q", i, points[i], want)
		}
	}
}

func TestDeriveEntryPoints_NonHTTPMethods(t *testing.T) {
	f := &domain.Feature{
		Surfaces: domain.Surfaces{
			API: []domain.APISurface{
				{Method: "GRPC", Path: "ProductsServer.CreateProduct"},
				{Method: "CONSUMER", Path: "OrderConsumer.HandleEvent"},
				{Method: "EVENT", Path: "NotifyService.OnUserCreated"},
			},
		},
	}

	points := deriveEntryPoints(f)

	expected := []string{
		"ProductsServer.CreateProduct",
		"OrderConsumer.HandleEvent",
		"NotifyService.OnUserCreated",
	}

	if len(points) != len(expected) {
		t.Fatalf("len(points) = %d, want %d", len(points), len(expected))
	}
	for i, want := range expected {
		if points[i] != want {
			t.Errorf("points[%d] = %q, want %q", i, points[i], want)
		}
	}
}

func TestDeriveEntryPoints_MixedMethods(t *testing.T) {
	f := &domain.Feature{
		Surfaces: domain.Surfaces{
			API: []domain.APISurface{
				{Method: "POST", Path: "/api/v1/products"},
				{Method: "GRPC", Path: "ProductsServer.CreateProduct"},
			},
		},
	}

	points := deriveEntryPoints(f)

	if len(points) != 2 {
		t.Fatalf("len(points) = %d, want 2", len(points))
	}
	if points[0] != "POST /api/v1/products" {
		t.Errorf("points[0] = %q, want %q", points[0], "POST /api/v1/products")
	}
	if points[1] != "ProductsServer.CreateProduct" {
		t.Errorf("points[1] = %q, want %q", points[1], "ProductsServer.CreateProduct")
	}
}

func TestDeriveEntryPoints_Empty(t *testing.T) {
	f := &domain.Feature{
		Surfaces: domain.Surfaces{},
	}

	points := deriveEntryPoints(f)
	if len(points) != 0 {
		t.Errorf("Expected 0 entry points for feature with no API surfaces, got %d", len(points))
	}
}

// ===========================================================================
// collectTestFiles
// ===========================================================================

func TestCollectTestFiles_GathersFromAllEntries(t *testing.T) {
	f := &domain.Feature{
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "service/auth_test.go"}},
				},
				Web: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "src/pages/__tests__/Login.test.tsx"}},
				},
				Mobile: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "mobile/auth_test.dart"}},
				},
			},
			Integration: domain.IntegrationCoverage{
				Backend: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "integration/auth_test.go"}},
				},
				Mobile: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "mobile/integration/auth_test.dart"}},
				},
			},
			E2E: domain.E2ECoverage{
				Web: &domain.E2ECoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "e2e/login.spec.ts"}},
				},
				Mobile: &domain.E2ECoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "mobile/e2e/login.yaml"}},
				},
			},
		},
	}

	files := collectTestFiles(f)

	expected := map[string]bool{
		"service/auth_test.go":                   true,
		"src/pages/__tests__/Login.test.tsx":      true,
		"mobile/auth_test.dart":                   true,
		"integration/auth_test.go":                true,
		"mobile/integration/auth_test.dart":       true,
		"e2e/login.spec.ts":                       true,
		"mobile/e2e/login.yaml":                   true,
	}

	if len(files) != len(expected) {
		t.Fatalf("len(files) = %d, want %d; files = %v", len(files), len(expected), files)
	}
	for _, f := range files {
		if !expected[f] {
			t.Errorf("unexpected file %q in collectTestFiles output", f)
		}
	}
}

func TestCollectTestFiles_DeduplicatesSameFile(t *testing.T) {
	f := &domain.Feature{
		Coverage: domain.Coverage{
			Unit: domain.UnitCoverage{
				Backend: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					Tests:  []domain.TestEntry{{File: "service/auth_test.go"}},
				},
			},
			Integration: domain.IntegrationCoverage{
				Backend: &domain.CoverageEntry{
					Status: domain.StatusCovered,
					// Same file as unit
					Tests: []domain.TestEntry{{File: "service/auth_test.go"}},
				},
			},
		},
	}

	files := collectTestFiles(f)

	if len(files) != 1 {
		t.Errorf("Expected 1 deduplicated file, got %d: %v", len(files), files)
	}
}

func TestCollectTestFiles_EmptyCoverage(t *testing.T) {
	f := &domain.Feature{
		Coverage: domain.Coverage{},
	}

	files := collectTestFiles(f)
	if len(files) != 0 {
		t.Errorf("Expected 0 files for empty coverage, got %d: %v", len(files), files)
	}
}

// ===========================================================================
// buildLinearFullStackTrace
// ===========================================================================

func TestBuildLinearFullStackTrace_StitchesFrontendToBackend(t *testing.T) {
	graph := buildTraceTestGraph()

	// Simulate the frontend chain: route:/login → LoginPage → useAuth → authApi → POST /api/v1/auth/login
	chain := []string{"route:/login", "LoginPage", "useAuth", "authApi", "POST /api/v1/auth/login"}

	// Build the backend trace starting from the endpoint.
	backendTrace := graph.TraceFrom("POST /api/v1/auth/login", 10)
	if backendTrace.Root == nil {
		t.Fatal("backend TraceFrom returned nil Root")
	}

	fullTrace := buildLinearFullStackTrace(graph, chain, backendTrace)

	if fullTrace.Root == nil {
		t.Fatal("Expected non-nil Root in full-stack trace")
	}

	// Walk the linear chain.
	expectedIDs := []string{"route:/login", "LoginPage", "useAuth", "authApi", "POST /api/v1/auth/login", "AuthService.Login", "UserRepo.FindByEmail", "find_user_by_email"}
	node := fullTrace.Root
	for i, wantID := range expectedIDs {
		if node == nil {
			t.Fatalf("chain broke at index %d: expected %q", i, wantID)
		}
		if node.Node.ID != wantID {
			t.Errorf("chain[%d] = %q, want %q", i, node.Node.ID, wantID)
		}
		// Verify depths are sequential.
		if node.Depth != i {
			t.Errorf("chain[%d] depth = %d, want %d", i, node.Depth, i)
		}
		if i < len(expectedIDs)-1 {
			if len(node.Children) == 0 {
				t.Fatalf("chain[%d] (%q) has no children", i, node.Node.ID)
			}
			node = node.Children[0]
		}
	}

	// TotalNodes = backend nodes + frontend nodes (excluding endpoint since counted in backend).
	// Backend trace has 4 nodes (handler, service, repo, query).
	// Frontend chain has 4 nodes excluding endpoint = 4 nodes.
	// Total = 4 + 4 = 8
	if fullTrace.TotalNodes != 8 {
		t.Errorf("TotalNodes = %d, want 8", fullTrace.TotalNodes)
	}

	// MaxDepth = backend MaxDepth + frontend extra nodes.
	// Backend: depth 0,1,2,3 → MaxDepth 3. Frontend adds 4 extra → 3+4=7.
	if fullTrace.MaxDepth != 7 {
		t.Errorf("MaxDepth = %d, want 7", fullTrace.MaxDepth)
	}
}

func TestBuildLinearFullStackTrace_BackendOnlyWhenChainIsEndpoint(t *testing.T) {
	graph := buildTraceTestGraph()

	// Chain with only the endpoint itself (length 1 means no frontend caller).
	// buildLinearFullStackTrace is called with len(chain) > 1, but let's test
	// the minimal case where chain = [endpoint] just to verify robustness.
	chain := []string{"POST /api/v1/auth/login"}
	backendTrace := graph.TraceFrom("POST /api/v1/auth/login", 10)

	fullTrace := buildLinearFullStackTrace(graph, chain, backendTrace)

	if fullTrace.Root == nil {
		t.Fatal("Expected non-nil Root")
	}
	if fullTrace.Root.Node.ID != "POST /api/v1/auth/login" {
		t.Errorf("root = %q, want %q", fullTrace.Root.Node.ID, "POST /api/v1/auth/login")
	}

	// Should still have backend children.
	if len(fullTrace.Root.Children) == 0 {
		t.Error("Expected backend children under the endpoint node")
	}
}

// ===========================================================================
// pickBestCallerChain (unexported helper)
// ===========================================================================

func TestPickBestCallerChain_MatchesByRoute(t *testing.T) {
	trees := []*domain.TraceNode{
		{Node: &domain.Node{ID: "route:/settings", Kind: domain.NodeComponent}},
		{Node: &domain.Node{ID: "route:/login", Kind: domain.NodeComponent}},
		{Node: &domain.Node{ID: "SomeOther", Kind: domain.NodeComponent}},
	}

	best := pickBestCallerChain(trees, "/login")
	if best == nil {
		t.Fatal("Expected non-nil best chain")
	}
	if best.Node.ID != "route:/login" {
		t.Errorf("best = %q, want %q", best.Node.ID, "route:/login")
	}
}

func TestPickBestCallerChain_FallsBackToDeepest(t *testing.T) {
	shallow := &domain.TraceNode{
		Node: &domain.Node{ID: "A", Kind: domain.NodeComponent},
	}
	deep := &domain.TraceNode{
		Node: &domain.Node{ID: "B", Kind: domain.NodeComponent},
		Children: []*domain.TraceNode{
			{Node: &domain.Node{ID: "C", Kind: domain.NodeComponent}},
		},
	}

	best := pickBestCallerChain([]*domain.TraceNode{shallow, deep}, "")
	if best == nil {
		t.Fatal("Expected non-nil best chain")
	}
	if best.Node.ID != "B" {
		t.Errorf("best = %q, want %q (deepest)", best.Node.ID, "B")
	}
}

func TestPickBestCallerChain_EmptyReturnsNil(t *testing.T) {
	best := pickBestCallerChain(nil, "/login")
	if best != nil {
		t.Errorf("Expected nil for empty callerTrees, got %v", best)
	}
}

// ===========================================================================
// countNodes (unexported helper)
// ===========================================================================

func TestCountNodes(t *testing.T) {
	tests := []struct {
		name string
		node *domain.TraceNode
		want int
	}{
		{name: "nil", node: nil, want: 0},
		{name: "single", node: &domain.TraceNode{Node: &domain.Node{ID: "A"}}, want: 1},
		{
			name: "tree with children",
			node: &domain.TraceNode{
				Node: &domain.Node{ID: "A"},
				Children: []*domain.TraceNode{
					{Node: &domain.Node{ID: "B"}},
					{
						Node: &domain.Node{ID: "C"},
						Children: []*domain.TraceNode{
							{Node: &domain.Node{ID: "D"}},
						},
					},
				},
			},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countNodes(tt.node)
			if got != tt.want {
				t.Errorf("countNodes() = %d, want %d", got, tt.want)
			}
		})
	}
}

// ===========================================================================
// reDepthAll (unexported helper)
// ===========================================================================

func TestReDepthAll(t *testing.T) {
	root := &domain.TraceNode{
		Node:  &domain.Node{ID: "A"},
		Depth: 0,
		Children: []*domain.TraceNode{
			{
				Node:  &domain.Node{ID: "B"},
				Depth: 0,
				Children: []*domain.TraceNode{
					{Node: &domain.Node{ID: "C"}, Depth: 0},
				},
			},
		},
	}

	reDepthAll(root, 5)

	if root.Depth != 5 {
		t.Errorf("root.Depth = %d, want 5", root.Depth)
	}
	if root.Children[0].Depth != 6 {
		t.Errorf("child.Depth = %d, want 6", root.Children[0].Depth)
	}
	if root.Children[0].Children[0].Depth != 7 {
		t.Errorf("grandchild.Depth = %d, want 7", root.Children[0].Children[0].Depth)
	}
}

// ===========================================================================
// YAMLStore wrapper for RegistryReader
// ===========================================================================

// newYAMLReader returns a RegistryReader backed by the real YAMLStore.
// The caller is responsible for writing registry files before calling this.
func newYAMLReader(t *testing.T, registryDir string) ports.RegistryReader {
	t.Helper()
	// Import the concrete adapter through the adapters package.
	// Since we're in the app package, we use the adapters.NewYAMLStore() as before.
	return &yamlStoreReader{dir: registryDir}
}

// yamlStoreReader wraps the file-system based YAML loading to satisfy ports.RegistryReader.
type yamlStoreReader struct {
	dir string
}

func (r *yamlStoreReader) LoadAll(dir string) (*domain.Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &domain.Registry{}, nil
		}
		return nil, err
	}

	reg := &domain.Registry{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var df domain.DomainFile
		if err := yaml.Unmarshal(data, &df); err != nil {
			return nil, err
		}
		reg.Domains = append(reg.Domains, df)
	}
	return reg, nil
}

func (r *yamlStoreReader) LoadDomain(dir, domainName string) (*domain.DomainFile, error) {
	data, err := os.ReadFile(filepath.Join(dir, domainName+".yaml"))
	if err != nil {
		return nil, err
	}
	var df domain.DomainFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return nil, err
	}
	return &df, nil
}
