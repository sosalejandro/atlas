package goscan

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex/annotations"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/resolver"
	"github.com/sosalejandro/atlas/packages/shared"
)

// Result is the output of Scan.
//
// Graph is the in-memory call graph (nodes + edges). Symbols is a flat
// denormalised list of every Node's embedded shared.Symbol — useful for
// callers (store/, contract/) that want the symbols without walking the
// graph. The two views are derived from the same underlying records, so a
// round-trip through either is lossless.
//
// SkippedFiles is the exclusion ledger: every .go file the walk declined
// to index, with the rule that declined it. Coverage denominators depend
// on this set, so it is reported rather than left implicit — a repo can
// then say "12% of executed statements are in generated code" instead of
// "12% unattributable".
type Result struct {
	Graph        *graph.Graph    `json:"graph"`
	Symbols      []shared.Symbol `json:"symbols"`
	SkippedFiles []SkippedFile   `json:"skipped_files,omitempty"`
	Warnings     []string        `json:"warnings,omitempty"`

	// Resolution is what the type-checked resolver achieved (issue #87):
	// how many packages type-checked, which ones did not and why, and
	// what the load cost. Nil when Options.SkipTypedResolution was set —
	// no resolver ran, so there is nothing to report, as opposed to a
	// report saying nothing was resolved.
	Resolution *ResolutionReport `json:"resolution,omitempty"`
}

// SkipReason names the rule that excluded a file from the index.
type SkipReason string

const (
	// SkipGeneratedHeader is Go's own convention: a line matching
	// `^// Code generated .* DO NOT EDIT\.$` ahead of the package clause.
	// This is the rule that travels — it holds wherever the tool put its
	// output.
	SkipGeneratedHeader SkipReason = "generated-header"

	// SkipGeneratedGlob is a hit on Options.GeneratedGlobs — for codegen
	// that omits the header (protoc-gen-go plugins, some ORMs).
	SkipGeneratedGlob SkipReason = "generated-glob"

	// SkipGeneratedDir is the legacy directory rule: any path segment
	// named "generated". Kept for repos that rely on it, but it is the
	// weakest signal of the three — it depends on where output landed,
	// not on what wrote it.
	SkipGeneratedDir SkipReason = "generated-dir"

	// SkipIgnoredPackage is an Options.IgnorePackages match. Not generated
	// code, but excluded from the same denominator, so it is reported
	// through the same ledger.
	SkipIgnoredPackage SkipReason = "ignored-package"
)

// SkippedFile is one entry in the exclusion ledger. Path is
// rootDir-relative and slash-separated, matching shared.FilePosition.
type SkippedFile struct {
	Path   string     `json:"path"`
	Reason SkipReason `json:"reason"`
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

	// Phase 0: Type-checked resolution (issue #87). Runs before the walk
	// because the walk adopts the resolver's syntax trees: types.Info is
	// keyed by ast.Node pointers, so a file parsed twice resolves once.
	// Every failure here degrades to the AST path and is reported on
	// Result.Resolution rather than returned.
	ctx2.initTypedResolution(ctx)

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

	if ctx2.collisions > maxCollisionWarnings {
		ctx2.warnings = append(ctx2.warnings, fmt.Sprintf(
			"%d symbol name collisions total (%d listed); colliding declarations are indexed under package-qualified ids",
			ctx2.collisions, maxCollisionWarnings))
	}

	ctx2.finishResolutionReport()

	// Materialise the flat Symbol view.
	symbols := make([]shared.Symbol, 0, len(ctx2.graph.Nodes))
	for _, n := range ctx2.graph.Nodes {
		symbols = append(symbols, n.Symbol)
	}

	return &Result{
		Graph:        ctx2.graph,
		Symbols:      symbols,
		SkippedFiles: ctx2.skippedFiles,
		Warnings:     ctx2.warnings,
		Resolution:   ctx2.resolution,
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
	generatedGlobs  []string

	// Exclusion ledger, appended in filepath.WalkDir's lexical order so
	// two scans of the same tree produce identical records.
	skippedFiles []SkippedFile

	// byMethod indexes declarations by the segment after the FIRST dot of
	// their short id ("Type.Method" -> "Method"), and byLastSegment by the
	// segment after the LAST dot of any id. Both replace full scans of
	// funcLookup in the fuzzy resolvers and in handler-ref resolution, which
	// were O(symbols) per unresolved call - and, being map iterations, also
	// returned a different winner from run to run.
	byMethod      map[string][]shared.SymbolID
	byLastSegment map[string][]shared.SymbolID

	// idsByPkg maps pkgKey(pkgDir, shortID) → the SymbolID the declaration
	// was actually registered under. Consulted by call resolution so a call
	// prefers the callee declared in the caller's own package over a
	// same-named declaration in another one.
	idsByPkg map[string]shared.SymbolID

	// collisions counts short-name clashes seen during discovery, and
	// qualifiedIDs records the ids that exist only because of one — they are
	// the same declaration under a longer name, not a rival candidate.
	collisions   int
	qualifiedIDs map[shared.SymbolID]bool

	// typed is the go/packages view of the tree, or nil when typed
	// resolution was skipped or could not load. typedIDs maps a
	// resolver.ObjectKey onto the SymbolID this scan registered the
	// declaration under — the whole point of #87 is that call resolution
	// goes object → id and never name → id.
	typed    *resolver.Program
	typedIDs map[string]shared.SymbolID

	// resolution accumulates what to report about the typed pass;
	// indexedFiles / typedIndexedFiles count what the walk actually
	// indexed, which is the denominator a reader cares about.
	resolution        *ResolutionReport
	indexedFiles      int
	typedIndexedFiles int

	// pendingTypedWarnings are typed-resolution complaints held until the
	// walk has counted the Go files this scan actually indexed. See
	// scanContext.deferWarning.
	pendingTypedWarnings []string

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
	pkgDir   string // backend-relative package directory

	// typed reports that this declaration's file was type-checked, so
	// its call sites are resolved by the type checker and never by the
	// name ladder. It is per declaration rather than per scan because a
	// tree degrades per package (issue #87).
	typed bool
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
	c := &scanContext{
		graph:                 graph.New(),
		opts:                  opts,
		projectRoot:           projectRoot,
		backendAbs:            backendAbs,
		funcLookup:            make(map[shared.SymbolID]*funcInfo),
		idsByPkg:              make(map[string]shared.SymbolID),
		byMethod:              make(map[string][]shared.SymbolID),
		byLastSegment:         make(map[string][]shared.SymbolID),
		qualifiedIDs:          make(map[shared.SymbolID]bool),
		structFields:          make(map[string]map[string]string),
		sqlcMethods:           sqlcMethods,
		interfaceBindings:     bindings,
		apiAnnotatedEndpoints: make(map[shared.SymbolID]bool),
		ignorePackages:        ignorePkgs,
		ignoreFuncGlobs:       opts.IgnoreFunctions,
	}
	c.generatedGlobs = c.validGeneratedGlobs(opts.GeneratedGlobs)
	return c
}

// validGeneratedGlobs drops patterns path.Match cannot parse, warning once
// each. Rejecting the whole scan would be worse: a single typo in
// .atlas.yaml would take down every audit that reads it, whereas a warning
// keeps the remaining rules working and still tells the operator that the
// exclusion set is not what they wrote.
func (c *scanContext) validGeneratedGlobs(globs []string) []string {
	if len(globs) == 0 {
		return nil
	}
	kept := make([]string, 0, len(globs))
	for _, g := range globs {
		probe := strings.TrimPrefix(strings.TrimSuffix(g, "/"), "**/")
		if _, err := path.Match(probe, "probe.go"); err != nil {
			c.warnings = append(c.warnings,
				fmt.Sprintf("generated glob %q: %v", g, err))
			continue
		}
		kept = append(kept, g)
	}
	return kept
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
		// The route table hands us the handler as a STRING
		// ("h.orderHandler.Create") which normaliseHandlerRef reshapes
		// and resolveHandlerRefs later matches against symbols by
		// method-name suffix. No name is bound here at all, so this is
		// tier C — and recording it as anything stronger would file
		// the weakest edges atlas produces alongside its
		// scope-resolved calls.
		c.graph.AddEdgeTier(endpointID, shared.SymbolID(handlerID), graph.TierSyntactic)
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
	if err := filepath.WalkDir(c.backendAbs, func(abs string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return c.visitDir(abs, d)
		}
		return c.visitFile(ctx, abs, d)
	}); err != nil {
		return fmt.Errorf("walk %s: %w", c.backendAbs, err)
	}
	return nil
}

// visitDir applies the directory-level prunes. vendor/, node_modules/ and
// hidden directories are dropped without a ledger entry — they are not the
// project's own code, so they never belonged in the denominator.
func (c *scanContext) visitDir(abs string, d os.DirEntry) error {
	name := d.Name()
	if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
		return filepath.SkipDir
	}
	relDir := filepath.ToSlash(relOrSelf(c.backendAbs, abs))
	if c.ignorePackages[relDir] || c.ignorePackages[name] {
		c.recordIgnoredPackage(abs)
		return filepath.SkipDir
	}
	return nil
}

// visitFile decides whether one .go file is indexed, and records it on the
// ledger when it is not.
func (c *scanContext) visitFile(ctx context.Context, abs string, d os.DirEntry) error {
	if !strings.HasSuffix(d.Name(), ".go") {
		return nil
	}
	// Test files are included by default — see Options.SkipTests godoc:
	// Atlas's feature-attribution workflow relies on `_test.go` because
	// that's where `@atlas:feature` / `@testreg` annotations live.
	if c.opts.SkipTests && strings.HasSuffix(d.Name(), "_test.go") {
		return nil
	}
	relPath := filepath.ToSlash(relOrSelf(c.projectRoot, abs))
	// Under IncludeGenerated the classification cannot change the outcome,
	// so skip it entirely rather than pay a header read per file.
	if !c.opts.IncludeGenerated {
		if reason, generated := c.generatedReason(abs, relPath); generated {
			c.skippedFiles = append(c.skippedFiles,
				SkippedFile{Path: relPath, Reason: reason})
			return nil
		}
	}
	return c.parseFile(ctx, abs, relPath)
}

// recordIgnoredPackage enumerates the .go files under a pruned directory so
// the skip reaches the ledger as files rather than as a package name — the
// coverage denominator is counted in files. One readdir per subtree buys
// that; the parses being avoided cost orders of magnitude more.
func (c *scanContext) recordIgnoredPackage(dirAbs string) {
	_ = filepath.WalkDir(dirAbs, func(abs string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		c.skippedFiles = append(c.skippedFiles, SkippedFile{
			Path:   filepath.ToSlash(relOrSelf(c.projectRoot, abs)),
			Reason: SkipIgnoredPackage,
		})
		return nil
	})
}

// generatedReason classifies one file against the three generated-code
// rules, STRONGEST SIGNAL FIRST — header, then glob, then directory.
//
// Order matters because the reason is the product here: it is what the
// ledger reports and what `atlas doctor` explains a denominator with. A
// sqlc file inside a generated/ directory should be reported as
// generated-header — the rule that holds wherever the tool put its output —
// rather than as generated-dir, which only says where someone filed it.
// Ordering by cost instead would make the cheapest rule the loudest, and
// the explanation the least informative one available.
//
// The header check reads the first few kilobytes; every other rule is
// string work. That read is the price of a truthful reason.
func (c *scanContext) generatedReason(absPath, relPath string) (SkipReason, bool) {
	if c.hasGeneratedHeader(absPath, relPath) {
		return SkipGeneratedHeader, true
	}
	for _, glob := range c.generatedGlobs {
		if matchGeneratedGlob(glob, relPath) {
			return SkipGeneratedGlob, true
		}
	}
	if hasPathSegment(relPath, "generated") {
		return SkipGeneratedDir, true
	}
	return "", false
}

// generatedHeaderRe is Go's own marker for machine-written source
// (https://go.dev/s/generatedcode). Anchored at both ends: a sentence that
// merely mentions the convention inside a longer comment is not a claim
// that the file is generated.
var generatedHeaderRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// maxHeaderLines bounds the header probe. The convention puts the marker
// before the package clause and the probe stops there anyway, so the cap
// only bites on files with an unusually long licence preamble — where
// reading further costs more than the rule is worth.
const maxHeaderLines = 32

// hasGeneratedHeader reports whether the file carries the generated marker
// ahead of its package clause. A read failure is a warning, not an error:
// a file we cannot open is a file we cannot index either, and the parse
// step downstream will report it in its own voice.
func (c *scanContext) hasGeneratedHeader(absPath, relPath string) bool {
	f, err := os.Open(absPath)
	if err != nil {
		c.warnings = append(c.warnings,
			fmt.Sprintf("generated-header probe %s: %v", relPath, err))
		return false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for i := 0; i < maxHeaderLines && sc.Scan(); i++ {
		line := sc.Text()
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if generatedHeaderRe.MatchString(line) {
			return true
		}
	}
	return false
}

// matchGeneratedGlob reports whether relPath matches one
// Options.GeneratedGlobs pattern; that field's godoc documents the four
// shapes. The extra rules exist because path.Match cannot express
// "anywhere in the tree", and "anywhere in the tree" is how every codegen
// convention is actually written down: `*.pb.go`, not `**/**/*.pb.go`.
func matchGeneratedGlob(pattern, relPath string) bool {
	if pattern == "" {
		return false
	}
	if dir := strings.TrimSuffix(pattern, "/"); dir != pattern {
		return relPath == dir ||
			strings.HasPrefix(relPath, dir+"/") ||
			strings.Contains(relPath, "/"+dir+"/")
	}
	pattern = strings.TrimPrefix(pattern, "**/")
	if ok, err := path.Match(pattern, relPath); err == nil && ok {
		return true
	}
	if strings.Contains(pattern, "/") {
		return false
	}
	// A pattern naming no directory is a filename convention, so it has to
	// reach files at any depth.
	ok, err := path.Match(pattern, path.Base(relPath))
	return err == nil && ok
}

func hasPathSegment(relPath, segment string) bool {
	for _, part := range strings.Split(relPath, "/") {
		if part == segment {
			return true
		}
	}
	return false
}

// relOrSelf falls back to the target path when it is not under base. That
// only happens for symlinked trees, where an absolute path in the ledger
// is still more useful than an empty one.
func relOrSelf(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

// syntaxFor returns the syntax tree the scan should index for a file,
// and whether it came from the type checker.
//
// A type-checked file is ADOPTED, not re-parsed. types.Info is keyed by
// ast.Node pointers, so parsing the file again would produce a tree that
// looks identical and resolves nothing — the single most likely way to
// wire this up and have every typed lookup silently miss.
func (c *scanContext) syntaxFor(absPath string) (*token.FileSet, *ast.File, bool, error) {
	if file, ok := c.typedSyntax(absPath); ok {
		return c.typed.Fset(), file, true, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nil, nil, false, fmt.Errorf("go/parser: %w", err)
	}
	return fset, file, false, nil
}

func (c *scanContext) parseFile(ctx context.Context, absPath, relPath string) error {
	fset, file, typed, err := c.syntaxFor(absPath)
	if err != nil {
		c.warnings = append(c.warnings, fmt.Sprintf("parse %s: %v", relPath, err))
		return nil // graceful skip
	}
	c.indexedFiles++
	if typed {
		c.typedIndexedFiles++
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
		c.registerFunction(fn, fset, file, relPath, pkgDir, typed)

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
			// The endpoint→handler association is "this @api comment
			// sits within ten lines above that declaration". The
			// declaration is exact, the association is a proximity
			// rule over source text — which is tier C. An @api comment
			// moved past a helper function silently retargets the
			// edge, and no name resolution would notice.
			c.graph.AddEdgeTier(endpointID, handlerID, graph.TierSyntactic)
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

func (c *scanContext) registerFunction(fn *ast.FuncDecl, fset *token.FileSet, file *ast.File, relPath, pkgDir string, typed bool) {
	if fn.Name == nil {
		return
	}
	if !fn.Name.IsExported() && fn.Recv == nil && c.opts.SkipUnexportedFuncs {
		// Graph-only audits can opt out of package-private helpers; the
		// default indexes them because the compiler instruments them for
		// coverage (see Options.SkipUnexportedFuncs).
		return
	}
	receiver := receiverTypeName(fn)
	funcName := fn.Name.Name

	var short shared.SymbolID
	if receiver != "" {
		short = shared.SymbolID(receiver + "." + funcName)
	} else {
		short = shared.SymbolID(file.Name.Name + "." + funcName)
	}
	id := c.uniqueSymbolID(short, receiver, funcName, relPath, pkgDir)
	if id == "" {
		return
	}

	kind := classifyNodeKind(pkgDir, c.opts.LayerRules)
	line := fset.Position(fn.Pos()).Line
	endLine := fset.Position(fn.End()).Line

	doc := ""
	if fn.Doc != nil {
		doc = strings.TrimSpace(fn.Doc.Text())
	}

	node := &graph.Node{
		Symbol: shared.Symbol{
			ID:        id,
			Kind:      kind,
			Position:  shared.FilePosition{Path: relPath, Line: line},
			EndLine:   endLine,
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
		pkgDir:   pkgDir,
		typed:    typed,
	}
	c.idsByPkg[pkgKey(pkgDir, short)] = id
	c.indexSymbolName(id)
	// The object → id binding is what makes #87 work: a call resolved by
	// the type checker arrives as a *types.Func, and this is the only
	// place that says which SymbolID that declaration was filed under.
	// Registering it here, beside AddNode, is deliberate — an id that
	// exists in the graph but not in this map is a callee no typed edge
	// can ever reach.
	if typed {
		c.recordTypedID(file, fn, id)
	}
}

// indexSymbolName records a registered id in the name indexes. Slices are
// kept in insertion (lexical walk) order and looked up read-only, so every
// resolver that consults them sees a stable candidate order.
func (c *scanContext) indexSymbolName(id shared.SymbolID) {
	if parts := strings.SplitN(string(id), ".", 2); len(parts) == 2 {
		c.byMethod[parts[1]] = append(c.byMethod[parts[1]], id)
	}
	if i := strings.LastIndexByte(string(id), '.'); i >= 0 {
		last := string(id)[i+1:]
		c.byLastSegment[last] = append(c.byLastSegment[last], id)
	}
}

// pkgKey is the lookup key for "the declaration of <short> that lives in
// package directory <pkgDir>". Call resolution consults it before the bare
// short ID so a call inside package P binds to P's own declaration rather
// than to whichever package happened to be walked first.
func pkgKey(pkgDir string, short shared.SymbolID) string {
	return pkgDir + "\x00" + string(short)
}

// resolveInScope returns the SymbolID a short name is registered under,
// preferring the declaration that lives in the CALLER's own package. Without
// this preference, a call to `Chat.MarkLoaded` inside the ai-chat context
// would bind to the messaging context's identically-named method purely
// because messaging was walked first — a wrong call edge, and one of the
// reasons a feature's impl surface reaches the wrong symbols (issue #84).
func (c *scanContext) resolveInScope(caller *funcInfo, short shared.SymbolID) (shared.SymbolID, bool) {
	if caller != nil {
		if id, ok := c.idsByPkg[pkgKey(caller.pkgDir, short)]; ok {
			return id, true
		}
	}
	if _, ok := c.funcLookup[short]; ok {
		return short, true
	}
	return "", false
}

// qualifiedSymbolID renders the package-qualified form of a declaration:
// "<pkgDir>.<Receiver>.<Method>" for methods, "<pkgDir>.<Func>" for plain
// functions (the package name is already the leading segment of pkgDir, so
// repeating it would read as "services.services.NewChatService").
func qualifiedSymbolID(pkgDir, receiver, funcName string) shared.SymbolID {
	prefix := pkgDir
	if prefix == "" || prefix == "." {
		return ""
	}
	if receiver != "" {
		return shared.SymbolID(prefix + "." + receiver + "." + funcName)
	}
	return shared.SymbolID(prefix + "." + funcName)
}

// uniqueSymbolID returns the ID to register this declaration under.
//
// The graph keys nodes by SymbolID, so two declarations sharing a short name
// — endemic in monorepos where every bounded context declares its own `Chat`
// or `NewAvailabilityService` — used to collapse into one node: the first
// walked won, and every symbol in the losing FILE disappeared from the store.
// On a 39-module workspace that silently dropped 210 production files, which
// is why a quarter of the coverage profile reconciled to zero symbols
// (issue #85).
//
// Resolution order, first free wins:
//  1. the bare short ID (so existing annotations, traces and stored symbol
//     names keep resolving for the overwhelmingly common unique case),
//  2. the package-qualified ID,
//  3. the package-qualified ID plus the file's base name (two packages in
//     one directory — e.g. `foo` and `foo_test`).
//
// Walk order is lexical, so which declaration keeps the bare ID is stable
// for a given file set. A collision is reported as a scan warning: an
// ambiguous short name degrades call-edge precision even when both symbols
// are indexed.
func (c *scanContext) uniqueSymbolID(short shared.SymbolID, receiver, funcName, relPath, pkgDir string) shared.SymbolID {
	existing, taken := c.funcLookup[short]
	if !taken {
		return short
	}
	if existing.node.Position.Path == relPath && existing.node.Position.Line > 0 {
		// Same file, same short name: a genuine redeclaration (or a repeated
		// scan of one file). Keep the first — re-registering would just
		// overwrite identical data.
		return ""
	}
	c.noteCollision(short, existing.node.Position.Path, relPath)
	if qualified := qualifiedSymbolID(pkgDir, receiver, funcName); qualified != "" {
		if _, taken := c.funcLookup[qualified]; !taken {
			c.qualifiedIDs[qualified] = true
			return qualified
		}
		withFile := shared.SymbolID(string(qualified) + "#" + path.Base(relPath))
		if _, taken := c.funcLookup[withFile]; !taken {
			c.qualifiedIDs[withFile] = true
			return withFile
		}
	}
	// Every candidate id is taken (a package-less root file whose short name
	// clashes, or a third declaration in one directory). Dropping is the last
	// resort — say so, because a dropped declaration is invisible to coverage.
	c.warnings = append(c.warnings, fmt.Sprintf(
		"symbol %s in %s could not be given a unique id and is NOT indexed", short, relPath))
	return ""
}

// maxCollisionWarnings caps the per-collision detail lines so a monorepo
// with thousands of duplicated short names doesn't drown the scan output;
// the summary count is always reported.
const maxCollisionWarnings = 20

func (c *scanContext) noteCollision(short shared.SymbolID, firstFile, secondFile string) {
	c.collisions++
	if c.collisions <= maxCollisionWarnings {
		c.warnings = append(c.warnings, fmt.Sprintf(
			"symbol name collision: %s declared in both %s and %s — the second is indexed under a package-qualified id",
			short, firstFile, secondFile))
	}
}

// receiverTypeName is the short name of a method's receiver type, or ""
// for a plain function.
//
// The type-parameter cases are not cosmetic. `func (c *Cache[K, V]) Put`
// has an IndexListExpr receiver, and returning "" for it filed the method
// under the plain-function id `collections.Put` -- a method that looked
// like a package-level function, under a name no call site would ever
// render, so nothing could resolve to it. That is the symbol-table
// fragmentation issue #87 names: every generic type in a repo loses its
// methods.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return receiverIdent(fn.Recv.List[0].Type)
}

func receiverIdent(t ast.Expr) string {
	switch e := t.(type) {
	case *ast.StarExpr:
		return receiverIdent(e.X)
	case *ast.IndexExpr:
		// func (c *Cache[T]) -- one type parameter.
		return receiverIdent(e.X)
	case *ast.IndexListExpr:
		// func (c *Cache[K, V]) -- two or more.
		return receiverIdent(e.X)
	case *ast.Ident:
		return e.Name
	default:
		return ""
	}
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
	// Type parameters, when the declaration has them. Without this,
	// `func NewCache[K comparable, V any]() *Cache[K, V]` renders as
	// `func NewCache() *Cache[K, V]` -- a signature that names types
	// nothing in it declares, which reads as a bug in the scanner rather
	// than as a generic function.
	b.WriteString(typeParamList(fn.Type.TypeParams))
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

// typeParamList renders "[K comparable, V any]", or "" for a
// non-generic declaration.
func typeParamList(params *ast.FieldList) string {
	if params == nil || len(params.List) == 0 {
		return ""
	}
	entries := make([]string, 0, len(params.List))
	for _, field := range params.List {
		constraint := typeExprString(field.Type)
		for _, name := range field.Names {
			entries = append(entries, name.Name+" "+constraint)
		}
	}
	return "[" + strings.Join(entries, ", ") + "]"
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
	case *ast.IndexExpr:
		// An instantiated generic with one type argument: Cache[string].
		return typeExprString(e.X) + "[" + typeExprString(e.Index) + "]"
	case *ast.IndexListExpr:
		// ... and with more than one. Rendered rather than reduced to the
		// bare name so a signature says which instantiation a field holds;
		// the AST call ladder keys on the string and matches nothing
		// either way, so nothing downstream binds to it by accident.
		args := make([]string, 0, len(e.Indices))
		for _, idx := range e.Indices {
			args = append(args, typeExprString(idx))
		}
		return typeExprString(e.X) + "[" + strings.Join(args, ", ") + "]"
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
		var matches []shared.SymbolID
		for _, id := range c.byLastSegment[methodName] {
			info, ok := c.funcLookup[id]
			if !ok || info.node.Kind != shared.KindHandler {
				continue
			}
			lower := strings.ToLower(info.node.Position.Path)
			if strings.Contains(lower, "test") || strings.Contains(lower, "mock") {
				continue
			}
			matches = append(matches, id)
		}
		matches = c.narrowHandlerMatches(matches)
		switch len(matches) {
		case 1:
			c.graph.MergeNode(placeholderID, c.funcLookup[matches[0]].node)
		case 0:
			// Broaden across all kinds.
			matches = append(matches, c.byLastSegment[methodName]...)
			matches = c.narrowHandlerMatches(matches)
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

// narrowHandlerMatches applies two tie-breaks to a set of same-method-name
// candidates before the caller decides between "merge" and "ambiguous":
//
//  1. an exported method beats unexported ones — a route's handler is always
//     exported, so a package-private helper that happens to end in the same
//     name is not a candidate;
//  2. a bare id beats the package-qualified id of a colliding declaration —
//     those extra candidates only exist because some OTHER package declares
//     the same short name, so treating them as rival handlers would turn
//     every duplicated handler name in a monorepo ambiguous.
//
// Each tie-break is applied only when it leaves exactly one candidate;
// anything else is genuinely ambiguous and is reported as such.
func (c *scanContext) narrowHandlerMatches(matches []shared.SymbolID) []shared.SymbolID {
	if len(matches) <= 1 {
		return matches
	}
	var exported []shared.SymbolID
	for _, id := range matches {
		name := string(id)
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if name != "" && strings.ToUpper(name[:1]) == name[:1] {
			exported = append(exported, id)
		}
	}
	if len(exported) == 1 {
		return exported
	}
	if len(exported) > 1 {
		matches = exported
	}
	var bare []shared.SymbolID
	for _, id := range matches {
		if !c.qualifiedIDs[id] {
			bare = append(bare, id)
		}
	}
	if len(bare) == 1 {
		return bare
	}
	return matches
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
	// Walk callers in a fixed order. funcLookup is a map, so ranging it
	// directly visits declarations differently on every Scan -- and edge
	// INSERTION order is observable, because AddEdge decides Edge.Cycle by
	// asking whether the edges added SO FAR already contain a path back. For
	// a mutually recursive pair (a calls b, b calls a) that means whichever
	// edge happens to be added second carries the flag, so two scans of one
	// unchanged tree disagree about which edge closes the cycle.
	//
	// docs/testing/determinism.md states the contract this breaks ("every
	// scan produces the same ... edges"), and PR #99 applied the same fix to
	// the fuzzy resolvers. The golden corpus never caught it because it holds
	// no mutual recursion; a generated tree that does caught it on the first
	// run (TestProperty_Scan_IsReproducibleAndRootRelative).
	//
	// Sorting changes no edge and no flag semantics -- it only fixes WHICH
	// member of a recursive pair is the one marked.
	for _, id := range sortedLookupIDs(c.funcLookup) {
		info := c.funcLookup[id]
		if info.funcDecl.Body == nil {
			continue
		}
		c.walkBody(info)
	}
}

// sortedLookupIDs returns funcLookup's keys in lexical order.
func sortedLookupIDs(lookup map[shared.SymbolID]*funcInfo) []shared.SymbolID {
	ids := make([]shared.SymbolID, 0, len(lookup))
	for id := range lookup {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
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
		for _, r := range c.resolveCallSite(info, call) {
			callees = append(callees, c.emitCallEdge(info, r)...)
		}
		return true
	})
	return callees
}

// resolveCallSite is the one place the two resolvers meet.
//
// The typed resolver is AUTHORITATIVE for a file it type-checked: when it
// runs, the name ladder does not, even when it returns nothing. That is
// the rule that makes the tier histogram mean something. Falling back
// after a typed miss would mean every call the type checker declined to
// resolve — a conversion, a call through a func value, a call into a
// dependency — came back as a substring guess, and the syntactic bucket
// would grow on the very change meant to shrink it.
func (c *scanContext) resolveCallSite(info *funcInfo, call *ast.CallExpr) []callResolution {
	if resolutions, handled := c.resolveCallTyped(info, call); handled {
		return resolutions
	}
	r := c.resolveCall(info, call)
	if r.ID == "" {
		return nil
	}
	return []callResolution{r}
}

// emitCallEdge turns one resolution into at most one edge and reports the
// callee it reached, for the BuildFrom traversal.
func (c *scanContext) emitCallEdge(info *funcInfo, r callResolution) []shared.SymbolID {
	if c.shouldIgnore(r.ID) {
		return nil
	}
	// SQLC: callee method maps to a generated query.
	if sqlcMap, ok := c.sqlcMethods[extractMethodName(string(r.ID))]; ok {
		queryID := shared.SymbolID("sql:" + sqlcMap.QueryName)
		c.graph.AddNode(&graph.Node{
			Symbol: shared.Symbol{
				ID:       queryID,
				Kind:     shared.KindQuery,
				Position: shared.FilePosition{Path: sqlcMap.SQLFile, Line: sqlcMap.SQLLine},
				Doc:      fmt.Sprintf("SQLC query: %s (:%s)", sqlcMap.QueryName, sqlcMap.QueryType),
			},
		})
		// The redirect DOES re-resolve, and by the weakest rule atlas
		// has: c.sqlcMethods is keyed on a bare method name, and the
		// lookup above is the last dot-segment of whatever id the
		// resolver produced. Nothing about the receiver, the package or
		// the types survives that hop, so two repositories with a
		// GetUser method both land on the same query node.
		//
		// The tier therefore restarts at C. Carrying the callee's tier
		// across would let a bare-name match inherit a guarantee the
		// type checker made about a DIFFERENT edge — the one to the Go
		// method, which this edge replaced. Ambiguity does carry over:
		// the redirect adds doubt and removes none.
		c.addResolvedEdge(info.node.ID, queryID, callResolution{
			ID:        queryID,
			Ambiguous: r.Ambiguous,
			Tier:      graph.TierSyntactic,
		})
		return []shared.SymbolID{queryID}
	}
	if _, exists := c.funcLookup[r.ID]; exists {
		c.addResolvedEdge(info.node.ID, r.ID, r)
		return []shared.SymbolID{r.ID}
	}
	if isExternalCall(string(r.ID)) {
		c.graph.AddNode(&graph.Node{
			Symbol: shared.Symbol{ID: r.ID, Kind: shared.KindExternal},
		})
		// An external call is a package-qualified name atlas has no
		// declaration for: the target is a stub built from the source
		// text, so the edge is syntactic no matter which rung produced
		// the name. The typed path never reaches here — it only offers
		// callees it has already matched to an indexed declaration.
		c.graph.AddEdgeTier(info.node.ID, r.ID, graph.TierSyntactic)
	}
	return nil
}

// addResolvedEdge emits one call edge carrying everything the resolver
// concluded. Both flags come from the same callResolution rather than
// being re-derived here, so an edge cannot end up with one rung's
// ambiguity and another rung's tier.
func (c *scanContext) addResolvedEdge(from, to shared.SymbolID, r callResolution) {
	if r.Ambiguous {
		c.graph.AddAmbiguousEdgeTier(from, to, r.Tier)
		return
	}
	c.graph.AddEdgeTier(from, to, r.Tier)
}

// callResolution is what the resolver managed to say about one call
// site: which symbol it landed on, whether it had to choose between
// candidates, and — the part issue #146 adds — by which mechanism.
//
// The three travel together because they are decided together, at the
// bottom of a resolution ladder several returns deep. Handing the tier
// back as a third bare return value beside a bool is how a later edit
// ends up passing the ambiguity flag into the tier slot.
type callResolution struct {
	ID        shared.SymbolID
	Ambiguous bool
	Tier      graph.ResolutionTier
}

// unresolved is the "nothing to emit" answer. Its tier is deliberately
// TierUnset: no edge is produced, so no mechanism is claimed.
var unresolved = callResolution{}

// nameResolved records a callee bound to a declaration this scan
// actually indexed, through package scope or the global short-name
// table (resolveInScope). No types were consulted — a call through an
// interface-typed variable never reaches here — which is exactly what
// tier B claims and no more.
func nameResolved(id shared.SymbolID) callResolution {
	return callResolution{ID: id, Tier: graph.TierNameResolved}
}

// syntactic records a callee the shape of the source suggested and
// nothing bound: a substring match on a lowercased identifier, a DI
// binding guessed from a name, or the "Type.Method" rendering kept
// verbatim because no candidate matched at all. Tier C — the target may
// not exist, and may be the wrong one of several same-named candidates.
func syntactic(id shared.SymbolID, ambiguous bool) callResolution {
	return callResolution{ID: id, Ambiguous: ambiguous, Tier: graph.TierSyntactic}
}

func (c *scanContext) resolveCall(caller *funcInfo, call *ast.CallExpr) callResolution {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return c.resolveSelectorCall(caller, fn)
	case *ast.Ident:
		if caller.file != nil {
			short := shared.SymbolID(caller.file.Name.Name + "." + fn.Name)
			if id, ok := c.resolveInScope(caller, short); ok {
				return nameResolved(id)
			}
		}
		return unresolved
	default:
		return unresolved
	}
}

// resolveSelectorCall walks a ladder from strongest rung to weakest,
// and the tier each rung reports is the honest description of that
// rung. resolveInScope binds a name to a declaration this scan indexed
// (tier B). Everything below it — the sqlc "Queries" substring test,
// the fuzzy interface/DI matcher, and the bare fall-through that keeps
// the rendered "Type.Method" because nothing matched — is a guess about
// a symbol that may not exist (tier C).
func (c *scanContext) resolveSelectorCall(caller *funcInfo, sel *ast.SelectorExpr) callResolution {
	method := sel.Sel.Name

	// Case 1: x.field.Method() — chained selector.
	if innerSel, ok := sel.X.(*ast.SelectorExpr); ok {
		if ident, ok := innerSel.X.(*ast.Ident); ok {
			fieldName := innerSel.Sel.Name
			fieldType := c.resolveFieldType(caller.receiver, ident.Name, fieldName)
			if fieldType != "" {
				calleeID := shared.SymbolID(fieldType + "." + method)
				if id, ok := c.resolveInScope(caller, calleeID); ok {
					return nameResolved(id)
				}
				// The field's declared type merely CONTAINS "Queries"
				// and the method name appears in the sqlc table. Two
				// name tests, no binding.
				if strings.Contains(fieldType, "Queries") {
					if _, ok := c.sqlcMethods[method]; ok {
						return syntactic(shared.SymbolID(fieldType+"."+method), false)
					}
				}
				if resolved := c.fuzzyResolveMethod(fieldType, method); resolved != "" {
					return syntactic(resolved, false)
				}
				// Nothing matched: the edge points at a rendering of
				// the source text, which may name no symbol at all.
				return syntactic(calleeID, true)
			}
		}
	}

	// Case 2: x.Method()
	if ident, ok := sel.X.(*ast.Ident); ok {
		varName := ident.Name
		if caller.receiver != "" && (varName == "r" || varName == "s" || varName == "h" || varName == "a") {
			calleeID := shared.SymbolID(caller.receiver + "." + method)
			if id, ok := c.resolveInScope(caller, calleeID); ok {
				return nameResolved(id)
			}
		}
		fieldType := c.resolveFieldType(caller.receiver, "", varName)
		if fieldType != "" {
			calleeID := shared.SymbolID(fieldType + "." + method)
			if id, ok := c.resolveInScope(caller, calleeID); ok {
				return nameResolved(id)
			}
			if resolved := c.fuzzyResolveMethod(fieldType, method); resolved != "" {
				return syntactic(resolved, false)
			}
			return syntactic(calleeID, true)
		}
		calleeID := shared.SymbolID(varName + "." + method)
		if id, ok := c.resolveInScope(caller, calleeID); ok {
			return nameResolved(id)
		}
		return c.fuzzyResolve(varName, method)
	}
	return unresolved
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
	for _, id := range c.byMethod[method] {
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

// fuzzyResolve is the weakest rung on the ladder: any indexed method of
// this name whose receiver type name merely CONTAINS the caller's
// variable name, case-insensitively. Tier C by construction — no scope,
// no import graph, no type, only a substring.
func (c *scanContext) fuzzyResolve(varName, method string) callResolution {
	lower := strings.ToLower(varName)
	for _, id := range c.byMethod[method] {
		parts := strings.SplitN(string(id), ".", 2)
		if len(parts) != 2 || parts[1] != method {
			continue
		}
		if strings.Contains(strings.ToLower(parts[0]), lower) {
			return syntactic(id, true)
		}
	}
	return unresolved
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
