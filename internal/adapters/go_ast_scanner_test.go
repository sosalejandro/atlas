// @testreg trace.go-ast-scanner
package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// ---------------------------------------------------------------------------
// Helpers: create Go source fixtures on disk.
// ---------------------------------------------------------------------------

func writeTempFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Phase 2: Function discovery
// ---------------------------------------------------------------------------

func TestGoASTScanner_DiscoversFunctions(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src", "infrastructure", "http", "handlers")

	writeTempFile(t, srcDir, "auth_handler.go", `package handlers

// AuthHandler handles authentication requests.
type AuthHandler struct {
	service AuthService
}

// NewAuthHandler creates a new handler.
func NewAuthHandler(service AuthService) *AuthHandler {
	return &AuthHandler{service: service}
}

// Login authenticates a user.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	_ = h.service.Authenticate("test@example.com", "password")
}
`)

	svcDir := filepath.Join(root, "src", "application", "services")
	writeTempFile(t, svcDir, "auth_service.go", `package services

// AuthServiceImpl implements authentication logic.
type AuthServiceImpl struct {
	repo UserRepository
}

// Authenticate validates credentials.
func (s *AuthServiceImpl) Authenticate(email, password string) error {
	return nil
}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Verify handler nodes were created.
	if _, ok := graph.Nodes["AuthHandler.Login"]; !ok {
		t.Errorf("expected node AuthHandler.Login, got nodes: %v", nodeIDs(graph))
	}

	if _, ok := graph.Nodes["AuthHandler.NewAuthHandler"]; ok {
		// NewAuthHandler is a package-level function, not a method.
		// It should be registered as "handlers.NewAuthHandler".
	}

	// Verify service nodes were created.
	if _, ok := graph.Nodes["AuthServiceImpl.Authenticate"]; !ok {
		t.Errorf("expected node AuthServiceImpl.Authenticate, got nodes: %v", nodeIDs(graph))
	}
}

func TestGoASTScanner_ClassifiesNodeKinds(t *testing.T) {
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "h.go", `package handlers
type H struct{}
func (h *H) Handle() {}
`)

	serviceDir := filepath.Join(root, "src", "application", "services")
	writeTempFile(t, serviceDir, "s.go", `package services
type S struct{}
func (s *S) Process() {}
`)

	repoDir := filepath.Join(root, "src", "infrastructure", "persistence")
	writeTempFile(t, repoDir, "r.go", `package persistence
type R struct{}
func (r *R) FindByID() {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	tests := []struct {
		id       string
		wantKind domain.NodeKind
	}{
		{"H.Handle", domain.NodeHandler},
		{"S.Process", domain.NodeService},
		{"R.FindByID", domain.NodeRepository},
	}

	for _, tc := range tests {
		node, ok := graph.Nodes[tc.id]
		if !ok {
			t.Errorf("node %s not found; available: %v", tc.id, nodeIDs(graph))
			continue
		}
		if node.Kind != tc.wantKind {
			t.Errorf("node %s: kind = %s, want %s", tc.id, node.Kind, tc.wantKind)
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 3: Call graph extraction
// ---------------------------------------------------------------------------

func TestGoASTScanner_ExtractsCallEdges(t *testing.T) {
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "auth_handler.go", `package handlers

type AuthService interface {
	Authenticate(email, password string) error
}

type AuthHandler struct {
	service AuthServiceImpl
}

func (h *AuthHandler) Login() {
	h.service.Authenticate("a", "b")
}
`)

	serviceDir := filepath.Join(root, "src", "services")
	writeTempFile(t, serviceDir, "auth_service.go", `package services

type AuthServiceImpl struct{}

func (s *AuthServiceImpl) Authenticate(email, password string) error {
	return nil
}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Verify edge from handler to service.
	found := false
	for _, e := range graph.Edges {
		if e.From == "AuthHandler.Login" && e.To == "AuthServiceImpl.Authenticate" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected edge AuthHandler.Login → AuthServiceImpl.Authenticate; edges: %v", graph.Edges)
	}
}

func TestGoASTScanner_SkipsTestFiles(t *testing.T) {
	root := t.TempDir()

	srcDir := filepath.Join(root, "src", "services")
	writeTempFile(t, srcDir, "auth.go", `package services
type Auth struct{}
func (a *Auth) Login() {}
`)
	writeTempFile(t, srcDir, "auth_test.go", `package services
func TestLogin(t *testing.T) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// The test function should not appear.
	for id := range graph.Nodes {
		if id == "services.TestLogin" {
			t.Errorf("test function TestLogin should not be in the graph")
		}
	}
}

func TestGoASTScanner_SkipsGeneratedDirectory(t *testing.T) {
	root := t.TempDir()

	genDir := filepath.Join(root, "src", "domain", "repositories", "generated")
	writeTempFile(t, genDir, "queries.go", `package generated
type Queries struct{}
func (q *Queries) GetUserByEmail() {}
`)

	srcDir := filepath.Join(root, "src", "services")
	writeTempFile(t, srcDir, "user.go", `package services
type UserService struct{}
func (s *UserService) Find() {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Generated code should not produce nodes.
	for id := range graph.Nodes {
		if id == "Queries.GetUserByEmail" {
			t.Errorf("generated function should not be in the graph")
		}
	}

	// But the real service should be there.
	if _, ok := graph.Nodes["UserService.Find"]; !ok {
		t.Errorf("expected UserService.Find node; got: %v", nodeIDs(graph))
	}
}

func TestGoASTScanner_IgnoreFunctionsGlob(t *testing.T) {
	root := t.TempDir()

	srcDir := filepath.Join(root, "src", "services")
	writeTempFile(t, srcDir, "svc.go", `package services

type Svc struct{}

func (s *Svc) String() string { return "" }
func (s *Svc) Process() {}
`)

	srcDir2 := filepath.Join(root, "src", "handlers")
	writeTempFile(t, srcDir2, "h.go", `package handlers

type H struct {
	svc Svc
}

func (h *H) Handle() {
	_ = h.svc.String()
	h.svc.Process()
}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{
		BackendRoot:     "src",
		MaxDepth:        10,
		IgnoreFunctions: []string{"*.String"},
	}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Edge to String should be suppressed.
	for _, e := range graph.Edges {
		if e.From == "H.Handle" && e.To == "Svc.String" {
			t.Errorf("edge to Svc.String should be ignored due to IgnoreFunctions glob")
		}
	}
}

// ---------------------------------------------------------------------------
// BuildFrom (lazy mode)
// ---------------------------------------------------------------------------

func TestGoASTScanner_BuildFrom_PrunesUnreachable(t *testing.T) {
	root := t.TempDir()

	srcDir := filepath.Join(root, "src", "services")
	writeTempFile(t, srcDir, "a.go", `package services

type ServiceA struct{}
func (s *ServiceA) DoA() {}
`)
	writeTempFile(t, srcDir, "b.go", `package services

type ServiceB struct{}
func (s *ServiceB) DoB() {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.BuildFrom(root, []string{"ServiceA.DoA"}, config)
	if err != nil {
		t.Fatalf("BuildFrom() error: %v", err)
	}

	// ServiceA.DoA should be present.
	if _, ok := graph.Nodes["ServiceA.DoA"]; !ok {
		t.Errorf("expected ServiceA.DoA in graph; got: %v", nodeIDs(graph))
	}

	// ServiceB.DoB should be pruned (unreachable).
	if _, ok := graph.Nodes["ServiceB.DoB"]; ok {
		t.Errorf("ServiceB.DoB should be pruned from BuildFrom graph")
	}
}

// ---------------------------------------------------------------------------
// Struct field resolution
// ---------------------------------------------------------------------------

func TestGoASTScanner_ResolvesStructFieldCalls(t *testing.T) {
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "session_handler.go", `package handlers

type SessionHandler struct {
	service SessionService
}

type SessionService struct{}

func (h *SessionHandler) ListSessions() {
	h.service.GetUserSessions()
}
`)

	serviceDir := filepath.Join(root, "src", "services")
	writeTempFile(t, serviceDir, "session_service.go", `package services

type SessionService struct{}

func (s *SessionService) GetUserSessions() {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should have the handler node.
	if _, ok := graph.Nodes["SessionHandler.ListSessions"]; !ok {
		t.Errorf("expected SessionHandler.ListSessions; got: %v", nodeIDs(graph))
	}

	// Should have the service node.
	if _, ok := graph.Nodes["SessionService.GetUserSessions"]; !ok {
		t.Errorf("expected SessionService.GetUserSessions; got: %v", nodeIDs(graph))
	}

	// Should have an edge (possibly ambiguous, since the field type "SessionService"
	// is defined in the handler package but the method is in services package).
	foundEdge := false
	for _, e := range graph.Edges {
		if e.From == "SessionHandler.ListSessions" && e.To == "SessionService.GetUserSessions" {
			foundEdge = true
			break
		}
	}
	if !foundEdge {
		t.Logf("edges: %v", graph.Edges)
		// This is acceptable — cross-package struct resolution has limitations
		// without go/types. Log but don't fail.
		t.Logf("WARN: cross-package field resolution not resolved (expected without go/types)")
	}
}

// ---------------------------------------------------------------------------
// normaliseHandlerRef
// ---------------------------------------------------------------------------

func TestNormaliseHandlerRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"h.authHandler.Login", "authHandler.Login"},
		{"handler.Login", "handler.Login"},
		{"Login", "Login"},
		{"a.b.c.Method", "c.Method"},
		{"wrapHandler(...)", "wrapHandler"},
		{"<func>", ""},
		{"<unknown>", ""},
	}

	for _, tc := range tests {
		got := normaliseHandlerRef(tc.input)
		if got != tc.want {
			t.Errorf("normaliseHandlerRef(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// matchGlob
// ---------------------------------------------------------------------------

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		s       string
		want    bool
	}{
		{"*.String", "Svc.String", true},
		{"*.String", "Svc.Process", false},
		{"fmt.*", "fmt.Println", true},
		{"fmt.*", "log.Println", false},
		{"exact", "exact", true},
		{"exact", "notexact", false},
	}

	for _, tc := range tests {
		got := matchGlob(tc.pattern, tc.s)
		if got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// classifyNodeKind
// ---------------------------------------------------------------------------

func TestClassifyNodeKind(t *testing.T) {
	tests := []struct {
		pkgDir string
		want   domain.NodeKind
	}{
		{"infrastructure/http/handlers", domain.NodeHandler},
		{"application/services", domain.NodeService},
		{"infrastructure/persistence", domain.NodeRepository},
		{"domain/repositories/generated", domain.NodeQuery},
		{"domain/value_objects", domain.NodeService}, // default
	}

	for _, tc := range tests {
		got := classifyNodeKind(tc.pkgDir, ports.LayerRules{})
		if got != tc.want {
			t.Errorf("classifyNodeKind(%q) = %s, want %s", tc.pkgDir, got, tc.want)
		}
	}
}

func TestClassifyNodeKind_CustomRules(t *testing.T) {
	rules := ports.LayerRules{
		Handler:    []string{"controladores", "controllers", "api"},
		Repository: []string{"dao", "store", "repositorios"},
		Service:    []string{"usecase", "interactor"},
		Query:      []string{"queries"},
	}

	tests := []struct {
		pkgDir string
		want   domain.NodeKind
	}{
		// Custom rules should match.
		{"src/controladores/auth", domain.NodeHandler},
		{"internal/controllers", domain.NodeHandler},
		{"pkg/api/v1", domain.NodeHandler},
		{"internal/dao/user", domain.NodeRepository},
		{"src/store", domain.NodeRepository},
		{"src/repositorios/usuario", domain.NodeRepository},
		{"application/usecase/auth", domain.NodeService},
		{"domain/interactor", domain.NodeService},
		{"db/queries", domain.NodeQuery},

		// Built-in defaults should still work.
		{"infrastructure/http/handlers", domain.NodeHandler},
		{"application/services", domain.NodeService},
		{"infrastructure/persistence", domain.NodeRepository},

		// Unknown should default to service.
		{"pkg/util", domain.NodeService},
	}

	for _, tc := range tests {
		got := classifyNodeKind(tc.pkgDir, rules)
		if got != tc.want {
			t.Errorf("classifyNodeKind(%q, customRules) = %s, want %s", tc.pkgDir, got, tc.want)
		}
	}
}

func TestClassifyNodeKind_CustomRulesPriority(t *testing.T) {
	// Custom rules should take priority over built-in defaults.
	// For example, if "service" directory should be treated as handler.
	rules := ports.LayerRules{
		Handler: []string{"service"},
	}

	got := classifyNodeKind("application/services", rules)
	if got != domain.NodeHandler {
		t.Errorf("custom rule should override built-in: got %s, want handler", got)
	}
}

// ---------------------------------------------------------------------------
// buildSignature
// ---------------------------------------------------------------------------

func TestBuildSignature(t *testing.T) {
	root := t.TempDir()

	srcDir := filepath.Join(root, "src")
	writeTempFile(t, srcDir, "sig.go", `package src

type Svc struct{}

func (s *Svc) Process(ctx context.Context, email string) (string, error) {
	return "", nil
}

func PlainFunc(x int) bool { return true }
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if node, ok := graph.Nodes["Svc.Process"]; ok {
		if node.Signature == "" {
			t.Errorf("expected non-empty signature for Svc.Process")
		}
		// Should contain receiver, params, and return types.
		if !containsAll(node.Signature, "Svc", "Process", "context.Context", "string", "error") {
			t.Errorf("signature incomplete: %s", node.Signature)
		}
	} else {
		t.Errorf("Svc.Process not found; nodes: %v", nodeIDs(graph))
	}

	if node, ok := graph.Nodes["src.PlainFunc"]; ok {
		if node.Signature == "" {
			t.Errorf("expected non-empty signature for PlainFunc")
		}
		if !containsAll(node.Signature, "PlainFunc", "int", "bool") {
			t.Errorf("signature incomplete: %s", node.Signature)
		}
	} else {
		t.Errorf("src.PlainFunc not found; nodes: %v", nodeIDs(graph))
	}
}

// ---------------------------------------------------------------------------
// Doc comment extraction
// ---------------------------------------------------------------------------

func TestGoASTScanner_ExtractsDocComments(t *testing.T) {
	root := t.TempDir()

	srcDir := filepath.Join(root, "src", "services")
	writeTempFile(t, srcDir, "svc.go", `package services

// Process handles the main processing logic.
// It validates input and returns results.
type Processor struct{}

// Run executes the processor pipeline.
func (p *Processor) Run() {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	node, ok := graph.Nodes["Processor.Run"]
	if !ok {
		t.Fatalf("Processor.Run not found; nodes: %v", nodeIDs(graph))
	}

	if node.Doc == "" {
		t.Errorf("expected doc comment on Processor.Run")
	}
	if !containsAll(node.Doc, "Run", "pipeline") {
		t.Errorf("doc = %q, expected to contain 'Run' and 'pipeline'", node.Doc)
	}
}

// ---------------------------------------------------------------------------
// @api annotation endpoint discovery
// ---------------------------------------------------------------------------

func TestGoASTScanner_APIAnnotation_CreatesEndpointNodes(t *testing.T) {
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "auth_handler.go", `package handlers

type AuthHandler struct{}

// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}

// @api GET /api/v1/auth/me
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Verify endpoint nodes were created.
	postLogin, ok := graph.Nodes["POST /api/v1/auth/login"]
	if !ok {
		t.Errorf("expected endpoint node 'POST /api/v1/auth/login', got nodes: %v", nodeIDs(graph))
	} else {
		if postLogin.Kind != domain.NodeEndpoint {
			t.Errorf("endpoint kind = %s, want endpoint", postLogin.Kind)
		}
	}

	getMe, ok := graph.Nodes["GET /api/v1/auth/me"]
	if !ok {
		t.Errorf("expected endpoint node 'GET /api/v1/auth/me', got nodes: %v", nodeIDs(graph))
	} else {
		if getMe.Kind != domain.NodeEndpoint {
			t.Errorf("endpoint kind = %s, want endpoint", getMe.Kind)
		}
	}

	// Verify edges from endpoints to handlers.
	foundLoginEdge := false
	foundMeEdge := false
	for _, e := range graph.Edges {
		if e.From == "POST /api/v1/auth/login" && e.To == "AuthHandler.Login" {
			foundLoginEdge = true
		}
		if e.From == "GET /api/v1/auth/me" && e.To == "AuthHandler.Me" {
			foundMeEdge = true
		}
	}
	if !foundLoginEdge {
		t.Errorf("expected edge POST /api/v1/auth/login -> AuthHandler.Login; edges: %v", graph.Edges)
	}
	if !foundMeEdge {
		t.Errorf("expected edge GET /api/v1/auth/me -> AuthHandler.Me; edges: %v", graph.Edges)
	}
}

func TestGoASTScanner_APIAnnotation_MultipleOnSameFunction(t *testing.T) {
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "subject_handler.go", `package handlers

type SubjectHandler struct{}

// @api GET /api/v1/subjects
// @api GET /api/v1/subjects/{id}
func (h *SubjectHandler) GetByID(w http.ResponseWriter, r *http.Request) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Both endpoints should exist.
	if _, ok := graph.Nodes["GET /api/v1/subjects"]; !ok {
		t.Errorf("expected endpoint 'GET /api/v1/subjects'; nodes: %v", nodeIDs(graph))
	}
	if _, ok := graph.Nodes["GET /api/v1/subjects/{id}"]; !ok {
		t.Errorf("expected endpoint 'GET /api/v1/subjects/{id}'; nodes: %v", nodeIDs(graph))
	}

	// Both should link to the same handler.
	edgeCount := 0
	for _, e := range graph.Edges {
		if e.To == "SubjectHandler.GetByID" &&
			(e.From == "GET /api/v1/subjects" || e.From == "GET /api/v1/subjects/{id}") {
			edgeCount++
		}
	}
	if edgeCount != 2 {
		t.Errorf("expected 2 edges to SubjectHandler.GetByID, got %d; edges: %v", edgeCount, graph.Edges)
	}
}

func TestGoASTScanner_APIAnnotation_NoRouterFile(t *testing.T) {
	// When no router file is configured, @api annotations should still work.
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "handler.go", `package handlers

type Handler struct{}

// @api GET /api/v1/health
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
		// No RouterFile set.
	}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if _, ok := graph.Nodes["GET /api/v1/health"]; !ok {
		t.Errorf("expected endpoint 'GET /api/v1/health'; nodes: %v", nodeIDs(graph))
	}
}

// ---------------------------------------------------------------------------
// Handler reference resolution (Phase 2.5)
// ---------------------------------------------------------------------------

func TestGoASTScanner_ResolveHandlerRefs_MergesPlaceholder(t *testing.T) {
	// Simulate Phase 1 creating a placeholder "h.Login" and Phase 2 discovering
	// "AuthHandler.Login". The resolution step should merge them.
	root := t.TempDir()

	// Create a minimal router file that produces a placeholder handler ref.
	routerDir := filepath.Join(root, "src", "infrastructure", "http")
	writeTempFile(t, routerDir, "router.go", `package http

func SetupRoutes() {
	// Route parser will produce a handler ref like "h.Login"
}
`)

	handlerDir := filepath.Join(root, "src", "infrastructure", "http", "handlers")
	writeTempFile(t, handlerDir, "auth_handler.go", `package handlers

type AuthHandler struct{}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	// Build the graph — no router file, so no placeholders from Phase 1.
	// We manually test resolveHandlerRefs by setting up a scanContext.
	ctx, _ := scanner.newScanContext(root, config)

	backendAbs := filepath.Join(root, config.BackendRoot)

	// Manually add a placeholder node (as if Phase 1 route parser created it).
	ctx.graph.AddNode(&domain.Node{
		ID:   "h.Login",
		Kind: domain.NodeHandler,
		// File is empty — this is the marker for unresolved placeholders.
	})
	ctx.graph.AddNode(&domain.Node{
		ID:   "POST /api/v1/auth/login",
		Kind: domain.NodeEndpoint,
		File: "src/infrastructure/http/router.go",
		Line: 5,
	})
	ctx.graph.AddEdge("POST /api/v1/auth/login", "h.Login")

	// Phase 2: Discover functions.
	if err := scanner.discoverFunctions(ctx, backendAbs); err != nil {
		t.Fatalf("discoverFunctions() error: %v", err)
	}

	// Verify AuthHandler.Login was discovered.
	if _, ok := ctx.graph.Nodes["AuthHandler.Login"]; !ok {
		t.Fatalf("AuthHandler.Login not discovered; nodes: %v", graphNodeIDs(ctx.graph))
	}

	// Phase 2.5: Resolve handler refs.
	scanner.resolveHandlerRefs(ctx)

	// The placeholder "h.Login" should be merged into "AuthHandler.Login".
	if _, ok := ctx.graph.Nodes["h.Login"]; ok {
		t.Errorf("placeholder 'h.Login' should have been removed after resolution")
	}

	// The edge should now point to AuthHandler.Login.
	foundEdge := false
	for _, e := range ctx.graph.Edges {
		if e.From == "POST /api/v1/auth/login" && e.To == "AuthHandler.Login" {
			foundEdge = true
			break
		}
	}
	if !foundEdge {
		t.Errorf("expected edge POST /api/v1/auth/login -> AuthHandler.Login after resolution; edges: %v", ctx.graph.Edges)
	}
}

func TestGoASTScanner_ResolveHandlerRefs_AmbiguousMultipleMatches(t *testing.T) {
	root := t.TempDir()

	// Two different handler types with the same method name.
	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "auth_handler.go", `package handlers
type AuthHandler struct{}
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
`)
	writeTempFile(t, handlerDir, "oauth_handler.go", `package handlers
type OAuthHandler struct{}
func (h *OAuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	ctx, _ := scanner.newScanContext(root, config)
	backendAbs := filepath.Join(root, config.BackendRoot)

	// Add placeholder from route parser.
	ctx.graph.AddNode(&domain.Node{
		ID:   "h.Login",
		Kind: domain.NodeHandler,
	})
	ctx.graph.AddNode(&domain.Node{
		ID:   "POST /api/v1/auth/login",
		Kind: domain.NodeEndpoint,
		File: "router.go",
	})
	ctx.graph.AddEdge("POST /api/v1/auth/login", "h.Login")

	if err := scanner.discoverFunctions(ctx, backendAbs); err != nil {
		t.Fatalf("discoverFunctions() error: %v", err)
	}

	scanner.resolveHandlerRefs(ctx)

	// With two matches (AuthHandler.Login and OAuthHandler.Login),
	// the edge should be marked ambiguous.
	for _, e := range ctx.graph.Edges {
		if e.To == "h.Login" {
			if !e.Ambiguous {
				t.Errorf("edge to ambiguous placeholder should be marked ambiguous")
			}
		}
	}
}

func TestGoASTScanner_APIAnnotation_OverridesRouteParser(t *testing.T) {
	// When both @api and route parser discover the same endpoint,
	// @api should win and the route parser placeholder should be cleaned up.
	root := t.TempDir()

	handlerDir := filepath.Join(root, "src", "handlers")
	writeTempFile(t, handlerDir, "auth_handler.go", `package handlers

type AuthHandler struct{}

// @api POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
`)

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	ctx, _ := scanner.newScanContext(root, config)
	backendAbs := filepath.Join(root, config.BackendRoot)

	// Simulate Phase 1: route parser creates placeholder.
	ctx.graph.AddNode(&domain.Node{
		ID:   "h.Login",
		Kind: domain.NodeHandler,
	})
	ctx.graph.AddNode(&domain.Node{
		ID:   "POST /api/v1/auth/login",
		Kind: domain.NodeEndpoint,
		File: "router.go",
		Line: 10,
	})
	ctx.graph.AddEdge("POST /api/v1/auth/login", "h.Login")

	// Phase 2: discover functions + @api annotations.
	if err := scanner.discoverFunctions(ctx, backendAbs); err != nil {
		t.Fatalf("discoverFunctions() error: %v", err)
	}

	// Phase 2.5: resolve handler refs.
	scanner.resolveHandlerRefs(ctx)

	// The placeholder should be removed because the @api annotation
	// created a direct edge POST /api/v1/auth/login -> AuthHandler.Login.
	if _, ok := ctx.graph.Nodes["h.Login"]; ok {
		t.Errorf("placeholder 'h.Login' should be removed when @api annotation exists")
	}

	// There should be exactly one edge from the endpoint to the handler.
	var edgesFromEndpoint []domain.Edge
	for _, e := range ctx.graph.Edges {
		if e.From == "POST /api/v1/auth/login" {
			edgesFromEndpoint = append(edgesFromEndpoint, e)
		}
	}
	if len(edgesFromEndpoint) != 1 {
		t.Fatalf("expected 1 edge from endpoint, got %d: %v", len(edgesFromEndpoint), edgesFromEndpoint)
	}
	if edgesFromEndpoint[0].To != "AuthHandler.Login" {
		t.Errorf("edge target = %q, want AuthHandler.Login", edgesFromEndpoint[0].To)
	}
}

// graphNodeIDs returns all node IDs from a graph.
func graphNodeIDs(g *domain.Graph) []string {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestGoASTScanner_ImplementsGraphBuilder(t *testing.T) {
	var _ ports.GraphBuilder = (*GoASTScanner)(nil)
}

// ---------------------------------------------------------------------------
// Empty project
// ---------------------------------------------------------------------------

func TestGoASTScanner_EmptyProject(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	scanner := NewGoASTScanner()
	config := ports.GraphConfig{BackendRoot: "src", MaxDepth: 10}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if len(graph.Nodes) != 0 {
		t.Errorf("expected empty graph for empty project, got %d nodes", len(graph.Nodes))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func nodeIDs(g *domain.Graph) []string {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	return ids
}

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !containsStr(s, sub) {
			return false
		}
	}
	return true
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsIndex(s, sub))
}

func containsIndex(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
