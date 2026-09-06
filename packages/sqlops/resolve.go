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
	ops, ok := formatOperands(format)
	if !ok {
		// A directive Atlas could not account for means it cannot say which
		// argument reaches the query text. What it must not do is conclude
		// that none of them does: on an injection check a silent miss is the
		// expensive failure, so every argument is weighed instead.
		return s.firstCallerArg(args)
	}
	for _, op := range ops {
		if !op.str || op.arg < 0 || op.arg >= len(args) {
			continue
		}
		if s.isCallerData(args[op.arg]) {
			return renderExpr(args[op.arg]), true
		}
	}
	return "", false
}

func (s *funcScope) firstCallerArg(args []ast.Expr) (string, bool) {
	for _, a := range args {
		if s.isCallerData(a) {
			return renderExpr(a), true
		}
	}
	return "", false
}

// formatOperand is one verb of a format string together with the argument it
// consumes.
type formatOperand struct {
	arg int
	str bool
}

// formatOperands pairs each verb in a format string with the argument index it
// actually reads, reporting false when it meets a directive it cannot account
// for.
//
// Zipping verbs against arguments by position -- one verb, one argument, in
// order -- is wrong in both directions. `%*d` takes its width from an argument
// too, so everything after it shifts by one; `%[1]s` names its argument
// outright and resets where the next one comes from. Both are how a table
// name ends up checked against the width operand of some earlier verb, and the
// injection check then reports on the wrong expression or on none at all.
func formatOperands(format string) ([]formatOperand, bool) {
	f := &formatScanner{src: format}
	if !f.run() {
		return nil, false
	}
	return f.ops, true
}

type formatScanner struct {
	src string
	i   int
	// next is the argument the next verb reads, in Go's own terms: implicit
	// unless a `[n]` index moved it.
	next int
	ops  []formatOperand
}

func (f *formatScanner) run() bool {
	for f.i = 0; f.i < len(f.src); f.i++ {
		if f.src[f.i] != '%' {
			continue
		}
		f.i++
		if f.i >= len(f.src) {
			return false // a trailing '%' is not a directive atlas can read.
		}
		if f.src[f.i] == '%' {
			continue // an escaped percent consumes nothing.
		}
		if !f.directive() {
			return false
		}
	}
	return true
}

// directive consumes one `%[flags][index][width][.precision]verb`, leaving i
// on the verb.
func (f *formatScanner) directive() bool {
	f.skipWhile("+-# 0")
	if !f.argIndex() {
		return false
	}
	if !f.starOrDigits() {
		return false
	}
	if f.at('.') {
		f.i++
		if !f.starOrDigits() {
			return false
		}
	}
	if f.i >= len(f.src) || !isVerbLetter(f.src[f.i]) {
		return false
	}
	verb := f.src[f.i]
	f.take(verb == 's' || verb == 'v' || verb == 'q')
	return true
}

// argIndex reads an explicit `[n]` argument index, which repoints the cursor
// the way Go's fmt does: `%[2]s` reads the second argument, and the verb after
// it reads the third.
func (f *formatScanner) argIndex() bool {
	if !f.at('[') {
		return true
	}
	j := f.i + 1
	n := 0
	for j < len(f.src) && f.src[j] >= '0' && f.src[j] <= '9' {
		n = n*10 + int(f.src[j]-'0')
		j++
	}
	if j >= len(f.src) || f.src[j] != ']' || j == f.i+1 || n < 1 {
		return false
	}
	f.next = n - 1
	f.i = j + 1
	return true
}

// starOrDigits consumes a width or precision: `*` reads it from an argument
// (which is what shifts every later verb along), digits do not.
func (f *formatScanner) starOrDigits() bool {
	if f.at('*') {
		f.take(false)
		f.i++
		return true
	}
	f.skipWhile("0123456789")
	// A second `[n]` here is Go's `%[2]*[1]d`. Atlas does not read it rather
	// than reading it wrong.
	return !f.at('[')
}

func (f *formatScanner) take(str bool) {
	f.ops = append(f.ops, formatOperand{arg: f.next, str: str})
	f.next++
}

func (f *formatScanner) at(c byte) bool { return f.i < len(f.src) && f.src[f.i] == c }

func (f *formatScanner) skipWhile(set string) {
	for f.i < len(f.src) && strings.IndexByte(set, f.src[f.i]) >= 0 {
		f.i++
	}
}

func isVerbLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
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
