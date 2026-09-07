package resolver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// The unit tests for the types-level dispatch index. They cover the four
// shapes invokeparity_test.go's corpora happen not to contain, which is
// the only reason to hand-write a fixture when a whole-repository
// comparison against CHA is available.

// checkStandalone type-checks src into the supplied Info and returns it as
// the dispatch index sees a loaded package.
//
// It drives types.Config.Check by hand rather than going through
// packages.Load, because one of these tests is about what happens when
// types.Info is populated differently from the way go/packages populates
// it -- which packages.Load offers no way to ask for.
func checkStandalone(t *testing.T, src string, info *types.Info) checkedPackage {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dispatch.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkg := types.NewPackage("example.com/dispatch", "dispatch")
	if err := types.NewChecker(&types.Config{}, fset, pkg, info).Files([]*ast.File{file}); err != nil {
		t.Fatalf("type-check: %v", err)
	}
	return checkedPackage{types: pkg, info: info, syntax: []*ast.File{file}}
}

// dispatchIn resolves src and returns the callees of its single interface
// call site, by ObjectKey. Every fixture below has exactly one, so a
// second would mean the fixture drifted rather than the index.
func dispatchIn(t *testing.T, src string) []string {
	t.Helper()
	invokes := dispatchOver([]checkedPackage{checkStandalone(t, src, fullInfo())})
	if len(invokes) != 1 {
		t.Fatalf("interface call sites = %d, want exactly 1", len(invokes))
	}
	for _, fns := range invokes {
		return candidateKeys(fns, nil)
	}
	return nil
}

func assertCallees(t *testing.T, got, want []string, why string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("callees = %v, want %v; %s", got, want, why)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("callees = %v, want %v; %s", got, want, why)
		}
	}
}

// TestDispatch_DoesNotNeedTypesInfoTypes re-opens and re-tests issue
// #152's cause 2, which issue #155 asked for by name.
//
// #152 measured 98.4 MB of CUMULATIVE ALLOCATION in
// go/types.(*Checker).recordTypeAndValue, found that nothing in atlas
// reads the map it fills, and closed the item as unavailable anyway --
// because go/ssa read it. ssa.Function.typeOf calls types.Info.TypeOf,
// whose only fallback for a nil Types map answers for *ast.Ident and
// nothing else, so the first composite expression in the first function
// body panicked. TestSSA_RequiresTypesInfoTypes is still that
// measurement, and it still passes.
//
// That constraint is gone. The dispatch index reads Selections and Defs
// and never asks an expression for its type. This is the proof, on the
// same six-declaration program the SSA test panics on, so the two can be
// read side by side.
//
// What did NOT change is whether the bytes can be collected, and
// docs/performance.md carries that half: packages.LoadMode has no bit for
// a subset of types.Info -- NeedTypesInfo fills every map or none -- so
// taking the saving still means driving types.Config.Check by hand, which
// still means reimplementing the per-package degradation path issue #87
// built. The constraint that moved is a correctness one, not a cost one.
func TestDispatch_DoesNotNeedTypesInfoTypes(t *testing.T) {
	t.Parallel()

	full := checkStandalone(t, dispatchSrc, fullInfo())
	want := calleesOfSoleSite(t, dispatchOver([]checkedPackage{full}))
	if len(want) != 2 {
		t.Fatalf("callees with a full types.Info = %v, want both Mem.Save and PG.Save", want)
	}

	lean := fullInfo()
	lean.Types = nil // the 98.4 MB issue #152 wants back
	got := calleesOfSoleSite(t, dispatchOver([]checkedPackage{checkStandalone(t, dispatchSrc, lean)}))

	assertCallees(t, got, want,
		"the dispatch index must answer identically with types.Info.Types nil; "+
			"if it no longer does, issue #152 cause 2 is blocked again and by something new")
}

func calleesOfSoleSite(t *testing.T, invokes map[token.Pos][]*types.Func) []string {
	t.Helper()
	if len(invokes) != 1 {
		t.Fatalf("interface call sites = %d, want exactly 1", len(invokes))
	}
	for _, fns := range invokes {
		return candidateKeys(fns, nil)
	}
	return nil
}

// An interface behind a struct field is the one shape where the value
// dispatch happens on is not the receiver expression. SSA makes the two
// steps explicit -- a FieldAddr, then an Invoke typed by the field -- and
// receiverInterface is the types-level spelling of that walk. Resolve
// against the STRUCT instead and the answer is Store itself, which
// implements Repo by promotion, rather than the two implementations the
// field can hold.
const embeddedIfaceSrc = `package dispatch

type Repo interface{ Save(id string) error }

type Mem struct{}

func (Mem) Save(id string) error { return nil }

type PG struct{}

func (PG) Save(id string) error { return nil }

type Store struct{ Repo }

func Use(s Store, id string) error { return s.Save(id) }
`

func TestDispatch_ThroughAnInterfaceEmbeddedInAStruct(t *testing.T) {
	t.Parallel()
	assertCallees(t, dispatchIn(t, embeddedIfaceSrc),
		[]string{"(example.com/dispatch.Mem).Save", "(example.com/dispatch.PG).Save"},
		"the dispatch is on the embedded Repo field, not on Store")
}

// A type declared inside a function body cannot carry a method -- Go
// requires a method's receiver to name a package-scope type -- so it
// reaches an interface only by EMBEDDING one. That makes it easy to
// believe function-local types cannot matter here. They can: `both` below
// is the only type in the program that satisfies Repo, and it exists in
// no package scope. Enumerate package scopes alone and this call site
// silently resolves to nothing, which is the failure mode issue #155
// ranks above any saving. addLocalTypes walking types.Info.Defs is what
// reaches it.
const localTypeSrc = `package dispatch

type Repo interface {
	Save(id string) error
	Load(id string) error
}

type saver struct{}

func (saver) Save(id string) error { return nil }

type loader struct{}

func (loader) Load(id string) error { return nil }

func New() Repo {
	type both struct {
		saver
		loader
	}
	return both{}
}

func Store(r Repo, id string) error { return r.Save(id) }
`

func TestDispatch_ImplementationAssembledInsideAFunction(t *testing.T) {
	t.Parallel()
	assertCallees(t, dispatchIn(t, localTypeSrc),
		[]string{"(example.com/dispatch.saver).Save"},
		"saver alone does not satisfy Repo; the only type that does is declared "+
			"inside New, and package-scope enumeration cannot see it")
}

// Dispatch on a type parameter resolves against the CONSTRAINT, and the
// candidates must be the declarations rather than the instantiations.
// types.Func.Origin is what collapses them -- the types-level counterpart
// of the origin() that mapped an ssa.Function back to its declaration --
// and without it a generic method produces one callee per instantiation,
// none of whose ids match the one the scanner registered.
const genericSrc = `package dispatch

type Named interface{ Name() string }

type Box[T any] struct{ v T }

func (Box[T]) Name() string { return "box" }

func Describe[T Named](t T) string { return t.Name() }

var _ = Describe(Box[int]{})
var _ = Describe(Box[string]{})
`

func TestDispatch_OnATypeParameterKeysToTheDeclaration(t *testing.T) {
	t.Parallel()
	assertCallees(t, dispatchIn(t, genericSrc),
		[]string{"(example.com/dispatch.Box[T]).Name"},
		"two instantiations of Box must collapse to the one declaration they share")
}
