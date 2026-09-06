package cfg

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

// Build constructs the control-flow graph of one function body.
//
// The graph has a single synthetic entry (block 0) and a single synthetic
// exit (block 1); every `return`, and the fall-off-the-end path, edges into
// the exit. Those two indices are fixed because they are persisted as block
// ids and a reader that has to search for the entry node cannot render the
// graph without loading all of it.
//
// fn.Body must be non-nil — an external (assembly or //go:linkname) function
// has no flow to model, and silently returning an empty graph for one would
// make it indistinguishable from a function with a single straight path.
func Build(fset *token.FileSet, fn *ast.FuncDecl) (*Graph, error) {
	if fn == nil || fn.Body == nil {
		return nil, fmt.Errorf("cfg: function has no body")
	}
	if fset == nil {
		return nil, fmt.Errorf("cfg: nil FileSet")
	}
	pos := fset.Position(fn.Pos())
	b := &builder{
		fset: fset,
		g: &Graph{
			Func:      funcName(fn),
			File:      pos.Filename,
			StartLine: pos.Line,
			EndLine:   fset.Position(fn.End()).Line,
		},
	}
	b.newBlock(BlockEntry, b.g.StartLine, b.g.StartLine) // 0
	b.newBlock(BlockExit, b.g.EndLine, b.g.EndLine)      // 1
	b.cur = b.newBlock(BlockBody, b.g.StartLine, b.g.StartLine)
	b.addEdge(entryBlock, b.cur, EdgeSeq, "")

	b.stmts(fn.Body.List)

	// Falling off the end is a return. Wiring it explicitly keeps the exit
	// node the single sink, which is what makes E-N+2 meaningful.
	if b.cur >= 0 {
		b.addEdge(b.cur, exitBlock, EdgeSeq, "")
	}

	// Decisions are recorded as each construct COMPLETES, which puts an
	// enclosing loop after the branches nested inside it. Readers (and the
	// golden tests) expect source order, so re-sort by line — stably, so two
	// decisions on one line keep the order they were built in, which is the
	// order the operands short-circuit in.
	sort.SliceStable(b.g.Decisions, func(i, j int) bool {
		return b.g.Decisions[i].Line < b.g.Decisions[j].Line
	})
	for i := range b.g.Decisions {
		b.g.Decisions[i].Index = i
	}
	return b.g, nil
}

const (
	entryBlock = 0
	exitBlock  = 1
)

// funcName renders a receiver-qualified name: `Sum`, `(*Repo).Load`. It is
// only used for display and for matching against a store symbol when the
// caller has nothing better; the CLI matches on declaration line instead,
// which is exact.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

// pend is an edge whose target block does not exist yet: the arms of an if
// waiting for their join, or the breaks of a loop waiting for the statement
// after it. Deferring them is what lets the builder create join blocks only
// when something actually reaches them — a construct all of whose arms return
// leaves no empty join behind to be mis-reported as unreachable code.
type pend struct {
	from int
	kind EdgeKind
	cond string
}

// frame is one enclosing breakable/continuable construct.
type frame struct {
	label  string
	loop   bool
	header int // continue target; -1 for switch/select
	breaks *[]pend
}

type builder struct {
	fset *token.FileSet
	g    *Graph
	// cur is the block statements append to, or -1 when control cannot
	// reach this point (right after a return, break, or continue). The next
	// statement then starts a fresh block with no predecessor, which is
	// exactly what Graph.Unreachable looks for.
	cur       int
	frames    []frame
	loopDepth int
	// nextLabel carries a `Loop:` label down to the construct it labels.
	nextLabel string
}

func (b *builder) line(p token.Pos) int { return b.fset.Position(p).Line }

func (b *builder) newBlock(kind BlockKind, start, end int) int {
	idx := len(b.g.Blocks)
	b.g.Blocks = append(b.g.Blocks, Block{Index: idx, Kind: kind, StartLine: start, EndLine: end})
	return idx
}

func (b *builder) addEdge(from, to int, kind EdgeKind, cond string) {
	if from < 0 || to < 0 {
		return
	}
	b.g.Edges = append(b.g.Edges, Edge{From: from, To: to, Kind: kind, Condition: cond})
}

// span widens the current block to cover [start, end], creating one when
// control is unreachable (so the statement still appears in the graph and can
// be reported as unreachable rather than dropped).
func (b *builder) span(start, end int) {
	if b.cur < 0 {
		b.cur = b.newBlock(BlockBody, start, end)
		return
	}
	blk := &b.g.Blocks[b.cur]
	if blk.StartLine == 0 || start < blk.StartLine {
		blk.StartLine = start
	}
	if end > blk.EndLine {
		blk.EndLine = end
	}
}

// join materialises the target of a set of deferred edges. Returns -1 when
// nothing reaches it, so callers can leave b.cur unreachable.
func (b *builder) join(ps []pend, kind BlockKind, start, end int) int {
	if len(ps) == 0 {
		return -1
	}
	blk := b.newBlock(kind, start, end)
	for _, p := range ps {
		b.addEdge(p.from, blk, p.kind, p.cond)
	}
	return blk
}

func (b *builder) addDecision(d Decision) int {
	d.Index = len(b.g.Decisions)
	b.g.Decisions = append(b.g.Decisions, d)
	return d.Index
}

// stmts walks a statement list, passing each statement the line its successor
// starts on. That successor line is the anchor the decision-coverage analysis
// uses to find "the block execution resumes in" — without it, an `if` with no
// `else` has no observable false outcome at all.
func (b *builder) stmts(list []ast.Stmt) {
	for i, s := range list {
		succ := 0
		if i+1 < len(list) {
			succ = b.line(list[i+1].Pos())
		}
		b.stmt(s, succ)
	}
}

// stmt dispatches one statement. It is a flat switch over the statement node
// set — one short case per construct — so gocyclo's threshold is not a useful
// signal here; splitting it would only move the same switch behind a map.
//
//nolint:gocyclo // dispatch table, see above.
func (b *builder) stmt(s ast.Stmt, succLine int) {
	switch x := s.(type) {
	case *ast.IfStmt:
		b.buildIf(x, succLine)
	case *ast.ForStmt:
		b.buildFor(x, succLine)
	case *ast.RangeStmt:
		b.buildRange(x, succLine)
	case *ast.SwitchStmt:
		b.buildSwitch(x, succLine)
	case *ast.TypeSwitchStmt:
		b.buildTypeSwitch(x, succLine)
	case *ast.SelectStmt:
		b.buildSelect(x, succLine)
	case *ast.ReturnStmt:
		b.shortCircuits(s)
		b.span(b.line(x.Pos()), b.line(x.End()))
		b.addEdge(b.cur, exitBlock, EdgeSeq, "")
		b.cur = -1
	case *ast.BranchStmt:
		b.buildBranch(x)
	case *ast.BlockStmt:
		b.span(b.line(x.Pos()), b.line(x.Pos()))
		b.stmts(x.List)
	case *ast.LabeledStmt:
		b.nextLabel = x.Label.Name
		b.stmt(x.Stmt, succLine)
		b.nextLabel = ""
	case *ast.DeferStmt:
		// Recorded, never wired: the deferred call runs at every exit, so an
		// edge for it would put its body on every path and count it as a
		// branch arm it is not. See Graph.Defers.
		b.g.Defers++
		b.noteFuncLits(x.Call)
		b.span(b.line(x.Pos()), b.line(x.End()))
	case *ast.ExprStmt:
		b.shortCircuits(s)
		b.noteFuncLits(s)
		b.span(b.line(x.Pos()), b.line(x.End()))
		if isPanicCall(x.X) {
			// A panic leaves the function exactly as a return does. Modelling
			// it matters for more than tidiness: without this edge a guard
			// arm ending in `panic` looks like it falls through to the
			// statement after the branch, and the coverage analysis would
			// difference two counts that never both happened.
			b.addEdge(b.cur, exitBlock, EdgeSeq, "")
			b.cur = -1
		}
	default:
		b.shortCircuits(s)
		b.noteFuncLits(s)
		b.span(b.line(s.Pos()), b.line(s.End()))
	}
}

// isPanicCall reports a call to the builtin `panic`. Only the builtin: a
// helper that always panics (`mustNot()`, `log.Fatal`) is indistinguishable
// from an ordinary call without whole-program analysis, and guessing at one
// would put an exit edge on a path that has none.
func isPanicCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "panic"
}

// buildBranch handles break / continue / goto / fallthrough.
//
// `fallthrough` is handled by buildSwitch (it needs the next clause's block),
// so reaching it here means a stray one the parser accepted; treat it as a
// no-op rather than losing the enclosing flow.
func (b *builder) buildBranch(x *ast.BranchStmt) {
	line := b.line(x.Pos())
	b.span(line, line)
	switch x.Tok {
	case token.BREAK:
		if f := b.frameFor(x.Label, false); f != nil {
			*f.breaks = append(*f.breaks, pend{from: b.cur, kind: EdgeSeq})
			b.cur = -1
			return
		}
		b.warn("break at line %d has no enclosing loop or switch", line)
	case token.CONTINUE:
		if f := b.frameFor(x.Label, true); f != nil && f.header >= 0 {
			b.addEdge(b.cur, f.header, EdgeLoopBack, "")
			b.cur = -1
			return
		}
		b.warn("continue at line %d has no enclosing loop", line)
	case token.GOTO:
		// A goto's target is a label that may not have been visited yet.
		// Modelling it would need a second pass; recording the imprecision
		// is better than silently drawing a graph that has no such edge.
		// HasGoto is what makes the imprecision actionable: two analyses
		// downstream must decline to answer over this graph rather than
		// answer confidently from edges it does not contain.
		b.g.HasGoto = true
		b.warn("goto at line %d is not modelled; the graph omits its edge", line)
		b.cur = -1
	case token.FALLTHROUGH:
		// handled by buildSwitch
	}
}

// frameFor resolves a break/continue target, honouring labels. `loopOnly`
// restricts the search to loops, which is what `continue` means.
func (b *builder) frameFor(label *ast.Ident, loopOnly bool) *frame {
	for i := len(b.frames) - 1; i >= 0; i-- {
		f := &b.frames[i]
		if loopOnly && !f.loop {
			continue
		}
		if label == nil || f.label == label.Name {
			return f
		}
	}
	return nil
}

func (b *builder) warn(format string, args ...any) {
	b.g.Warnings = append(b.g.Warnings, fmt.Sprintf(format, args...))
}

// noteFuncLits records that a function literal was skipped when it contains
// control flow of its own.
//
// A closure is a separate flow with its own coverage counters; folding its
// branches into the enclosing function would attribute them to a symbol that
// does not contain them. Dropping them silently, though, would let a
// goroutine body with ten untested branches read as a straight line — so the
// omission is reported on the graph instead of being invisible.
func (b *builder) noteFuncLits(n ast.Node) {
	ast.Inspect(n, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		if litHasFlow(lit) {
			b.warn("function literal at line %d has branches of its own and is not modelled",
				b.line(lit.Pos()))
		}
		return false // do not descend: nested literals belong to this one
	})
}

func litHasFlow(lit *ast.FuncLit) bool {
	found := false
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt:
			found = true
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				found = true
			}
		}
		return !found
	})
	return found
}

// exprText renders an expression back to source-ish text for the `condition`
// column and for MC/DC condition enumeration. go/types.ExprString is purely
// syntactic — no type information required — which is what keeps this package
// usable on a package that does not compile.
func exprText(e ast.Expr) string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(types.ExprString(e))
}
