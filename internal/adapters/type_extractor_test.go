// @testreg trace.type-extractor
package adapters

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/sosalejandro/testreg/internal/ports"
)

func loadTestPackages(t *testing.T, dir string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo |
			packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("packages.Load failed: %v", err)
	}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			t.Fatalf("package %s has errors: %v", pkg.PkgPath, pkg.Errors)
		}
	}
	return pkgs
}

func TestTypeExtractor_ExtractStructFields(t *testing.T) {
	root := t.TempDir()

	writeTempFile(t, root, "go.mod", `module example.com/extract
go 1.21
`)

	writeTempFile(t, root, "models.go", `package extract

type User struct {
	ID    int
	Name  string
	Email string
	age   int  // unexported — should be skipped
}
`)

	pkgs := loadTestPackages(t, root)
	extractor := NewTypeExtractor(pkgs)

	fields := extractor.ExtractStructFields("User")
	if len(fields) != 3 {
		t.Fatalf("expected 3 exported fields, got %d: %+v", len(fields), fields)
	}

	// All non-pointer fields should be required.
	for _, f := range fields {
		if !f.Required {
			t.Errorf("field %s should be required (non-pointer)", f.Name)
		}
	}

	// Check expected field names.
	names := map[string]bool{}
	for _, f := range fields {
		names[f.Name] = true
	}
	for _, want := range []string{"ID", "Name", "Email"} {
		if !names[want] {
			t.Errorf("expected field %s not found", want)
		}
	}
}

func TestTypeExtractor_ExtractStructFieldsWithPointers(t *testing.T) {
	root := t.TempDir()

	writeTempFile(t, root, "go.mod", `module example.com/extract
go 1.21
`)

	writeTempFile(t, root, "models.go", `package extract

type Input struct {
	SessionID string
	Weight    *float64
	Reps      *int
}
`)

	pkgs := loadTestPackages(t, root)
	extractor := NewTypeExtractor(pkgs)

	fields := extractor.ExtractStructFields("Input")
	if len(fields) != 3 {
		t.Fatalf("expected 3 fields, got %d: %+v", len(fields), fields)
	}

	for _, f := range fields {
		switch f.Name {
		case "SessionID":
			if !f.Required {
				t.Error("SessionID should be required")
			}
		case "Weight":
			if f.Required {
				t.Error("Weight (*float64) should NOT be required")
			}
		case "Reps":
			if f.Required {
				t.Error("Reps (*int) should NOT be required")
			}
		default:
			t.Errorf("unexpected field: %s", f.Name)
		}
	}
}

func TestTypeExtractor_ExtractFuncSignatureWithStructParams(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")

	writeTempFile(t, srcDir, "go.mod", `module example.com/extract
go 1.21
`)

	writeTempFile(t, srcDir, "service.go", `package src

type CreateInput struct {
	Name  string
	Email string
	Age   *int
}

type CreateOutput struct {
	ID    int
	Name  string
	Email string
}

type Service struct{}

func (s *Service) Create(input CreateInput) (*CreateOutput, error) {
	return &CreateOutput{ID: 1, Name: input.Name, Email: input.Email}, nil
}
`)

	pkgs := loadTestPackages(t, srcDir)
	extractor := NewTypeExtractor(pkgs)

	fc, err := extractor.ExtractFuncSignature("Service.Create")
	if err != nil {
		t.Fatalf("ExtractFuncSignature error: %v", err)
	}
	if fc == nil {
		t.Fatal("expected non-nil FuncContract")
	}

	// Should have 1 param (CreateInput struct).
	if len(fc.Params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(fc.Params))
	}
	p := fc.Params[0]
	if !p.IsStruct {
		t.Error("param should be a struct")
	}
	if p.TypeStr != "CreateInput" {
		t.Errorf("param type = %q, want CreateInput", p.TypeStr)
	}
	if len(p.Fields) != 3 {
		t.Fatalf("expected 3 fields in CreateInput, got %d", len(p.Fields))
	}

	// Should have 2 returns (*CreateOutput, error).
	if len(fc.Returns) != 2 {
		t.Fatalf("expected 2 returns, got %d", len(fc.Returns))
	}
	r := fc.Returns[0]
	if !r.IsStruct {
		t.Error("first return should be a struct (pointer to struct)")
	}
	if r.TypeStr != "CreateOutput" {
		t.Errorf("return type = %q, want CreateOutput", r.TypeStr)
	}

	// Second return should be error (not a struct).
	if fc.Returns[1].IsStruct {
		t.Error("error return should not be a struct")
	}
}

func TestTypeExtractor_ExtractFuncSignatureSkipsContextParam(t *testing.T) {
	root := t.TempDir()

	writeTempFile(t, root, "go.mod", `module example.com/extract
go 1.21
`)

	writeTempFile(t, root, "handler.go", `package extract

import "context"

type Input struct {
	Value string
}

type Handler struct{}

func (h *Handler) Handle(ctx context.Context, input Input) error {
	return nil
}
`)

	pkgs := loadTestPackages(t, root)
	extractor := NewTypeExtractor(pkgs)

	fc, err := extractor.ExtractFuncSignature("Handler.Handle")
	if err != nil {
		t.Fatalf("ExtractFuncSignature error: %v", err)
	}
	if fc == nil {
		t.Fatal("expected non-nil FuncContract")
	}

	// Should have 2 params: context.Context and Input.
	if len(fc.Params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(fc.Params))
	}

	// First param is context.Context — should NOT be a struct.
	if fc.Params[0].IsStruct {
		t.Error("context.Context should not be marked as a struct")
	}

	// Second param is Input — should be a struct.
	if !fc.Params[1].IsStruct {
		t.Error("Input should be a struct")
	}
	if fc.Params[1].TypeStr != "Input" {
		t.Errorf("second param type = %q, want Input", fc.Params[1].TypeStr)
	}
}

func TestTypeExtractor_ExtractFuncSignatureNotFound(t *testing.T) {
	root := t.TempDir()

	writeTempFile(t, root, "go.mod", `module example.com/extract
go 1.21
`)

	writeTempFile(t, root, "main.go", `package extract

func Hello() {}
`)

	pkgs := loadTestPackages(t, root)
	extractor := NewTypeExtractor(pkgs)

	fc, err := extractor.ExtractFuncSignature("Nonexistent.Method")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fc != nil {
		t.Fatal("expected nil FuncContract for nonexistent function")
	}
}

func TestTypeExtractor_CachedPackagesFromTypedScanner(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")

	writeTempFile(t, srcDir, "go.mod", `module example.com/test
go 1.21
`)

	writeTempFile(t, srcDir, "svc.go", `package src

type Svc struct{}

func (s *Svc) Do() string { return "ok" }
`)

	scanner := NewTypedScanner()
	config := ports.GraphConfig{
		BackendRoot: "src",
		MaxDepth:    10,
	}

	// Build populates the cache.
	_, err := scanner.Build(root, config)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	pkgs := scanner.LoadedPackages()
	if pkgs == nil {
		t.Fatal("expected cached packages after Build, got nil")
	}

	// Create extractor from cached packages.
	extractor := NewTypeExtractor(pkgs)
	fc, err := extractor.ExtractFuncSignature("Svc.Do")
	if err != nil {
		t.Fatalf("ExtractFuncSignature error: %v", err)
	}
	if fc == nil {
		t.Fatal("expected to find Svc.Do via cached packages")
	}
}
