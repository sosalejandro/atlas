package resolver

import (
	"context"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a one-line helper so the temp-module test reads as what it
// is testing rather than as file plumbing.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// The fixtures live with the scanner that consumes them: this package has
// no testdata of its own on purpose, so a corpus edit cannot make the
// resolver's tests and the scanner's golden snapshot disagree about what
// the tree contains.
const (
	goldenCorpus = "../codeindex/go/testdata/goldencorpus"
	brokenCorpus = "../codeindex/go/testdata/brokencorpus"
	noModule     = "../codeindex/go/testdata/sampleproject"
)

func load(t *testing.T, dir string, opts Options) *Program {
	t.Helper()
	p, err := Load(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	return p
}

// findCall returns the first call expression inside the named function of
// the named file, whose callee identifier is want.
func findCall(t *testing.T, p *Program, file, fn, want string) (*ast.File, *ast.CallExpr) {
	t.Helper()
	abs, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	syntax, ok := p.Syntax(abs)
	if !ok {
		t.Fatalf("%s is not type-checked", file)
	}
	var found *ast.CallExpr
	for _, decl := range syntax.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Name.Name != fn || d.Body == nil {
			continue
		}
		ast.Inspect(d.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || found != nil {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == want {
				found = call
			}
			return true
		})
	}
	if found == nil {
		t.Fatalf("no call to %s inside %s in %s", want, fn, file)
	}
	return syntax, found
}

// The interface call is the one the AST resolver could not follow. CHA
// must name every implementation, and only implementations -- not the
// interface method itself, which is not a function anything runs.
func TestResolveCall_InterfaceDispatchNamesEveryImplementation(t *testing.T) {
	p := load(t, goldenCorpus, Options{IncludeTests: true})
	file, call := findCall(t, p, goldenCorpus+"/internal/services/orders/service.go", "Create", "Save")

	r := p.ResolveCall(file, call)
	if !r.Resolved || !r.ViaInterface {
		t.Fatalf("Resolution = %+v, want a resolved interface dispatch", r)
	}
	got := map[string]bool{}
	for _, fn := range r.Targets {
		got[Receiver(fn)] = true
	}
	for _, want := range []string{"MemoryOrderRepository", "PostgresOrderRepository"} {
		if !got[want] {
			t.Errorf("no target on %s; got %v", want, got)
		}
	}
	if len(r.Targets) != 2 {
		t.Errorf("got %d targets, want exactly the two implementations", len(r.Targets))
	}
}

// A direct method call is not an interface dispatch, and must not be
// reported as one: the caller marks multi-target sites ambiguous, so
// mislabelling a static call would put doubt on an exact answer.
func TestResolveCall_StaticCallIsNotInterfaceDispatch(t *testing.T) {
	p := load(t, goldenCorpus, Options{IncludeTests: true})
	file, call := findCall(t, p, goldenCorpus+"/internal/handlers/order_handler.go", "Create", "decode")

	r := p.ResolveCall(file, call)
	if !r.Resolved || r.ViaInterface || len(r.Targets) != 1 {
		t.Fatalf("Resolution = %+v, want one static target", r)
	}
	if got := r.Targets[0].Name(); got != "decode" {
		t.Errorf("target = %q, want decode", got)
	}
}

// A call on an instantiated generic type must key to the same declaration
// as the generic itself, or the caller mints one symbol id per
// instantiation.
func TestObjectKey_GenericInstantiationKeysToItsDeclaration(t *testing.T) {
	p := load(t, goldenCorpus, Options{IncludeTests: true})
	file, call := findCall(t, p, goldenCorpus+"/internal/persistence/memory_repository.go", "Save", "Put")

	r := p.ResolveCall(file, call)
	if !r.Resolved || len(r.Targets) != 1 {
		t.Fatalf("Resolution = %+v, want one target", r)
	}
	key := ObjectKey(r.Targets[0])
	if strings.Contains(key, "string") || strings.Contains(key, "Order]") {
		t.Errorf("ObjectKey = %q; it carries the type ARGUMENTS, so every instantiation is a different symbol", key)
	}
	if Receiver(r.Targets[0]) != "Cache" {
		t.Errorf("Receiver = %q, want Cache", Receiver(r.Targets[0]))
	}
}

// A conversion is not a call. Reporting it as unresolved rather than as
// "resolved to nothing" is what lets the caller tell "the type checker
// declined" from "the type checker had no opinion".
func TestResolveCall_ConversionIsNotACall(t *testing.T) {
	p := load(t, goldenCorpus, Options{IncludeTests: true})
	abs, _ := filepath.Abs(goldenCorpus + "/internal/platform/config/config.go")
	syntax, ok := p.Syntax(abs)
	if !ok {
		t.Fatal("config.go is not type-checked")
	}
	var conv *ast.CallExpr
	ast.Inspect(syntax, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || conv != nil {
			return true
		}
		// `string(raw)` in Load.
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "string" {
			conv = call
		}
		return true
	})
	if conv == nil {
		t.Skip("fixture no longer contains a string() conversion")
	}
	if r := p.ResolveCall(syntax, conv); r.Resolved {
		t.Errorf("string(raw) reported as a resolved call: %+v", r)
	}
}

// One broken package must not take the tree with it, and the report must
// distinguish it from a tree that could not be enumerated at all.
func TestLoad_DegradesPerPackage(t *testing.T) {
	p := load(t, brokenCorpus, Options{})
	st := p.Status()

	if st.TypeChecked != 1 || st.Degraded != 1 {
		t.Fatalf("Status = %+v, want one package checked and one degraded", st)
	}
	if len(st.LoadErrors) != 0 {
		t.Errorf("LoadErrors = %v; a package that does not compile is not a load failure", st.LoadErrors)
	}
	if !strings.Contains(st.DegradedList[0].Path, "broken") {
		t.Errorf("degraded package = %q, want the `broken` one", st.DegradedList[0].Path)
	}

	sound, _ := filepath.Abs(brokenCorpus + "/sound/sound.go")
	if !p.TypeChecked(sound) {
		t.Error("sound.go is not type-checked, but its package compiles")
	}
	bad, _ := filepath.Abs(brokenCorpus + "/broken/broken.go")
	if p.TypeChecked(bad) {
		t.Error("broken.go is reported as type-checked")
	}
}

// Every message that leaves this package is carried on a scan result that
// gets serialised, stored and diffed. An absolute path in one makes two
// checkouts of one commit disagree.
func TestLoad_ErrorsAreRepoRelative(t *testing.T) {
	p := load(t, noModule, Options{})
	for _, d := range p.Status().DegradedList {
		if filepath.IsAbs(strings.SplitN(d.Error, ":", 2)[0]) {
			t.Errorf("degraded package %s carries an absolute path: %q", d.Path, d.Error)
		}
	}
}

// A directory outside any module is a LOAD failure, not a repo full of
// broken packages, and the two have to read differently: one says fix
// your environment, the other says fix your code.
func TestLoad_OutsideAnyModuleReportsALoadError(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, "x.go"), "package x\n\nfunc F() {}\n"); err != nil {
		t.Fatal(err)
	}
	p := load(t, dir, Options{})
	st := p.Status()
	if len(st.LoadErrors) == 0 {
		t.Fatalf("Status = %+v, want a load error", st)
	}
	if st.TypeChecked != 0 {
		t.Errorf("TypeChecked = %d, want 0", st.TypeChecked)
	}
	if st.Degraded != 0 {
		t.Errorf("Degraded = %d; nothing was found, so nothing degraded", st.Degraded)
	}
}

// The scanner adopts these trees and looks calls up in types.Info, which
// is keyed by node pointers. Handing back a file the program does not own
// has to answer "no" rather than panic or silently resolve nothing.
func TestResolveCall_UnknownFileIsUnresolved(t *testing.T) {
	p := load(t, goldenCorpus, Options{})
	if r := p.ResolveCall(&ast.File{}, &ast.CallExpr{}); r.Resolved {
		t.Errorf("Resolution = %+v for a file this program never saw", r)
	}
	if _, ok := p.Syntax("/nowhere/at/all.go"); ok {
		t.Error("Syntax returned a tree for a path that does not exist")
	}
}

// Interface dispatch answers must not depend on map iteration order: the
// scanner emits one edge per target and the golden snapshot pins the set,
// so an unstable order is an unstable snapshot.
func TestResolveCall_TargetOrderIsStable(t *testing.T) {
	var first []string
	for range 3 {
		p := load(t, goldenCorpus, Options{IncludeTests: true})
		file, call := findCall(t, p, goldenCorpus+"/internal/services/orders/service.go", "Create", "Save")
		var got []string
		for _, fn := range p.ResolveCall(file, call).Targets {
			got = append(got, ObjectKey(fn))
		}
		if first == nil {
			first = got
			continue
		}
		if strings.Join(first, "|") != strings.Join(got, "|") {
			t.Fatalf("target order changed between loads: %v vs %v", first, got)
		}
	}
}
