package resolver

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// These tests pin the two things a reader of issue #152 will want to
// change and cannot: the fields of types.Info this package pays for, and
// the set of packages SSA is constructed over. Both look like waste from
// the outside — atlas itself never reads types.Info.Types, and it never
// walks a dependency's call sites — and both are load-bearing through
// go/ssa. Each test is the measurement that says so, kept executable so
// the answer stays true against a future x/tools rather than being a
// paragraph someone has to trust.

// dispatchSrc is the smallest program containing an interface dispatch.
// It imports nothing, so SSA can be built for it alone and any failure is
// about the types.Info it was handed rather than about a missing
// dependency.
const dispatchSrc = `package dispatch

type Repo interface{ Save(id string) error }

type Mem struct{}

func (Mem) Save(id string) error { return nil }

type PG struct{}

func (PG) Save(id string) error { return nil }

func Store(r Repo, id string) error { return r.Save(id) }
`

// fullInfo is every types.Info field packages.NeedTypesInfo populates.
func fullInfo() *types.Info {
	return &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Instances:    make(map[*ast.Ident]types.Instance),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Implicits:    make(map[ast.Node]types.Object),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:       make(map[ast.Node]*types.Scope),
		FileVersions: make(map[*ast.File]string),
	}
}

// checkDispatch type-checks dispatchSrc into the supplied Info and
// creates its SSA package without building any function bodies.
func checkDispatch(t *testing.T, info *types.Info) (*ssa.Program, *ssa.Package) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dispatch.go", dispatchSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkg := types.NewPackage("example.com/dispatch", "dispatch")
	if err := types.NewChecker(&types.Config{}, fset, pkg, info).Files([]*ast.File{file}); err != nil {
		t.Fatalf("type-check: %v", err)
	}
	prog := ssa.NewProgram(fset, ssa.BuilderMode(0))
	return prog, prog.CreatePackage(pkg, []*ast.File{file}, info, false)
}

// buildRecovering builds one SSA package and returns what it panicked
// with, or nil. ssa.Package.Build runs in the caller's goroutine, unlike
// ssa.Program.Build, which is the only reason a panic here is observable
// at all — see TestSSA_DependencyPackagesAreLoadBearing.
func buildRecovering(p *ssa.Package) (recovered any) {
	defer func() { recovered = recover() }()
	p.Build()
	return nil
}

// invokeSites counts what buildCallGraph would index: interface dispatch
// sites CHA resolved to at least one concrete method.
func invokeSites(prog *ssa.Program) int {
	seen := map[token.Pos]bool{}
	for _, node := range cha.CallGraph(prog).Nodes {
		for _, edge := range node.Out {
			if pos, _, ok := invokeTarget(edge); ok {
				seen[pos] = true
			}
		}
	}
	return len(seen)
}

// Issue #152 counted go/types.(*Checker).recordTypeAndValue as the single
// largest allocator in a scan — 100.22 MB, 12.5% — and observed that
// nothing in atlas reads the map it fills. Both halves are true and the
// conclusion does not follow: go/ssa reads it. ssa.Function.typeOf calls
// types.Info.TypeOf, whose only fallback for a nil Types map is
// ObjectOf, which answers for *ast.Ident and nothing else, so the first
// composite expression in the first function body panics.
//
// This test is what closes that line item. It is the measurement, not a
// claim: it type-checks the same source twice into the same checker,
// differing only in whether Types was allocated, and shows the second one
// cannot be turned into SSA at all.
//
// Issue #155 changes who this binds. SSA left the resolver, so production
// no longer needs types.Info.Types for THIS reason — see
// TestLoad_TypesInfoTypesIsStillLoadBearing for the reason it still cannot
// be dropped, and docs/performance.md for the measurement. What the test
// pins now is the CHA oracle: it needs Types, so a future attempt to check
// the tree with a leaner types.Info has to leave the oracle a full one or
// lose its only second opinion.
func TestSSA_RequiresTypesInfoTypes(t *testing.T) {
	t.Parallel()

	t.Run("with Types, SSA builds and CHA resolves the dispatch", func(t *testing.T) {
		t.Parallel()
		prog, pkg := checkDispatch(t, fullInfo())
		if r := buildRecovering(pkg); r != nil {
			t.Fatalf("Build panicked with a fully populated types.Info: %v", r)
		}
		if got := invokeSites(prog); got != 1 {
			t.Fatalf("invoke sites = %d, want 1 (r.Save in Store)", got)
		}
	})

	t.Run("without Types, SSA cannot be built", func(t *testing.T) {
		t.Parallel()
		info := fullInfo()
		info.Types = nil // the 100.22 MB issue #152 wants back
		prog, pkg := checkDispatch(t, info)

		r := buildRecovering(pkg)
		if r == nil {
			t.Fatalf("Build succeeded with types.Info.Types nil; " +
				"x/tools no longer needs it and issue #152 cause 2 should be reopened")
		}
		if got := invokeSites(prog); got != 0 {
			t.Fatalf("invoke sites = %d after a failed build, want 0", got)
		}
	})
}

// checkedPackages is what Load computes dispatch over, obtained the same
// way Load obtains it -- see resolvedPackages.
func checkedPackages(t *testing.T, dir string) []*packages.Package {
	t.Helper()
	return resolvedPackages(t, dir)
}

// viewedPackages is the set of packages a loaded Program indexes files for.
func viewedPackages(p *Program) []*packages.Package {
	seen := map[*packages.Package]bool{}
	var out []*packages.Package
	for _, view := range p.byPath {
		if !seen[view.pkg] {
			seen[view.pkg] = true
			out = append(out, view.pkg)
		}
	}
	return out
}

// loadObservingSSA builds the CHA oracle over the packages production
// resolves for dir, and returns the ssa.Program it built, the packages it
// was built over, and a real Load of the same tree.
//
// The oracle's SCOPE is what makes invokeparity_test.go's comparison mean
// anything: ssautil.Packages gives bodies to the packages it is handed and
// declaration-only shells to everything reached from them, so the set
// handed in fixes which concrete methods CHA can name at all. Widen it and
// the oracle answers a different question while still looking like CHA.
// That used to be a property of production; since issue #155 removed SSA
// from the resolver it is a property of the oracle, and it needs the same
// assertion for a better reason.
func loadObservingSSA(t *testing.T, dir string) (*Program, *ssa.Program, []*packages.Package) {
	t.Helper()

	built := resolvedPackages(t, dir)

	var got *ssa.Program
	ssaObserver = func(prog *ssa.Program, _ []*packages.Package) { got = prog }
	t.Cleanup(func() { ssaObserver = nil })

	if _, ok := chaInvokes(built); !ok {
		t.Fatalf("the CHA oracle failed on %s; there is no program to measure", dir)
	}
	if got == nil {
		t.Fatal("the oracle did not reach the SSA observer; this test is measuring nothing")
	}

	p, err := Load(context.Background(), dir, Options{IncludeTests: true})
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	if !p.Status().CallGraph {
		t.Fatalf("Load(%s) computed no dispatch; the fixture is broken", dir)
	}
	return p, got, built
}

// ssaCensus is what an SSA scope measurement counts. The same census backs
// TestSSA_NoFunctionBodiesOutsideTheScannedTree and
// BenchmarkCallGraphScope, so the test's assertion and the benchmark's
// reported metrics cannot drift apart.
//
// The four body counts partition bodies exactly:
//
//	declared + syntheticInside + outsideSource + outsideSynthetic
//	    + unattributable == bodies
type ssaCensus struct {
	ssaPkgs int // ssa.Packages created, in total
	shells  int // ...of those, for a package outside the scanned set
	bodies  int // functions with at least one basic block

	declared         int // built from a scanned package's own syntax
	syntheticInside  int // a wrapper over a scanned package's method
	outsideSource    int // built from a DEPENDENCY's syntax
	outsideSynthetic int // a wrapper over a dependency's method
	unattributable   int // a body naming no types.Package at all

	// instrs sizes the population the counts describe, because "81
	// bodies" and "81 functions' worth of code" are very different
	// claims and the whole ~99 MB question is about the second.
	instrs        int // SSA instructions across every counted body
	outsideInstrs int // ...of those, in outsideSynthetic bodies

	invokeSites int
}

// takeCensus walks every function in a built program and attributes it.
//
// The attribution is the point, and it is why this is not the two-line
// loop it replaced. That loop skipped every function with fn.Pkg == nil.
// Those are not a handful of oddities: go/ssa synthesises a wrapper
// whenever a pointer type needs a value-receiver method in its method set,
// for every bound method expression, and for every generic instantiation,
// and none of them carries an ssa.Package. Skipping them and reporting "0
// bodies outside the scanned tree" left a whole class out of a number
// presented as a census — 87 of the golden corpus's 167 built bodies, and
// 1,262 of this repository's 10,074 — and a wrapper over a dependency's
// method is exactly the kind of body it claimed there were none of. 81 of
// the golden corpus's 87 are over `time`, `os` and `sync/atomic`.
//
// A synthetic function is attributed through the object it wraps, then
// through the origin it was instantiated from. Both name the declaration
// the wrapper exists to reach, which is the package that would have to be
// in scope for it to be built at all.
func takeCensus(prog *ssa.Program, scanned map[*types.Package]bool) ssaCensus {
	var c ssaCensus
	for _, ssaPkg := range prog.AllPackages() {
		c.ssaPkgs++
		if !scanned[ssaPkg.Pkg] {
			c.shells++
		}
	}
	for fn := range ssautil.AllFunctions(prog) {
		if len(fn.Blocks) == 0 {
			continue
		}
		c.bodies++
		n := 0
		for _, b := range fn.Blocks {
			n += len(b.Instrs)
		}
		c.instrs += n

		owner := ownerPackage(fn)
		switch {
		case owner == nil:
			c.unattributable++
		case scanned[owner] && fn.Pkg != nil:
			c.declared++
		case scanned[owner]:
			c.syntheticInside++
		case fn.Pkg != nil:
			c.outsideSource++
		default:
			c.outsideSynthetic++
			c.outsideInstrs += n
		}
	}

	seen := map[token.Pos]bool{}
	for _, node := range cha.CallGraph(prog).Nodes {
		for _, edge := range node.Out {
			if pos, _, ok := invokeTarget(edge); ok {
				seen[pos] = true
			}
		}
	}
	c.invokeSites = len(seen)
	return c
}

// ownerPackage names the package a built function belongs to, including
// the synthetic ones ssautil.AllFunctions reaches that have no
// ssa.Package of their own.
func ownerPackage(fn *ssa.Function) *types.Package {
	if fn.Pkg != nil {
		return fn.Pkg.Pkg
	}
	if obj := fn.Object(); obj != nil && obj.Pkg() != nil {
		return obj.Pkg()
	}
	if o := fn.Origin(); o != nil && o != fn {
		return ownerPackage(o)
	}
	return nil
}

// Issue #152 cause 3 supposed SSA function bodies were being built for
// every transitive dependency and that narrowing the scope would recover
// ~99 MB. The scope is already narrow: buildCallGraph calls
// ssautil.Packages, which passes syntax and types.Info only for the
// packages it was handed, so a dependency gets an ssa.Package with
// declarations and no code.
//
// This test measures that on the program the RESOLVER built, through the
// ssaObserver hook. It used to build its own with its own call to
// ssautil.Packages, which measured the test's arguments rather than
// production's: switching buildCallGraph to ssautil.AllPackages — the one
// word this whole decision comes down to — left it green.
//
// WHAT "0" MEANS HERE, precisely, because the earlier version of this test
// overstated it. Zero bodies are built from a dependency's SYNTAX, which
// is the property ssautil.Packages guarantees and the only one the ~99 MB
// hypothesis was about: a dependency arrives with no syntax at all, so
// there is nothing to build. It is NOT zero bodies touching dependency
// code. go/ssa synthesises a pointer-receiver wrapper whenever the scanned
// tree needs *T's method set for a T declared elsewhere, and on the golden
// corpus 81 such wrappers exist over `time`, `os` and `sync/atomic`. They
// are counted, they are reported, and they are three instructions each —
// a load, a call and a return — which is why they are not a memory
// finding. The point of counting them is that nobody has to take that on
// trust.
func TestSSA_NoFunctionBodiesOutsideTheScannedTree(t *testing.T) {
	// No t.Parallel: ssaObserver is package state.
	p, prog, built := loadObservingSSA(t, goldenCorpus)

	// "The scanned tree" is the set production handed to ssautil.Packages,
	// because that set is what decides the scope: everything in it is an
	// initial package and gets bodies, everything reached from it is a
	// declaration-only shell. Taking the set from anywhere else would let
	// the census pass while production widened.
	scanned := map[*types.Package]bool{}
	for _, pkg := range built {
		scanned[pkg.Types] = true
	}
	// ...and that set has to cover the resolver's own packages rather than
	// some other selection. Every package a real Load indexes files for
	// has to be in it, or the oracle is answering about a different tree
	// than the one production resolved. By path, not by pointer: the two
	// come from two loads and share no *types.Package values.
	paths := map[string]bool{}
	for _, pkg := range built {
		paths[pkg.PkgPath] = true
	}
	for _, pkg := range viewedPackages(p) {
		if !paths[pkg.PkgPath] {
			t.Fatalf("the Program indexes %s and the oracle was not given it; "+
				"the census is comparing different sets", pkg.PkgPath)
		}
	}

	c := takeCensus(prog, scanned)
	t.Logf("ssaPkgs=%d shells=%d bodies=%d instrs=%d "+
		"(declared=%d syntheticInside=%d outsideSource=%d outsideSynthetic=%d[%d instrs] "+
		"unattributable=%d) invokeSites=%d",
		c.ssaPkgs, c.shells, c.bodies, c.instrs,
		c.declared, c.syntheticInside, c.outsideSource, c.outsideSynthetic, c.outsideInstrs,
		c.unattributable, c.invokeSites)

	if got := c.declared + c.syntheticInside + c.outsideSource + c.outsideSynthetic +
		c.unattributable; got != c.bodies {
		t.Fatalf("the census does not partition its own population: %d accounted for, %d bodies",
			got, c.bodies)
	}
	if c.outsideSource != 0 {
		t.Errorf("%d function bodies built from dependency SYNTAX, want 0; the oracle is "+
			"no longer handing ssautil.Packages only the scanned tree", c.outsideSource)
	}
	if c.shells == 0 {
		t.Errorf("no dependency ssa.Packages created; this fixture no longer exercises the split")
	}
	// The two guards that stop the census quietly shrinking back to what it
	// was. Without them the zero above is satisfiable by a population with
	// every synthetic wrapper excluded from it, which is the defect this
	// test was rewritten for.
	if c.outsideSynthetic == 0 {
		t.Errorf("no synthetic wrappers over dependency methods counted; the census is back " +
			"to skipping fn.Pkg == nil and outsideSource == 0 no longer covers them")
	}
	// Size, not share. The share is meaningless on a four-package fixture —
	// the golden corpus has so little code of its own that these wrappers
	// are 371 of its 993 instructions — but the SIZE of each one is the
	// claim being made, and it is what separates "a wrapper reaching a
	// dependency's method" from "a dependency's body". A wrapper is a load,
	// a call and a return; a real function is not. If a dependency's syntax
	// ever did reach the builder, outsideSource above catches it and this
	// mean moves with it.
	if mean := float64(c.outsideInstrs) / float64(c.outsideSynthetic); mean > 10 {
		t.Errorf("synthetic bodies over dependencies average %.1f SSA instructions each "+
			"(%d over %d), which is no longer wrapper-sized; re-measure before calling this "+
			"scope narrow", mean, c.outsideInstrs, c.outsideSynthetic)
	}
}

// The dependency ssa.Packages the previous test counts hold no code, so
// skipping them looks like free memory. It is not available: SSA calls
// every imported package's init from the importing package's init, and
// asserts the import was created.
//
// The failure mode is the reason this is a test rather than a comment.
// ssa.Program.Build builds packages on parallel goroutines, so that panic
// does not travel to the recover in chaInvokes — it takes the process
// down. This test provokes it through ssa.Package.Build, which runs
// inline, so it can be observed without dying.
func TestSSA_DependencyPackagesAreLoadBearing(t *testing.T) {
	t.Parallel()
	checked := checkedPackages(t, goldenCorpus)

	var fset *token.FileSet
	var withImports *packages.Package
	for _, pkg := range checked {
		if fset == nil {
			fset = pkg.Fset
		}
		if len(pkg.Types.Imports()) > 0 {
			withImports = pkg
			break
		}
	}
	if withImports == nil {
		t.Skip("no package in the fixture imports anything")
	}

	// Only the one package. Its imports are deliberately not created,
	// which is what "narrow the SSA scope to what atlas indexes" would
	// mean if taken literally.
	prog := ssa.NewProgram(fset, ssa.BuilderMode(0))
	pkg := prog.CreatePackage(withImports.Types, withImports.Syntax, withImports.TypesInfo, false)

	if r := buildRecovering(pkg); r == nil {
		t.Fatalf("building %s without its imports succeeded; "+
			"x/tools dropped the unsatisfied-import invariant and issue #152 cause 3 should be reopened",
			withImports.PkgPath)
	}
}

// buildCallGraph promises, in a comment, that a panic out of SSA
// construction costs interface dispatch and not the scan. Until this test
// existed the promise was not kept for the panic that actually happens:
// ssa.Program.Build builds packages on goroutines it spawns itself and
// recovers nothing, so a panic in the builder unwinds a goroutine no
// recover in this package is on the stack of, and the process dies.
//
// The trigger used here is the one cause 2 established is real — a
// types.Info with a nil Types map. accept() is supposed to keep such a
// package out of the call graph, and this is what happens when something
// gets past it: on a mid-edit tree that is a crashed scan rather than a
// degraded one, which is the opposite of what issue #87 built.
//
// If buildAllSSA is ever replaced by prog.Build() again, this test does
// not fail; the test binary dies.
func TestBuildAllSSA_SurvivesABuilderPanic(t *testing.T) {
	t.Parallel()

	info := fullInfo()
	info.Types = nil
	prog, _ := checkDispatch(t, info)

	if buildAllSSA(prog) {
		t.Fatal("buildAllSSA reported success on a package it cannot build")
	}
}
