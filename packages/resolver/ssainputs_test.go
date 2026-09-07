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
// The panic is worth understanding before anyone tries again. It is
// caught by the recover in buildCallGraph, so the visible symptom of
// "save 100 MB by leaving Types nil" would not be a crash — it would be
// Status.CallGraph going quietly false and every interface edge in the
// repository disappearing.
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

// checkedPackages is what Load hands to buildCallGraph, obtained the same
// way Load obtains it so the scope these tests measure is the scope the
// resolver actually uses.
func checkedPackages(t *testing.T, dir string) []*packages.Package {
	t.Helper()
	p, err := Load(context.Background(), dir, Options{IncludeTests: true})
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	if !p.Status().CallGraph {
		t.Fatalf("Load(%s) built no call graph; the fixture is broken", dir)
	}
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

// Issue #152 cause 3 supposed SSA function bodies were being built for
// every transitive dependency and that narrowing the scope would recover
// ~99 MB. The scope is already narrow: ssautil.Packages passes syntax and
// types.Info only for the packages it was handed, so a dependency gets an
// ssa.Package with declarations and no code. This test measures that,
// because the cheap way to lose it is to add packages.NeedDeps to
// loadMode — at which point every dependency arrives with syntax, becomes
// an initial package, and the ~99 MB the issue was looking for appears
// for real.
func TestSSA_NoFunctionBodiesOutsideTheScannedTree(t *testing.T) {
	t.Parallel()
	checked := checkedPackages(t, goldenCorpus)

	scanned := map[*types.Package]bool{}
	for _, pkg := range checked {
		scanned[pkg.Types] = true
	}

	prog, _ := ssautil.Packages(checked, ssa.BuilderMode(0))
	prog.Build()

	shells, outsiders := 0, 0
	for _, ssaPkg := range prog.AllPackages() {
		if !scanned[ssaPkg.Pkg] {
			shells++
		}
	}
	for fn := range ssautil.AllFunctions(prog) {
		if len(fn.Blocks) == 0 || fn.Pkg == nil {
			continue
		}
		if !scanned[fn.Pkg.Pkg] {
			outsiders++
		}
	}
	if outsiders != 0 {
		t.Errorf("%d function bodies built outside the scanned tree, want 0", outsiders)
	}
	if shells == 0 {
		t.Errorf("no dependency ssa.Packages created; this fixture no longer exercises the split")
	}
}

// The dependency ssa.Packages the previous test counts hold no code, so
// skipping them looks like free memory. It is not available: SSA calls
// every imported package's init from the importing package's init, and
// asserts the import was created.
//
// The failure mode is the reason this is a test rather than a comment.
// ssa.Program.Build builds packages on parallel goroutines, so that panic
// does not travel to the recover in buildCallGraph — it takes the process
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
