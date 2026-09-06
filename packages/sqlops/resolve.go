package sqlops

import (
	"go/ast"
	"go/printer"
	"go/token"
	"strconv"
	"strings"
)

// resolution is the outcome of trying to recover a query's text from the
// expression that was handed to a database/sql method.
//
// Text is populated even when OK is false: the visible half of a concatenated
// or formatted query is what makes the unresolved entry actionable, and what
// lets the caller tell a query builder apart from a query it could nearly
// read.
type resolution struct {
	Text       string
	OK         bool
	Reason     string
	Interp     Interpolation
	CallerData bool
	// Expr renders the non-static operand, for the advisory message.
	Expr string
}

// maxResolveDepth stops a resolution walk that a cyclic local assignment
// (`q = q + x`) would otherwise send around forever.
const maxResolveDepth = 12

// funcScope is the per-function context a resolution needs: the parameters
// (which decide whether interpolated data is caller-supplied), the local
// single-assignment string variables, and whether the body appends to a slice.
type funcScope struct {
	params  map[string]bool
	locals  map[string]ast.Expr
	rebound map[string]bool
	// hasAppend is the row-shape signal: a body containing append() is
	// collecting rows into a slice.
	hasAppend bool
}

func newFuncScope(fn *ast.FuncDecl) *funcScope {
	s := &funcScope{
		params:  paramNames(fn),
		locals:  map[string]ast.Expr{},
		rebound: map[string]bool{},
	}
	s.collectBody(fn.Body)
	return s
}

func paramNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	if fn.Type == nil || fn.Type.Params == nil {
		return out
	}
	for _, f := range fn.Type.Params.List {
		for _, n := range f.Names {
			out[n.Name] = true
		}
	}
	return out
}

// collectBody records single-assignment locals and notices append(). A name
// written twice goes into `rebound` and resolves to nothing: picking either
// value would be a guess about control flow, and the query Atlas reports must
// be one that can actually run.
func (s *funcScope) collectBody(body *ast.BlockStmt) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "append" {
				s.hasAppend = true
			}
		case *ast.AssignStmt:
			s.noteAssign(v)
		case *ast.DeclStmt:
			if gd, ok := v.Decl.(*ast.GenDecl); ok {
				s.noteLocalDecl(gd)
			}
		}
		return true
	})
}

func (s *funcScope) noteAssign(a *ast.AssignStmt) {
	if len(a.Lhs) != 1 || len(a.Rhs) != 1 {
		return
	}
	id, ok := a.Lhs[0].(*ast.Ident)
	if !ok {
		return
	}
	s.bind(id.Name, a.Rhs[0])
}

func (s *funcScope) noteLocalDecl(gd *ast.GenDecl) {
	if gd.Tok != token.VAR && gd.Tok != token.CONST {
		return
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok || len(vs.Names) != len(vs.Values) {
			continue
		}
		for i, n := range vs.Names {
			s.bind(n.Name, vs.Values[i])
		}
	}
}

func (s *funcScope) bind(name string, val ast.Expr) {
	if _, seen := s.locals[name]; seen {
		s.rebound[name] = true
		return
	}
	s.locals[name] = val
}

// resolve recovers the query text of e, or explains why it could not.
func (s *funcScope) resolve(e ast.Expr, consts map[string]string, depth int) resolution {
	if depth > maxResolveDepth {
		return resolution{Reason: ReasonDynamic, Expr: renderExpr(e)}
	}
	switch v := e.(type) {
	case *ast.ParenExpr:
		return s.resolve(v.X, consts, depth+1)
	case *ast.BasicLit:
		return literalResolution(v)
	case *ast.Ident:
		return s.resolveIdent(v, consts, depth)
	case *ast.BinaryExpr:
		return s.resolveBinary(v, consts, depth)
	case *ast.CallExpr:
		return s.resolveCall(v, consts, depth)
	}
	return resolution{Reason: ReasonDynamic, Expr: renderExpr(e)}
}

func literalResolution(v *ast.BasicLit) resolution {
	if v.Kind != token.STRING {
		return resolution{Reason: ReasonDynamic, Expr: v.Value}
	}
	s, err := strconv.Unquote(v.Value)
	if err != nil {
		return resolution{Reason: ReasonDynamic, Expr: v.Value}
	}
	return resolution{Text: s, OK: true}
}

func (s *funcScope) resolveIdent(id *ast.Ident, consts map[string]string, depth int) resolution {
	if s.rebound[id.Name] {
		return resolution{Reason: ReasonReassigned, Expr: id.Name}
	}
	if v, ok := s.locals[id.Name]; ok {
		return s.resolve(v, consts, depth+1)
	}
	if v, ok := consts[id.Name]; ok {
		return resolution{Text: v, OK: true}
	}
	return resolution{Reason: ReasonDynamic, Expr: id.Name}
}

// resolveBinary folds `a + b`. Two resolvable halves make a resolvable whole;
// one unresolvable half makes the query interpolated, and the fragment that
// survives is still worth recording.
func (s *funcScope) resolveBinary(b *ast.BinaryExpr, consts map[string]string, depth int) resolution {
	if b.Op != token.ADD {
		return resolution{Reason: ReasonDynamic, Expr: renderExpr(b)}
	}
	l := s.resolve(b.X, consts, depth+1)
	r := s.resolve(b.Y, consts, depth+1)
	out := resolution{Text: l.Text + r.Text, OK: l.OK && r.OK}
	if out.OK {
		out.Interp = firstInterp(l, r)
		out.CallerData = l.CallerData || r.CallerData
		return out
	}
	out.Reason = ReasonConcat
	out.Interp = InterpolationConcat
	out.CallerData = l.CallerData || r.CallerData ||
		(!l.OK && s.isCallerData(b.X)) || (!r.OK && s.isCallerData(b.Y))
	out.Expr = firstNonEmpty(unresolvedExpr(l, r), renderExpr(b))
	return out
}

// resolveCall handles the one call shape that carries query text: a formatting
// call whose format string is a literal. Everything else is a query builder,
// a helper, or a value from somewhere Atlas cannot follow -- all of which are
// unresolved, and none of which are guessed at.
func (s *funcScope) resolveCall(c *ast.CallExpr, consts map[string]string, depth int) resolution {
	if !isFormattingCall(c) || len(c.Args) == 0 {
		return resolution{Reason: ReasonDynamic, Expr: renderExpr(c)}
	}
	format := s.resolve(c.Args[0], consts, depth+1)
	out := resolution{
		Text:   format.Text,
		Reason: ReasonSprintf,
		Interp: InterpolationSprintf,
	}
	arg, caller := s.formattedCallerArg(format.Text, c.Args[1:])
	out.CallerData = caller
	out.Expr = firstNonEmpty(arg, renderExpr(c))
	return out
}

// formattedCallerArg reports whether any *string-shaped* verb in the format
// consumes a value that traces back to a parameter of the enclosing function.
//
// Only %s, %v and %q count. A %d formats an integer, and an integer spliced
// into SQL text cannot carry a quote or a semicolon -- treating it as an
// injection risk is exactly the kind of noisy finding that makes a team stop
// reading the whole report.
func (s *funcScope) formattedCallerArg(format string, args []ast.Expr) (string, bool) {
	for i, verb := range stringVerbs(format) {
		if !verb || i >= len(args) {
			continue
		}
		if s.isCallerData(args[i]) {
			return renderExpr(args[i]), true
		}
	}
	return "", false
}

// stringVerbs returns one entry per verb in the format, true when that verb
// interpolates a string.
func stringVerbs(format string) []bool {
	var out []bool
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			continue
		}
		j := i + 1
		for j < len(format) && strings.ContainsRune("+-# 0123456789.*", rune(format[j])) {
			j++
		}
		if j >= len(format) {
			break
		}
		if format[j] == '%' {
			i = j
			continue
		}
		out = append(out, format[j] == 's' || format[j] == 'v' || format[j] == 'q')
		i = j
	}
	return out
}

// isCallerData reports that an expression is a *reference* rooted at a
// parameter of the enclosing function.
//
// The deliberate exclusion is the call expression. `strings.Repeat("?,", n)`
// mentions a parameter but produces a placeholder list, and
// `sanitize(name)` mentions one but is the sanitiser. Treating a value that
// has been through a function as caller data would fire the injection
// advisory on the two idioms that are most often correct, and an advisory
// that fires on correct code is one nobody reads.
func (s *funcScope) isCallerData(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return s.params[v.Name]
	case *ast.SelectorExpr:
		return s.isCallerData(v.X)
	case *ast.IndexExpr:
		return s.isCallerData(v.X)
	case *ast.ParenExpr:
		return s.isCallerData(v.X)
	case *ast.StarExpr:
		return s.isCallerData(v.X)
	}
	return false
}

// isFormattingCall recognises fmt.Sprintf / fmt.Sprint / fmt.Sprintln by
// selector name. The package identifier is not checked against the file's
// imports because a dot-import or an aliased fmt is still fmt, and a
// same-named helper in another package formats strings too.
func isFormattingCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Sprintf", "Sprint", "Sprintln":
		return true
	}
	return false
}

func firstInterp(rs ...resolution) Interpolation {
	for _, r := range rs {
		if r.Interp != InterpolationNone {
			return r.Interp
		}
	}
	return InterpolationNone
}

func unresolvedExpr(rs ...resolution) string {
	for _, r := range rs {
		if !r.OK && r.Expr != "" {
			return r.Expr
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// renderExpr prints an expression back to source so an advisory can quote the
// thing it is complaining about. Failure yields the empty string; the message
// then omits the quote rather than the advisory.
func renderExpr(e ast.Expr) string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	if err := printer.Fprint(&b, token.NewFileSet(), e); err != nil {
		return ""
	}
	return b.String()
}
