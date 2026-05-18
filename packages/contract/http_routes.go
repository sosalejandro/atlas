package contract

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// extractRoutes walks every .go file under the project root (skipping
// vendor / generated / _test.go) and emits one KindRoute ContractDef per
// router-registration call site.
//
// Supported router patterns (ported from testreg internal/adapters/
// route_parser.go):
//
//   - Chi style:   r.Get("/path", handler), r.Post(...), r.With(mw).Get(...)
//   - Chi nested:  r.Route("/prefix", func(r chi.Router) { ... })
//   - Chi group:   r.Group(func(r chi.Router) { ... })   (no prefix change)
//   - Echo style:  e.GET("/path", handler), e.POST(...), e.Any(...)
//   - Echo groups: grp := e.Group("/api"); grp.POST(...)
//   - stdlib:      mux.HandleFunc("/path", handler), mux.HandleFunc("POST /p", h)
//
// Huma operations are NOT extracted here — they live in huma.go because
// their argument shape (Operation struct literal) is distinct enough to
// warrant its own pass.
//
// FeatureID resolution: per-route. The annotation must be within
// e.opts.AnnotationProximity lines above the registration call (NOT the
// handler func — the handler may live in a different file). This matches
// the pattern of `// @testreg trace.route-parser` style comments above
// router registrations.
func (e *Extractor) extractRoutes(ctx context.Context, idx *codeindex.Index, annIdx *annotationIndex) ([]ContractDef, []string) {
	if e.opts.ProjectRoot == "" {
		return nil, nil
	}
	handlerByShortID := buildHandlerIndex(idx)
	var (
		defs  []ContractDef
		warns []string
	)
	err := filepath.WalkDir(e.opts.ProjectRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isContractGoFile(path, d.Name()) {
			return nil
		}
		fileDefs, fileWarns := e.parseRoutesFromFile(path, idx, annIdx, handlerByShortID)
		defs = append(defs, fileDefs...)
		warns = append(warns, fileWarns...)
		return nil
	})
	if err != nil {
		warns = append(warns, fmt.Sprintf("contract routes: walk: %v", err))
	}
	return defs, warns
}

// parseRoutesFromFile is the per-file half of extractRoutes — kept as a
// method so the outer walker stays under the funlen limit and each
// per-file failure stays isolated.
func (e *Extractor) parseRoutesFromFile(absPath string, idx *codeindex.Index, annIdx *annotationIndex, handlerByShortID map[string]shared.SymbolID) ([]ContractDef, []string) {
	relPath := normaliseRelPath(e.opts.ProjectRoot, absPath)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nil, []string{fmt.Sprintf("contract routes: parse %s: %v", relPath, err)}
	}

	// First pass: collect every route registration line so the annotation
	// pairing can apply its strict rule.
	var routeLines []int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, r := range extractRoutesFromFunc(fset, fn, relPath) {
			routeLines = append(routeLines, r.Line)
		}
	}
	annIdx.registerDeclLines(relPath, routeLines)

	var defs []ContractDef
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, r := range extractRoutesFromFunc(fset, fn, relPath) {
			defs = append(defs, e.buildRouteDef(r, idx, annIdx, handlerByShortID))
		}
	}
	return defs, nil
}

// buildRouteDef converts a single routeMapping into a ContractDef and
// resolves both handler-sym and feature-id (with handler-symbol-fallback
// when the call site isn't annotated).
func (e *Extractor) buildRouteDef(r routeMapping, idx *codeindex.Index, annIdx *annotationIndex, handlerByShortID map[string]shared.SymbolID) ContractDef {
	def := ContractDef{
		Name:      r.Method + " " + r.Path,
		Kind:      KindRoute,
		Language:  LangGo,
		Signature: r.Method + " " + r.Path,
		FilePath:  r.File,
		Line:      r.Line,
		Source:    "http-routes",
		Operation: OperationDetail{
			Method:  r.Method,
			Path:    r.Path,
			Handler: r.Handler,
		},
	}
	if sid := resolveHandlerSymbol(r.Handler, handlerByShortID); sid != "" {
		def.Operation.HandlerSym = string(sid)
		def.Symbols = []shared.SymbolID{sid}
	}
	if fid, ok := annIdx.findFor(r.File, r.Line); ok {
		def.FeatureID = ptrFeatureID(fid)
		return def
	}
	if def.Operation.HandlerSym != "" {
		if sym, ok := lookupSymbolByID(idx, shared.SymbolID(def.Operation.HandlerSym)); ok {
			if fid, ok := annIdx.findFor(sym.Position.Path, sym.Position.Line); ok {
				def.FeatureID = ptrFeatureID(fid)
			}
		}
	}
	return def
}

// shouldSkipDir returns true for vendored / dot / fixture dirs the
// extractors never want to recurse into.
func shouldSkipDir(name string) bool {
	return name == "vendor" || name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".")
}

// isContractGoFile returns true for a .go file the extractors should
// consider — skipping _test.go and /generated/ paths.
func isContractGoFile(absPath, base string) bool {
	if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
		return false
	}
	if strings.Contains(filepath.ToSlash(absPath), "/generated/") {
		return false
	}
	return true
}

// routeMapping mirrors the legacy testreg adapters.RouteMapping. Internal
// to this package; the exported shape is OperationDetail.
type routeMapping struct {
	Method  string
	Path    string
	Handler string
	File    string
	Line    int
}

// httpMethods is the closed set of router-method names this extractor
// recognises. Mirrors the legacy testreg map verbatim (Chi capitalized +
// Echo uppercase forms).
var httpMethods = map[string]string{
	// Chi style (capitalized).
	"Get":     "GET",
	"Post":    "POST",
	"Put":     "PUT",
	"Delete":  "DELETE",
	"Patch":   "PATCH",
	"Head":    "HEAD",
	"Options": "OPTIONS",
	"Connect": "CONNECT",
	"Trace":   "TRACE",
	// Echo style (uppercase).
	"GET":     "GET",
	"POST":    "POST",
	"PUT":     "PUT",
	"DELETE":  "DELETE",
	"PATCH":   "PATCH",
	"HEAD":    "HEAD",
	"OPTIONS": "OPTIONS",
}

// extractRoutesFromFunc walks fn.Body looking for route registrations.
func extractRoutesFromFunc(fset *token.FileSet, fn *ast.FuncDecl, filePath string) []routeMapping {
	if fn.Body == nil {
		return nil
	}
	groupVars := buildGroupVarMap(fn.Body)
	var routes []routeMapping
	for _, stmt := range fn.Body.List {
		routes = append(routes, extractRoutesFromStmt(fset, stmt, "", filePath, groupVars)...)
	}
	return routes
}

func extractRoutesFromStmt(fset *token.FileSet, stmt ast.Stmt, prefix, filePath string, groupVars map[string]string) []routeMapping {
	var routes []routeMapping
	switch s := stmt.(type) {
	case *ast.ExprStmt:
		// Echo group-var dispatch: grp.POST("/path", handler).
		if call, ok := s.X.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				receiverName := exprToString(sel.X)
				if groupPrefix, ok := groupVars[receiverName]; ok {
					routes = append(routes, extractRoutesFromExpr(fset, s.X, groupPrefix, filePath)...)
					return routes
				}
			}
		}
		routes = append(routes, extractRoutesFromExpr(fset, s.X, prefix, filePath)...)
	case *ast.IfStmt:
		if s.Body != nil {
			for _, inner := range s.Body.List {
				routes = append(routes, extractRoutesFromStmt(fset, inner, prefix, filePath, groupVars)...)
			}
		}
		if s.Else != nil {
			routes = append(routes, extractRoutesFromStmt(fset, s.Else, prefix, filePath, groupVars)...)
		}
	case *ast.BlockStmt:
		for _, inner := range s.List {
			routes = append(routes, extractRoutesFromStmt(fset, inner, prefix, filePath, groupVars)...)
		}
	}
	return routes
}

// buildGroupVarMap collects Echo-style "grp := e.Group("/prefix")"
// assignments and returns var-name → accumulated prefix. Resolves chains
// (childGrp := apiGrp.Group("/foo")).
func buildGroupVarMap(body *ast.BlockStmt) map[string]string {
	groups := make(map[string]string)
	for _, stmt := range body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) == 0 || len(assign.Rhs) == 0 {
			continue
		}
		varIdent, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			continue
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Group" {
			continue
		}
		if len(call.Args) == 0 {
			continue
		}
		pathLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			continue
		}
		prefix := strings.Trim(pathLit.Value, `"`)
		receiverName := exprToString(sel.X)
		if parent, ok := groups[receiverName]; ok {
			prefix = joinPath(parent, prefix)
		}
		groups[varIdent.Name] = prefix
	}
	return groups
}

func extractRoutesFromExpr(fset *token.FileSet, expr ast.Expr, prefix, filePath string) []routeMapping {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}
	methodName, _ := selectorName(call.Fun)
	if methodName == "" {
		// Possibly a chained call like r.With(mw).Get(...).
		return extractFromChainedCall(fset, call, prefix, filePath)
	}
	if httpMethod, ok := httpMethods[methodName]; ok {
		return extractHTTPRoute(fset, call, httpMethod, prefix, filePath)
	}
	switch methodName {
	case "Route":
		return extractNestedRoute(fset, call, prefix, filePath)
	case "Group":
		return extractGroupCall(fset, call, prefix, filePath)
	case "HandleFunc", "Handle":
		return extractStdlibRoute(fset, call, prefix, filePath)
	case "Any":
		return extractHTTPRoute(fset, call, "ANY", prefix, filePath)
	}
	return nil
}

func extractHTTPRoute(fset *token.FileSet, call *ast.CallExpr, method, prefix, filePath string) []routeMapping {
	if len(call.Args) < 2 {
		return nil
	}
	pathLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	path := strings.Trim(pathLit.Value, `"`)
	handler := exprToString(call.Args[1])
	line := fset.Position(call.Pos()).Line
	return []routeMapping{{
		Method:  method,
		Path:    joinPath(prefix, path),
		Handler: handler,
		File:    filePath,
		Line:    line,
	}}
}

func extractNestedRoute(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []routeMapping {
	if len(call.Args) < 2 {
		return nil
	}
	pathLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	sub := strings.Trim(pathLit.Value, `"`)
	fnLit, ok := call.Args[1].(*ast.FuncLit)
	if !ok {
		return nil
	}
	newPrefix := joinPath(prefix, sub)
	return extractRoutesFromBody(fset, fnLit.Body, newPrefix, filePath)
}

func extractGroupCall(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []routeMapping {
	if len(call.Args) < 1 {
		return nil
	}
	fnLit, ok := call.Args[0].(*ast.FuncLit)
	if !ok {
		return nil
	}
	return extractRoutesFromBody(fset, fnLit.Body, prefix, filePath)
}

func extractFromChainedCall(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []routeMapping {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	chained := sel.Sel.Name
	innerCall, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	innerName, _ := selectorName(innerCall.Fun)
	if innerName == "With" {
		if method, ok := httpMethods[chained]; ok {
			return extractHTTPRoute(fset, call, method, prefix, filePath)
		}
		switch chained {
		case "Route":
			return extractNestedRoute(fset, call, prefix, filePath)
		case "Group":
			return extractGroupCall(fset, call, prefix, filePath)
		}
	}
	// Deeper chains: r.With(mw1).With(mw2).Get(...).
	if method, ok := httpMethods[chained]; ok && isWithChain(innerCall) {
		return extractHTTPRoute(fset, call, method, prefix, filePath)
	}
	return nil
}

func isWithChain(call *ast.CallExpr) bool {
	name, _ := selectorName(call.Fun)
	if name == "With" {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "With" {
		return false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	return isWithChain(inner)
}

func extractRoutesFromBody(fset *token.FileSet, body *ast.BlockStmt, prefix, filePath string) []routeMapping {
	if body == nil {
		return nil
	}
	var routes []routeMapping
	for _, stmt := range body.List {
		routes = append(routes, extractRoutesFromStmt(fset, stmt, prefix, filePath, nil)...)
	}
	return routes
}

func extractStdlibRoute(fset *token.FileSet, call *ast.CallExpr, prefix, filePath string) []routeMapping {
	if len(call.Args) < 2 {
		return nil
	}
	pathLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok {
		return nil
	}
	pattern := strings.Trim(pathLit.Value, `"`)
	method, path := parseMethodPath(pattern)
	handler := resolveHandlerArg(call.Args[1])
	line := fset.Position(call.Pos()).Line
	return []routeMapping{{
		Method:  method,
		Path:    joinPath(prefix, path),
		Handler: handler,
		File:    filePath,
		Line:    line,
	}}
}

func parseMethodPath(pattern string) (method, path string) {
	parts := strings.SplitN(pattern, " ", 2)
	if len(parts) == 2 && isHTTPMethod(parts[0]) {
		return parts[0], parts[1]
	}
	return "", pattern
}

func isHTTPMethod(s string) bool {
	switch s {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		return true
	}
	return false
}

func resolveHandlerArg(expr ast.Expr) string {
	if call, ok := expr.(*ast.CallExpr); ok {
		if len(call.Args) == 1 {
			inner := resolveHandlerArg(call.Args[0])
			if inner != "<unknown>" {
				return inner
			}
		}
	}
	return exprToString(expr)
}

// selectorName returns the trailing method name from a SelectorExpr.
// receiver is the textual form of the receiver expression (mostly used
// for diagnostic warnings; the routing logic itself only consumes the
// method name).
func selectorName(expr ast.Expr) (method, receiver string) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	return sel.Sel.Name, exprToString(sel.X)
}

func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		return exprToString(e.Fun) + "(...)"
	case *ast.FuncLit:
		return "<func>"
	case *ast.IndexExpr:
		return exprToString(e.X) + "[" + exprToString(e.Index) + "]"
	}
	return "<unknown>"
}

func joinPath(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "/" || path == "" {
		return prefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return prefix + path
}

// lookupSymbolByID returns the shared.Symbol matching the given id, if
// present in idx.Symbols. Linear scan — symbol counts are bounded (a
// realistic project has <100k symbols and this is invoked at most once
// per ContractDef during extraction).
func lookupSymbolByID(idx *codeindex.Index, id shared.SymbolID) (shared.Symbol, bool) {
	if idx == nil {
		return shared.Symbol{}, false
	}
	for _, sym := range idx.Symbols {
		if sym.ID == id {
			return sym, true
		}
	}
	return shared.Symbol{}, false
}

// buildHandlerIndex extracts every "Receiver.Method" SymbolID from idx
// into a short-name lookup that mirrors the heuristic in the codeindex/go
// scanner's normaliseHandlerRef helper. Used to resolve "h.authHandler.Login"
// → "authHandler.Login" → matching SymbolID.
func buildHandlerIndex(idx *codeindex.Index) map[string]shared.SymbolID {
	out := make(map[string]shared.SymbolID, len(idx.Symbols))
	for _, sym := range idx.Symbols {
		id := string(sym.ID)
		out[id] = sym.ID
		// Also index by method-only ("Login") and trailing-two-segments
		// ("authHandler.Login") so callers with shorter refs resolve.
		if idx := strings.LastIndex(id, "."); idx >= 0 {
			method := id[idx+1:]
			if _, exists := out[method]; !exists {
				out[method] = sym.ID
			}
		}
	}
	return out
}

func resolveHandlerSymbol(handlerRef string, idx map[string]shared.SymbolID) shared.SymbolID {
	handlerRef = strings.TrimSuffix(handlerRef, "(...)")
	if handlerRef == "" || handlerRef == "<unknown>" || handlerRef == "<func>" {
		return ""
	}
	// Direct match.
	if sid, ok := idx[handlerRef]; ok {
		return sid
	}
	// Two-segment match: "h.authHandler.Login" → "authHandler.Login".
	parts := strings.Split(handlerRef, ".")
	if len(parts) >= 2 {
		short := parts[len(parts)-2] + "." + parts[len(parts)-1]
		if sid, ok := idx[short]; ok {
			return sid
		}
	}
	// Method-only fallback.
	if len(parts) > 0 {
		if sid, ok := idx[parts[len(parts)-1]]; ok {
			return sid
		}
	}
	return ""
}
