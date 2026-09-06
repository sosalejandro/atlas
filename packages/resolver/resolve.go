package resolver

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"time"
)

// timeNow / timeSince are indirected so callgraph.go can be read without
// importing time twice; they are not a seam for tests.
var (
	timeNow   = time.Now
	timeSince = time.Since
)

// Resolution is what the type checker (and, for interface calls, CHA)
// concluded about one call expression.
//
// Resolved distinguishes the two ways a call site produces no target,
// which the caller must not confuse:
//
//   - Resolved=false — this expression is not a call to a declared
//     function at all. A conversion `Duration(n)`, a builtin `len(x)`, a
//     call through a func-typed variable. There is nothing to emit and
//     nothing to guess.
//   - Resolved=true with no Targets — it IS a call to a declared
//     function, but no target survives: an interface whose
//     implementations all live outside the loaded tree, most often.
//
// A name heuristic would answer both with a plausible-looking string.
type Resolution struct {
	Resolved     bool
	ViaInterface bool
	Targets      []*types.Func
}

// TypeChecked reports whether this program can answer for a file.
//
// The scanner asks per file, not per repository, because that is the
// granularity at which a real tree fails: one package mid-edit does not
// make the other two hundred unresolvable.
func (p *Program) TypeChecked(absPath string) bool {
	_, ok := p.byPath[filepath.Clean(absPath)]
	return ok
}

// Syntax returns the type-checked syntax tree for a file.
//
// The scanner adopts this tree instead of parsing the file again — not
// only to save the parse, but because types.Info is keyed by ast.Node
// POINTERS. A second parse produces a structurally identical tree whose
// nodes are absent from every types map, and every lookup against it
// would silently miss.
func (p *Program) Syntax(absPath string) (*ast.File, bool) {
	view, ok := p.byPath[filepath.Clean(absPath)]
	if !ok {
		return nil, false
	}
	return view.syntax, true
}

// Declared returns the object a function declaration defines, or nil if
// this program did not type-check it.
func (p *Program) Declared(file *ast.File, fn *ast.FuncDecl) *types.Func {
	view, ok := p.bySyntax[file]
	if !ok || fn == nil || fn.Name == nil {
		return nil
	}
	obj, _ := view.pkg.TypesInfo.Defs[fn.Name].(*types.Func)
	if obj == nil {
		return nil
	}
	return obj.Origin()
}

// ResolveCall answers one call expression inside a type-checked file.
func (p *Program) ResolveCall(file *ast.File, call *ast.CallExpr) Resolution {
	view, ok := p.bySyntax[file]
	if !ok || call == nil {
		return Resolution{}
	}
	callee := calleeObject(view.pkg.TypesInfo, call)
	if callee == nil {
		return Resolution{}
	}
	if !isInterfaceMethod(callee) {
		return Resolution{Resolved: true, Targets: []*types.Func{callee}}
	}
	return Resolution{
		Resolved:     true,
		ViaInterface: true,
		Targets:      p.invokes[call.Lparen],
	}
}

// calleeObject names the function a call expression calls, using only
// what the type checker recorded.
//
// The three lookups are not interchangeable. Selections answers method
// calls — including methods promoted through embedding, where the
// selector names a field the method is not declared on. Uses answers
// package-qualified calls and plain identifiers. Anything that resolves
// to something other than a *types.Func — a type (a conversion), a
// variable (a call through a func value), a builtin — is not a call to a
// declaration, and saying so is the point.
func calleeObject(info *types.Info, call *ast.CallExpr) *types.Func {
	var obj types.Object
	switch fun := instantiated(ast.Unparen(call.Fun)).(type) {
	case *ast.SelectorExpr:
		if sel, ok := info.Selections[fun]; ok {
			obj = sel.Obj()
		} else {
			obj = info.Uses[fun.Sel]
		}
	case *ast.Ident:
		obj = info.Uses[fun]
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn == nil {
		return nil
	}
	return fn.Origin()
}

// instantiated strips an explicit type-argument list off a call target:
// `Map[int, string](xs, f)` parses as an IndexListExpr (or, for a single
// argument, an IndexExpr) wrapping the function being called. Both forms
// have to be unwrapped or an explicitly instantiated generic call looks
// like an index expression and resolves to nothing.
func instantiated(expr ast.Expr) ast.Expr {
	switch e := expr.(type) {
	case *ast.IndexExpr:
		return ast.Unparen(e.X)
	case *ast.IndexListExpr:
		return ast.Unparen(e.X)
	default:
		return expr
	}
}

// isInterfaceMethod reports whether fn is declared on an interface, which
// is the one case where the statically named callee is not the function
// that will run.
func isInterfaceMethod(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	return types.IsInterface(sig.Recv().Type())
}

// ObjectKey is a stable string identity for a function declaration.
//
// It exists because the same declaration is type-checked more than once.
// `go list -test` compiles a package plainly and again as a test variant,
// producing two distinct *types.Func values for one `func` in one file —
// so a map keyed by the pointer would fail to recognise, from a test
// file, the very function the non-test pass had just registered. The two
// variants share a package PATH, and types.Func.FullName is built from
// the path, the receiver type and the name, so it unifies them.
//
// Origin is applied first: an instantiated generic's FullName carries its
// type arguments, and Cache[string,*Order].Put must key to the same
// declaration as Cache[int,string].Put.
func ObjectKey(fn *types.Func) string {
	if fn == nil {
		return ""
	}
	return fn.Origin().FullName()
}

// Receiver returns the short name of fn's receiver type ("OrderService"),
// or "" for a plain function. Pointers, named types and generic
// instantiations are unwrapped, so a method on *Cache[K,V] reports
// "Cache" — the name the scanner registers it under.
func Receiver(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return ""
	}
	return typeName(sig.Recv().Type())
}

func typeName(t types.Type) string {
	switch typ := t.(type) {
	case *types.Pointer:
		return typeName(typ.Elem())
	case *types.Named:
		return typ.Obj().Name()
	case *types.Alias:
		return typ.Obj().Name()
	default:
		return ""
	}
}

// PackageName is the short package name a function is declared in
// ("config"), or "" when the object carries no package (a builtin).
func PackageName(fn *types.Func) string {
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	return fn.Pkg().Name()
}

// PackagePath is the import path a function is declared in.
func PackagePath(fn *types.Func) string {
	if fn == nil || fn.Pkg() == nil {
		return ""
	}
	return fn.Pkg().Path()
}
