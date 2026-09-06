package atlastest

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// Decl is one declaration the generator emitted, with the position the
// scanner is expected to report for it.
//
// Position rather than predicted id is what the properties key on. The
// scanner picks a declaration's id from three candidates (bare short name,
// package-qualified, package-qualified plus file base) depending on what is
// already taken, so a test that predicts the id is really a test of the
// prediction. (File, Line) is unambiguous, is what a user sees, and — this is
// the point — a collapse of two declarations onto one id shows up as a
// declaration with NO node at its position, which is exactly the shape of
// issue #85.
type Decl struct {
	// Pkg is the package clause name. It is what the scanner uses to build
	// the short id of a plain function, and it is NOT always the directory:
	// this generator emits two `package main` directories on purpose.
	Pkg string
	// Dir is the repo-relative package directory, "" for the tree root.
	Dir string
	// File is the repo-relative, slash-separated path ("pkg0/f0.go").
	File string
	// Receiver is the receiver TYPE name for a method, "" for a plain func.
	Receiver string
	// Name is the func or method name.
	Name string
	// Line is the 1-based line of the `func` keyword.
	Line int
	// EndLine is the 1-based line of the closing brace.
	EndLine int
	// Stmts is how many coverable statements the body holds, so a
	// synthesised profile can be proportionate to the source it describes.
	Stmts int
}

// ShortID is the bare id the scanner tries first: "Recv.Method" for a method,
// "pkg.Func" for a plain function.
func (d Decl) ShortID() string {
	if d.Receiver != "" {
		return d.Receiver + "." + d.Name
	}
	return d.Pkg + "." + d.Name
}

// Project is a generated Go source tree plus the ground truth about what is
// in it. Files maps repo-relative path to content; Decls is every function
// and method declared, in emission order.
type Project struct {
	Files map[string]string
	Decls []Decl
}

// GoProjectOptions shapes the generated tree. Zero values mean "pick a
// reasonable random size", so callers usually pass the zero struct.
type GoProjectOptions struct {
	// Packages, FilesPerPackage and DeclsPerFile bound the tree size. Left
	// at zero they are drawn from small ranges chosen so a whole property
	// run stays inside a couple of seconds.
	Packages        int
	FilesPerPackage int
	DeclsPerFile    int

	// CollisionPressure is the percentage of METHODS drawn from the shared
	// hot-pair pool instead of a name unique to the package.
	//
	// The default is high on purpose. The bug class this guards only exists
	// in the colliding part of the input space, and a generator that mostly
	// produces easy input mostly proves the easy case. Measured over the 64
	// default seeds, the first version of this generator (pressure 40 on
	// each half of the pair independently) produced a cross-package
	// collision in 2 of the 64 default trees; drawing the PAIR at pressure
	// 70 produces one in 64 of 64. That difference is the difference
	// between a property test and a slower unit test.
	CollisionPressure int

	// NoRootCollision suppresses the two-`package main` shape below. It
	// exists so a caller that wants a tree with no dropped declarations
	// (the acceptance fixture, say) can ask for one.
	NoRootCollision bool
}

// hotPairs is the shared receiver+method pool. Real monorepos collide on
// exactly these: every bounded context declares its own Chat, Order or
// Session, and methods on them are keyed by "Recv.Method" with no package in
// the id at all — which is why methods are where cross-package collisions
// come from and plain functions are not (a plain function's short id already
// carries its package clause).
var hotPairs = [][2]string{
	{"Chat", "MarkLoaded"},
	{"Order", "Total"},
	{"Session", "Close"},
	{"Repo", "Load"},
}

// hotFuncs collide only WITHIN a package, which Go forbids, so they exist to
// vary the shape of the tree rather than to force the collision path.
var hotFuncs = []string{"New", "normalize", "parse", "Load"}

// GenGoProject builds a random Go source tree containing the shapes the
// scanner has historically got wrong: the same receiver+method declared in
// several packages (issue #85), unexported helpers that the compiler
// instruments for coverage, several declarations per file so span
// attribution has neighbours to confuse, and — unless suppressed — two
// `package main` directories, which is the one shape where the scanner
// legitimately gives up and must say so.
//
// The output is written for the PARSER, not the compiler. goscan is AST-only
// (parser.ParseFile, no go/types, no build), and the give-up branch is only
// reachable in a root-level package where a compiler would reject the tree
// anyway. Requiring compilability would make that branch untestable, which
// is the wrong trade for a fixture nothing ever builds.
func GenGoProject(r *Rand, opts GoProjectOptions) Project {
	if opts.Packages <= 0 {
		opts.Packages = r.IntRange(2, 4)
	}
	if opts.FilesPerPackage <= 0 {
		opts.FilesPerPackage = r.IntRange(1, 3)
	}
	if opts.DeclsPerFile <= 0 {
		opts.DeclsPerFile = r.IntRange(2, 5)
	}
	if opts.CollisionPressure == 0 {
		opts.CollisionPressure = 70
	}

	p := Project{Files: map[string]string{}}
	for pi := range opts.Packages {
		dir := fmt.Sprintf("pkg%d", pi)
		genPackage(r, &p, dir, opts)
	}

	if !opts.NoRootCollision {
		addMainCollision(&p)
	}
	return p
}

// addMainCollision emits atlas's own shape: a `package main` in a
// subdirectory and another at the tree root, both declaring func main. Both
// compute the short id "main.main"; the second one walked cannot be
// package-qualified (its package directory is "." so there is no prefix to
// qualify with) and is dropped with a warning.
//
// It is here rather than left to chance because that give-up branch is the
// only place the scanner is allowed to lose a declaration, and a property
// that never reaches it is not testing the "or reported" half of its own
// claim. Which of the two is dropped is left to walk order — the property
// asserts only that the loser is named.
func addMainCollision(p *Project) {
	const body = "func main() {\n\tx := 1\n\tif x > 0 {\n\t\tx++\n\t}\n\t_ = x\n}\n"
	for _, f := range []struct{ dir, rel string }{
		{"cmdx", "cmdx/main.go"},
		{"", "main.go"},
	} {
		p.Files[f.rel] = "package main\n\n" + body
		p.Decls = append(p.Decls, Decl{
			Pkg: "main", Dir: f.dir, File: f.rel, Name: "main",
			Line: 3, EndLine: 8, Stmts: 4,
		})
	}
}

// plannedDecl is one declaration decided in genPackage's first pass, before
// any source has been written.
type plannedDecl struct {
	file           string
	recv, name     string
	firstOfItsType bool
}

// genPackage plans one package's declarations, then emits them.
//
// Two passes rather than one, for a reason a vacuous property exposed: a body
// can only CALL a declaration the generator already knows about, so a single
// streaming pass produces calls that all point backwards and, in the first
// file, almost none at all. The first version of this generator emitted no
// calls whatsoever — which meant the scanner's referential-closure property
// and the store's edge-wiring property were both being asked about graphs with
// zero edges. Green, and measuring nothing.
//
// Planning the package first lets any declaration call any other, including
// one in a sibling file and one declared later in the same file. Both are
// legal Go, and cross-file resolution is exactly where the scanner's
// package-scoped name lookup earns its keep.
func genPackage(r *Rand, p *Project, dir string, opts GoProjectOptions) {
	// Names already used in THIS package. Go forbids two funcs with the same
	// name in one package, and the scanner reads a same-file redeclaration as
	// a repeated scan rather than a collision, so a generator that emitted
	// duplicates would be testing invalid input.
	used := map[string]bool{}
	types := map[string]bool{}

	plans := make([][]plannedDecl, opts.FilesPerPackage)
	for fi := range opts.FilesPerPackage {
		rel := path.Join(dir, fmt.Sprintf("f%d.go", fi))
		for range opts.DeclsPerFile {
			pl := plannedDecl{file: rel}
			if r.Chance(1, 2) {
				pl.recv, pl.name = methodName(r, used, opts.CollisionPressure)
				used[pl.recv+"."+pl.name] = true
				// The struct is declared in the first file that needs it;
				// methods in sibling files hang off that one declaration.
				if !types[pl.recv] {
					types[pl.recv] = true
					pl.firstOfItsType = true
				}
			} else {
				pl.name = uniqueName(r, used, hotFuncs, opts.CollisionPressure, "F")
				used[pl.name] = true
			}
			plans[fi] = append(plans[fi], pl)
		}
	}

	// Callable targets, split by how a body can reach them: a plain function
	// by bare name, a method only through a receiver of its own type.
	var funcs []string
	methodsByRecv := map[string][]string{}
	for _, file := range plans {
		for _, pl := range file {
			if pl.recv == "" {
				funcs = append(funcs, pl.name)
			} else {
				methodsByRecv[pl.recv] = append(methodsByRecv[pl.recv], pl.name)
			}
		}
	}

	for fi, file := range plans {
		rel := path.Join(dir, fmt.Sprintf("f%d.go", fi))
		var b strings.Builder
		b.WriteString("package " + dir + "\n\n")
		line := 3 // the next line that will be written
		emit := func(s string) {
			b.WriteString(s)
			line += strings.Count(s, "\n")
		}

		for _, pl := range file {
			if pl.firstOfItsType {
				emit("type " + pl.recv + " struct{ n int }\n\n")
			}
			body, stmts := genBody(r, pl.recv, pl.name, funcs, methodsByRecv)
			startLine := line
			emit(declHeader(pl.recv, pl.name))
			emit(body)
			emit("}\n\n")
			p.Decls = append(p.Decls, Decl{
				Pkg: dir, Dir: dir, File: rel, Receiver: pl.recv, Name: pl.name,
				Line: startLine, EndLine: line - 2, Stmts: stmts,
			})
		}
		p.Files[rel] = b.String()
	}
}

// methodName draws a receiver+method PAIR, from the hot pool with
// probability pressure/100. Drawing the pair rather than each half
// independently is what actually produces cross-package collisions: a
// method's short id is "Recv.Method" with no package in it, so both halves
// have to match for two packages to clash.
func methodName(r *Rand, used map[string]bool, pressure int) (string, string) {
	if r.Chance(pressure, 100) {
		hp := Pick(r, hotPairs)
		if !used[hp[0]+"."+hp[1]] {
			return hp[0], hp[1]
		}
	}
	recv := Pick(r, hotPairs)[0]
	for i := 0; ; i++ {
		name := fmt.Sprintf("M%d_%d", r.IntN(1<<20), i)
		if !used[recv+"."+name] {
			return recv, name
		}
	}
}

// declHeader renders the one-line signature. Methods take a pointer receiver
// — the shape almost all real code uses, and the one receiverTypeName has to
// unwrap through a StarExpr to read.
func declHeader(recv, name string) string {
	if recv != "" {
		return fmt.Sprintf("func (t *%s) %s(x int) int {\n", recv, name)
	}
	return fmt.Sprintf("func %s(x int) int {\n", name)
}

// genBody emits a body with a random but known number of coverable
// statements, and — where there is something to call — a call.
//
// The branch is always there: a body that is one straight line gives the
// coverage generators a single block per symbol, and single-block symbols
// never exercise the partial-coverage arithmetic. The calls are there because
// without them the scanned graph has nodes and no edges, and every property
// about edges passes without asking anything.
func genBody(r *Rand, recv, self string, funcs []string, methodsByRecv map[string][]string) (string, int) {
	var b strings.Builder
	stmts := 2 // the `if` and the `x++` inside it
	b.WriteString("\tif x > 0 {\n\t\tx++\n\t}\n")

	// A method reaches its own type's other methods through the receiver;
	// anything reaches a package-level function by bare name.
	if recv != "" {
		if peer := pickOther(r, methodsByRecv[recv], self); peer != "" {
			b.WriteString("\tx += t." + peer + "(x - 1)\n")
			stmts++
		}
	}
	if callee := pickOther(r, funcs, self); callee != "" {
		b.WriteString("\tx += " + callee + "(x - 1)\n")
		stmts++
	}

	for range r.IntRange(0, 3) {
		b.WriteString("\tx += 2\n")
		stmts++
	}
	b.WriteString("\treturn x\n")
	stmts++
	return b.String(), stmts
}

// pickOther returns a randomly chosen element of xs other than `self`, or ""
// when there is none (and, a quarter of the time, when there is — so some
// declarations stay leaves and the graph is not uniformly dense).
//
// Excluding self keeps the generated code free of unconditional
// self-recursion: an infinite loop if anything ever ran it, and a self-edge
// that FindCycles deliberately does not report, so it would be a call that
// contributes nothing to any property here.
func pickOther(r *Rand, xs []string, self string) string {
	others := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != self {
			others = append(others, x)
		}
	}
	if len(others) == 0 || r.Chance(1, 4) {
		return ""
	}
	return Pick(r, others)
}

// uniqueName draws from a shared pool with probability pressure/100 and
// otherwise mints a name unique to this package.
func uniqueName(r *Rand, used map[string]bool, pool []string, pressure int, prefix string) string {
	if pressure > 0 && r.Chance(pressure, 100) {
		if n := Pick(r, pool); !used[n] {
			return n
		}
	}
	for i := 0; ; i++ {
		n := fmt.Sprintf("%s%d_%d", prefix, r.IntN(1<<20), i)
		if !used[n] {
			return n
		}
	}
}

// WriteProject materialises a project under dir. It fails the test rather
// than returning an error: a generator that cannot write its own fixture is
// not something the caller can meaningfully recover from.
func WriteProject(t *testing.T, dir string, p Project) {
	t.Helper()
	for rel, content := range p.Files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("atlastest: mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
			t.Fatalf("atlastest: write %s: %v", abs, err)
		}
	}
}
