package goscan

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Result is the output of Scan.
//
// Graph is the in-memory call graph (nodes + edges). Symbols is a flat
// denormalised list of every Node's embedded shared.Symbol — useful for
// callers (store/, contract/) that want the symbols without walking the
// graph. The two views are derived from the same underlying records, so a
// round-trip through either is lossless.
type Result struct {
	Graph    *graph.Graph     `json:"graph"`
	Symbols  []shared.Symbol  `json:"symbols"`
	Warnings []string         `json:"warnings,omitempty"`
}

// Scan runs the 4-phase Go AST scan on rootDir and returns a Result.
//
// The scan is single-pass for now (no parallelism inside this package).
// Callers that want frontend-parallel scans can run codeindex/ts/.Scan in
// a goroutine alongside this one — they share no state.
func Scan(ctx context.Context, rootDir string, opts Options) (*Result, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("goscan: rootDir is required")
	}
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("goscan: abs rootDir: %w", err)
	}

	if opts.Logger == nil {
		opts.Logger = shared.NopLogger{}
	}
	backendRel := opts.BackendRoot
	if backendRel == "" {
		backendRel = "."
	}
	backendAbs := filepath.Join(abs, backendRel)

	ctx2 := newScanContext(abs, backendAbs, opts)

	// Phase 1: Route discovery (uses pre-supplied routes; no parsing here).
	if err := ctx2.discoverRoutes(); err != nil {
		opts.Logger.Warn(ctx, "route discovery", "err", err)
	}

	// Phase 2: Function discovery.
	if err := ctx2.discoverFunctions(ctx); err != nil {
		return nil, fmt.Errorf("function discovery: %w", err)
	}

	// Phase 2.5: Resolve unresolved handler references.
	ctx2.resolveHandlerRefs()

	// Phase 3: Call graph extraction.
	if len(opts.EntryPoints) > 0 {
		ctx2.extractCallsFrom(opts.EntryPoints)
		ctx2.pruneUnreachable(opts.EntryPoints)
	} else {
		ctx2.extractCalls()
	}

	// Materialise the flat Symbol view.
	symbols := make([]shared.Symbol, 0, len(ctx2.graph.Nodes))
	for _, n := range ctx2.graph.Nodes {
		symbols = append(symbols, n.Symbol)
	}

	return &Result{
		Graph:    ctx2.graph,
		Symbols:  symbols,
		Warnings: ctx2.warnings,
	}, nil
}

// scanContext holds mutable state for a single Scan call.
//
// Mirrors the original testreg scanContext one-to-one — same fields, same
// semantics — but typed with shared.SymbolID for graph keys instead of
// bare strings.
type scanContext struct {
	graph       *graph.Graph
	opts        Options
	projectRoot string
	backendAbs  string

	// Function lookup: SymbolID ("ReceiverType.MethodName" or "pkg.FuncName")
	// → funcInfo. Populated in Phase 2 (discoverFunctions), consumed in
	// Phase 2.5 (resolveHandlerRefs) and Phase 3 (extractCalls).
	funcLookup map[shared.SymbolID]*funcInfo

	// Struct field types: StructName → fieldName → fieldType (string form).
	structFields map[string]map[string]string

	// Pre-resolved hooks.
	sqlcMethods           map[string]SQLCMapping
	interfaceBindings     map[string]InterfaceBinding
	apiAnnotatedEndpoints map[shared.SymbolID]bool

	// Pre-compiled ignore patterns.
	ignorePackages  map[string]bool
	ignoreFuncGlobs []string

	warnings []string
}

// funcInfo is the per-function AST handle the scanner uses during Phase 3
// (extractCalls). It carries the AST node + a back-pointer to its file so
// call resolution can package-qualify plain function calls.
type funcInfo struct {
	node     *graph.Node
	funcDecl *ast.FuncDecl
	fset     *token.FileSet
	file     *ast.File
	receiver string // empty for plain functions
}

func newScanContext(projectRoot, backendAbs string, opts Options) *scanContext {
	ignorePkgs := make(map[string]bool, len(opts.IgnorePackages))
	for _, p := range opts.IgnorePackages {
		ignorePkgs[p] = true
	}
	sqlcMethods := opts.SQLCMethods
	if sqlcMethods == nil {
		sqlcMethods = map[string]SQLCMapping{}
	}
	bindings := opts.InterfaceBindings
	if bindings == nil {
		bindings = map[string]InterfaceBinding{}
	}
	return &scanContext{
		graph:                 graph.New(),
		opts:                  opts,
		projectRoot:           projectRoot,
		backendAbs:            backendAbs,
		funcLookup:            make(map[shared.SymbolID]*funcInfo),
		structFields:          make(map[string]map[string]string),
		sqlcMethods:           sqlcMethods,
		interfaceBindings:     bindings,
		apiAnnotatedEndpoints: make(map[shared.SymbolID]bool),
		ignorePackages:        ignorePkgs,
		ignoreFuncGlobs:       opts.IgnoreFunctions,
	}
}

// ---------------------------------------------------------------------------
// Phase 1: Route discovery (uses pre-supplied routes)
// ---------------------------------------------------------------------------

func (c *scanContext) discoverRoutes() error {
	for _, r := range c.opts.Routes {
		endpointID := shared.SymbolID(fmt.Sprintf("%s %s", r.Method, r.Path))
		relFile := r.File
		if filepath.IsAbs(relFile) {
			if rel, err := filepath.Rel(c.projectRoot, relFile); err == nil {
				relFile = rel
			}
		}
		relFile = filepath.ToSlash(relFile)

		c.graph.AddNode(&graph.Node{
			Symbol: shared.Symbol{
				ID:       endpointID,
				Kind:     shared.KindEndpoint,
				Position: shared.FilePosition{Path: relFile, Line: r.Line},
			},
		})
		handlerID := normaliseHandlerRef(r.Handler)
		if handlerID == "" {
			continue
		}
		c.graph.AddNode(&graph.Node{
			Symbol: shared.Symbol{
				ID:   shared.SymbolID(handlerID),
				Kind: shared.KindHandler,
			},
		})
		c.graph.AddEdge(endpointID, shared.SymbolID(handlerID))
	}
	return nil
}

// normaliseHandlerRef converts a route handler expression like
// "h.authHandler.Login" into a SymbolID-shape "authHandler.Login".
func normaliseHandlerRef(handler string) string {
	handler = strings.TrimSuffix(handler, "(...)")
	handler = strings.TrimPrefix(handler, "<func>")
	if handler == "" || handler == "<unknown>" {
		return ""
	}
	parts := strings.Split(handler, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return handler
}

// ---------------------------------------------------------------------------
// Phase 2: Function discovery
// ---------------------------------------------------------------------------

func (c *scanContext) discoverFunctions(ctx context.Context) error {
	if err := filepath.WalkDir(c.backendAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			relDir, _ := filepath.Rel(c.backendAbs, path)
			relDir = filepath.ToSlash(relDir)
			if c.ignorePackages[relDir] || c.ignorePackages[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		// Test files are included by default — see Options.SkipTests godoc:
		// Atlas's feature-attribution workflow relies on `_test.go` because
		// that's where `@atlas:feature` / `@testreg` annotations live.
		if c.opts.SkipTests && strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		relPath, _ := filepath.Rel(c.projectRoot, path)
		relPath = filepath.ToSlash(relPath)
		if strings.Contains(relPath, "/generated/") {
			return nil
		}
		return c.parseFile(ctx, path, relPath)
	}); err != nil {
		return fmt.Errorf("walk %s: %w", c.backendAbs, err)
	}
	return nil
}

func (c *scanContext) parseFile(ctx context.Context, absPath, relPath string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		c.warnings = append(c.warnings, fmt.Sprintf("parse %s: %v", relPath, err))
		return nil // graceful skip
	}

	// Per-file package directory (for layer classification).
	relFromBackend, _ := filepath.Rel(c.backendAbs, absPath)
	pkgDir := filepath.ToSlash(filepath.Dir(relFromBackend))

	c.extractStructFields(file)

	// Parse @api annotations to discover endpoint → handler edges. We feed
	// the file through packages/codeindex/annotations rather than reading
	// the raw lines a second time.
	apis, _ := annotations.ParseRelative(ctx, absPath, relPath)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		c.registerFunction(fn, fset, file, relPath, pkgDir)

		// Match each @api annotation to the next function declaration
		// (legacy ParseAnnotatedSource semantics ported into the annotations
		// pkg + matched here based on line proximity).
		funcLine := fset.Position(fn.Pos()).Line
		for _, a := range apis {
			if a.Kind != shared.AnnAPI {
				continue
			}
			// @api comments precede the function: line must be before the
			// declaration but within the immediately-preceding doc block.
			if a.Position.Line >= funcLine || funcLine-a.Position.Line > 10 {
				continue
			}
			endpointID := shared.SymbolID(a.Method + " " + a.Path)
			c.graph.AddNode(&graph.Node{
				Symbol: shared.Symbol{
					ID:       endpointID,
					Kind:     shared.KindEndpoint,
					Position: shared.FilePosition{Path: relPath, Line: funcLine},
				},
			})
			handlerID := c.funcDeclGraphID(fn, file)
			c.graph.AddEdge(endpointID, handlerID)
			c.apiAnnotatedEndpoints[endpointID] = true
		}
	}
	return nil
}

func (c *scanContext) funcDeclGraphID(fn *ast.FuncDecl, file *ast.File) shared.SymbolID {
	if recv := receiverTypeName(fn); recv != "" {
		return shared.SymbolID(recv + "." + fn.Name.Name)
	}
	return shared.SymbolID(file.Name.Name + "." + fn.Name.Name)
}

func (c *scanContext) registerFunction(fn *ast.FuncDecl, fset *token.FileSet, file *ast.File, relPath, pkgDir string) {
	if fn.Name == nil {
		return
	}
	if !fn.Name.IsExported() {
		// Per the original scanner: unexported plain functions are skipped
		// (low signal), but unexported METHODS are kept (they may be called
		// internally by other methods on the same receiver).
		if fn.Recv == nil {
			return
		}
	}
	receiver := receiverTypeName(fn)
	funcName := fn.Name.Name

	var id shared.SymbolID
	if receiver != "" {
		id = shared.SymbolID(receiver + "." + funcName)
	} else {
		id = shared.SymbolID(file.Name.Name + "." + funcName)
	}

	kind := classifyNodeKind(pkgDir, c.opts.LayerRules)
	line := fset.Position(fn.Pos()).Line

	doc := ""
	if fn.Doc != nil {
		doc = strings.TrimSpace(fn.Doc.Text())
	}

	node := &graph.Node{
		Symbol: shared.Symbol{
			ID:        id,
			Kind:      kind,
			Position:  shared.FilePosition{Path: relPath, Line: line},
			Doc:       doc,
			Signature: buildSignature(fn),
			Package:   pkgDir,
		},
	}
	c.graph.AddNode(node)
	c.funcLookup[id] = &funcInfo{
		node:     node,
		funcDecl: fn,
		fset:     fset,
		file:     file,
		receiver: receiver,
	}
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if ident, ok := t.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func classifyNodeKind(pkgDir string, rules LayerRules) shared.SymbolKind {
	lower := strings.ToLower(pkgDir)
	for _, p := range rules.Handler {
		if strings.Contains(lower, strings.ToLower(p)) {
			return shared.KindHandler
		}
	}
	for _, p := range rules.Repository {
		if strings.Contains(lower, strings.ToLower(p)) {
			return shared.KindRepository
		}
	}
	for _, p := range rules.Service {
		if strings.Contains(lower, strings.ToLower(p)) {
			return shared.KindService
		}
	}
	for _, p := range rules.Query {
		if strings.Contains(lower, strings.ToLower(p)) {
			return shared.KindQuery
		}
	}
	switch {
	case strings.Contains(lower, "handler") || strings.Contains(lower, "resolver"):
		return shared.KindHandler
	case strings.Contains(lower, "persistence") || strings.Contains(lower, "repository"):
		return shared.KindRepository
	case strings.Contains(lower, "service"):
		return shared.KindService
	case strings.Contains(lower, "generated"):
		return shared.KindQuery
	default:
		return shared.KindService
	}
}

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

func (c *scanContext) extractStructFields(file *ast.File) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := make(map[string]string)
			for _, field := range st.Fields.List {
				ft := typeExprString(field.Type)
				for _, name := range field.Names {
					fields[name.Name] = ft
				}
			}
			c.structFields[ts.Name.Name] = fields
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 2.5: Resolve placeholder handler refs from Phase 1
// ---------------------------------------------------------------------------

func (c *scanContext) resolveHandlerRefs() {
	var placeholders []shared.SymbolID
	for id, node := range c.graph.Nodes {
		if node.Kind == shared.KindHandler && node.Position.Path == "" {
			placeholders = append(placeholders, id)
		}
	}
	for _, placeholderID := range placeholders {
		// If an @api-annotated endpoint already targets this placeholder,
		// the placeholder is redundant — remove it and the route-parser
		// edge it created.
		hasAPISource := false
		for _, e := range c.graph.Edges {
			if e.To == placeholderID && c.apiAnnotatedEndpoints[e.From] {
				hasAPISource = true
				break
			}
		}
		if hasAPISource {
			c.removePlaceholder(placeholderID)
			continue
		}
		methodName := extractMethodName(string(placeholderID))
		if methodName == "" {
			continue
		}
		suffix := "." + methodName
		var matches []shared.SymbolID
		for id, info := range c.funcLookup {
			if strings.HasSuffix(string(id), suffix) && info.node.Kind == shared.KindHandler {
				lower := strings.ToLower(info.node.Position.Path)
				if strings.Contains(lower, "test") || strings.Contains(lower, "mock") {
					continue
				}
				matches = append(matches, id)
			}
		}
		switch len(matches) {
		case 1:
			c.graph.MergeNode(placeholderID, c.funcLookup[matches[0]].node)
		case 0:
			// Broaden across all kinds.
			for id := range c.funcLookup {
				if strings.HasSuffix(string(id), suffix) {
					matches = append(matches, id)
				}
			}
			if len(matches) == 1 {
				c.graph.MergeNode(placeholderID, c.funcLookup[matches[0]].node)
			}
		default:
			for i := range c.graph.Edges {
				if c.graph.Edges[i].To == placeholderID {
					c.graph.Edges[i].Ambiguous = true
				}
			}
		}
	}
}

func (c *scanContext) removePlaceholder(placeholderID shared.SymbolID) {
	delete(c.graph.Nodes, placeholderID)
	kept := make([]graph.Edge, 0, len(c.graph.Edges))
	for _, e := range c.graph.Edges {
		if e.To == placeholderID || e.From == placeholderID {
			continue
		}
		kept = append(kept, e)
	}
	c.graph.Edges = kept
	c.graph.InvalidateAdjacency()
}

// ---------------------------------------------------------------------------
// Phase 3: Call graph extraction
// ---------------------------------------------------------------------------

func (c *scanContext) extractCalls() {
	for _, info := range c.funcLookup {
		if info.funcDecl.Body == nil {
			continue
		}
		c.walkBody(info)
	}
}

func (c *scanContext) extractCallsFrom(entryPoints []shared.SymbolID) {
	visited := make(map[shared.SymbolID]bool)
	queue := make([]shared.SymbolID, 0, len(entryPoints))

	for _, ep := range entryPoints {
		for _, m := range c.findMatching(ep) {
			if !visited[m] {
				visited[m] = true
				queue = append(queue, m)
			}
		}
		// Also seed from endpoint→handler edges so endpoint entry points
		// follow through.
		for _, e := range c.graph.Edges {
			if e.From == ep && !visited[e.To] {
				visited[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		info, ok := c.funcLookup[current]
		if !ok || info.funcDecl.Body == nil {
			// Follow graph edges through non-function nodes too.
			for _, e := range c.graph.Edges {
				if e.From == current && !visited[e.To] {
					visited[e.To] = true
					queue = append(queue, e.To)
				}
			}
			continue
		}
		for _, calleeID := range c.walkBody(info) {
			if !visited[calleeID] {
				visited[calleeID] = true
				queue = append(queue, calleeID)
			}
		}
	}
}

func (c *scanContext) findMatching(entry shared.SymbolID) []shared.SymbolID {
	if _, ok := c.funcLookup[entry]; ok {
		return []shared.SymbolID{entry}
	}
	var matches []shared.SymbolID
	suffix := "." + string(entry)
	for id := range c.funcLookup {
		if strings.HasSuffix(string(id), suffix) || id == entry {
			matches = append(matches, id)
		}
	}
	for id := range c.graph.Nodes {
		if id == entry || strings.Contains(string(id), string(entry)) {
			matches = append(matches, id)
		}
	}
	return matches
}

func (c *scanContext) walkBody(info *funcInfo) []shared.SymbolID {
	var callees []shared.SymbolID
	ast.Inspect(info.funcDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		calleeID, ambiguous := c.resolveCall(info, call)
		if calleeID == "" {
			return true
		}
		if c.shouldIgnore(calleeID) {
			return true
		}
		// SQLC: callee method maps to a generated query.
		if sqlcMap, ok := c.sqlcMethods[extractMethodName(string(calleeID))]; ok {
			queryID := shared.SymbolID("sql:" + sqlcMap.QueryName)
			c.graph.AddNode(&graph.Node{
				Symbol: shared.Symbol{
					ID:       queryID,
					Kind:     shared.KindQuery,
					Position: shared.FilePosition{Path: sqlcMap.SQLFile, Line: sqlcMap.SQLLine},
					Doc:      fmt.Sprintf("SQLC query: %s (:%s)", sqlcMap.QueryName, sqlcMap.QueryType),
				},
			})
			if ambiguous {
				c.graph.AddAmbiguousEdge(info.node.ID, queryID)
			} else {
				c.graph.AddEdge(info.node.ID, queryID)
			}
			callees = append(callees, queryID)
			return true
		}
		if _, exists := c.funcLookup[calleeID]; exists {
			if ambiguous {
				c.graph.AddAmbiguousEdge(info.node.ID, calleeID)
			} else {
				c.graph.AddEdge(info.node.ID, calleeID)
			}
			callees = append(callees, calleeID)
		} else if isExternalCall(string(calleeID)) {
			c.graph.AddNode(&graph.Node{
				Symbol: shared.Symbol{ID: calleeID, Kind: shared.KindExternal},
			})
			c.graph.AddEdge(info.node.ID, calleeID)
		}
		return true
	})
	return callees
}

func (c *scanContext) resolveCall(caller *funcInfo, call *ast.CallExpr) (shared.SymbolID, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return c.resolveSelectorCall(caller, fn)
	case *ast.Ident:
		if caller.file != nil {
			id := shared.SymbolID(caller.file.Name.Name + "." + fn.Name)
			if _, ok := c.funcLookup[id]; ok {
				return id, false
			}
		}
		return "", false
	default:
		return "", false
	}
}

func (c *scanContext) resolveSelectorCall(caller *funcInfo, sel *ast.SelectorExpr) (shared.SymbolID, bool) {
	method := sel.Sel.Name

	// Case 1: x.field.Method() — chained selector.
	if innerSel, ok := sel.X.(*ast.SelectorExpr); ok {
		if ident, ok := innerSel.X.(*ast.Ident); ok {
			fieldName := innerSel.Sel.Name
			fieldType := c.resolveFieldType(caller.receiver, ident.Name, fieldName)
			if fieldType != "" {
				calleeID := shared.SymbolID(fieldType + "." + method)
				if _, ok := c.funcLookup[calleeID]; ok {
					return calleeID, false
				}
				if strings.Contains(fieldType, "Queries") {
					if _, ok := c.sqlcMethods[method]; ok {
						return shared.SymbolID(fieldType + "." + method), false
					}
				}
				if resolved := c.fuzzyResolveMethod(fieldType, method); resolved != "" {
					return resolved, false
				}
				return calleeID, true
			}
		}
	}

	// Case 2: x.Method()
	if ident, ok := sel.X.(*ast.Ident); ok {
		varName := ident.Name
		if caller.receiver != "" && (varName == "r" || varName == "s" || varName == "h" || varName == "a") {
			calleeID := shared.SymbolID(caller.receiver + "." + method)
			if _, ok := c.funcLookup[calleeID]; ok {
				return calleeID, false
			}
		}
		fieldType := c.resolveFieldType(caller.receiver, "", varName)
		if fieldType != "" {
			calleeID := shared.SymbolID(fieldType + "." + method)
			if _, ok := c.funcLookup[calleeID]; ok {
				return calleeID, false
			}
			if resolved := c.fuzzyResolveMethod(fieldType, method); resolved != "" {
				return resolved, false
			}
			return calleeID, true
		}
		calleeID := shared.SymbolID(varName + "." + method)
		if _, ok := c.funcLookup[calleeID]; ok {
			return calleeID, false
		}
		return c.fuzzyResolve(varName, method)
	}
	return "", false
}

func (c *scanContext) resolveFieldType(receiverType, _ /* receiverVar */, fieldName string) string {
	if receiverType == "" {
		return ""
	}
	fields, ok := c.structFields[receiverType]
	if !ok {
		return ""
	}
	t, ok := fields[fieldName]
	if !ok {
		return ""
	}
	return strings.TrimPrefix(t, "*")
}

func (c *scanContext) fuzzyResolveMethod(fieldType, method string) shared.SymbolID {
	shortIface := fieldType
	if idx := strings.LastIndex(fieldType, "."); idx >= 0 {
		shortIface = fieldType[idx+1:]
	}
	// Priority 1: DI bindings.
	if binding, ok := c.interfaceBindings[shortIface]; ok {
		concreteShort := shortTypeName(binding.Concrete)
		id := shared.SymbolID(concreteShort + "." + method)
		if _, ok := c.funcLookup[id]; ok {
			return id
		}
	}
	// Priority 2: fuzzy name matching.
	lowerIface := strings.ToLower(shortIface)
	var candidates []shared.SymbolID
	for id := range c.funcLookup {
		parts := strings.SplitN(string(id), ".", 2)
		if len(parts) != 2 || parts[1] != method {
			continue
		}
		if strings.Contains(strings.ToLower(parts[0]), lowerIface) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	if len(candidates) > 1 {
		pkgPrefix := ""
		if idx := strings.LastIndex(fieldType, "."); idx >= 0 {
			pkgPrefix = strings.ToLower(fieldType[:idx])
		}
		if pkgPrefix != "" {
			var pkgMatches []shared.SymbolID
			for _, cnd := range candidates {
				info, ok := c.funcLookup[cnd]
				if ok && strings.Contains(strings.ToLower(info.node.Position.Path), pkgPrefix) {
					pkgMatches = append(pkgMatches, cnd)
				}
			}
			if len(pkgMatches) == 1 {
				return pkgMatches[0]
			}
		}
	}
	return ""
}

func (c *scanContext) fuzzyResolve(varName, method string) (shared.SymbolID, bool) {
	lower := strings.ToLower(varName)
	for id := range c.funcLookup {
		parts := strings.SplitN(string(id), ".", 2)
		if len(parts) != 2 {
			continue
		}
		typeName, m := parts[0], parts[1]
		if m != method {
			continue
		}
		if strings.Contains(strings.ToLower(typeName), lower) {
			return id, true
		}
	}
	return "", false
}

func (c *scanContext) shouldIgnore(callee shared.SymbolID) bool {
	for _, glob := range c.ignoreFuncGlobs {
		if matchGlob(glob, string(callee)) {
			return true
		}
	}
	parts := strings.SplitN(string(callee), ".", 2)
	if len(parts) == 2 && c.ignorePackages[parts[0]] {
		return true
	}
	return false
}

func matchGlob(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
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

func extractMethodName(id string) string {
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

func shortTypeName(qualified string) string {
	qualified = strings.TrimPrefix(qualified, "*")
	if idx := strings.LastIndex(qualified, "."); idx >= 0 {
		return qualified[idx+1:]
	}
	return qualified
}

var externalPrefixes = []string{
	"http.", "fmt.", "log.", "json.", "context.",
	"strings.", "strconv.", "time.", "errors.", "os.",
	"sync.", "sort.", "io.", "bytes.", "regexp.",
}

func isExternalCall(callee string) bool {
	for _, p := range externalPrefixes {
		if strings.HasPrefix(callee, p) {
			return true
		}
	}
	return false
}

// pruneUnreachable removes nodes/edges not reachable from entryPoints.
// Mirrors the legacy semantics: traverse from each entry point via the
// outgoing graph, then keep endpoint nodes that reference reachable handlers.
func (c *scanContext) pruneUnreachable(entryPoints []shared.SymbolID) {
	reachable := make(map[shared.SymbolID]bool)
	queue := make([]shared.SymbolID, 0)
	for _, ep := range entryPoints {
		for _, m := range c.findMatching(ep) {
			if !reachable[m] {
				reachable[m] = true
				queue = append(queue, m)
			}
		}
	}
	adj := make(map[shared.SymbolID][]shared.SymbolID)
	for _, e := range c.graph.Edges {
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
	// Keep endpoints that reference reachable handlers (so trace
	// consumers can show the entry route alongside the call chain).
	for _, e := range c.graph.Edges {
		if reachable[e.To] {
			reachable[e.From] = true
		}
	}
	for id := range c.graph.Nodes {
		if !reachable[id] {
			delete(c.graph.Nodes, id)
		}
	}
	kept := make([]graph.Edge, 0, len(c.graph.Edges))
	for _, e := range c.graph.Edges {
		if reachable[e.From] && reachable[e.To] {
			kept = append(kept, e)
		}
	}
	c.graph.Edges = kept
	c.graph.InvalidateAdjacency()
}
