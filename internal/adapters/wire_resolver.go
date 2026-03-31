package adapters

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// InterfaceMapping maps an interface type to its concrete implementation.
type InterfaceMapping struct {
	Interface    string // e.g. "repositories.UserRepository"
	Concrete     string // e.g. "persistence.PostgresUserRepository"
	ProviderFunc string // e.g. "NewPostgresUserRepository"
	File         string // where the provider is defined
}

// WireResolver parses Wire provider sets to resolve interface to concrete mappings.
type WireResolver struct{}

// NewWireResolver creates a new resolver.
func NewWireResolver() *WireResolver {
	return &WireResolver{}
}

// Resolve reads a wire.go file and returns interface to concrete mappings.
// It looks for wire.NewSet() calls and wire.Bind() calls.
//
// Patterns it handles:
//
//	wire.NewSet(NewFoo, wire.Bind(new(Interface), new(*Concrete)))
//	wire.NewSet(provider1, provider2, ...)
//	wire.Build(provider1, wire.Bind(...), ...)
//
// For providers without explicit Bind, it infers the interface from the
// return type of the provider function (if it returns an interface type).
// This requires parsing the provider function source files found via
// imports in the wire.go file.
func (r *WireResolver) Resolve(wireFilePath string) (map[string]InterfaceMapping, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, wireFilePath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing wire file %s: %w", wireFilePath, err)
	}

	result := make(map[string]InterfaceMapping)

	// Phase 1: Extract explicit wire.Bind() calls from NewSet/Build calls.
	bindMappings := r.extractBindCalls(node)
	for k, v := range bindMappings {
		v.File = wireFilePath
		result[k] = v
	}

	// Phase 2: Collect provider function names from NewSet/Build calls.
	providers := r.extractProviderNames(node)

	// Phase 3: Resolve provider function signatures by scanning the directory.
	// Provider functions are typically in sibling files (wire_services.go,
	// wire_repositories.go, etc.) within the same package.
	dir := filepath.Dir(wireFilePath)
	dirMappings := r.resolveProviderSignatures(dir, fset, providers, wireFilePath)
	for k, v := range dirMappings {
		if _, exists := result[k]; !exists {
			result[k] = v
		}
	}

	return result, nil
}

// ResolveFromSource reads wire source content directly (useful for testing
// without touching the filesystem for provider resolution). This only
// extracts explicit wire.Bind mappings and provider names from the source.
func (r *WireResolver) ResolveFromSource(src string, filename string) (map[string]InterfaceMapping, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	result := make(map[string]InterfaceMapping)

	// Extract explicit wire.Bind() calls.
	bindMappings := r.extractBindCalls(node)
	for k, v := range bindMappings {
		v.File = filename
		result[k] = v
	}

	return result, nil
}

// extractBindCalls walks the AST to find wire.Bind(new(Interface), new(*Concrete))
// calls inside wire.NewSet() and wire.Build() calls.
func (r *WireResolver) extractBindCalls(file *ast.File) map[string]InterfaceMapping {
	result := make(map[string]InterfaceMapping)

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if !isWireSetOrBuild(call) {
			return true
		}

		// Inspect arguments of wire.NewSet/wire.Build for wire.Bind calls.
		for _, arg := range call.Args {
			bindCall, ok := arg.(*ast.CallExpr)
			if !ok {
				continue
			}

			if !isWireBind(bindCall) {
				continue
			}

			iface, concrete := parseBindArgs(bindCall)
			if iface == "" || concrete == "" {
				continue
			}

			shortName := shortTypeName(iface)
			result[shortName] = InterfaceMapping{
				Interface: iface,
				Concrete:  concrete,
			}
		}

		return true
	})

	return result
}

// extractProviderNames collects all identifier arguments from wire.NewSet()
// and wire.Build() calls. These are the provider function names.
func (r *WireResolver) extractProviderNames(file *ast.File) []string {
	var providers []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if !isWireSetOrBuild(call) {
			return true
		}

		for _, arg := range call.Args {
			switch a := arg.(type) {
			case *ast.Ident:
				providers = append(providers, a.Name)
			case *ast.SelectorExpr:
				// pkg.FuncName — qualified provider reference.
				providers = append(providers, wireExprToString(a))
			}
		}

		return true
	})

	return providers
}

// resolveProviderSignatures scans Go files in dir for provider function
// definitions. When a function returns an interface type (identified by
// being a non-pointer named type from another package), it creates a mapping.
//
// This handles the common Wire pattern where provider functions return
// interface types:
//
//	func ProvideRecipeRepository(...) repositories.RecipeRepository {
//	    return persistence.NewPostgresRecipeRepository(...)
//	}
func (r *WireResolver) resolveProviderSignatures(dir string, existingFset *token.FileSet, providers []string, wireFilePath string) map[string]InterfaceMapping {
	result := make(map[string]InterfaceMapping)

	providerSet := make(map[string]bool, len(providers))
	for _, p := range providers {
		providerSet[p] = true
	}

	goFiles, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return result
	}

	for _, goFile := range goFiles {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, goFile, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				// Skip methods — only look at package-level functions.
				continue
			}

			if !providerSet[fn.Name.Name] {
				continue
			}

			mapping := r.analyzeProviderFunc(fn, goFile)
			if mapping != nil {
				shortName := shortTypeName(mapping.Interface)
				result[shortName] = *mapping
			}
		}
	}

	return result
}

// analyzeProviderFunc inspects a provider function's return type and body
// to determine what interface it satisfies and what concrete type it returns.
//
// A provider function qualifies when:
//  1. It has exactly one return type (or the first of multiple)
//  2. The return type is a qualified identifier (pkg.Type) indicating an
//     interface from another package
//  3. Its body contains a return statement that calls a constructor
func (r *WireResolver) analyzeProviderFunc(fn *ast.FuncDecl, file string) *InterfaceMapping {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return nil
	}

	// Get the return type.
	returnField := fn.Type.Results.List[0]
	returnType := typeExprToString(returnField.Type)
	if returnType == "" {
		return nil
	}

	// Only consider qualified types (pkg.Type) as interfaces.
	// Pointer types (*pkg.Type or *Type) are concrete implementations.
	if isPointerType(returnField.Type) {
		return nil
	}

	// Must be a selector expression (pkg.Interface) to be an interface.
	if _, ok := returnField.Type.(*ast.SelectorExpr); !ok {
		return nil
	}

	// Extract the concrete type from the function body.
	concrete := r.extractConcreteFromBody(fn.Body)
	providerFunc := ""
	if concrete != "" {
		// The provider func name is typically the constructor being called.
		providerFunc = r.extractConstructorName(fn.Body)
	}

	return &InterfaceMapping{
		Interface:    returnType,
		Concrete:     concrete,
		ProviderFunc: providerFunc,
		File:         file,
	}
}

// extractConcreteFromBody looks at return statements in the function body
// to determine what concrete type is being returned. It looks for patterns:
//
//	return persistence.NewFoo(...)   // constructor call returning concrete
//	return services.NewBar(...)
func (r *WireResolver) extractConcreteFromBody(body *ast.BlockStmt) string {
	if body == nil {
		return ""
	}

	var concrete string
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}

		for _, result := range ret.Results {
			call, ok := result.(*ast.CallExpr)
			if !ok {
				continue
			}

			funcName := wireExprToString(call.Fun)
			if funcName == "" {
				continue
			}

			// The constructor name typically starts with "New" and the
			// concrete type name is derived from it (e.g. NewPostgresUserRepo
			// creates *PostgresUserRepo). We record the package-qualified
			// constructor; the caller can derive the type from naming
			// conventions.
			concrete = constructorToConcrete(funcName)
			return false
		}
		return true
	})

	return concrete
}

// extractConstructorName extracts the constructor function name from the body.
func (r *WireResolver) extractConstructorName(body *ast.BlockStmt) string {
	if body == nil {
		return ""
	}

	var name string
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}

		for _, result := range ret.Results {
			call, ok := result.(*ast.CallExpr)
			if !ok {
				continue
			}

			name = wireExprToString(call.Fun)
			return false
		}
		return true
	})

	return name
}

// isWireSetOrBuild checks whether a call expression is wire.NewSet(...) or
// wire.Build(...).
func isWireSetOrBuild(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	return ident.Name == "wire" && (sel.Sel.Name == "NewSet" || sel.Sel.Name == "Build")
}

// isWireBind checks whether a call expression is wire.Bind(...).
func isWireBind(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	return ident.Name == "wire" && sel.Sel.Name == "Bind"
}

// parseBindArgs extracts the interface and concrete types from a wire.Bind
// call: wire.Bind(new(Interface), new(*Concrete))
//
// Returns (interfaceType, concreteType) or ("", "") if parsing fails.
func parseBindArgs(call *ast.CallExpr) (string, string) {
	if len(call.Args) < 2 {
		return "", ""
	}

	iface := extractNewArg(call.Args[0])
	concrete := extractNewArg(call.Args[1])

	return iface, concrete
}

// extractNewArg extracts the type from a new(Type) or new(*Type) expression.
func extractNewArg(expr ast.Expr) string {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return ""
	}

	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return ""
	}

	if ident.Name != "new" {
		return ""
	}

	if len(call.Args) != 1 {
		return ""
	}

	return typeExprToString(call.Args[0])
}

// typeExprToString converts a type expression to its string representation.
func typeExprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		pkg := wireExprToString(e.X)
		return pkg + "." + e.Sel.Name
	case *ast.StarExpr:
		inner := typeExprToString(e.X)
		if inner == "" {
			return ""
		}
		return "*" + inner
	}
	return ""
}

// wireExprToString converts an expression to a string representation for
// Wire-specific contexts (function names, package references).
func wireExprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		pkg := wireExprToString(e.X)
		return pkg + "." + e.Sel.Name
	}
	return ""
}

// shortTypeName returns the short form of a type name, stripping the package
// prefix and any pointer stars. "repositories.UserRepository" becomes
// "UserRepository", "*persistence.PostgresUserRepository" becomes
// "PostgresUserRepository".
func shortTypeName(fullType string) string {
	// Remove pointer star.
	name := strings.TrimPrefix(fullType, "*")

	// Take the part after the last dot.
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// isPointerType checks whether a type expression is a pointer type (*T).
func isPointerType(expr ast.Expr) bool {
	_, ok := expr.(*ast.StarExpr)
	return ok
}

// constructorToConcrete converts a constructor function name to the concrete
// type it likely creates.
//
// Examples:
//
//	"persistence.NewPostgresUserRepository" -> "persistence.PostgresUserRepository"
//	"services.NewMealLogService"            -> "services.MealLogService"
//	"auth.NewJWTValidator"                  -> "auth.JWTValidator"
//	"NewFoo"                                -> "Foo"
func constructorToConcrete(constructor string) string {
	// Split on last dot to separate package from function name.
	pkg := ""
	funcName := constructor
	if idx := strings.LastIndex(constructor, "."); idx >= 0 {
		pkg = constructor[:idx]
		funcName = constructor[idx+1:]
	}

	// Strip "New" prefix to get the type name.
	typeName := strings.TrimPrefix(funcName, "New")
	if typeName == "" {
		typeName = funcName
	}

	if pkg != "" {
		return pkg + "." + typeName
	}
	return typeName
}
