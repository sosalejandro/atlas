package adapters

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// RouteMapping maps an HTTP route to its handler function.
type RouteMapping struct {
	Method  string // GET, POST, PUT, DELETE, PATCH
	Path    string // /api/v1/auth/login
	Handler string // handler function reference (e.g. "h.authHandler.Login")
	File    string
	Line    int
}

// RouteParser extracts route registrations from Go source files.
type RouteParser struct{}

// NewRouteParser creates a new parser.
func NewRouteParser() *RouteParser {
	return &RouteParser{}
}

// httpMethods lists the Chi router methods we recognise. The map keys are the
// method names as they appear in Go source (e.g. r.Get, r.Post).
var httpMethods = map[string]string{
	"Get":     "GET",
	"Post":    "POST",
	"Put":     "PUT",
	"Delete":  "DELETE",
	"Patch":   "PATCH",
	"Head":    "HEAD",
	"Options": "OPTIONS",
	"Connect": "CONNECT",
	"Trace":   "TRACE",
}

// Parse reads a Go router file and extracts route-to-handler mappings.
// Supports Chi router patterns:
//
//	r.Get("/path", handler)
//	r.Post("/path", handler)
//	r.Route("/prefix", func(r chi.Router) { r.Get("/sub", handler) })
//	r.Group(func(r chi.Router) { ... })
//	r.With(middleware).Get("/path", handler)
func (p *RouteParser) Parse(filePath string) ([]RouteMapping, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing Go file %s: %w", filePath, err)
	}

	var routes []RouteMapping
	// Walk the entire AST looking for function declarations that contain route
	// registrations (e.g. RegisterAuthRoutes, RegisterRoutes).
	ast.Inspect(node, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			found := extractRoutesFromFunc(fset, fn, filePath)
			routes = append(routes, found...)
		}
		return true
	})

	return routes, nil
}

// ParseDir scans all .go files (excluding _test.go) in a directory and
// extracts route registrations from each. If path is a file, it falls
// back to Parse.
func (p *RouteParser) ParseDir(dirPath string) ([]RouteMapping, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dirPath, err)
	}
	if !info.IsDir() {
		return p.Parse(dirPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("reading dir %s: %w", dirPath, err)
	}

	var all []RouteMapping
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		routes, err := p.Parse(filepath.Join(dirPath, e.Name()))
		if err != nil {
			continue // skip unparseable files
		}
		all = append(all, routes...)
	}
	return all, nil
}

// extractRoutesFromFunc walks the body of a function declaration and extracts
// route registrations with an initial empty prefix.
func extractRoutesFromFunc(fset *token.FileSet, fn *ast.FuncDecl, filePath string) []RouteMapping {
	if fn.Body == nil {
		return nil
	}

	var routes []RouteMapping
	for _, stmt := range fn.Body.List {
		routes = append(routes, extractRoutesFromStmt(fset, stmt, "", filePath)...)
	}
	return routes
}

// extractRoutesFromStmt extracts routes from a single statement, with a prefix
// for nested Route() calls.
func extractRoutesFromStmt(fset *token.FileSet, stmt ast.Stmt, prefix, filePath string) []RouteMapping {
	var routes []RouteMapping

	switch s := stmt.(type) {
	case *ast.ExprStmt:
		routes = append(routes, extractRoutesFromExpr(fset, s.X, prefix, filePath)...)
	case *ast.IfStmt:
		// Handle conditionals like: if subscriptionService != nil { r.With(...).Post(...) }
		if s.Body != nil {
			for _, inner := range s.Body.List {
				routes = append(routes, extractRoutesFromStmt(fset, inner, prefix, filePath)...)
			}
		}
		if s.Else != nil {
			routes = append(routes, extractRoutesFromStmt(fset, s.Else, prefix, filePath)...)
		}
	case *ast.BlockStmt:
		for _, inner := range s.List {
			routes = append(routes, extractRoutesFromStmt(fset, inner, prefix, filePath)...)
		}
	}

	return routes
}

// extractRoutesFromExpr analyses a call expression to determine whether it
// registers routes. Handles the following patterns:
//
//	router.Get("/path", handler)          — direct HTTP method
//	router.Route("/prefix", func(r) { }) — nested prefix
//	router.Group(func(r) { })            — group (no prefix change)
//	router.With(mw).Get("/path", handler) — middleware chain
func extractRoutesFromExpr(fset *token.FileSet, expr ast.Expr, prefix, filePath string) []RouteMapping {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}

	methodName, _ := selectorName(call.Fun)

	// If the function is a call on a With() result (e.g. r.With(mw).Get(...)),
	// we need to unwrap the chain.
	if methodName == "" {
		return extractFromChainedCall(fset, call, prefix, filePath)
	}

	// Direct HTTP method calls: r.Get("/path", handler)
	if httpMethod, ok := httpMethods[methodName]; ok {
		return extractHTTPRoute(fset, call, httpMethod, prefix, filePath)
	}

	switch methodName {
	case "Route":
		return extractNestedRoute(fset, call, prefix, filePath)
	case "Group":
		return extractGroup(fset, call, prefix, filePath)
	case "HandleFunc", "Handle":
		return extractStdlibRoute(fset, call, prefix, filePath)
	}

	return nil
}

// extractHTTPRoute handles: r.Get("/path", handler)
func extractHTTPRoute(fset *token.FileSet, call *ast.CallExpr, httpMethod, prefix, filePath string) []RouteMapping {
	if len(call.Args) < 2 {
		return nil
	}

	pathLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	path := strings.Trim(pathLit.Value, `"`)

	fullPath := joinPath(prefix, path)
	handler := exprToString(call.Args[1])
	line := fset.Position(call.Pos()).Line

	return []RouteMapping{{
		Method:  httpMethod,
		Path:    fullPath,
		Handler: handler,
		File:    filePath,
		Line:    line,
	}}
}

// extractNestedRoute handles: r.Route("/prefix", func(r chi.Router) { ... })
func extractNestedRoute(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []RouteMapping {
	if len(call.Args) < 2 {
		return nil
	}

	pathLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	subPrefix := strings.Trim(pathLit.Value, `"`)
	newPrefix := joinPath(prefix, subPrefix)

	// The second argument should be a function literal.
	fnLit, ok := call.Args[1].(*ast.FuncLit)
	if !ok {
		return nil
	}

	return extractRoutesFromBody(fset, fnLit.Body, newPrefix, filePath)
}

// extractGroup handles: r.Group(func(r chi.Router) { ... })
func extractGroup(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []RouteMapping {
	if len(call.Args) < 1 {
		return nil
	}

	fnLit, ok := call.Args[0].(*ast.FuncLit)
	if !ok {
		return nil
	}

	// Group keeps the same prefix.
	return extractRoutesFromBody(fset, fnLit.Body, prefix, filePath)
}

// extractFromChainedCall handles r.With(middleware).Get("/path", handler)
// where call.Fun is itself a selector on a call expression.
func extractFromChainedCall(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []RouteMapping {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	chainedMethodName := sel.Sel.Name

	// The receiver of the selector should be a call to With().
	innerCall, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return nil
	}

	innerMethodName, _ := selectorName(innerCall.Fun)

	// Handle r.With(mw).Get(...)
	if innerMethodName == "With" {
		if httpMethod, ok := httpMethods[chainedMethodName]; ok {
			return extractHTTPRoute(fset, call, httpMethod, prefix, filePath)
		}
		// Handle r.With(mw).Route(...) or r.With(mw).Group(...)
		switch chainedMethodName {
		case "Route":
			return extractNestedRoute(fset, call, prefix, filePath)
		case "Group":
			return extractGroup(fset, call, prefix, filePath)
		}
	}

	// Handle deeper chains like r.With(mw1).With(mw2).Get(...)
	// Recursively unwrap the inner call.
	if httpMethod, ok := httpMethods[chainedMethodName]; ok {
		if isWithChain(innerCall) {
			return extractHTTPRoute(fset, call, httpMethod, prefix, filePath)
		}
	}

	return nil
}

// isWithChain checks whether a call expression is a With() call or a chained
// sequence of With() calls.
func isWithChain(call *ast.CallExpr) bool {
	name, _ := selectorName(call.Fun)
	if name == "With" {
		return true
	}

	// Check if it is something.With() where something is also a call chain.
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "With" {
		return false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	return isWithChain(inner)
}

// extractRoutesFromBody walks a block statement and extracts routes.
func extractRoutesFromBody(fset *token.FileSet, body *ast.BlockStmt, prefix, filePath string) []RouteMapping {
	if body == nil {
		return nil
	}

	var routes []RouteMapping
	for _, stmt := range body.List {
		routes = append(routes, extractRoutesFromStmt(fset, stmt, prefix, filePath)...)
	}
	return routes
}

// selectorName returns the method name from a *ast.SelectorExpr, e.g. for
// "r.Get" it returns "Get". Returns empty string for non-selector expressions.
func selectorName(expr ast.Expr) (string, string) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}

	receiver := exprToString(sel.X)
	return sel.Sel.Name, receiver
}

// exprToString converts an AST expression to a human-readable string
// representation suitable for the Handler field.
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		// For handler factories like wrapHandler(h.Login), return the inner
		// expression.
		return exprToString(e.Fun) + "(...)"
	case *ast.FuncLit:
		return "<func>"
	case *ast.IndexExpr:
		return exprToString(e.X) + "[" + exprToString(e.Index) + "]"
	}
	return "<unknown>"
}

// extractStdlibRoute handles stdlib patterns:
//
//	mux.HandleFunc("/path", handler)
//	mux.Handle("/path", handler)
//	http.HandleFunc("/path", handler)
//	mux.HandleFunc("GET /path", handler)  — Go 1.22+ pattern
func extractStdlibRoute(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []RouteMapping {
	if len(call.Args) < 2 {
		return nil
	}

	pathLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	pattern := strings.Trim(pathLit.Value, `"`)

	// Parse Go 1.22 method+path pattern: "POST /path" or just "/path"
	method, path := parseMethodPath(pattern)
	fullPath := joinPath(prefix, path)
	handler := resolveHandlerArg(call.Args[1])
	line := fset.Position(call.Pos()).Line

	return []RouteMapping{{
		Method:  method,
		Path:    fullPath,
		Handler: handler,
		File:    filePath,
		Line:    line,
	}}
}

// parseMethodPath splits a Go 1.22+ routing pattern like "POST /api/v1/users"
// into method and path components. Patterns without a method prefix (e.g. "/healthz")
// return an empty method string, meaning all methods are accepted.
func parseMethodPath(pattern string) (method, path string) {
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) == 2 && isHTTPMethod(parts[0]) {
		return parts[0], parts[1]
	}
	return "", pattern
}

// isHTTPMethod returns true if s is a standard HTTP method name.
func isHTTPMethod(s string) bool {
	switch s {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		return true
	}
	return false
}

// resolveHandlerArg unwraps common handler wrapper patterns to extract the
// underlying handler reference. For example:
//
//	chain(uploadHandler)                        → "uploadHandler"
//	chain(http.HandlerFunc(fileHandler.Handle))  → "fileHandler.Handle"
//	http.HandlerFunc(handler)                    → "handler"
func resolveHandlerArg(expr ast.Expr) string {
	if call, ok := expr.(*ast.CallExpr); ok {
		// If it's a single-arg wrapper like chain(x) or http.HandlerFunc(x),
		// try to unwrap and return the inner expression.
		if len(call.Args) == 1 {
			inner := resolveHandlerArg(call.Args[0])
			if inner != "<unknown>" {
				return inner
			}
		}
	}
	return exprToString(expr)
}

// joinPath joins two path segments, avoiding double slashes and handling the
// root path "/" correctly.
func joinPath(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "/" || path == "" {
		return prefix
	}

	// Normalise: strip trailing slash from prefix before joining.
	prefix = strings.TrimRight(prefix, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return prefix + path
}
