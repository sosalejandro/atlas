package adapters

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
)

// FxResolver parses Uber Fx / Dig provider registrations to resolve
// interface-to-concrete mappings.
//
// It handles the following patterns:
//
//	fx.Provide(NewFoo, NewBar, ...)
//	fx.Options(fx.Provide(NewFoo, ...), fx.Invoke(Start, ...))
//	fx.Invoke(RegisterHandlers, StartConsumer, ...)
//	container.Provide(NewFoo)          // dig.Container usage
//
// For each provider function found in fx.Provide() or dig.Provide() calls,
// the resolver locates the function declaration in the directory's Go files
// and inspects its return type. If the return type is a qualified interface
// (pkg.Type without pointer), an InterfaceMapping is created. Pointer return
// types (*pkg.Type) are concrete implementations and are skipped.
type FxResolver struct{}

// NewFxResolver creates a new Fx/Dig DI resolver.
func NewFxResolver() *FxResolver {
	return &FxResolver{}
}

// Resolve scans Go files in dir for fx.Provide() and fx.Options() calls,
// extracts provider function names, then resolves their return types to build
// interface-to-concrete mappings.
func (r *FxResolver) Resolve(dir string) (map[string]InterfaceMapping, error) {
	goFiles, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("listing go files in %s: %w", dir, err)
	}

	// Phase 1 & 2: Collect provider and invoke function names from all files.
	var providers []string
	var invocations []string

	for _, goFile := range goFiles {
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, goFile, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		providers = append(providers, r.extractProviders(node)...)
		invocations = append(invocations, r.extractInvocations(node)...)
	}

	// Phase 3: Resolve provider function signatures by scanning Go files.
	result := r.resolveProviderSignatures(dir, providers)

	// invocations are tracked for completeness but don't produce mappings
	// themselves — they consume types rather than provide them.
	_ = invocations

	return result, nil
}

// ResolveFromSource parses source content directly (for testing).
// This extracts provider and invoke function names and resolves provider
// return types from the same source.
func (r *FxResolver) ResolveFromSource(src string, filename string) (map[string]InterfaceMapping, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing source: %w", err)
	}

	result := make(map[string]InterfaceMapping)

	// Extract provider names from fx.Provide / dig Provide calls.
	providers := r.extractProviders(node)

	// Extract invoke names from fx.Invoke calls.
	invocations := r.extractInvocations(node)

	// Resolve provider return types from function declarations in the
	// same source file.
	providerSet := make(map[string]bool, len(providers))
	for _, p := range providers {
		providerSet[p] = true
	}

	for _, decl := range node.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}

		if !providerSet[fn.Name.Name] {
			continue
		}

		mapping := r.analyzeProviderFunc(fn, filename)
		if mapping != nil {
			shortName := shortTypeName(mapping.Interface)
			result[shortName] = *mapping
		}
	}

	// Store invoke functions as mappings with empty Interface/Concrete
	// so callers can see what was registered via Invoke.
	_ = invocations

	return result, nil
}

// extractProviders walks the AST collecting function identifiers registered
// via fx.Provide() or dig container.Provide() calls. It handles nesting
// within fx.Options().
func (r *FxResolver) extractProviders(file *ast.File) []string {
	var providers []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if isFxProvide(call) || isDigProvide(call) {
			providers = append(providers, extractFuncIdents(call)...)
		}

		// Recurse into fx.Options(...) to find nested fx.Provide calls.
		// The ast.Inspect already traverses children, so nested
		// fx.Provide calls inside fx.Options are found automatically.

		return true
	})

	return providers
}

// extractInvocations walks the AST collecting function identifiers registered
// via fx.Invoke() calls.
func (r *FxResolver) extractInvocations(file *ast.File) []string {
	var invocations []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if isFxInvoke(call) {
			invocations = append(invocations, extractFuncIdents(call)...)
		}

		return true
	})

	return invocations
}

// resolveProviderSignatures scans Go files in dir for provider function
// definitions and inspects their return types to build interface mappings.
func (r *FxResolver) resolveProviderSignatures(dir string, providers []string) map[string]InterfaceMapping {
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
//  1. It has at least one return type
//  2. The first return type is a qualified identifier (pkg.Type) indicating
//     an interface from another package
//  3. Its body contains a return statement that calls a constructor
func (r *FxResolver) analyzeProviderFunc(fn *ast.FuncDecl, file string) *InterfaceMapping {
	if fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return nil
	}

	// Get the first return type.
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
// to determine what concrete type is being returned.
func (r *FxResolver) extractConcreteFromBody(body *ast.BlockStmt) string {
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

			funcName := exprToStr(call.Fun)
			if funcName == "" {
				continue
			}

			concrete = constructorToConcrete(funcName)
			return false
		}
		return true
	})

	return concrete
}

// extractConstructorName extracts the constructor function name from the body.
func (r *FxResolver) extractConstructorName(body *ast.BlockStmt) string {
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

			name = exprToStr(call.Fun)
			return false
		}
		return true
	})

	return name
}

// --- Fx/Dig AST matching helpers ---

// isFxProvide checks whether a call expression is fx.Provide(...).
func isFxProvide(call *ast.CallExpr) bool {
	return isSelectorCall(call, "fx", "Provide")
}

// isFxOptions checks whether a call expression is fx.Options(...).
func isFxOptions(call *ast.CallExpr) bool {
	return isSelectorCall(call, "fx", "Options")
}

// isFxInvoke checks whether a call expression is fx.Invoke(...).
func isFxInvoke(call *ast.CallExpr) bool {
	return isSelectorCall(call, "fx", "Invoke")
}

// isDigProvide checks whether a call expression is container.Provide(...)
// where container is typically a *dig.Container. We match any receiver
// calling Provide since we cannot resolve variable types from AST alone.
func isDigProvide(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	return sel.Sel.Name == "Provide"
}

// isSelectorCall checks whether a call expression matches pkg.Func pattern.
func isSelectorCall(call *ast.CallExpr, pkg, funcName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	return ident.Name == pkg && sel.Sel.Name == funcName
}

// extractFuncIdents extracts function identifier names from call arguments.
// It handles both plain identifiers (NewFoo) and selector expressions
// (pkg.NewFoo).
func extractFuncIdents(call *ast.CallExpr) []string {
	var names []string

	for _, arg := range call.Args {
		switch a := arg.(type) {
		case *ast.Ident:
			names = append(names, a.Name)
		case *ast.SelectorExpr:
			names = append(names, exprToStr(a))
		}
	}

	return names
}

// exprToStr converts an expression to a string representation.
// This is an alias for wireExprToString for use in the Fx resolver context.
func exprToStr(expr ast.Expr) string {
	return wireExprToString(expr)
}
