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

// extractHuma walks every .go file under the project root and emits one
// KindHumaOp ContractDef per huma.Register(api, huma.Operation{...},
// handler) call site.
//
// Why this is its own pass (not folded into http_routes.go): Huma's
// argument shape is structurally different. The route is declared via
// a composite literal field-by-field assignment rather than the
// "/path", handler positional convention. Conflating the two passes
// would balloon http_routes.go's complexity without a payoff.
//
// We recognise BOTH `huma.Register(...)` and `<receiver>.Register(...)`
// where the first arg is a `huma.API`-like identifier — the latter
// because nutrition-v2-go uses a tiny `humafx` wrapper that re-exposes
// Register with the same shape. Matching is based on the call-site
// argv (3 args, second is a composite literal, third is a func ref).
//
// Discovered fields:
//
//   - Method        string        — HTTP method
//   - Path          string        — Huma's path template
//   - OperationID   string        — stable id
//   - Summary       string        — one-line summary
//   - Tags          []string
//   - DefaultStatus int           — captured but not currently surfaced
//   - Description   string        — captured but not currently surfaced
//
// Handler symbol resolution mirrors http_routes.go — short ID lookup
// against the codeindex symbol table.
func (e *Extractor) extractHuma(ctx context.Context, idx *codeindex.Index, annIdx *annotationIndex) ([]ContractDef, []string) {
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
		fileDefs, fileWarns := e.parseHumaFromFile(path, idx, annIdx, handlerByShortID)
		defs = append(defs, fileDefs...)
		warns = append(warns, fileWarns...)
		return nil
	})
	if err != nil {
		warns = append(warns, fmt.Sprintf("contract huma: walk: %v", err))
	}
	return defs, warns
}

// parseHumaFromFile is the per-file half of extractHuma. Returns the
// per-file defs + warnings so the outer walker stays funlen-friendly.
func (e *Extractor) parseHumaFromFile(absPath string, idx *codeindex.Index, annIdx *annotationIndex, handlerByShortID map[string]shared.SymbolID) ([]ContractDef, []string) {
	relPath := normaliseRelPath(e.opts.ProjectRoot, absPath)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nil, []string{fmt.Sprintf("contract huma: parse %s: %v", relPath, err)}
	}
	if !fileMentionsHuma(file) {
		return nil, nil
	}

	// Pre-pass: collect every Huma register-call line so the annotation
	// pairing rule sees them BEFORE pass 2 attempts a lookup.
	var registerLines []int
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, _, ok := matchHumaRegister(call); ok {
			registerLines = append(registerLines, fset.Position(call.Pos()).Line)
		}
		return true
	})
	annIdx.registerDeclLines(relPath, registerLines)

	var defs []ContractDef
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		op, handlerRef, ok := matchHumaRegister(call)
		if !ok {
			return true
		}
		line := fset.Position(call.Pos()).Line
		defs = append(defs, e.buildHumaDef(op, handlerRef, relPath, line, idx, annIdx, handlerByShortID))
		return true
	})
	return defs, nil
}

// buildHumaDef materialises a single humaOperation into a ContractDef
// and runs handler-sym + feature-id resolution (with handler-symbol-
// fallback when the call site isn't annotated).
func (e *Extractor) buildHumaDef(op humaOperation, handlerRef, relPath string, line int, idx *codeindex.Index, annIdx *annotationIndex, handlerByShortID map[string]shared.SymbolID) ContractDef {
	def := ContractDef{
		Name:      operationDisplayName(op),
		Kind:      KindHumaOp,
		Language:  LangGo,
		Signature: op.Method + " " + op.Path,
		FilePath:  relPath,
		Line:      line,
		Source:    "huma",
		Operation: OperationDetail{
			Method:      op.Method,
			Path:        op.Path,
			Handler:     handlerRef,
			OperationID: op.OperationID,
			Summary:     op.Summary,
			Tags:        op.Tags,
		},
	}
	if sid := resolveHandlerSymbol(handlerRef, handlerByShortID); sid != "" {
		def.Operation.HandlerSym = string(sid)
		def.Symbols = []shared.SymbolID{sid}
	}
	if fid, ok := annIdx.findFor(relPath, line); ok {
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

// humaOperation is the subset of huma.Operation fields the extractor cares
// about. Mirrors the upstream struct's field names so the regex / AST
// inspection can be a simple field-name match.
type humaOperation struct {
	Method        string
	Path          string
	OperationID   string
	Summary       string
	Description   string
	Tags          []string
	DefaultStatus int
}

// fileMentionsHuma is a cheap pre-walk filter — if no "huma" import and
// no top-level "Register" / "Operation" identifier appears in any func
// body, we know there can't be a huma.Register call. Avoids the AST walk
// for the bulk of a backend repo.
//
// We are intentionally generous: any "huma" string in an import path OR
// any `Register(` / `Operation{` mention inside any func body counts as a
// hit. False positives are cheap (we re-walk the AST) — false negatives
// would silently drop contracts.
func fileMentionsHuma(file *ast.File) bool {
	for _, imp := range file.Imports {
		v := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(strings.ToLower(v), "huma") {
			return true
		}
	}
	// The package itself may BE a huma adapter (in-repo helper or fixture).
	if file.Name != nil && strings.Contains(strings.ToLower(file.Name.Name), "huma") {
		return true
	}
	// Fall through to a top-level scan for "Register(" + "Operation{" pair
	// inside any function body.
	hasRegister, hasOperation := false, false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.Ident:
				switch v.Name {
				case "huma":
					hasRegister, hasOperation = true, true
				case "Register":
					hasRegister = true
				case "Operation":
					hasOperation = true
				}
			}
			return !hasRegister || !hasOperation
		})
		if hasRegister && hasOperation {
			return true
		}
	}
	return hasRegister && hasOperation
}

// matchHumaRegister recognises the standard `huma.Register(api,
// huma.Operation{...}, handler)` call shape and returns the parsed
// operation plus the handler reference (textual). Returns ok=false for
// anything that doesn't match exactly that shape.
//
// Three-argument check: first arg is the api (any expression), second is
// a composite literal whose type ends in "Operation", third is the
// handler reference (selector or identifier).
func matchHumaRegister(call *ast.CallExpr) (humaOperation, string, bool) {
	if len(call.Args) != 3 {
		return humaOperation{}, "", false
	}
	// The callee must be a `Register` reference — either bare (in-package
	// or dot-imported) or selector-qualified (`huma.Register`, wrapper-
	// .Register). Reject everything else.
	if !isRegisterCallee(call.Fun) {
		return humaOperation{}, "", false
	}
	// Second arg: composite literal whose type ends in "Operation".
	cl, ok := call.Args[1].(*ast.CompositeLit)
	if !ok {
		return humaOperation{}, "", false
	}
	if !isOperationTypeExpr(cl.Type) {
		return humaOperation{}, "", false
	}
	op := parseHumaOperationLit(cl)

	// Third arg: a handler reference — selector or identifier.
	handlerRef := exprToString(call.Args[2])
	return op, handlerRef, op.Method != "" && op.Path != ""
}

// isRegisterCallee returns true if the call's function reference is a
// `Register` symbol — accepting bare identifiers (in-package or dot-
// imported), selector forms (huma.Register, wrapper.Register), and any
// generic-type-arg wrapped variants (Register[T, U]).
func isRegisterCallee(expr ast.Expr) bool {
	switch f := expr.(type) {
	case *ast.Ident:
		return f.Name == "Register"
	case *ast.SelectorExpr:
		return f.Sel.Name == "Register"
	case *ast.IndexExpr:
		return isRegisterCallee(f.X)
	case *ast.IndexListExpr:
		return isRegisterCallee(f.X)
	}
	return false
}

// isOperationTypeExpr returns true if expr is an `Operation` or
// `huma.Operation` (or any selector ending in "Operation") type
// expression. Conservative — anything else fails the match.
func isOperationTypeExpr(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Operation"
	case *ast.SelectorExpr:
		return t.Sel.Name == "Operation"
	}
	return false
}

// parseHumaOperationLit walks the composite-literal element list and pulls
// the field values we care about. Unknown fields are silently skipped
// (forward compat with future Huma fields).
func parseHumaOperationLit(cl *ast.CompositeLit) humaOperation {
	var op humaOperation
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Method":
			op.Method = literalOrHTTPConst(kv.Value)
		case "Path":
			op.Path = stringLit(kv.Value)
		case "OperationID":
			op.OperationID = stringLit(kv.Value)
		case "Summary":
			op.Summary = stringLit(kv.Value)
		case "Description":
			op.Description = stringLit(kv.Value)
		case "Tags":
			op.Tags = sliceStringLit(kv.Value)
		case "DefaultStatus":
			// Most Huma callers pass http.StatusCreated etc. as
			// references; the integer literal path is rare. We keep
			// this branch for completeness but the value is currently
			// not surfaced on OperationDetail.
			_ = kv.Value
		}
	}
	return op
}

// literalOrHTTPConst resolves a Method field. Huma callers typically
// write either the raw string literal "POST" or the stdlib constant
// http.MethodPost. We translate the latter to its uppercase form so
// downstream consumers see "POST" regardless of source style.
func literalOrHTTPConst(expr ast.Expr) string {
	if lit := stringLit(expr); lit != "" {
		return lit
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "http" {
		return ""
	}
	switch sel.Sel.Name {
	case "MethodGet":
		return "GET"
	case "MethodPost":
		return "POST"
	case "MethodPut":
		return "PUT"
	case "MethodPatch":
		return "PATCH"
	case "MethodDelete":
		return "DELETE"
	case "MethodHead":
		return "HEAD"
	case "MethodOptions":
		return "OPTIONS"
	case "MethodConnect":
		return "CONNECT"
	case "MethodTrace":
		return "TRACE"
	}
	return ""
}

func stringLit(expr ast.Expr) string {
	// Direct basic literal.
	if lit, ok := expr.(*ast.BasicLit); ok {
		v := strings.Trim(lit.Value, "\"`")
		return v
	}
	// String concatenation: "foo" + "bar".
	if bin, ok := expr.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
		l := stringLit(bin.X)
		r := stringLit(bin.Y)
		if l != "" || r != "" {
			return l + r
		}
	}
	return ""
}

func sliceStringLit(expr ast.Expr) []string {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(cl.Elts))
	for _, el := range cl.Elts {
		if v := stringLit(el); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// operationDisplayName picks the best human-readable name for a Huma op:
//   1. OperationID (the explicit stable id)
//   2. "METHOD /path" fallback
func operationDisplayName(op humaOperation) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	return strings.TrimSpace(op.Method + " " + op.Path)
}

