package sqlops

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// ExtractOptions tunes the Go-source sweep.
type ExtractOptions struct {
	// SkipDirs are directory names pruned from the walk. Empty means the
	// defaults below.
	SkipDirs []string
	// IncludeTests indexes _test.go files too. Off by default: a query in a
	// test fixture is not a production data path, and advising on it buries
	// the advisories that matter.
	IncludeTests bool
	// HandleNames are the receiver names treated as database handles when the
	// query argument cannot be resolved at all. See the note on
	// looksLikeHandle.
	HandleNames []string
}

// defaultSkipDirs are pruned from the walk. `testdata` is excluded for the
// same reason `_test.go` is: a query in a fixture is not a production data
// path, and advising on it buries the advisories that matter. Passing such a
// directory as the root still works -- the rule applies to subdirectories.
var defaultSkipDirs = []string{
	"vendor", "node_modules", ".git", "dist", "build", "testdata",
}

// defaultHandleNames is the receiver-name allowlist for the one case where
// Atlas has nothing else to go on.
//
// The problem: `x.Query(something)` where `something` is a query builder gives
// no text to inspect, so "is this SQL?" cannot be answered from the argument.
// Type-checking would answer it properly, but that needs a fully resolvable
// build of the target repository, which Atlas cannot assume. The name of the
// handle is the cheap signal that remains. Getting it wrong in the permissive
// direction would put non-SQL calls into the inventory; getting it wrong in
// the strict direction loses a query Atlas was never going to analyse anyway
// and records nothing false. Strict it is.
var defaultHandleNames = []string{
	"db", "conn", "tx", "q", "queries", "dbtx", "pool", "sqldb", "database",
	"session", "sess", "stmt", "executor", "ext", "sql", "store", "dbc",
}

// dbSQLArg maps a database/sql method name to the index of its SQL argument.
// The Context variants take ctx first; the others do not.
var dbSQLArg = map[string]int{
	"Query": 0, "Exec": 0, "QueryRow": 0, "Prepare": 0,
	"QueryContext": 1, "ExecContext": 1, "QueryRowContext": 1, "PrepareContext": 1,
}

// ExtractGo sweeps root for SQL passed to database/sql methods and returns one
// Operation per call site -- resolved or not.
func ExtractGo(root string, opts ExtractOptions) ([]Operation, []string, error) {
	opts = opts.withDefaults()
	dirs, err := goDirs(root, opts)
	if err != nil {
		return nil, nil, err
	}
	var (
		ops      []Operation
		warnings []string
	)
	for _, dir := range dirs {
		dirOps, dirWarn := extractDir(root, dir, opts)
		ops = append(ops, dirOps...)
		warnings = append(warnings, dirWarn...)
	}
	sortOperations(ops)
	return ops, warnings, nil
}

func (o ExtractOptions) withDefaults() ExtractOptions {
	if len(o.SkipDirs) == 0 {
		o.SkipDirs = defaultSkipDirs
	}
	if len(o.HandleNames) == 0 {
		o.HandleNames = defaultHandleNames
	}
	return o
}

// goDirs returns every directory under root that holds at least one indexable
// .go file. Extraction is per-directory because package-level constants are
// visible across the files of a package, and a query const declared in
// queries.go is routinely used from repository.go.
func goDirs(root string, opts ExtractOptions) ([]string, error) {
	skip := map[string]bool{}
	for _, d := range opts.SkipDirs {
		skip[d] = true
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal.
		}
		if d.IsDir() {
			if p != root && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if indexableGoFile(d.Name(), opts) {
			seen[filepath.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk go sources under %s: %w", root, err)
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func indexableGoFile(name string, opts ExtractOptions) bool {
	if !strings.HasSuffix(name, ".go") {
		return false
	}
	return opts.IncludeTests || !strings.HasSuffix(name, "_test.go")
}

// extractDir parses one package directory and extracts its operations. Parse
// errors are reported as warnings and the file is skipped: a repository with
// one unparseable file must still yield an inventory for the rest.
func extractDir(root, dir string, opts ExtractOptions) ([]Operation, []string) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{fmt.Sprintf("read dir %s: %v", dir, err)}
	}
	var (
		files    []*ast.File
		paths    []string
		warnings []string
	)
	for _, e := range entries {
		if e.IsDir() || !indexableGoFile(e.Name(), opts) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s: %v", relativeTo(root, p), err))
			continue
		}
		files = append(files, f)
		paths = append(paths, p)
	}

	consts := packageStringConsts(files)
	var ops []Operation
	for i, f := range files {
		fx := &fileExtract{
			fset:    fset,
			file:    f,
			rel:     relativeTo(root, paths[i]),
			consts:  consts,
			handles: setOf(opts.HandleNames),
			marks:   indexComments(fset, f),
		}
		ops = append(ops, fx.run()...)
	}
	return ops, warnings
}

// packageStringConsts collects every package-level string constant and
// initialised string var across the package's files.
func packageStringConsts(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			absorbValueSpecs(gd, out)
		}
	}
	return out
}

func absorbValueSpecs(gd *ast.GenDecl, out map[string]string) {
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || len(vs.Names) != len(vs.Values) {
			continue
		}
		for i, name := range vs.Names {
			if s, ok := staticString(vs.Values[i]); ok {
				out[name.Name] = s
			}
		}
	}
}

// staticString folds an expression made only of string literals and `+`.
func staticString(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.ParenExpr:
		return staticString(v.X)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := staticString(v.X)
		r, rok := staticString(v.Y)
		return l + r, lok && rok
	}
	return "", false
}

// --- per-file extraction -------------------------------------------------

type fileExtract struct {
	fset    *token.FileSet
	file    *ast.File
	rel     string
	consts  map[string]string
	handles map[string]bool
	marks   commentMarks
	out     []Operation
}

func (fx *fileExtract) run() []Operation {
	for _, d := range fx.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		fx.walkFunc(fn)
	}
	return fx.out
}

func (fx *fileExtract) walkFunc(fn *ast.FuncDecl) {
	scope := newFuncScope(fn)
	docSuppressions := directivesIn(fn.Doc)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if op, ok := fx.operationFor(call, fn, scope, docSuppressions); ok {
			fx.out = append(fx.out, op)
		}
		return true
	})
}

// operationFor turns one call expression into an Operation, or reports that
// the call is none of Atlas's business.
func (fx *fileExtract) operationFor(
	call *ast.CallExpr, fn *ast.FuncDecl, scope *funcScope, docSuppress []string,
) (Operation, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return Operation{}, false
	}
	argIdx, ok := dbSQLArg[sel.Sel.Name]
	if !ok || argIdx >= len(call.Args) {
		return Operation{}, false
	}
	res := scope.resolve(call.Args[argIdx], fx.consts, 0)
	if !LooksLikeSQL(res.Text) && !fx.looksLikeHandle(sel) {
		return Operation{}, false
	}

	pos := fx.fset.Position(call.Lparen)
	op := Operation{
		Source:           SourceGo,
		Position:         shared.FilePosition{Path: fx.rel, Line: pos.Line},
		SymbolName:       symbolNameFor(fn, fx.file),
		SQL:              res.Text,
		RowScan:          scanShape(sel.Sel.Name, scope.hasAppend),
		Interpolated:     res.Interp != InterpolationNone,
		Interpolation:    res.Interp,
		CallerData:       res.CallerData,
		InterpolatedExpr: res.Expr,
		Suppressions:     mergeSuppressions(docSuppress, fx.marks.at(pos.Line)),
	}
	fx.classify(&op, res)
	return op, true
}

// classify decides the resolved/unresolved verdict. A statement that resolved
// textually but has no recognisable verb is unresolved too: a shape Atlas
// cannot read must never inherit the zero value of Statement, because that
// zero value looks exactly like an unbounded SELECT.
func (fx *fileExtract) classify(op *Operation, res resolution) {
	if !res.OK {
		op.Resolved = false
		op.UnresolvedReason = res.Reason
		return
	}
	st, ok := AnalyzeStatement(res.Text)
	if !ok {
		op.Resolved = false
		op.UnresolvedReason = ReasonNotAStatement
		return
	}
	op.Resolved = true
	op.Statement = st
}

// looksLikeHandle is the fallback recogniser described on defaultHandleNames:
// the last selector segment before the method name (the `db` of
// `r.db.QueryContext`).
func (fx *fileExtract) looksLikeHandle(sel *ast.SelectorExpr) bool {
	var name string
	switch v := sel.X.(type) {
	case *ast.Ident:
		name = v.Name
	case *ast.SelectorExpr:
		name = v.Sel.Name
	default:
		return false
	}
	return fx.handles[strings.ToLower(name)]
}

// scanShape maps the method to what the call site does with the result. Query
// without an append() nearby is left unknown rather than assumed to be a
// slice scan -- the unbounded-read advisory grades its confidence on this, and
// a guess here would be a guess there.
func scanShape(method string, hasAppend bool) RowScan {
	switch method {
	case "QueryRow", "QueryRowContext":
		return ScanSingle
	case "Exec", "ExecContext":
		return ScanExec
	case "Query", "QueryContext":
		if hasAppend {
			return ScanSlice
		}
	}
	return ScanUnknown
}

// symbolNameFor reproduces the Go scanner's short symbol id --
// `Receiver.Method` or `pkg.Func` -- so an operation joins the rest of the
// graph by qualified name without a second naming convention to keep in sync
// (see packages/codeindex/go/scanner.go).
func symbolNameFor(fn *ast.FuncDecl, file *ast.File) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		t := fn.Recv.List[0].Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		if id, ok := t.(*ast.Ident); ok {
			return id.Name + "." + fn.Name.Name
		}
	}
	if file.Name != nil {
		return file.Name.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func sortOperations(ops []Operation) {
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].Position.Path != ops[j].Position.Path {
			return ops[i].Position.Path < ops[j].Position.Path
		}
		if ops[i].Position.Line != ops[j].Position.Line {
			return ops[i].Position.Line < ops[j].Position.Line
		}
		return ops[i].Name < ops[j].Name
	})
}

func setOf(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[strings.ToLower(s)] = true
	}
	return out
}
