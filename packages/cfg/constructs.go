package cfg

import (
	"go/ast"
	"go/token"
	"strings"
)

// cond emits the CFG for a boolean expression in CONDITION position and
// returns the deferred edges for its true and false outcomes.
//
// This is where && and || become real branches. `if a && b` evaluates `b`
// only when `a` was true, so the two operands live in different blocks and
// the `&&` is a decision with its own two outcomes — outcomes that Go's
// statement coverage cannot see, since both operands sit inside one counter.
// Enumerating them anyway is the whole point: the analysis can then say "this
// branch exists and no execution data can tell you whether you took it",
// which is a very different answer from not knowing the branch is there.
func (b *builder) cond(e ast.Expr) (tp, fp []pend) {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return b.cond(x.X)
	case *ast.BinaryExpr:
		switch x.Op {
		case token.LAND:
			return b.shortCircuitCond(x, DecisionAnd)
		case token.LOR:
			return b.shortCircuitCond(x, DecisionOr)
		}
	}
	// Leaf: evaluated in the current block, which thereby becomes a branch.
	// A `!x` is deliberately a leaf — inverting the true/false edge labels
	// to match the un-negated operand would make the rendered graph disagree
	// with the source it is drawn from.
	b.span(b.line(e.Pos()), b.line(e.End()))
	b.promoteBranch(b.cur)
	txt := exprText(e)
	return []pend{{from: b.cur, kind: EdgeTrue, cond: txt}},
		[]pend{{from: b.cur, kind: EdgeFalse, cond: txt}}
}

// shortCircuitCond wires `X && Y` / `X || Y` in condition position.
//
// For `&&`, Y is reached on X's true outcome and X's false outcome is already
// the whole expression's false outcome; `||` is the mirror image.
func (b *builder) shortCircuitCond(x *ast.BinaryExpr, kind DecisionKind) (tp, fp []pend) {
	tL, fL := b.cond(x.X)
	// The right operand is reached on the outcome that does NOT decide the
	// whole expression: true for `&&`, false for `||`.
	entered := tL
	if kind == DecisionOr {
		entered = fL
	}
	deciding := b.cur
	b.cur = b.join(entered, BlockBranch, b.line(x.Y.Pos()), b.line(x.Y.End()))
	tR, fR := b.cond(x.Y)
	b.addDecision(Decision{
		Kind:   kind,
		Line:   b.line(x.OpPos),
		Block:  deciding,
		Text:   exprText(x),
		Arms:   shortCircuitArms(kind),
		InLoop: b.loopDepth > 0,
	})
	if kind == DecisionOr {
		return append(tL, tR...), fR
	}
	return tR, append(fL, fR...)
}

// shortCircuitArms are the two outcomes of a short-circuit operator. Neither
// carries a source span, and that is not an omission: there is no counter
// anywhere in a Go coverage profile that distinguishes them.
func shortCircuitArms(kind DecisionKind) []Arm {
	right := "right operand evaluated"
	if kind == DecisionOr {
		return []Arm{{Label: right}, {Label: "short-circuited (left was true)"}}
	}
	return []Arm{{Label: right}, {Label: "short-circuited (left was false)"}}
}

func (b *builder) promoteBranch(idx int) {
	if idx < 0 {
		return
	}
	// Never overwrite entry/exit/loop: a loop header that evaluates a
	// condition is still the node the back edge returns to, and losing that
	// makes the graph unrenderable as a loop.
	if b.g.Blocks[idx].Kind == BlockBody {
		b.g.Blocks[idx].Kind = BlockBranch
	}
}

func (b *builder) buildIf(x *ast.IfStmt, succLine int) {
	if x.Init != nil {
		b.span(b.line(x.Init.Pos()), b.line(x.Init.End()))
	}
	b.span(b.line(x.Pos()), b.line(x.Cond.End()))
	tp, fp := b.cond(x.Cond)
	condBlock := b.cur

	thenStart, thenEnd := b.line(x.Body.Lbrace), b.line(x.Body.Rbrace)
	thenBlock := b.join(tp, BlockBody, thenStart, thenEnd)
	b.cur = thenBlock
	b.stmts(x.Body.List)
	var outs []pend
	if b.cur >= 0 {
		outs = append(outs, pend{from: b.cur, kind: EdgeSeq})
	}
	thenArm := Arm{
		Label: "true", Start: thenStart, End: thenEnd,
		Terminates: b.cur < 0, Block: blockOrNone(thenBlock),
	}

	elseArm := Arm{Label: "false"}
	if x.Else != nil {
		elseStart, elseEnd := b.line(x.Else.Pos()), b.line(x.Else.End())
		elseBlock := b.join(fp, BlockBody, elseStart, elseEnd)
		b.cur = elseBlock
		b.stmt(x.Else, succLine)
		if b.cur >= 0 {
			outs = append(outs, pend{from: b.cur, kind: EdgeSeq})
		}
		_, chained := x.Else.(*ast.IfStmt)
		elseArm = Arm{
			Label: "false", Start: elseStart, End: elseEnd,
			Chained: chained, Terminates: b.cur < 0, Block: blockOrNone(elseBlock),
		}
	} else {
		outs = append(outs, fp...)
	}

	joinBlock := b.join(outs, BlockBody, succLine, succLine)
	b.cur = joinBlock
	b.addDecision(Decision{
		Kind:          DecisionIf,
		Line:          b.line(x.Pos()),
		Block:         condBlock,
		Text:          exprText(x.Cond),
		Conditions:    b.atomicConditions(x.Cond),
		Arms:          []Arm{thenArm, elseArm},
		InLoop:        b.loopDepth > 0,
		SuccessorLine: succLine,
		JoinBlock:     blockOrNone(joinBlock),
	})
}

// blockOrNone normalises the builder's -1 ("control cannot reach here") to the
// 0 that Arm.Block and Decision.JoinBlock document as "no such block". Block 0
// is always the synthetic entry, so it can never be a real arm body or join.
func blockOrNone(idx int) int {
	if idx < 0 {
		return 0
	}
	return idx
}

// pushLoop opens the break/continue context for a loop and returns the slice
// its `break` statements accumulate into. The breaks cannot be wired when they
// are seen -- the block after the loop does not exist yet -- so they are held
// here and joined once the loop is closed.
func (b *builder) pushLoop(header int) *[]pend {
	breaks := &[]pend{}
	b.frames = append(b.frames, frame{label: b.nextLabel, loop: true, header: header, breaks: breaks})
	b.nextLabel = ""
	b.loopDepth++
	return breaks
}

func (b *builder) popLoop() {
	b.frames = b.frames[:len(b.frames)-1]
	b.loopDepth--
}

func (b *builder) buildFor(x *ast.ForStmt, succLine int) {
	if x.Init != nil {
		b.span(b.line(x.Init.Pos()), b.line(x.Init.End()))
	}
	header := b.newBlock(BlockLoop, b.line(x.Pos()), b.line(x.Pos()))
	b.addEdge(b.cur, header, EdgeSeq, "")
	b.cur = header

	var tp, fp []pend
	text := ""
	if x.Cond != nil {
		text = exprText(x.Cond)
		tp, fp = b.cond(x.Cond)
	} else {
		// `for {}` has one successor. Recording a false arm it does not have
		// would inflate complexity and invent an outcome no test can take.
		tp = []pend{{from: header, kind: EdgeSeq}}
	}

	arms := []Arm{{Label: "body entered", Start: b.line(x.Body.Lbrace), End: b.line(x.Body.Rbrace)}}
	if x.Cond != nil {
		arms = append(arms, Arm{Label: "loop exited"})
	}
	conds := b.atomicConditions(x.Cond)

	breaks := b.pushLoop(header)
	bodyBlock := b.join(tp, BlockBody, arms[0].Start, arms[0].End)
	b.cur = bodyBlock
	b.stmts(x.Body.List)
	bodyLeaves := b.cur < 0
	if b.cur >= 0 {
		b.addEdge(b.cur, header, EdgeLoopBack, "")
	}
	b.popLoop()

	arms[0].Terminates = bodyLeaves
	arms[0].Block = blockOrNone(bodyBlock)
	outs := append(fp, *breaks...) //nolint:gocritic // fp is not reused after this.
	joinBlock := b.join(outs, BlockBody, succLine, succLine)
	b.cur = joinBlock
	b.addDecision(Decision{
		Kind:          DecisionFor,
		Line:          b.line(x.Pos()),
		Block:         header,
		Text:          text,
		Conditions:    conds,
		Arms:          arms,
		InLoop:        b.loopDepth > 0,
		SuccessorLine: succLine,
		JoinBlock:     blockOrNone(joinBlock),
		// BreaksOut is what makes the "loop exited" outcome undecidable: a
		// break reaches the successor without the condition ever going
		// false, so a positive count there proves nothing about the arm.
		BreaksOut: len(*breaks) > 0,
	})
}

func (b *builder) buildRange(x *ast.RangeStmt, succLine int) {
	header := b.newBlock(BlockLoop, b.line(x.Pos()), b.line(x.Pos()))
	b.addEdge(b.cur, header, EdgeSeq, "")
	b.cur = header

	text := "range " + exprText(x.X)
	tp := []pend{{from: header, kind: EdgeTrue, cond: text}}
	fp := []pend{{from: header, kind: EdgeFalse, cond: text}}

	arms := []Arm{
		{Label: "body entered", Start: b.line(x.Body.Lbrace), End: b.line(x.Body.Rbrace)},
		{Label: "range exhausted"},
	}
	breaks := b.pushLoop(header)
	bodyBlock := b.join(tp, BlockBody, arms[0].Start, arms[0].End)
	b.cur = bodyBlock
	b.stmts(x.Body.List)
	arms[0].Terminates = b.cur < 0
	arms[0].Block = blockOrNone(bodyBlock)
	if b.cur >= 0 {
		b.addEdge(b.cur, header, EdgeLoopBack, "")
	}
	b.popLoop()

	outs := append(fp, *breaks...) //nolint:gocritic // fp is not reused after this.
	joinBlock := b.join(outs, BlockBody, succLine, succLine)
	b.cur = joinBlock
	b.addDecision(Decision{
		Kind:          DecisionRange,
		Line:          b.line(x.Pos()),
		Block:         header,
		Text:          text,
		Arms:          arms,
		InLoop:        b.loopDepth > 0,
		SuccessorLine: succLine,
		JoinBlock:     blockOrNone(joinBlock),
		BreaksOut:     len(*breaks) > 0,
		// Collection is what separates an N+1 from a retry loop: `range xs`
		// iterates a collection, `for i := range 10` does not.
		Collection: rangesOverCollection(x),
	})
}

// rangesOverCollection reports whether the range operand is a container
// rather than an integer count. Go 1.22's `range n` and `range f` (an
// iterator over a fixed sequence) are loops, but a query inside one is not
// the per-row query that makes an N+1.
func rangesOverCollection(x *ast.RangeStmt) bool {
	if lit, ok := x.X.(*ast.BasicLit); ok {
		return lit.Kind != token.INT
	}
	// A range with a value variable is iterating something with elements;
	// `range n` can only bind the index.
	return x.Value != nil || x.Key != nil
}

// clauseSpan is the source range a switch/select clause body occupies. It
// starts at the colon because that is where Go's cover instrumentation opens
// the clause's counter.
func (b *builder) clauseSpan(colon token.Pos, body []ast.Stmt) (int, int) {
	start := b.line(colon)
	end := start
	if len(body) > 0 {
		end = b.line(body[len(body)-1].End())
	}
	return start, end
}

// switchClause is the shape buildSwitch shares between value switches, type
// switches and selects.
type switchClause struct {
	colon token.Pos
	body  []ast.Stmt
	label string
}

// buildMultiway wires any n-way construct: a single deciding block with one
// edge per clause, clause bodies that may fall through to the next, and a
// join for whatever falls out.
func (b *builder) buildMultiway(kind DecisionKind, pos token.Pos, text string,
	clauses []switchClause, succLine int, implicitDefault bool,
) {
	b.span(b.line(pos), b.line(pos))
	if b.cur < 0 {
		b.cur = b.newBlock(BlockBody, b.line(pos), b.line(pos))
	}
	deciding := b.cur
	b.promoteBranch(deciding)

	blocks := make([]int, len(clauses))
	arms := make([]Arm, 0, len(clauses)+1)
	for i, c := range clauses {
		start, end := b.clauseSpan(c.colon, c.body)
		blocks[i] = b.newBlock(BlockBody, start, end)
		b.addEdge(deciding, blocks[i], EdgeTrue, c.label)
		arms = append(arms, Arm{Label: c.label, Start: start, End: end, Block: blocks[i]})
	}

	breaks := &[]pend{}
	b.frames = append(b.frames, frame{label: b.nextLabel, header: -1, breaks: breaks})
	b.nextLabel = ""

	var outs []pend
	for i, c := range clauses {
		b.cur = blocks[i]
		b.stmts(trimFallthrough(c.body))
		if hasFallthrough(c.body) && i+1 < len(clauses) {
			b.addEdge(b.cur, blocks[i+1], EdgeFallthrough, "")
			b.cur = -1
		}
		arms[i].Terminates = b.cur < 0
		if b.cur >= 0 {
			outs = append(outs, pend{from: b.cur, kind: EdgeSeq})
		}
	}
	b.frames = b.frames[:len(b.frames)-1]

	if implicitDefault {
		// A switch with no default has a "nothing matched" outcome that runs
		// no statement and therefore increments no counter. It is a real arm
		// and it is permanently unobservable; both facts are recorded.
		outs = append(outs, pend{from: deciding, kind: EdgeFalse, cond: "no case matched"})
		arms = append(arms, Arm{Label: "no case matched"})
	}
	outs = append(outs, *breaks...)
	joinBlock := b.join(outs, BlockBody, succLine, succLine)
	b.cur = joinBlock
	b.addDecision(Decision{
		Kind:          kind,
		Line:          b.line(pos),
		Block:         deciding,
		Text:          text,
		Arms:          arms,
		InLoop:        b.loopDepth > 0,
		SuccessorLine: succLine,
		JoinBlock:     blockOrNone(joinBlock),
	})
}

func hasFallthrough(body []ast.Stmt) bool {
	if len(body) == 0 {
		return false
	}
	br, ok := body[len(body)-1].(*ast.BranchStmt)
	return ok && br.Tok == token.FALLTHROUGH
}

func trimFallthrough(body []ast.Stmt) []ast.Stmt {
	if hasFallthrough(body) {
		return body[:len(body)-1]
	}
	return body
}

func (b *builder) buildSwitch(x *ast.SwitchStmt, succLine int) {
	if x.Init != nil {
		b.span(b.line(x.Init.Pos()), b.line(x.Init.End()))
	}
	clauses, hasDefault := b.caseClauses(x.Body, "case ")
	b.buildMultiway(DecisionSwitch, x.Pos(), exprText(x.Tag), clauses, succLine, !hasDefault)
}

func (b *builder) buildTypeSwitch(x *ast.TypeSwitchStmt, succLine int) {
	if x.Init != nil {
		b.span(b.line(x.Init.Pos()), b.line(x.Init.End()))
	}
	if x.Assign != nil {
		b.span(b.line(x.Assign.Pos()), b.line(x.Assign.End()))
	}
	clauses, hasDefault := b.caseClauses(x.Body, "case ")
	b.buildMultiway(DecisionTypeSwitch, x.Pos(), "", clauses, succLine, !hasDefault)
}

func (b *builder) caseClauses(body *ast.BlockStmt, prefix string) ([]switchClause, bool) {
	var out []switchClause
	hasDefault := false
	for _, s := range body.List {
		cc, ok := s.(*ast.CaseClause)
		if !ok {
			continue
		}
		label := "default"
		if len(cc.List) > 0 {
			parts := make([]string, 0, len(cc.List))
			for _, e := range cc.List {
				parts = append(parts, exprText(e))
			}
			label = prefix + strings.Join(parts, ", ")
		} else {
			hasDefault = true
		}
		out = append(out, switchClause{colon: cc.Colon, body: cc.Body, label: label})
	}
	return out, hasDefault
}

func (b *builder) buildSelect(x *ast.SelectStmt, succLine int) {
	var clauses []switchClause
	for _, s := range x.Body.List {
		cc, ok := s.(*ast.CommClause)
		if !ok {
			continue
		}
		label := "default"
		if cc.Comm != nil {
			label = commText(cc.Comm)
		}
		clauses = append(clauses, switchClause{colon: cc.Colon, body: cc.Body, label: label})
	}
	// A select with no default has no "nothing matched" arm: it blocks until
	// one clause is ready, so there is no fall-through outcome to record.
	b.buildMultiway(DecisionSelect, x.Pos(), "", clauses, succLine, false)
}

func commText(s ast.Stmt) string {
	switch c := s.(type) {
	case *ast.SendStmt:
		return exprText(c.Chan) + " <- " + exprText(c.Value)
	case *ast.ExprStmt:
		return exprText(c.X)
	case *ast.AssignStmt:
		lhs := make([]string, 0, len(c.Lhs))
		for _, e := range c.Lhs {
			lhs = append(lhs, exprText(e))
		}
		rhs := make([]string, 0, len(c.Rhs))
		for _, e := range c.Rhs {
			rhs = append(rhs, exprText(e))
		}
		return strings.Join(lhs, ", ") + " " + c.Tok.String() + " " + strings.Join(rhs, ", ")
	}
	return "comm"
}

// shortCircuits models `&&` / `||` that appear OUTSIDE a condition — in an
// assignment, a return value, a call argument. They still branch at runtime
// (the right operand may not be evaluated) and they still cost a path, so
// they are still decisions. Their arms carry no span for the same reason
// condition-position ones do not: one counter covers both.
func (b *builder) shortCircuits(s ast.Stmt) {
	ast.Inspect(s, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false // a closure is a separate flow; see noteFuncLits
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				b.valueShortCircuit(x)
				return false
			}
		}
		return true
	})
}

func (b *builder) valueShortCircuit(x *ast.BinaryExpr) {
	// Nested operands first, so `a && b && c` contributes one decision per
	// operator rather than collapsing to one.
	for _, side := range []ast.Expr{x.X, x.Y} {
		if inner, ok := unparen(side).(*ast.BinaryExpr); ok &&
			(inner.Op == token.LAND || inner.Op == token.LOR) {
			b.valueShortCircuit(inner)
		}
	}
	line := b.line(x.OpPos)
	b.span(line, line)
	from := b.cur
	b.promoteBranch(from)

	enter, skip := EdgeTrue, EdgeFalse
	kind := DecisionAnd
	if x.Op == token.LOR {
		enter, skip = EdgeFalse, EdgeTrue
		kind = DecisionOr
	}
	rhs := b.newBlock(BlockBody, b.line(x.Y.Pos()), b.line(x.Y.End()))
	b.addEdge(from, rhs, enter, exprText(x.X))
	cont := b.newBlock(BlockBody, line, line)
	b.addEdge(rhs, cont, EdgeSeq, "")
	b.addEdge(from, cont, skip, exprText(x.X))
	b.cur = cont
	b.addDecision(Decision{
		Kind:   kind,
		Line:   line,
		Block:  from,
		Text:   exprText(x),
		Arms:   shortCircuitArms(kind),
		InLoop: b.loopDepth > 0,
	})
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// atomicConditions flattens a decision's boolean expression into the operands
// MC/DC would have to exercise independently: the leaves of the &&/|| tree,
// with parentheses and a leading `!` stripped (MC/DC reasons about the
// condition, not about its negation).
func (b *builder) atomicConditions(e ast.Expr) []Condition {
	if e == nil {
		return nil
	}
	var out []Condition
	var walk func(ast.Expr)
	walk = func(x ast.Expr) {
		switch v := unparen(x).(type) {
		case *ast.BinaryExpr:
			if v.Op == token.LAND || v.Op == token.LOR {
				walk(v.X)
				walk(v.Y)
				return
			}
			out = append(out, Condition{Text: exprText(v), Line: b.line(v.Pos())})
		case *ast.UnaryExpr:
			if v.Op == token.NOT {
				walk(v.X)
				return
			}
			out = append(out, Condition{Text: exprText(v), Line: b.line(v.Pos())})
		default:
			out = append(out, Condition{Text: exprText(v), Line: b.line(x.Pos())})
		}
	}
	walk(e)
	return out
}
