// Package runner is the driving half of per-test coverage collection: the
// side that reads a repo's test sources, decides what granularity each
// package can honestly be collected at, runs the suite with the shim armed,
// and turns the counter snapshots it left behind into coverprofiles.
//
// It is deliberately separate from packages/coverage/shim, which is linked
// into every test binary that opts in and therefore stays stdlib-thin. This
// package is only ever imported by atlas itself.
package runner

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Analysis is what a static read of one package's directory says about how
// its tests can be collected.
type Analysis struct {
	// Dir is the directory analysed.
	Dir string
	// Clause is the package clause a generated TestMain must use. For a
	// suite that lives entirely in an external test package that is
	// "<pkg>_test", not "<pkg>".
	Clause string
	// Tests are the package's top-level test functions, sorted by name so
	// every consumer renders them in the same order.
	Tests []Test
	// TestMain is the package's existing TestMain, if it has one.
	TestMain *TestMain
}

// Test is one top-level test function.
type Test struct {
	Name string
	File string
	// Parallel records a t.Parallel() call in the test's own body. See
	// Analysis.ParallelTests for what that costs and why a miss is safe.
	Parallel bool
}

// TestMain is a package's existing TestMain declaration.
type TestMain struct {
	File string
	// Generated is true when the declaration is one atlas wrote, which is
	// the only kind it may overwrite.
	Generated bool
}

// TestNames returns the test names, sorted.
func (a Analysis) TestNames() []string {
	out := make([]string, 0, len(a.Tests))
	for _, t := range a.Tests {
		out = append(out, t.Name)
	}
	return out
}

// ParallelTests returns the tests that call t.Parallel() in their own body.
//
// Such a package cannot be collected per test without atlas quietly running
// its suite in a shape the developer did not choose: one test at a time,
// with the concurrency those tests asked for taken away. So it degrades to
// per-package instead, and says so.
//
// The read is deliberately shallow — a t.Parallel() reached through a helper
// is not detected. That miss is safe in one direction only, which is the
// direction it falls in: the package gets collected per test, serially, and
// the attribution is still exactly what ran. What is lost is the
// concurrency, not the truth.
func (a Analysis) ParallelTests() []string {
	var out []string
	for _, t := range a.Tests {
		if t.Parallel {
			out = append(out, t.Name)
		}
	}
	return out
}

// Analyze reads one package directory. A directory with no Go files at all
// is not an error: `--package ./...` hands this plenty of them.
func Analyze(dir string) (Analysis, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Analysis{}, fmt.Errorf("runner: read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	a := Analysis{Dir: dir}
	fset := token.NewFileSet()
	var prodClause, testClause string
	for _, name := range names {
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			// A file atlas cannot parse is one it cannot reason about; the
			// go tool will report it far better than we can, and guessing
			// here would produce a plan for a package that will not build.
			return Analysis{}, fmt.Errorf("runner: parse %s: %w", path, err)
		}
		clause := file.Name.Name
		isTest := strings.HasSuffix(name, "_test.go")
		if !isTest {
			if prodClause == "" {
				prodClause = clause
			}
			continue
		}
		tests, hasMain := scanTestFile(file)
		if hasMain {
			a.TestMain = &TestMain{File: path, Generated: isGenerated(path)}
		}
		if len(tests) > 0 && testClause == "" {
			testClause = clause
		}
		for i := range tests {
			tests[i].File = path
		}
		a.Tests = append(a.Tests, tests...)
	}

	a.Clause = testClause
	if a.Clause == "" {
		a.Clause = prodClause
	}
	sort.Slice(a.Tests, func(i, j int) bool { return a.Tests[i].Name < a.Tests[j].Name })
	return a, nil
}

// scanTestFile pulls the top-level tests out of one parsed test file and
// reports whether it declares TestMain.
func scanTestFile(file *ast.File) ([]Test, bool) {
	var tests []Test
	hasMain := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if fn.Name.Name == "TestMain" {
			hasMain = true
			continue
		}
		param, ok := testParam(fn)
		if !ok {
			continue
		}
		tests = append(tests, Test{Name: fn.Name.Name, Parallel: callsParallel(fn, param)})
	}
	return tests, hasMain
}

// testParam reports whether fn is a test function — `func TestXxx(t
// *testing.T)`, by the same naming rule the testing package applies — and
// returns the receiver-parameter's name so parallelism can be looked for on
// it. The name may be "_", in which case the test cannot call t.Parallel().
func testParam(fn *ast.FuncDecl) (string, bool) {
	name := fn.Name.Name
	if !strings.HasPrefix(name, "Test") {
		return "", false
	}
	if rest := name[len("Test"):]; rest != "" && unicode.IsLower(rune(rest[0])) {
		return "", false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return "", false
	}
	p := fn.Type.Params.List[0]
	star, ok := p.Type.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "testing" {
		return "", false
	}
	if len(p.Names) != 1 {
		return "", false
	}
	return p.Names[0].Name, true
}

// callsParallel looks for `<param>.Parallel()` in the test's own body,
// stepping over function literals: a t.Parallel() inside one belongs to a
// subtest, and subtests finish before their parent's run returns.
func callsParallel(fn *ast.FuncDecl, param string) bool {
	if param == "_" || fn.Body == nil {
		return false
	}
	found := false
	walk := func(n ast.Node) bool {
		if found {
			return false
		}
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Parallel" {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == param {
			found = true
			return false
		}
		return true
	}
	ast.Inspect(fn.Body, walk)
	return found
}

// isGenerated reports whether a file carries atlas's generated marker. Read
// as bytes rather than from the parsed comments: the marker is a contract
// with whoever opens the file, not with the parser.
func isGenerated(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), generatedMarker)
}
