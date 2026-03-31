package adapters

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/testreg/internal/domain"
	"github.com/sosalejandro/testreg/internal/ports"
)

// GoASTScanner builds call graphs by parsing Go source code using only the
// standard library go/ast and go/parser packages. It does not require full
// module resolution (no go/types or golang.org/x/tools).
//
// The scanner works in four phases:
//  1. Pre-resolution: SQLC method→SQL mappings (via SQLCMapper).
//  2. Route discovery: HTTP route→handler edges (via RouteParser).
//  3. Function discovery: Walk all .go files, create nodes, build type→field maps.
//  4. Call graph extraction: Walk function bodies, resolve calls, add edges.
type GoASTScanner struct {
	routeParser     *RouteParser
	sqlcMapper      *SQLCMapper
	frontendScanner *FrontendScanner
}

// NewGoASTScanner creates a scanner with its collaborators pre-wired.
func NewGoASTScanner() *GoASTScanner {
	return &GoASTScanner{
		routeParser:     NewRouteParser(),
		sqlcMapper:      NewSQLCMapper(),
		frontendScanner: NewFrontendScanner(),
	}
}

// Build constructs the full call graph for the project rooted at projectRoot.
func (s *GoASTScanner) Build(projectRoot string, config ports.GraphConfig) (*domain.Graph, error) {
	ctx, err := s.newScanContext(projectRoot, config)
	if err != nil {
		return nil, err
	}

	backendAbs := filepath.Join(projectRoot, config.BackendRoot)

	// Phase 0: Pre-resolution.
	if err := s.preResolve(ctx, projectRoot, config); err != nil {
		// Non-fatal: degrade gracefully if SQLC config is missing.
		_ = err
	}

	// Phase 1: Route discovery.
	if config.RouterFile != "" {
		if err := s.discoverRoutes(ctx, projectRoot, config); err != nil {
			// Non-fatal: continue without route nodes.
			_ = err
		}
	}

	// Phase 2: Function discovery (all .go files under backend root).
	// Also discovers @api annotations and creates endpoint nodes.
	if err := s.discoverFunctions(ctx, backendAbs); err != nil {
		return nil, fmt.Errorf("function discovery: %w", err)
	}

	// Phase 2.5: Resolve unresolved handler references from Phase 1.
	s.resolveHandlerRefs(ctx)

	// Phase 3: Call graph extraction.
	s.extractCalls(ctx)

	// Phase 4: Frontend graph (TypeScript scanner) — uses shared instance for caching.
	if len(config.FrontendRoots) > 0 {
		frontendResult, err := s.frontendScanner.Scan(projectRoot)
		if err != nil {
			// Non-fatal — log warning but continue with backend-only graph.
			fmt.Fprintf(os.Stderr, "warning: frontend scan failed: %v\n", err)
		} else {
			s.frontendScanner.MergeIntoGraph(ctx.graph, frontendResult)
		}
	}

	return ctx.graph, nil
}

// BuildFrom constructs a partial graph starting from specific entry points.
// It is the lazy/efficient version: only files containing the named functions
// and their transitive callees are parsed.
func (s *GoASTScanner) BuildFrom(projectRoot string, entryPoints []string, config ports.GraphConfig) (*domain.Graph, error) {
	ctx, err := s.newScanContext(projectRoot, config)
	if err != nil {
		return nil, err
	}

	backendAbs := filepath.Join(projectRoot, config.BackendRoot)

	// Phase 0: Pre-resolution (always needed for SQLC lookups).
	if err := s.preResolve(ctx, projectRoot, config); err != nil {
		_ = err
	}

	// Phase 1: Route discovery.
	if config.RouterFile != "" {
		if err := s.discoverRoutes(ctx, projectRoot, config); err != nil {
			_ = err
		}
	}

	// Phase 2: Discover all functions so we can locate entry points.
	// Also discovers @api annotations and creates endpoint nodes.
	if err := s.discoverFunctions(ctx, backendAbs); err != nil {
		return nil, fmt.Errorf("function discovery: %w", err)
	}

	// Phase 2.5: Resolve unresolved handler references from Phase 1.
	s.resolveHandlerRefs(ctx)

	// Phase 3: Extract calls only for the reachable subgraph.
	s.extractCallsFrom(ctx, entryPoints)

	// Prune unreachable nodes from the graph.
	s.pruneUnreachable(ctx, entryPoints)

	// Phase 4: Frontend graph (TypeScript scanner) — uses shared instance for caching.
	if len(config.FrontendRoots) > 0 {
		frontendResult, err := s.frontendScanner.Scan(projectRoot)
		if err != nil {
			// Non-fatal — log warning but continue with backend-only graph.
			fmt.Fprintf(os.Stderr, "warning: frontend scan failed: %v\n", err)
		} else {
			s.frontendScanner.MergeIntoGraph(ctx.graph, frontendResult)
		}
	}

	return ctx.graph, nil
}

// ---------------------------------------------------------------------------
// Internal types
// ---------------------------------------------------------------------------

// scanContext holds mutable state accumulated during a single Build/BuildFrom.
type scanContext struct {
	graph       *domain.Graph
	config      ports.GraphConfig
	projectRoot string

	// Function lookup: "ReceiverType.MethodName" or "pkgName.FuncName" → *funcInfo.
	funcLookup map[string]*funcInfo

	// Struct field type maps: "StructName" → { "fieldName": "FieldType" }.
	structFields map[string]map[string]string

	// SQLC generated method → SQLCMapping.
	sqlcMethods map[string]SQLCMapping

	// Endpoints discovered via @api annotations (takes precedence over route parser).
	apiAnnotatedEndpoints map[string]bool

	// DI bindings: short interface name → InterfaceMapping (from Wire and/or Fx resolvers).
	interfaceBindings map[string]InterfaceMapping

	// Ignore patterns (pre-compiled).
	ignorePackages  map[string]bool
	ignoreFuncGlobs []string
}

// funcInfo stores the parsed AST and metadata for a discovered function/method.
type funcInfo struct {
	node     *domain.Node
	funcDecl *ast.FuncDecl
	fset     *token.FileSet
	file     *ast.File
	receiver string // empty for plain functions
}

func (s *GoASTScanner) newScanContext(projectRoot string, config ports.GraphConfig) (*scanContext, error) {
	ignorePkgs := make(map[string]bool, len(config.IgnorePackages))
	for _, pkg := range config.IgnorePackages {
		ignorePkgs[pkg] = true
	}

	return &scanContext{
		graph:                 domain.NewGraph(),
		config:                config,
		projectRoot:           projectRoot,
		funcLookup:            make(map[string]*funcInfo),
		structFields:          make(map[string]map[string]string),
		sqlcMethods:           make(map[string]SQLCMapping),
		apiAnnotatedEndpoints: make(map[string]bool),
		interfaceBindings:     make(map[string]InterfaceMapping),
		ignorePackages:        ignorePkgs,
		ignoreFuncGlobs:       config.IgnoreFunctions,
	}, nil
}

// ---------------------------------------------------------------------------
// Phase 0: Pre-resolution
// ---------------------------------------------------------------------------

func (s *GoASTScanner) preResolve(ctx *scanContext, projectRoot string, config ports.GraphConfig) error {
	// SQLC pre-resolution.
	if config.SQLCConfig != "" {
		mappings, err := s.sqlcMapper.Map(projectRoot, config.SQLCConfig)
		if err != nil {
			return err
		}
		ctx.sqlcMethods = mappings
	}

	// Wire DI pre-resolution.
	if config.WireFile != "" {
		wirePath := config.WireFile
		if !filepath.IsAbs(wirePath) {
			wirePath = filepath.Join(projectRoot, wirePath)
		}
		wireResolver := NewWireResolver()
		bindings, err := wireResolver.Resolve(wirePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: wire resolution failed: %v\n", err)
		} else {
			for k, v := range bindings {
				ctx.interfaceBindings[k] = v
			}
		}
	}

	// Uber Fx/Dig DI pre-resolution.
	if config.FxDir != "" {
		fxDir := config.FxDir
		if !filepath.IsAbs(fxDir) {
			fxDir = filepath.Join(projectRoot, fxDir)
		}
		fxResolver := NewFxResolver()
		bindings, err := fxResolver.Resolve(fxDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: fx resolution failed: %v\n", err)
		} else {
			for k, v := range bindings {
				ctx.interfaceBindings[k] = v
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Route discovery
// ---------------------------------------------------------------------------

func (s *GoASTScanner) discoverRoutes(ctx *scanContext, projectRoot string, config ports.GraphConfig) error {
	routerPath := config.RouterFile
	if !filepath.IsAbs(routerPath) {
		routerPath = filepath.Join(projectRoot, routerPath)
	}

	routes, err := s.routeParser.ParseDir(routerPath)
	if err != nil {
		return err
	}

	for _, route := range routes {
		endpointID := fmt.Sprintf("%s %s", route.Method, route.Path)

		relFile, relErr := filepath.Rel(projectRoot, route.File)
		if relErr != nil {
			relFile = route.File
		}
		relFile = filepath.ToSlash(relFile)

		endpointNode := &domain.Node{
			ID:   endpointID,
			Kind: domain.NodeEndpoint,
			File: relFile,
			Line: route.Line,
		}
		ctx.graph.AddNode(endpointNode)

		// The handler reference from the route parser is something like
		// "h.authHandler.Login" or "handler.Login". We normalise it to
		// a lookup key that Phase 3 can match.
		handlerID := normaliseHandlerRef(route.Handler)
		if handlerID != "" {
			// We create a placeholder node for the handler. If Phase 2
			// discovers the real function, AddNode deduplicates by ID.
			ctx.graph.AddNode(&domain.Node{
				ID:   handlerID,
				Kind: domain.NodeHandler,
			})
			ctx.graph.AddEdge(endpointID, handlerID)
		}
	}

	return nil
}

// normaliseHandlerRef converts a route handler expression like
// "h.authHandler.Login" to just the final "TypeName.Method" form suitable
// for lookup. If the handler is a plain function name it is returned as-is.
func normaliseHandlerRef(handler string) string {
	// Strip wrapper calls like "wrapHandler(...)" → inner content.
	handler = strings.TrimSuffix(handler, "(...)")
	handler = strings.TrimPrefix(handler, "<func>")
	if handler == "" || handler == "<unknown>" {
		return ""
	}

	// Take the last two dot-separated segments: "a.b.c.Method" → "c.Method".
	parts := strings.Split(handler, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return handler
}

// ---------------------------------------------------------------------------
// Phase 2.5: Handler reference resolution
// ---------------------------------------------------------------------------

// resolveHandlerRefs resolves placeholder handler nodes created by the route
// parser in Phase 1. A placeholder is a node with Kind=handler and File=""
// (no source location), typically with an ID like "h.Login" or
// "authHandler.Login" from normaliseHandlerRef.
//
// For each such placeholder, this method extracts the method name and searches
// all discovered nodes for a handler node whose ID ends with ".MethodName".
// If exactly one match is found, the placeholder is merged into the real node.
// If multiple matches exist, the edge is marked ambiguous.
//
// Endpoints with @api annotations (tracked in apiAnnotatedEndpoints) are
// skipped — their handler links are already correct from Phase 2.
func (s *GoASTScanner) resolveHandlerRefs(ctx *scanContext) {
	// Collect placeholder handler node IDs (File="" means unresolved).
	var placeholders []string
	for id, node := range ctx.graph.Nodes {
		if node.Kind == domain.NodeHandler && node.File == "" {
			placeholders = append(placeholders, id)
		}
	}

	for _, placeholderID := range placeholders {
		// Check if any edge to this placeholder comes from an @api-annotated endpoint.
		// If so, that means the route parser also found it but @api already handles it
		// via a direct link — we can remove the placeholder entirely.
		hasAPIEndpointSource := false
		for _, e := range ctx.graph.Edges {
			if e.To == placeholderID {
				if ctx.apiAnnotatedEndpoints[e.From] {
					hasAPIEndpointSource = true
					break
				}
			}
		}
		if hasAPIEndpointSource {
			// @api annotation already created a correct edge. Remove the placeholder
			// and any edges targeting it from the route parser.
			s.removePlaceholder(ctx, placeholderID)
			continue
		}

		// Extract the method name from the placeholder ID.
		// Placeholder IDs look like "authHandler.Login" or "h.Login".
		methodName := extractMethodName(placeholderID)
		if methodName == "" {
			continue
		}

		// Search all discovered handler nodes for one whose ID ends with ".MethodName".
		// Skip test helpers and mocks (files containing "test" or "mock" in the name).
		suffix := "." + methodName
		var matches []string
		for id, info := range ctx.funcLookup {
			if strings.HasSuffix(id, suffix) && info.node.Kind == domain.NodeHandler {
				lowerFile := strings.ToLower(info.node.File)
				if strings.Contains(lowerFile, "test") || strings.Contains(lowerFile, "mock") {
					continue
				}
				matches = append(matches, id)
			}
		}

		switch len(matches) {
		case 1:
			// Exact single match — merge placeholder into the real node.
			realNode := ctx.funcLookup[matches[0]].node
			ctx.graph.MergeNode(placeholderID, realNode)
		case 0:
			// No match found. Try a broader search across all node kinds.
			for id := range ctx.funcLookup {
				if strings.HasSuffix(id, suffix) {
					matches = append(matches, id)
				}
			}
			if len(matches) == 1 {
				realNode := ctx.funcLookup[matches[0]].node
				ctx.graph.MergeNode(placeholderID, realNode)
			}
			// If still no match, leave the placeholder as-is.
		default:
			// Multiple matches — mark edges to this placeholder as ambiguous.
			for i := range ctx.graph.Edges {
				if ctx.graph.Edges[i].To == placeholderID {
					ctx.graph.Edges[i].Ambiguous = true
				}
			}
		}
	}
}

// removePlaceholder removes a placeholder node and all edges targeting it.
func (s *GoASTScanner) removePlaceholder(ctx *scanContext, placeholderID string) {
	delete(ctx.graph.Nodes, placeholderID)

	kept := make([]domain.Edge, 0, len(ctx.graph.Edges))
	for _, e := range ctx.graph.Edges {
		if e.To == placeholderID {
			continue
		}
		if e.From == placeholderID {
			continue
		}
		kept = append(kept, e)
	}
	ctx.graph.Edges = kept
	ctx.graph.InvalidateAdjacency()
}

// ---------------------------------------------------------------------------
// Phase 2: Function discovery
// ---------------------------------------------------------------------------

func (s *GoASTScanner) discoverFunctions(ctx *scanContext, backendAbs string) error {
	return filepath.WalkDir(backendAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}

		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			// Check if the directory matches an ignored package.
			relDir, _ := filepath.Rel(backendAbs, path)
			relDir = filepath.ToSlash(relDir)
			if ctx.ignorePackages[relDir] || ctx.ignorePackages[name] {
				return filepath.SkipDir
			}
			return nil
		}

		// Only parse .go files, skip tests and generated code directory.
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		relPath, _ := filepath.Rel(ctx.projectRoot, path)
		relPath = filepath.ToSlash(relPath)

		// Skip files under "generated" directories (SQLC output).
		if strings.Contains(relPath, "/generated/") {
			return nil
		}

		return s.parseFile(ctx, path, relPath, backendAbs)
	})
}

// parseFile parses a single Go source file and extracts function declarations
// and struct type definitions. It also parses @api annotations to create
// endpoint nodes linked to handler functions.
func (s *GoASTScanner) parseFile(ctx *scanContext, absPath, relPath, backendAbs string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		// Skip unparseable files gracefully.
		return nil
	}

	// Determine the package-relative path for node kind classification.
	relFromBackend, _ := filepath.Rel(backendAbs, absPath)
	relFromBackend = filepath.ToSlash(relFromBackend)
	pkgDir := filepath.ToSlash(filepath.Dir(relFromBackend))

	// Extract struct field types from this file (needed for call resolution).
	s.extractStructFields(ctx, file)

	// Parse @api annotations from the source file.
	apiSource, _ := ParseAnnotatedSource(absPath, relPath)

	// Walk function/method declarations.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok {
			s.registerFunction(ctx, fn, fset, file, relPath, pkgDir)

			// If this function has @api annotations, create endpoint nodes.
			if apiSource != nil {
				funcID := s.funcDeclID(fn, file)
				if apis, ok := apiSource.FuncAPIs[funcID]; ok {
					for _, api := range apis {
						endpointID := api.Method + " " + api.Path
						endpointNode := &domain.Node{
							ID:   endpointID,
							Kind: domain.NodeEndpoint,
							File: relPath,
							Line: fset.Position(fn.Pos()).Line,
						}
						ctx.graph.AddNode(endpointNode)

						// The handler's graph ID uses the receiver type.
						handlerID := s.funcDeclGraphID(fn, file, pkgDir)
						ctx.graph.AddEdge(endpointID, handlerID)

						// Track this endpoint as annotation-sourced so Phase 1
						// route parser results don't override it.
						ctx.apiAnnotatedEndpoints[endpointID] = true
					}
				}
			}
		}
	}

	return nil
}

// funcDeclID returns the identifier used by ParseAnnotatedSource for a function.
// For methods: "ReceiverType.MethodName". For plain functions: "FuncName".
func (s *GoASTScanner) funcDeclID(fn *ast.FuncDecl, file *ast.File) string {
	receiver := receiverTypeName(fn)
	if receiver != "" {
		return receiver + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// funcDeclGraphID returns the graph node ID for a function declaration.
// For methods: "ReceiverType.MethodName". For plain functions: "pkgName.FuncName".
func (s *GoASTScanner) funcDeclGraphID(fn *ast.FuncDecl, file *ast.File, pkgDir string) string {
	receiver := receiverTypeName(fn)
	if receiver != "" {
		return receiver + "." + fn.Name.Name
	}
	return file.Name.Name + "." + fn.Name.Name
}

// registerFunction creates a domain.Node for a function/method declaration
// and stores it in the lookup map.
func (s *GoASTScanner) registerFunction(
	ctx *scanContext,
	fn *ast.FuncDecl,
	fset *token.FileSet,
	file *ast.File,
	relPath, pkgDir string,
) {
	if fn.Name == nil || !fn.Name.IsExported() {
		// Only track exported functions — unexported helpers are rarely
		// cross-boundary call targets. This keeps the graph manageable.
		// Exception: we still register unexported methods that have receivers
		// because they may be called internally (e.g. rowToAggregate).
		if fn.Recv == nil {
			return
		}
	}

	receiver := receiverTypeName(fn)
	funcName := fn.Name.Name

	// Build the lookup key.
	var id string
	if receiver != "" {
		id = receiver + "." + funcName
	} else {
		id = file.Name.Name + "." + funcName
	}

	kind := classifyNodeKind(pkgDir)
	line := fset.Position(fn.Pos()).Line

	doc := ""
	if fn.Doc != nil {
		doc = strings.TrimSpace(fn.Doc.Text())
	}

	sig := buildSignature(fn)

	node := &domain.Node{
		ID:        id,
		Kind:      kind,
		File:      relPath,
		Line:      line,
		Doc:       doc,
		Signature: sig,
		Package:   pkgDir,
	}

	ctx.graph.AddNode(node)

	ctx.funcLookup[id] = &funcInfo{
		node:     node,
		funcDecl: fn,
		fset:     fset,
		file:     file,
		receiver: receiver,
	}
}

// receiverTypeName extracts the type name from a method receiver.
// For pointer receivers (*AuthHandler) it returns "AuthHandler".
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}

	recvType := fn.Recv.List[0].Type
	// Unwrap pointer type.
	if star, ok := recvType.(*ast.StarExpr); ok {
		recvType = star.X
	}

	if ident, ok := recvType.(*ast.Ident); ok {
		return ident.Name
	}

	return ""
}

// classifyNodeKind maps a package directory path to a domain.NodeKind.
func classifyNodeKind(pkgDir string) domain.NodeKind {
	lower := strings.ToLower(pkgDir)

	switch {
	case strings.Contains(lower, "handler"):
		return domain.NodeHandler
	case strings.Contains(lower, "persistence") || strings.Contains(lower, "repository"):
		return domain.NodeRepository
	case strings.Contains(lower, "service"):
		return domain.NodeService
	case strings.Contains(lower, "generated"):
		return domain.NodeQuery
	default:
		return domain.NodeService
	}
}

// buildSignature produces a human-readable function signature from the AST.
func buildSignature(fn *ast.FuncDecl) string {
	var b strings.Builder

	b.WriteString("func ")

	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		b.WriteString("(")
		b.WriteString(typeExprString(fn.Recv.List[0].Type))
		b.WriteString(") ")
	}

	b.WriteString(fn.Name.Name)
	b.WriteString("(")

	if fn.Type.Params != nil {
		params := make([]string, 0, len(fn.Type.Params.List))
		for _, field := range fn.Type.Params.List {
			typeStr := typeExprString(field.Type)
			if len(field.Names) == 0 {
				params = append(params, typeStr)
			} else {
				for _, name := range field.Names {
					params = append(params, name.Name+" "+typeStr)
				}
			}
		}
		b.WriteString(strings.Join(params, ", "))
	}

	b.WriteString(")")

	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		results := make([]string, 0, len(fn.Type.Results.List))
		for _, field := range fn.Type.Results.List {
			results = append(results, typeExprString(field.Type))
		}
		if len(results) == 1 {
			b.WriteString(" " + results[0])
		} else {
			b.WriteString(" (" + strings.Join(results, ", ") + ")")
		}
	}

	return b.String()
}

// typeExprString converts a type expression to a readable string.
func typeExprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + typeExprString(e.X)
	case *ast.SelectorExpr:
		return typeExprString(e.X) + "." + e.Sel.Name
	case *ast.ArrayType:
		return "[]" + typeExprString(e.Elt)
	case *ast.MapType:
		return "map[" + typeExprString(e.Key) + "]" + typeExprString(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.Ellipsis:
		return "..." + typeExprString(e.Elt)
	case *ast.ChanType:
		return "chan " + typeExprString(e.Value)
	default:
		return "?"
	}
}

// extractStructFields scans type declarations for struct definitions and
// records their field names and types in the context's structFields map.
func (s *GoASTScanner) extractStructFields(ctx *scanContext, file *ast.File) {
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			structName := typeSpec.Name.Name
			fields := make(map[string]string)

			for _, field := range structType.Fields.List {
				fieldType := typeExprString(field.Type)
				for _, name := range field.Names {
					fields[name.Name] = fieldType
				}
			}

			ctx.structFields[structName] = fields
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 3: Call graph extraction
// ---------------------------------------------------------------------------

// extractCalls walks every discovered function body and resolves calls.
func (s *GoASTScanner) extractCalls(ctx *scanContext) {
	for _, info := range ctx.funcLookup {
		if info.funcDecl.Body == nil {
			continue
		}
		s.walkBody(ctx, info)
	}
}

// extractCallsFrom walks only the functions reachable from the entry points.
func (s *GoASTScanner) extractCallsFrom(ctx *scanContext, entryPoints []string) {
	visited := make(map[string]bool)
	queue := make([]string, 0, len(entryPoints))

	// Seed the queue with entry points and any partial matches.
	for _, ep := range entryPoints {
		matches := s.findMatching(ctx, ep)
		for _, m := range matches {
			if !visited[m] {
				visited[m] = true
				queue = append(queue, m)
			}
		}
	}

	// Also seed the queue with nodes reachable via graph edges (e.g.,
	// endpoint→handler links from Phase 1). This ensures that endpoint
	// entry points like "POST /api/v1/auth/login" follow through to
	// their handler functions.
	for _, ep := range entryPoints {
		for _, e := range ctx.graph.Edges {
			if e.From == ep && !visited[e.To] {
				visited[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		info, ok := ctx.funcLookup[current]
		if !ok || info.funcDecl.Body == nil {
			// Not a function — but follow graph edges from this node
			// (handles endpoint→handler and other non-function nodes).
			for _, e := range ctx.graph.Edges {
				if e.From == current && !visited[e.To] {
					visited[e.To] = true
					queue = append(queue, e.To)
				}
			}
			continue
		}

		callees := s.walkBody(ctx, info)
		for _, calleeID := range callees {
			if !visited[calleeID] {
				visited[calleeID] = true
				queue = append(queue, calleeID)
			}
		}
	}
}

// findMatching returns all function IDs that match a potentially partial
// entry point string. Supports exact match and suffix match (e.g.
// "Login" matches "AuthHandler.Login").
func (s *GoASTScanner) findMatching(ctx *scanContext, entryPoint string) []string {
	// Exact match first.
	if _, ok := ctx.funcLookup[entryPoint]; ok {
		return []string{entryPoint}
	}

	// Suffix match: entry point might be "Login" and we want "AuthHandler.Login".
	var matches []string
	suffix := "." + entryPoint
	for id := range ctx.funcLookup {
		if strings.HasSuffix(id, suffix) || id == entryPoint {
			matches = append(matches, id)
		}
	}

	// Also check endpoint nodes (e.g. "GET /api/v1/auth/login").
	for id := range ctx.graph.Nodes {
		if id == entryPoint || strings.Contains(id, entryPoint) {
			matches = append(matches, id)
		}
	}

	return matches
}

// walkBody walks the body of a function, resolving call expressions and
// adding edges to the graph. Returns the list of callee IDs discovered.
func (s *GoASTScanner) walkBody(ctx *scanContext, info *funcInfo) []string {
	var callees []string

	ast.Inspect(info.funcDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		calleeID, ambiguous := s.resolveCall(ctx, info, call)
		if calleeID == "" {
			return true
		}

		// Check if callee should be ignored.
		if s.shouldIgnore(ctx, calleeID) {
			return true
		}

		// Check if the callee is a SQLC generated method.
		if sqlcMapping, ok := ctx.sqlcMethods[extractMethodName(calleeID)]; ok {
			queryNodeID := "sql:" + sqlcMapping.QueryName
			queryNode := &domain.Node{
				ID:   queryNodeID,
				Kind: domain.NodeQuery,
				File: sqlcMapping.SQLFile,
				Line: sqlcMapping.SQLLine,
				Doc:  fmt.Sprintf("SQLC query: %s (:%s)", sqlcMapping.QueryName, sqlcMapping.QueryType),
			}
			ctx.graph.AddNode(queryNode)

			if ambiguous {
				ctx.graph.AddAmbiguousEdge(info.node.ID, queryNodeID)
			} else {
				ctx.graph.AddEdge(info.node.ID, queryNodeID)
			}
			callees = append(callees, queryNodeID)
			return true
		}

		// Add edge to the resolved callee if it exists in our lookup.
		if _, exists := ctx.funcLookup[calleeID]; exists {
			if ambiguous {
				ctx.graph.AddAmbiguousEdge(info.node.ID, calleeID)
			} else {
				ctx.graph.AddEdge(info.node.ID, calleeID)
			}
			callees = append(callees, calleeID)
		} else {
			// Check if this looks like an external call.
			if isExternalCall(calleeID) {
				extNode := &domain.Node{
					ID:   calleeID,
					Kind: domain.NodeExternal,
				}
				ctx.graph.AddNode(extNode)
				ctx.graph.AddEdge(info.node.ID, calleeID)
			}
		}

		return true
	})

	return callees
}

// resolveCall attempts to resolve a call expression to a callee ID.
// Returns the callee ID and whether the resolution is ambiguous.
func (s *GoASTScanner) resolveCall(ctx *scanContext, caller *funcInfo, call *ast.CallExpr) (string, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return s.resolveSelectorCall(ctx, caller, fn)
	case *ast.Ident:
		// Plain function call: funcName()
		// Try package-qualified: "pkgName.FuncName".
		if caller.file != nil {
			id := caller.file.Name.Name + "." + fn.Name
			if _, ok := ctx.funcLookup[id]; ok {
				return id, false
			}
		}
		return "", false
	default:
		return "", false
	}
}

// resolveSelectorCall resolves a call like x.Method() or x.y.Method().
func (s *GoASTScanner) resolveSelectorCall(ctx *scanContext, caller *funcInfo, sel *ast.SelectorExpr) (string, bool) {
	methodName := sel.Sel.Name

	// Case 1: s.field.Method() — chained selector on a struct field.
	if innerSel, ok := sel.X.(*ast.SelectorExpr); ok {
		if ident, ok := innerSel.X.(*ast.Ident); ok {
			// ident is the receiver variable (e.g. "h", "s").
			// innerSel.Sel is the field name (e.g. "service", "queries").
			fieldName := innerSel.Sel.Name

			// Look up the field type from the receiver's struct definition.
			fieldType := s.resolveFieldType(ctx, caller.receiver, ident.Name, fieldName)
			if fieldType != "" {
				calleeID := fieldType + "." + methodName
				if _, ok := ctx.funcLookup[calleeID]; ok {
					return calleeID, false
				}
				// Check SQLC — the field type might be the generated Queries type.
				if strings.Contains(fieldType, "Queries") || strings.HasSuffix(fieldType, "generated.Queries") {
					if _, ok := ctx.sqlcMethods[methodName]; ok {
						return fieldType + "." + methodName, false
					}
				}
				// Interface resolution: fieldType is "services.AuthService" but
				// the concrete impl is "authServiceImpl.Login". Try fuzzy match
				// by method name across all discovered nodes of compatible kind.
				if resolved := s.fuzzyResolveMethod(ctx, fieldType, methodName); resolved != "" {
					return resolved, false
				}
				return calleeID, true // ambiguous: type resolved but target not found
			}
		}
	}

	// Case 2: x.Method() — simple selector.
	if ident, ok := sel.X.(*ast.Ident); ok {
		varName := ident.Name

		// 2a: The variable is the receiver itself (e.g. r.handleLogin()).
		if caller.receiver != "" && (varName == "r" || varName == "s" || varName == "h" || varName == "a") {
			calleeID := caller.receiver + "." + methodName
			if _, ok := ctx.funcLookup[calleeID]; ok {
				return calleeID, false
			}
		}

		// 2b: Look up the variable as a struct field of the receiver.
		fieldType := s.resolveFieldType(ctx, caller.receiver, "", varName)
		if fieldType != "" {
			calleeID := fieldType + "." + methodName
			if _, ok := ctx.funcLookup[calleeID]; ok {
				return calleeID, false
			}
			// Interface resolution: try fuzzy match by method name.
			if resolved := s.fuzzyResolveMethod(ctx, fieldType, methodName); resolved != "" {
				return resolved, false
			}
			return calleeID, true
		}

		// 2c: Might be a package-level call: pkg.Function().
		calleeID := varName + "." + methodName
		if _, ok := ctx.funcLookup[calleeID]; ok {
			return calleeID, false
		}

		// 2d: Might be a method on an interface with the same name pattern.
		// Try matching any known struct type whose name contains the variable name.
		return s.fuzzyResolve(ctx, varName, methodName)
	}

	return "", false
}

// resolveFieldType looks up the type of a field in a struct.
//
// If receiverVarName is non-empty, it checks if receiverVarName matches common
// receiver names (h, s, r, a) for the given receiverType. If fieldAsVar is
// non-empty and receiverVarName is empty, it looks up fieldAsVar directly
// in the receiver's struct fields.
func (s *GoASTScanner) resolveFieldType(ctx *scanContext, receiverType, receiverVarName, fieldName string) string {
	if receiverType == "" {
		return ""
	}

	fields, ok := ctx.structFields[receiverType]
	if !ok {
		return ""
	}

	typeStr, ok := fields[fieldName]
	if !ok {
		return ""
	}

	// Strip pointer prefix for lookup.
	typeStr = strings.TrimPrefix(typeStr, "*")

	// Strip package qualifier if present (e.g. "generated.Queries" → "Queries"
	// for SQLC, but keep the full form for lookup).
	return typeStr
}

// fuzzyResolveMethod resolves an interface-typed field call to its concrete
// implementation. For example, fieldType="services.AuthService" and
// methodName="Login" → searches funcLookup for any "XxxImpl.Login" where
// Xxx relates to the interface name. Returns the resolved callee ID or "".
//
// Resolution priority:
//  1. DI bindings (Wire/Fx) — if an explicit interface→concrete mapping exists, use it
//  2. Fuzzy name matching — fall back to matching type names heuristically
func (s *GoASTScanner) fuzzyResolveMethod(ctx *scanContext, fieldType, methodName string) string {
	// Extract the short interface name: "services.AuthService" → "AuthService"
	shortIface := fieldType
	if idx := strings.LastIndex(fieldType, "."); idx >= 0 {
		shortIface = fieldType[idx+1:]
	}

	// Priority 1: Check DI bindings (Wire/Fx) for explicit resolution.
	if binding, ok := ctx.interfaceBindings[shortIface]; ok {
		concreteShort := shortTypeName(binding.Concrete)
		calleeID := concreteShort + "." + methodName
		if _, exists := ctx.funcLookup[calleeID]; exists {
			return calleeID
		}
	}

	// Priority 2: Fuzzy name matching.
	// Lowercase for fuzzy matching
	lowerIface := strings.ToLower(shortIface)

	var candidates []string
	for id := range ctx.funcLookup {
		parts := strings.SplitN(id, ".", 2)
		if len(parts) != 2 || parts[1] != methodName {
			continue
		}
		typeName := parts[0]
		lowerType := strings.ToLower(typeName)
		// Match if the concrete type name contains the interface name
		// e.g. "authServiceImpl" contains "authservice"
		if strings.Contains(lowerType, lowerIface) {
			candidates = append(candidates, id)
		}
	}

	if len(candidates) == 1 {
		return candidates[0]
	}
	// Multiple candidates — try to disambiguate by preferring the one whose
	// package path matches the interface's package prefix.
	// e.g., fieldType="services.RecipeService" → prefer candidates in "services" package.
	if len(candidates) > 1 {
		pkgPrefix := ""
		if idx := strings.LastIndex(fieldType, "."); idx >= 0 {
			pkgPrefix = strings.ToLower(fieldType[:idx])
		}
		if pkgPrefix != "" {
			var pkgMatches []string
			for _, c := range candidates {
				info, ok := ctx.funcLookup[c]
				if ok && strings.Contains(strings.ToLower(info.node.File), pkgPrefix) {
					pkgMatches = append(pkgMatches, c)
				}
			}
			if len(pkgMatches) == 1 {
				return pkgMatches[0]
			}
		}
	}
	return ""
}

// fuzzyResolve tries to find a matching function when exact resolution fails.
// It checks whether any known receiver type name contains the variable name
// (case-insensitive) and has the requested method.
func (s *GoASTScanner) fuzzyResolve(ctx *scanContext, varName, methodName string) (string, bool) {
	lowerVar := strings.ToLower(varName)

	for id := range ctx.funcLookup {
		parts := strings.SplitN(id, ".", 2)
		if len(parts) != 2 {
			continue
		}
		typeName, method := parts[0], parts[1]
		if method != methodName {
			continue
		}
		if strings.Contains(strings.ToLower(typeName), lowerVar) {
			return id, true // ambiguous because it was fuzzy matched
		}
	}

	return "", false
}

// shouldIgnore checks if a callee should be ignored based on configuration.
func (s *GoASTScanner) shouldIgnore(ctx *scanContext, calleeID string) bool {
	for _, glob := range ctx.ignoreFuncGlobs {
		if matchGlob(glob, calleeID) {
			return true
		}
	}

	// Check package-level ignores for the callee.
	parts := strings.SplitN(calleeID, ".", 2)
	if len(parts) == 2 {
		if ctx.ignorePackages[parts[0]] {
			return true
		}
	}

	return false
}

// matchGlob performs simple glob matching with * wildcard support.
// Supports patterns like "*.String", "*.Error", "fmt.*".
func matchGlob(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}

	// Split on * and check prefix/suffix.
	parts := strings.SplitN(pattern, "*", 2)
	prefix, suffix := parts[0], parts[1]

	if prefix != "" && !strings.HasPrefix(s, prefix) {
		return false
	}
	if suffix != "" && !strings.HasSuffix(s, suffix) {
		return false
	}

	return true
}

// extractMethodName returns the method part of "Type.Method" or the string
// itself if there is no dot.
func extractMethodName(id string) string {
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// isExternalCall checks if a callee ID looks like an external package call.
var externalPrefixes = []string{
	"http.", "fmt.", "log.", "json.", "context.",
	"strings.", "strconv.", "time.", "errors.", "os.",
	"sync.", "sort.", "io.", "bytes.", "regexp.",
}

func isExternalCall(calleeID string) bool {
	for _, prefix := range externalPrefixes {
		if strings.HasPrefix(calleeID, prefix) {
			return true
		}
	}
	return false
}

// pruneUnreachable removes nodes and edges that are not reachable from the
// entry points. This keeps the BuildFrom graph focused.
func (s *GoASTScanner) pruneUnreachable(ctx *scanContext, entryPoints []string) {
	// Collect all reachable node IDs via BFS from entry points.
	reachable := make(map[string]bool)

	queue := make([]string, 0)
	for _, ep := range entryPoints {
		matches := s.findMatching(ctx, ep)
		for _, m := range matches {
			if !reachable[m] {
				reachable[m] = true
				queue = append(queue, m)
			}
		}
	}

	// Build temporary adjacency from edges.
	adj := make(map[string][]string)
	for _, e := range ctx.graph.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, next := range adj[current] {
			if !reachable[next] {
				reachable[next] = true
				queue = append(queue, next)
			}
		}
	}

	// Also keep endpoint nodes that reference reachable handlers.
	for _, e := range ctx.graph.Edges {
		if reachable[e.To] {
			reachable[e.From] = true
		}
	}

	// Prune nodes not in the reachable set.
	for id := range ctx.graph.Nodes {
		if !reachable[id] {
			delete(ctx.graph.Nodes, id)
		}
	}

	// Prune edges referencing pruned nodes.
	kept := make([]domain.Edge, 0, len(ctx.graph.Edges))
	for _, e := range ctx.graph.Edges {
		if reachable[e.From] && reachable[e.To] {
			kept = append(kept, e)
		}
	}
	ctx.graph.Edges = kept
}

// Compile-time interface compliance check.
var _ ports.GraphBuilder = (*GoASTScanner)(nil)
