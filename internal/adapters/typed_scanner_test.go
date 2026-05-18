// @testreg trace.typed-scanner
package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/internal/ports"
)

func TestTypedScanner_NewCreatesWithFallback(t *testing.T) {
	scanner := NewTypedScanner()
	if scanner == nil {
		t.Fatal("NewTypedScanner() returned nil")
	}
	if scanner.fallback == nil {
		t.Fatal("fallback GoASTScanner is nil")
	}
	if scanner.frontendScanner == nil {
		t.Fatal("frontendScanner is nil")
	}
	if scanner.sqlcMapper == nil {
		t.Fatal("sqlcMapper is nil")
	}
}

func TestTypedScanner_FallsBackOnInvalidProject(t *testing.T) {
	// Create a temp dir with an invalid go.mod — packages.Load will fail.
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a broken go.mod.
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("this is not valid"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a Go file so there's something to scan.
	writeTempFile(t, srcDir, "main.go", `package main

func Hello() string {
	return "hello"
}
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	// Build should succeed by falling back to GoASTScanner.
	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() should fall back, got error: %v", err)
	}

	// The fallback GoASTScanner should have found the function.
	if len(graph.Nodes) == 0 {
		t.Fatal("expected nodes from fallback scanner, got 0")
	}
}

func TestTypedScanner_BuildProducesNodes(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")

	// Write a valid go.mod.
	writeTempFile(t, srcDir, "go.mod", `module example.com/test

go 1.21
`)

	// Write a service package with a struct and methods.
	svcDir := filepath.Join(srcDir, "service")
	writeTempFile(t, svcDir, "user.go", `package service

// UserService handles user operations.
type UserService struct{}

// GetUser retrieves a user by ID.
func (s *UserService) GetUser(id int) string {
	return "user"
}

// ListUsers returns all users.
func (s *UserService) ListUsers() []string {
	return nil
}
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should have found both methods.
	if _, ok := graph.Nodes["UserService.GetUser"]; !ok {
		t.Error("expected node UserService.GetUser, not found")
		t.Logf("nodes: %v", nodeIDs(graph))
	}
	if _, ok := graph.Nodes["UserService.ListUsers"]; !ok {
		t.Error("expected node UserService.ListUsers, not found")
		t.Logf("nodes: %v", nodeIDs(graph))
	}
}

func TestTypedScanner_ResolvesIntraPackageCalls(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")

	writeTempFile(t, srcDir, "go.mod", `module example.com/test

go 1.21
`)

	svcDir := filepath.Join(srcDir, "service")
	writeTempFile(t, svcDir, "auth.go", `package service

type AuthService struct{}

func (s *AuthService) Login(email string) bool {
	return s.validate(email)
}

func (s *AuthService) validate(email string) bool {
	return email != ""
}
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should have an edge from Login to validate.
	found := false
	for _, e := range graph.Edges {
		if e.From == "AuthService.Login" && e.To == "AuthService.validate" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected edge AuthService.Login -> AuthService.validate, not found")
		t.Logf("edges: %v", graph.Edges)
	}
}

func TestTypedScanner_ResolvesCrossPackageCalls(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")

	writeTempFile(t, srcDir, "go.mod", `module example.com/test

go 1.21
`)

	// Package A calls Package B.
	writeTempFile(t, filepath.Join(srcDir, "handler"), "handler.go", `package handler

import "example.com/test/service"

type Handler struct {
	svc *service.UserService
}

func (h *Handler) HandleGet() string {
	return h.svc.GetUser(1)
}
`)

	writeTempFile(t, filepath.Join(srcDir, "service"), "user.go", `package service

type UserService struct{}

func (s *UserService) GetUser(id int) string {
	return "user"
}
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	graph, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	// Should resolve the cross-package call Handler.HandleGet -> UserService.GetUser.
	found := false
	for _, e := range graph.Edges {
		if e.From == "Handler.HandleGet" && e.To == "UserService.GetUser" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected edge Handler.HandleGet -> UserService.GetUser, not found")
		t.Logf("nodes: %v", nodeIDs(graph))
		t.Logf("edges: %v", graph.Edges)
	}

	// None of the edges should be ambiguous (go/types resolves exactly).
	for _, e := range graph.Edges {
		if e.Ambiguous {
			t.Errorf("edge %s -> %s is ambiguous, expected exact resolution", e.From, e.To)
		}
	}
}

func TestTypedScanner_BuildFromPrunesUnreachable(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")

	writeTempFile(t, srcDir, "go.mod", `module example.com/test

go 1.21
`)

	svcDir := filepath.Join(srcDir, "service")
	writeTempFile(t, svcDir, "svc.go", `package service

type Svc struct{}

func (s *Svc) Reachable() string {
	return s.helper()
}

func (s *Svc) helper() string {
	return "ok"
}

func (s *Svc) Unreachable() string {
	return "should be pruned"
}
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	graph, err := scanner.BuildFrom(root, []string{"Svc.Reachable"}, config)
	if err != nil {
		t.Fatalf("BuildFrom() error: %v", err)
	}

	// Reachable and helper should be present.
	if _, ok := graph.Nodes["Svc.Reachable"]; !ok {
		t.Error("expected Svc.Reachable in pruned graph")
		t.Logf("nodes: %v", nodeIDs(graph))
	}
	if _, ok := graph.Nodes["Svc.helper"]; !ok {
		t.Error("expected Svc.helper in pruned graph")
		t.Logf("nodes: %v", nodeIDs(graph))
	}

	// Unreachable should be pruned.
	if _, ok := graph.Nodes["Svc.Unreachable"]; ok {
		t.Error("Svc.Unreachable should have been pruned from the graph")
	}
}

func TestTypedScanner_BuildFromFallsBack(t *testing.T) {
	// Verify BuildFrom also falls back on invalid project.
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeTempFile(t, srcDir, "go.mod", "not a valid go.mod")
	writeTempFile(t, srcDir, "main.go", `package main

func Target() {}
func Other() {}
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	graph, err := scanner.BuildFrom(root, []string{"main.Target"}, config)
	if err != nil {
		t.Fatalf("BuildFrom() should fall back, got error: %v", err)
	}

	// Fallback scanner should have produced some result.
	if graph == nil {
		t.Fatal("expected non-nil graph from fallback")
	}
}

