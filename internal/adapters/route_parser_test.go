// @testreg trace.route-parser
package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGoRouterFile writes a temporary .go file with the given source content.
func writeGoRouterFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRouteParser_DirectRoutes(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type AuthHandler struct{}

func (h *AuthHandler) RegisterAuthRoutes(router *chi.Mux) {
	router.Post("/api/v1/auth/login", h.Login)
	router.Post("/api/v1/auth/register", h.Register)
	router.Get("/api/v1/auth/profile", h.GetProfile)
	router.Delete("/api/v1/auth/session", h.Logout)
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {}
func (h *AuthHandler) GetProfile(w http.ResponseWriter, r *http.Request) {}
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	expected := []struct {
		method  string
		path    string
		handler string
	}{
		{"POST", "/api/v1/auth/login", "h.Login"},
		{"POST", "/api/v1/auth/register", "h.Register"},
		{"GET", "/api/v1/auth/profile", "h.GetProfile"},
		{"DELETE", "/api/v1/auth/session", "h.Logout"},
	}

	if len(routes) != len(expected) {
		t.Fatalf("expected %d routes, got %d: %+v", len(expected), len(routes), routes)
	}

	for i, want := range expected {
		got := routes[i]
		if got.Method != want.method {
			t.Errorf("route[%d] Method: got %q, want %q", i, got.Method, want.method)
		}
		if got.Path != want.path {
			t.Errorf("route[%d] Path: got %q, want %q", i, got.Path, want.path)
		}
		if got.Handler != want.handler {
			t.Errorf("route[%d] Handler: got %q, want %q", i, got.Handler, want.handler)
		}
		if got.File != path {
			t.Errorf("route[%d] File: got %q, want %q", i, got.File, path)
		}
	}
}

func TestRouteParser_NestedRoute(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type OrgHandler struct{}

func (h *OrgHandler) RegisterRoutes(router *chi.Mux) {
	router.Route("/api/v1/organizations", func(r chi.Router) {
		r.Get("/", h.ListOrganizations)
		r.Post("/", h.CreateOrganization)
		r.Post("/{id}/transfer-ownership", h.TransferOwnership)
	})
}

func (h *OrgHandler) ListOrganizations(w http.ResponseWriter, r *http.Request) {}
func (h *OrgHandler) CreateOrganization(w http.ResponseWriter, r *http.Request) {}
func (h *OrgHandler) TransferOwnership(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	expected := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/organizations"},
		{"POST", "/api/v1/organizations"},
		{"POST", "/api/v1/organizations/{id}/transfer-ownership"},
	}

	if len(routes) != len(expected) {
		t.Fatalf("expected %d routes, got %d: %+v", len(expected), len(routes), routes)
	}

	for i, want := range expected {
		got := routes[i]
		if got.Method != want.method {
			t.Errorf("route[%d] Method: got %q, want %q", i, got.Method, want.method)
		}
		if got.Path != want.path {
			t.Errorf("route[%d] Path: got %q, want %q", i, got.Path, want.path)
		}
	}
}

func TestRouteParser_GroupRoutes(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type Handler struct{}

func (h *Handler) RegisterRoutes(router *chi.Mux) {
	router.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Get("/users", h.ListUsers)
			r.Post("/users", h.CreateUser)
		})
	})
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	expected := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/users"},
		{"POST", "/api/v1/users"},
	}

	if len(routes) != len(expected) {
		t.Fatalf("expected %d routes, got %d: %+v", len(expected), len(routes), routes)
	}

	for i, want := range expected {
		got := routes[i]
		if got.Method != want.method {
			t.Errorf("route[%d] Method: got %q, want %q", i, got.Method, want.method)
		}
		if got.Path != want.path {
			t.Errorf("route[%d] Path: got %q, want %q", i, got.Path, want.path)
		}
	}
}

func TestRouteParser_WithMiddleware(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type ReceiptHandler struct{}

func rateLimitMiddleware(next http.Handler) http.Handler { return next }

func (h *ReceiptHandler) RegisterRoutes(router *chi.Mux) {
	router.Route("/api/v1/receipts", func(r chi.Router) {
		r.With(rateLimitMiddleware).Post("/upload", h.UploadReceipt)
		r.With(rateLimitMiddleware).Post("/manual", h.ManualEntry)
		r.Get("/", h.ListReceipts)
		r.Get("/{id}", h.GetReceipt)
	})
}

func (h *ReceiptHandler) UploadReceipt(w http.ResponseWriter, r *http.Request) {}
func (h *ReceiptHandler) ManualEntry(w http.ResponseWriter, r *http.Request) {}
func (h *ReceiptHandler) ListReceipts(w http.ResponseWriter, r *http.Request) {}
func (h *ReceiptHandler) GetReceipt(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	expected := []struct {
		method string
		path   string
	}{
		{"POST", "/api/v1/receipts/upload"},
		{"POST", "/api/v1/receipts/manual"},
		{"GET", "/api/v1/receipts"},
		{"GET", "/api/v1/receipts/{id}"},
	}

	if len(routes) != len(expected) {
		t.Fatalf("expected %d routes, got %d: %+v", len(expected), len(routes), routes)
	}

	for i, want := range expected {
		got := routes[i]
		if got.Method != want.method {
			t.Errorf("route[%d] Method: got %q, want %q", i, got.Method, want.method)
		}
		if got.Path != want.path {
			t.Errorf("route[%d] Path: got %q, want %q", i, got.Path, want.path)
		}
	}
}

func TestRouteParser_WithMiddlewareInsideConditional(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type Handler struct{}

func authMiddleware(next http.Handler) http.Handler { return next }

func (h *Handler) RegisterRoutes(router *chi.Mux) {
	router.Route("/api/v1/items", func(r chi.Router) {
		if true {
			r.With(authMiddleware).Post("/create", h.Create)
		} else {
			r.Post("/create", h.Create)
		}
		r.Get("/", h.List)
	})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	// Both branches of the if/else register the same route; we expect both.
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d: %+v", len(routes), routes)
	}

	// First: from the if branch (With middleware).
	if routes[0].Path != "/api/v1/items/create" || routes[0].Method != "POST" {
		t.Errorf("route[0]: got %s %s, want POST /api/v1/items/create", routes[0].Method, routes[0].Path)
	}
	// Second: from the else branch (direct).
	if routes[1].Path != "/api/v1/items/create" || routes[1].Method != "POST" {
		t.Errorf("route[1]: got %s %s, want POST /api/v1/items/create", routes[1].Method, routes[1].Path)
	}
	// Third: the Get outside the conditional.
	if routes[2].Path != "/api/v1/items" || routes[2].Method != "GET" {
		t.Errorf("route[2]: got %s %s, want GET /api/v1/items", routes[2].Method, routes[2].Path)
	}
}

func TestRouteParser_DoubleNestedRoute(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type Handler struct{}

func (h *Handler) RegisterRoutes(router *chi.Mux) {
	router.Route("/api/v1", func(r chi.Router) {
		r.Route("/users", func(r chi.Router) {
			r.Get("/", h.ListUsers)
			r.Route("/{userId}", func(r chi.Router) {
				r.Get("/", h.GetUser)
				r.Put("/", h.UpdateUser)
				r.Delete("/", h.DeleteUser)
			})
		})
	})
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	expected := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/users"},
		{"GET", "/api/v1/users/{userId}"},
		{"PUT", "/api/v1/users/{userId}"},
		{"DELETE", "/api/v1/users/{userId}"},
	}

	if len(routes) != len(expected) {
		t.Fatalf("expected %d routes, got %d: %+v", len(expected), len(routes), routes)
	}

	for i, want := range expected {
		got := routes[i]
		if got.Method != want.method {
			t.Errorf("route[%d] Method: got %q, want %q", i, got.Method, want.method)
		}
		if got.Path != want.path {
			t.Errorf("route[%d] Path: got %q, want %q", i, got.Path, want.path)
		}
	}
}

func TestRouteParser_AllHTTPMethods(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type Handler struct{}

func (h *Handler) RegisterRoutes(router *chi.Mux) {
	router.Get("/get", h.HandleGet)
	router.Post("/post", h.HandlePost)
	router.Put("/put", h.HandlePut)
	router.Delete("/delete", h.HandleDelete)
	router.Patch("/patch", h.HandlePatch)
	router.Head("/head", h.HandleHead)
	router.Options("/options", h.HandleOptions)
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) HandlePost(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) HandlePut(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) HandlePatch(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) HandleHead(w http.ResponseWriter, r *http.Request) {}
func (h *Handler) HandleOptions(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	expectedMethods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

	if len(routes) != len(expectedMethods) {
		t.Fatalf("expected %d routes, got %d", len(expectedMethods), len(routes))
	}

	for i, wantMethod := range expectedMethods {
		if routes[i].Method != wantMethod {
			t.Errorf("route[%d] Method: got %q, want %q", i, routes[i].Method, wantMethod)
		}
	}
}

func TestRouteParser_HandlerStringRepresentations(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	auth AuthHandler
}

type AuthHandler struct{}

func StandaloneHandler(w http.ResponseWriter, r *http.Request) {}

func (h *Handler) RegisterRoutes(router *chi.Mux) {
	router.Get("/standalone", StandaloneHandler)
	router.Get("/method", h.Method)
	router.Get("/nested", h.auth.Login)
}

func (h *Handler) Method(w http.ResponseWriter, r *http.Request) {}
func (a *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d: %+v", len(routes), routes)
	}

	if routes[0].Handler != "StandaloneHandler" {
		t.Errorf("route[0] Handler: got %q, want %q", routes[0].Handler, "StandaloneHandler")
	}
	if routes[1].Handler != "h.Method" {
		t.Errorf("route[1] Handler: got %q, want %q", routes[1].Handler, "h.Method")
	}
	if routes[2].Handler != "h.auth.Login" {
		t.Errorf("route[2] Handler: got %q, want %q", routes[2].Handler, "h.auth.Login")
	}
}

func TestRouteParser_EmptyFile(t *testing.T) {
	t.Parallel()

	src := `package handlers
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	if len(routes) != 0 {
		t.Errorf("expected 0 routes for empty file, got %d", len(routes))
	}
}

func TestRouteParser_InvalidGoFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.go")
	if err := os.WriteFile(path, []byte("this is not valid go code"), 0o644); err != nil {
		t.Fatal(err)
	}

	parser := NewRouteParser()
	_, err := parser.Parse(path)
	if err == nil {
		t.Fatal("expected error for invalid Go file")
	}
}

func TestRouteParser_InlineFuncHandler(t *testing.T) {
	t.Parallel()

	src := `package handlers

import (
	"net/http"
	"github.com/go-chi/chi/v5"
)

func RegisterRoutes(router *chi.Mux) {
	router.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
}
`
	path := writeGoRouterFile(t, src)
	parser := NewRouteParser()
	routes, err := parser.Parse(path)
	if err != nil {
		t.Fatalf("Parse() returned unexpected error: %v", err)
	}

	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}

	if routes[0].Method != "GET" {
		t.Errorf("Method: got %q, want GET", routes[0].Method)
	}
	if routes[0].Path != "/health" {
		t.Errorf("Path: got %q, want /health", routes[0].Path)
	}
	if routes[0].Handler != "<func>" {
		t.Errorf("Handler: got %q, want <func>", routes[0].Handler)
	}
}

func TestJoinPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		prefix string
		path   string
		want   string
	}{
		{"", "/api/v1", "/api/v1"},
		{"/api/v1", "/", "/api/v1"},
		{"/api/v1", "", "/api/v1"},
		{"/api/v1", "/users", "/api/v1/users"},
		{"/api/v1/", "/users", "/api/v1/users"},
		{"/api/v1", "/{id}", "/api/v1/{id}"},
		{"/api/v1/organizations", "/{id}/transfer-ownership", "/api/v1/organizations/{id}/transfer-ownership"},
	}

	for _, tt := range tests {
		got := joinPath(tt.prefix, tt.path)
		if got != tt.want {
			t.Errorf("joinPath(%q, %q) = %q, want %q", tt.prefix, tt.path, got, tt.want)
		}
	}
}
