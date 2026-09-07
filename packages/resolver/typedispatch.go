package resolver

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"
)

// buildDispatch answers, for every interface call site in the loaded
// tree, which concrete methods can run -- from go/types alone.
//
// It is the algorithm class-hierarchy analysis runs, minus the
// representation CHA needs and this package does not. x/tools's
// callgraph/cha walks SSA for one reason: to ENUMERATE CALL SITES (`for f
// := range allFuncs { for _, b := range f.Blocks { ... } }`). The dispatch
// computation underneath it, chautil.LazyCallees, is pure go/types --
// group every concrete method by types.Func.Id, then for an abstract
// method I.m keep the ones whose receiver satisfies I. Atlas already
// enumerates its own call sites: the scanner walks the AST and reads
// p.invokes[call.Lparen]. It was paying for an entire SSA program to find
// call sites it had in hand.
//
// Only *invoke* sites are indexed, and that predates issue #155. A static
// call needs no help: the type checker already named its callee exactly,
// and taking it from here instead would only add a second way to be
// wrong. A dynamic call through a func-typed variable is deliberately
// left out too -- CHA resolves those by matching signatures across the
// whole program, which for a common signature like func(error) means
// every function with that shape. That is not resolution, it is a
// fan-out, and issue #87 exists to reduce exactly this kind of guess.
// CHA computed that half and this package threw it away; now it is not
// computed.
//
// What is kept is CHA's conservatism, which is the property everything
// downstream leans on: a call through an OrderRepository reports every
// type implementing OrderRepository, whether or not the program ever
// constructs one. Atlas records that over-approximation as an ambiguous
// edge, which is the honest shape of the answer. The one place this
// differs from CHA is the universe of concrete types it draws from, and
// invokeparity_test.go is the account of that difference.
func (p *Program) buildDispatch(pkgs []*packages.Package) {
	defer func() {
		// This walks types the go tool decoded from export data and
		// syntax this package did not produce. go/types is not
		// documented to be panic-free on either, and a panic here must
		// cost interface dispatch, not the scan: static resolution has
		// already been established by the type checker and stands on
		// its own.
		//
		// The equivalent recover in the SSA path guarded a far larger
		// surface -- an entire program construction, on goroutines it
		// spawned -- which is most of what issue #155 removed. This one
		// is insurance, not a known failure mode.
		if r := recover(); r != nil {
			p.status.CallGraph = false
			p.status.InvokeSites = 0
			p.invokes = map[token.Pos][]*types.Func{}
		}
	}()

	start := timeNow()
	p.invokes = typeInvokes(pkgs)
	p.status.CallGraph = true
	p.status.InvokeSites = len(p.invokes)
	p.status.CallGraphDuration = timeSince(start)
}

// checkedPackage is the part of a loaded package the dispatch index
// reads, named rather than passed as a *packages.Package so that the part
// is legible.
//
// It is three fields, and the absent fourth is the point. The index needs
// the package's types (for the named types declared in it and the ones it
// imports), its syntax (to find call expressions), and exactly two maps
// of its types.Info: Selections, to name the method a call selects and
// the receiver it selects it on, and Defs, to reach named types declared
// inside function bodies. It does NOT need types.Info.Types -- the 98 MB
// issue #152 went after and could not have, because go/ssa read it.
// TestDispatch_DoesNotNeedTypesInfoTypes is that claim, executable;
// docs/performance.md records why the bytes are still not collectable.
type checkedPackage struct {
	types  *types.Package
	info   *types.Info
	syntax []*ast.File
}

func typeInvokes(pkgs []*packages.Package) map[token.Pos][]*types.Func {
	units := make([]checkedPackage, 0, len(pkgs))
	for _, pkg := range pkgs {
		units = append(units, checkedPackage{
			types:  pkg.Types,
			info:   pkg.TypesInfo,
			syntax: pkg.Syntax,
		})
	}
	return dispatchOver(units)
}

func dispatchOver(units []checkedPackage) map[token.Pos][]*types.Func {
	idx := newDispatchIndex(units)
	invokes := make(map[token.Pos][]*types.Func)
	seen := make(map[token.Pos]map[string]bool)

	for _, unit := range units {
		for _, file := range unit.syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				iface, method, ok := invokeSite(unit.info, call)
				if !ok {
					return true
				}
				for _, fn := range idx.callees(iface, method) {
					// The same file is type-checked twice -- plainly
					// and as a test variant -- so one Lparen is
					// visited from two packages. Dedup by ObjectKey,
					// which unifies the two variants' distinct
					// *types.Func values for one declaration, or every
					// call site in a package with tests reports its
					// candidates twice.
					key := ObjectKey(fn)
					if seen[call.Lparen] == nil {
						seen[call.Lparen] = map[string]bool{}
					}
					if seen[call.Lparen][key] {
						continue
					}
					seen[call.Lparen][key] = true
					invokes[call.Lparen] = append(invokes[call.Lparen], fn)
				}
				return true
			})
		}
	}

	// Determinism (#120). Map iteration decided the order of both
	// idx.callees's answer and the walk that produced it, so the same
	// tree would otherwise emit the same edges in a different order on
	// every run and the golden snapshot would fail at random.
	for pos, funcs := range invokes {
		sort.Slice(funcs, func(i, j int) bool {
			return ObjectKey(funcs[i]) < ObjectKey(funcs[j])
		})
		invokes[pos] = funcs
	}
	return invokes
}

// invokeSite reports the interface a call dispatches through and the
// abstract method it names, or that this call expression is not an
// interface dispatch.
//
// The interface is taken from the RECEIVER, not from the method's own
// declaration, and the difference is not cosmetic. For
//
//	type Repo interface { Reader; Writer }
//
// a call r.Read() on an r of type Repo names a *types.Func whose receiver
// is Reader -- interface embedding shares the method object. Resolving
// against Reader would return every type that can read, including those
// that cannot write and so can never be a Repo. CHA uses
// call.Value.Type(), the static type of the receiver, for the same
// reason.
func invokeSite(info *types.Info, call *ast.CallExpr) (*types.Interface, *types.Func, bool) {
	sel, ok := instantiated(ast.Unparen(call.Fun)).(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	selection, ok := info.Selections[sel]
	// MethodVal only. A method EXPRESSION, Repo.Read(r), is a call to a
	// function value that happens to be spelled with an interface name;
	// SSA compiles it to a static call to a thunk and CHA records no
	// invoke for it.
	if !ok || selection.Kind() != types.MethodVal {
		return nil, nil, false
	}
	method, ok := selection.Obj().(*types.Func)
	if !ok || !isInterfaceMethod(method) {
		return nil, nil, false
	}
	iface := receiverInterface(selection)
	if iface == nil {
		return nil, nil, false
	}
	return iface, method, true
}

// receiverInterface walks a selection's embedding path to the value the
// dispatch actually happens on.
//
// Usually that is the receiver expression itself. It is not when an
// interface is embedded in a struct: for `type Store struct{ Repo }`, the
// call s.Save() selects through field 0 and then dispatches on the Repo
// held there. SSA makes the two steps explicit -- a FieldAddr, then an
// Invoke whose Value is typed Repo -- and this loop is the types-level
// spelling of the same walk.
func receiverInterface(sel *types.Selection) *types.Interface {
	t := sel.Recv()
	path := sel.Index()
	for _, i := range path[:len(path)-1] {
		st, ok := deref(t).Underlying().(*types.Struct)
		if !ok || i >= st.NumFields() {
			return nil
		}
		t = st.Field(i).Type()
	}
	iface, _ := deref(t).Underlying().(*types.Interface)
	return iface
}

func deref(t types.Type) types.Type {
	if ptr, ok := types.Unalias(t).Underlying().(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

// concreteMethod is one entry in the dispatch index: a method, and the
// receiver type that has to satisfy an interface for it to be a
// candidate.
//
// The two are not derivable from one another. A method promoted through
// embedding is DECLARED on the embedded type and REACHED through the
// outer one, and it is the outer type that satisfies the interface: a
// struct embedding a Reader and a Writer implements Repo while neither
// half does. recv is the type whose method set contained fn; fn is the
// declaration atlas indexes.
type concreteMethod struct {
	recv types.Type
	fn   *types.Func
}

// imethod names an abstract method I.m. There is no go/types object for
// one -- a *types.Func may be shared by many interfaces through embedding
// -- so I has to be carried explicitly. This is chautil's struct, for
// chautil's reason.
type imethod struct {
	iface *types.Interface
	id    string
}

// dispatchIndex is chautil.LazyCallees over go/types.
type dispatchIndex struct {
	// byID groups every concrete method by types.Func.Id.
	//
	// Keyed by Id and not by Name, because two unexported methods
	// spelled the same in different packages are different methods, and
	// a concrete type implementing an interface does not mean a call
	// through that interface can reach both. Id folds the package path
	// in for exactly the unexported case.
	byID map[string][]concreteMethod

	// memo caches the answer for one (interface, method id). A
	// polymorphic method such as (io.Writer).Write is asked about at
	// hundreds of sites and the answer cannot change between them.
	memo map[imethod][]*types.Func

	msets typeutil.MethodSetCache

	// added is the guard against enumerating one type twice. A
	// dependency is reachable from many importers, and a package
	// loaded plainly and as a test variant declares the same types
	// under two *types.Package values -- but a *types.TypeName is one
	// per declaration per checker run, so this deduplicates the first
	// case exactly and leaves the second to ObjectKey downstream.
	added map[*types.TypeName]bool
}

func (idx *dispatchIndex) callees(iface *types.Interface, method *types.Func) []*types.Func {
	key := imethod{iface: iface, id: method.Id()}
	if cached, ok := idx.memo[key]; ok {
		return cached
	}
	var out []*types.Func
	for _, cm := range idx.byID[key.id] {
		if types.Implements(cm.recv, iface) {
			out = append(out, cm.fn)
		}
	}
	idx.memo[key] = out
	return out
}

// newDispatchIndex enumerates the concrete types the loaded tree can
// dispatch to and records their method sets.
//
// The universe is every named type declared in a type-checked package,
// including inside a function body, plus every named type in the
// transitive closure of what those packages import. Three types of
// candidate are deliberately drawn wider than they need to be, and the
// reason is the same each time: an UNDER-approximation here silently
// loses call edges, which is a worse outcome than any amount of memory
// this saves.
//
// It is wider than CHA's universe, which was whatever
// ssautil.AllFunctions happened to reach — package-level functions, the
// exported types of syntactic packages, and everything structurally
// reachable from a type converted to an interface, a rule x/tools's own
// doc comment calls unprincipled. Reproducing that set exactly would
// mean reproducing SSA's reachability, which is the thing being removed;
// being a strict superset of it is both achievable and safe.
// invokeparity_test.go is the account of the difference and the
// assertion that it cannot reach atlas's output.
//
// The cost of the wider universe is not theoretical and it is small:
// BenchmarkDispatchStage prices the whole index at 30 MB of cumulative
// allocation against CHA's 247 MB on this repository.
func newDispatchIndex(units []checkedPackage) *dispatchIndex {
	idx := &dispatchIndex{
		byID:  make(map[string][]concreteMethod),
		memo:  make(map[imethod][]*types.Func),
		added: make(map[*types.TypeName]bool),
	}
	visited := make(map[*types.Package]bool)
	for _, unit := range units {
		idx.addScope(unit.types.Scope())
		idx.addLocalTypes(unit.info)
		idx.addImports(unit.types, visited)
	}
	return idx
}

// addImports records the named types of everything a package imports,
// transitively.
//
// Dependencies are not decoration here. 41% of the candidates CHA found
// on this repository are declared in one — every `err.Error()`,
// `ctx.Done()` and `n.Pos()` in the tree dispatches into stdlib — and
// packages/codeindex/go/typed.go counts them towards Edge.Ambiguous
// precisely because it cannot see them. Enumerating only the scanned
// tree would drop 1,011 of 2,386 candidates.
//
// The walk is over types.Package.Imports rather than
// packages.Package.Imports because loadMode omits NeedDeps: the go tool
// leaves the latter unpopulated, while the type checker's own import
// graph is complete, having been decoded from export data.
func (idx *dispatchIndex) addImports(pkg *types.Package, visited map[*types.Package]bool) {
	for _, imp := range pkg.Imports() {
		if visited[imp] {
			continue
		}
		visited[imp] = true
		idx.addScope(imp.Scope())
		idx.addImports(imp, visited)
	}
}

// addScope records every named type declared at package scope.
func (idx *dispatchIndex) addScope(scope *types.Scope) {
	if scope == nil {
		return
	}
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		idx.addTypeName(tn)
	}
}

// addLocalTypes records named types declared inside function bodies.
//
// They cannot be reached through a package scope and they are not
// hypothetical: a type declared in a function, assigned to an interface
// and returned is an ordinary implementation. Missing one would lose
// edges silently, which is the one outcome issue #155 rules out.
func (idx *dispatchIndex) addLocalTypes(info *types.Info) {
	if info == nil {
		return
	}
	for _, obj := range info.Defs {
		if tn, ok := obj.(*types.TypeName); ok {
			idx.addTypeName(tn)
		}
	}
}

func (idx *dispatchIndex) addTypeName(tn *types.TypeName) {
	if tn == nil || idx.added[tn] || tn.IsAlias() {
		return
	}
	idx.added[tn] = true
	named, ok := tn.Type().(*types.Named)
	if !ok || types.IsInterface(named) {
		return
	}
	// Both T and *T. The method set of *T is the larger of the two, and
	// which one satisfies an interface is exactly what the pointer-versus-
	// value receiver rule decides: a type whose Save has a pointer
	// receiver implements Repo only as *T.
	idx.addMethodSet(named)
	idx.addMethodSet(types.NewPointer(named))
}

func (idx *dispatchIndex) addMethodSet(recv types.Type) {
	mset := idx.msets.MethodSet(recv)
	for i := range mset.Len() {
		fn, ok := mset.At(i).Obj().(*types.Func)
		if !ok {
			continue
		}
		// A concrete type's method set can contain an ABSTRACT method:
		// `type Store struct{ Repo }` promotes Repo's methods onto
		// Store, and their objects are still declared on the interface.
		// Recording one would answer a call through Repo with Repo.Save
		// itself -- a callee that runs nothing, and one the scanner
		// would happily emit an edge to, since it indexes interface
		// methods as symbols. The dispatch a struct embedding an
		// interface performs is the field's, and the field's
		// implementations are already in this index on their own
		// account.
		if isInterfaceMethod(fn) {
			continue
		}
		// Origin, for the reason ObjectKey applies it: one generic
		// declaration produces one *types.Func per instantiation, and
		// the scanner indexed the declaration.
		fn = fn.Origin()
		id := fn.Id()
		idx.byID[id] = append(idx.byID[id], concreteMethod{recv: recv, fn: fn})
	}
}
