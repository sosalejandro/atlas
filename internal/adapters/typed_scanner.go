package adapters

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"

	"github.com/sosalejandro/atlas/internal/domain"
	"github.com/sosalejandro/atlas/internal/ports"
)

// TypedScanner builds call graphs using golang.org/x/tools/go/packages for full
// type resolution. It produces the same graph format as GoASTScanner but resolves
// cross-package method calls exactly via TypesInfo rather than heuristics.
//
// If type checking fails (missing deps, broken build), it falls back silently
// to GoASTScanner with a warning on stderr.
//
// Loaded packages are cached so that repeated Build/BuildFrom calls for the same
// project root reuse the type-checked ASTs without re-invoking packages.Load.
type TypedScanner struct {
	fallback        *GoASTScanner
	frontendScanner *FrontendScanner
	sqlcMapper      *SQLCMapper
	cachedPkgs      []*packages.Package
	cachedFor       string // backendAbs path the cache was built for
}

// NewTypedScanner creates a TypedScanner with its fallback pre-wired.
func NewTypedScanner() *TypedScanner {
	return &TypedScanner{
		fallback:        NewGoASTScanner(),
		frontendScanner: NewFrontendScanner(),
		sqlcMapper:      NewSQLCMapper(),
	}
}

// LoadedPackages returns the cached type-checked packages, or nil if no
// packages have been loaded yet. This is used by the TypeExtractor to
// extract struct field information from Go types.
func (s *TypedScanner) LoadedPackages() []*packages.Package {
	return s.cachedPkgs
}

// Build constructs the full call graph using go/types for exact resolution.
func (s *TypedScanner) Build(projectRoot string, config ports.GraphConfig) (*domain.Graph, error) {
	graph, fallback, err := s.loadAndBuild(projectRoot, config, nil)
	if err != nil {
		return nil, err
	}
	if fallback {
		return s.fallback.Build(projectRoot, config)
	}
	return graph, nil
}

// BuildFrom constructs a partial graph starting from specific entry points,
// pruning unreachable nodes.
func (s *TypedScanner) BuildFrom(projectRoot string, entryPoints []string, config ports.GraphConfig) (*domain.Graph, error) {
	graph, fallback, err := s.loadAndBuild(projectRoot, config, entryPoints)
	if err != nil {
		return nil, err
	}
	if fallback {
		return s.fallback.BuildFrom(projectRoot, entryPoints, config)
	}

	// Prune unreachable nodes.
	pruneGraph(graph, entryPoints)

	return graph, nil
}

// loadAndBuild is the core implementation shared by Build and BuildFrom.
// If fallback is true, the caller should delegate to GoASTScanner.
// Loaded packages are cached so that subsequent calls for the same backend
// root reuse the type-checked ASTs without re-invoking packages.Load.
// parseGoWorkPatterns reads a go.work file and returns load patterns
// for each workspace module: ["./cmd/...", "./application/...", "./domain/...", ...]
func parseGoWorkPatterns(workDir string) []string {
	data, err := os.ReadFile(filepath.Join(workDir, "go.work"))
	if err != nil {
		return nil
	}

	var patterns []string
	inUseBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "use (" {
			inUseBlock = true
			continue
		}
		if trimmed == ")" {
			inUseBlock = false
			continue
		}
		if inUseBlock && trimmed != "" && !strings.HasPrefix(trimmed, "//") {
			// "./application" → "./application/..."
			mod := strings.TrimPrefix(trimmed, "./")
			patterns = append(patterns, "./"+mod+"/...")
		}
	}
	return patterns
}

// findGoWorkDir walks upward from dir looking for a go.work file.
// If found, returns the directory containing it. Otherwise returns dir unchanged.
func findGoWorkDir(dir string) string {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, "go.work")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return dir
}

func (s *TypedScanner) loadAndBuild(projectRoot string, config ports.GraphConfig, entryPoints []string) (graph *domain.Graph, fallback bool, err error) {
	backendAbs := filepath.Join(projectRoot, config.BackendRoot)

	// Reuse cached packages if available for the same backend root.
	pkgs := s.cachedPkgs
	if pkgs == nil || s.cachedFor != backendAbs {
		// Use the directory containing go.work if it exists (workspace mode),
		// otherwise use the backend root directly.
		loadDir := findGoWorkDir(backendAbs)

		cfg := &packages.Config{
			Mode: packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo |
				packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps,
			Dir: loadDir,
		}

		// Determine load patterns. In workspace mode (go.work), ./... doesn't work;
		// we must list each workspace module's ./module/... pattern explicitly.
		patterns := []string{"./..."}
		if _, err := os.Stat(filepath.Join(loadDir, "go.work")); err == nil {
			if wsPatterns := parseGoWorkPatterns(loadDir); len(wsPatterns) > 0 {
				patterns = wsPatterns
			}
		}

		pkgs, err = packages.Load(cfg, patterns...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: type checking failed, falling back to AST scanner: %v\n", err)
			return nil, true, nil
		}

		// Check for package-level errors. If any package has errors, fall back.
		for _, pkg := range pkgs {
			if len(pkg.Errors) > 0 {
				fmt.Fprintf(os.Stderr, "warning: type errors in %s, falling back to AST scanner: %v\n",
					pkg.PkgPath, pkg.Errors[0])
				return nil, true, nil
			}
		}

		// Cache the successfully loaded packages.
		s.cachedPkgs = pkgs
		s.cachedFor = backendAbs
	}

	graph = domain.NewGraph()

	// SQLC pre-resolution (same as GoASTScanner).
	var sqlcMethods map[string]SQLCMapping
	if config.SQLCConfig != "" {
		mappings, err := s.sqlcMapper.Map(projectRoot, config.SQLCConfig)
		if err == nil {
			sqlcMethods = mappings
		}
	}

	// Start frontend scan in parallel.
	var frontendResult *FrontendScanResult
	var frontendErr error
	var wg sync.WaitGroup

	if len(config.FrontendRoots) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frontendResult, frontendErr = s.frontendScanner.Scan(projectRoot)
		}()
	}

	// Build a map of funcID → enclosing FuncDecl position for edge creation.
	type funcEntry struct {
		id       string
		pkg      *packages.Package
		funcDecl *ast.FuncDecl
		fset     *token.FileSet
	}

	funcLookup := make(map[string]*funcEntry)

	// Phase 1: Create nodes from function declarations.
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			fset := pkg.Fset
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name == nil {
					continue
				}

				// Skip test functions.
				fileName := fset.Position(fn.Pos()).Filename
				if strings.HasSuffix(filepath.Base(fileName), "_test.go") {
					continue
				}

				receiver := receiverTypeName(fn)
				funcName := fn.Name.Name

				var id string
				if receiver != "" {
					id = receiver + "." + funcName
				} else {
					id = file.Name.Name + "." + funcName
				}

				// Compute relative file path and package dir for node kind.
				relPath, _ := filepath.Rel(projectRoot, fileName)
				relPath = filepath.ToSlash(relPath)

				pkgDir, _ := filepath.Rel(projectRoot, filepath.Dir(fileName))
				pkgDir = filepath.ToSlash(pkgDir)

				kind := classifyNodeKind(pkgDir, config.LayerRules)
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

				graph.AddNode(node)

				funcLookup[id] = &funcEntry{
					id:       id,
					pkg:      pkg,
					funcDecl: fn,
					fset:     fset,
				}
			}
		}
	}

	// Phase 2: Resolve calls using TypesInfo for exact resolution.
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			fset := pkg.Fset

			// Skip test files.
			fileName := fset.Position(file.Pos()).Filename
			if strings.HasSuffix(filepath.Base(fileName), "_test.go") {
				continue
			}

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name == nil || fn.Body == nil {
					continue
				}

				receiver := receiverTypeName(fn)
				funcName := fn.Name.Name

				var callerID string
				if receiver != "" {
					callerID = receiver + "." + funcName
				} else {
					callerID = file.Name.Name + "." + funcName
				}

				// Walk the function body for call expressions.
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}

					calleeID := resolveCalleeFromTypes(pkg.TypesInfo, call)
					if calleeID == "" {
						return true
					}

					// Check SQLC mappings.
					if sqlcMethods != nil {
						methodName := extractMethodName(calleeID)
						if sqlcMapping, ok := sqlcMethods[methodName]; ok {
							queryNodeID := "sql:" + sqlcMapping.QueryName
							queryNode := &domain.Node{
								ID:   queryNodeID,
								Kind: domain.NodeQuery,
								File: sqlcMapping.SQLFile,
								Line: sqlcMapping.SQLLine,
								Doc:  fmt.Sprintf("SQLC query: %s (:%s)", sqlcMapping.QueryName, sqlcMapping.QueryType),
							}
							graph.AddNode(queryNode)
							graph.AddEdge(callerID, queryNodeID)
							return true
						}
					}

					// Only add edges to known nodes.
					if _, exists := funcLookup[calleeID]; exists {
						graph.AddEdge(callerID, calleeID)
					}

					return true
				})
			}
		}
	}

	// Wait for frontend scan and merge.
	wg.Wait()
	if frontendErr != nil {
		fmt.Fprintf(os.Stderr, "warning: frontend scan failed: %v\n", frontendErr)
	} else if frontendResult != nil {
		s.frontendScanner.MergeIntoGraph(graph, frontendResult)
	}

	return graph, false, nil
}

// resolveCalleeFromTypes resolves a call expression to a callee ID using
// go/types information. This provides exact resolution for:
//   - Package-level function calls (fmt.Println)
//   - Method calls on concrete types (s.repo.FindUser)
//   - Method calls through interfaces (resolved to the interface method)
//   - Cross-package calls that go/ast heuristics can't resolve
func resolveCalleeFromTypes(info *types.Info, call *ast.CallExpr) string {
	if info == nil {
		return ""
	}

	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return resolveSelectorFromTypes(info, fn)

	case *ast.Ident:
		// Package-level function call.
		if obj, ok := info.Uses[fn]; ok {
			return qualifiedFuncName(obj)
		}
		return ""

	default:
		return ""
	}
}

// resolveSelectorFromTypes resolves a selector expression (x.Method) using
// TypesInfo.Selections for method calls and TypesInfo.Uses for package-qualified calls.
func resolveSelectorFromTypes(info *types.Info, sel *ast.SelectorExpr) string {
	// Check TypesInfo.Selections first — this handles method calls (x.Method()).
	if selection, ok := info.Selections[sel]; ok {
		obj := selection.Obj()
		recv := selection.Recv()
		recvName := extractRecvTypeName(recv)
		if recvName != "" {
			return recvName + "." + obj.Name()
		}
		return qualifiedFuncName(obj)
	}

	// Check TypesInfo.Uses — this handles package-qualified calls (pkg.Func()).
	if obj, ok := info.Uses[sel.Sel]; ok {
		return qualifiedFuncName(obj)
	}

	return ""
}

// extractRecvTypeName extracts the type name from a types.Type, unwrapping
// pointers and named types to get the short struct/interface name.
func extractRecvTypeName(t types.Type) string {
	// Unwrap pointer types.
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}

	switch typ := t.(type) {
	case *types.Named:
		return typ.Obj().Name()
	default:
		return ""
	}
}

// qualifiedFuncName builds a node ID from a types.Object.
// For methods, it returns "RecvType.MethodName".
// For package-level functions, it returns "pkgName.FuncName".
func qualifiedFuncName(obj types.Object) string {
	if obj == nil {
		return ""
	}

	fn, ok := obj.(*types.Func)
	if !ok {
		return ""
	}

	name := fn.Name()

	// Check if this is a method (has a receiver via the signature).
	sig, ok := fn.Type().(*types.Signature)
	if ok && sig.Recv() != nil {
		recvName := extractRecvTypeName(sig.Recv().Type())
		if recvName != "" {
			return recvName + "." + name
		}
	}

	// Package-level function — use the package name (short, not full path).
	if pkg := fn.Pkg(); pkg != nil {
		return pkg.Name() + "." + name
	}

	return name
}

// pruneGraph removes nodes and edges not reachable from the given entry points.
func pruneGraph(graph *domain.Graph, entryPoints []string) {
	reachable := make(map[string]bool)
	queue := make([]string, 0)

	// Seed with entry points (exact + suffix match).
	for _, ep := range entryPoints {
		for id := range graph.Nodes {
			if id == ep || strings.HasSuffix(id, "."+ep) || strings.Contains(id, ep) {
				if !reachable[id] {
					reachable[id] = true
					queue = append(queue, id)
				}
			}
		}
	}

	// Build adjacency for BFS.
	adj := make(map[string][]string)
	for _, e := range graph.Edges {
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
	for _, e := range graph.Edges {
		if reachable[e.To] {
			reachable[e.From] = true
		}
	}

	// Prune unreachable nodes.
	for id := range graph.Nodes {
		if !reachable[id] {
			delete(graph.Nodes, id)
		}
	}

	// Prune edges referencing pruned nodes.
	kept := make([]domain.Edge, 0, len(graph.Edges))
	for _, e := range graph.Edges {
		if reachable[e.From] && reachable[e.To] {
			kept = append(kept, e)
		}
	}
	graph.Edges = kept
}

// Compile-time interface compliance check.
var _ ports.GraphBuilder = (*TypedScanner)(nil)
