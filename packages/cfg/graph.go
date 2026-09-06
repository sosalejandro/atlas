// Package cfg builds an intra-procedural control-flow graph for a Go
// function and derives from it the three things a symbol's line range alone
// can never answer: how many independent paths run through the body
// (cyclomatic complexity), which branch outcomes a test suite actually took
// (decision coverage), and whether a query is issued once per element of a
// collection (the N+1 shape).
//
// # Why a hand-rolled CFG
//
// golang.org/x/tools/go/cfg exists and is good, but it models blocks of
// STATEMENTS: `if a && b` is one node there, because the short-circuit
// evaluation of `b` is not a statement. The whole point of this package is
// that `&&` and `||` ARE decisions — they are the branches hand-rolled
// implementations forget, and a coverage story that ignores them silently
// overstates how much of the logic was exercised. So the builder here splits
// short-circuit operands into their own condition blocks, and the graph is
// therefore a superset of what go/cfg would give us.
//
// # The honest ceiling on what execution data can tell us
//
// Go's `-coverprofile` is STATEMENT coverage: it records that a basic block
// executed and how often, never which way a condition went. That is enough
// for decision coverage in some shapes and not others, and this package is
// deliberately explicit about which is which (see Analyze in coverage.go).
// It is NEVER enough for MC/DC — modified condition/decision coverage, the
// DO-178C DAL A metric — because the individual operands of `a && b` share
// one counter and their independent outcomes are simply not recorded.
//
// This package therefore enumerates the conditions MC/DC would need and
// reports how many of them are independently exercisable IN PRINCIPLE
// (Analysis.ConditionsIndependent). That number is a property of the source,
// not a coverage verdict, and no caller may present it as one: a real MC/DC
// verdict needs condition-level instrumentation that atlas does not have and
// cannot fake. See MCDCNotDerivable, which every surface prints beside it.
package cfg

import "fmt"

// BlockKind classifies a CFG node. It is persisted verbatim in
// `cfg_blocks.kind` (migration 0015), so the set is a schema contract.
type BlockKind string

const (
	// BlockEntry is the synthetic single entry node. Its span is the
	// function's declaration line, so a reader can anchor the graph in
	// source without a second lookup.
	BlockEntry BlockKind = "entry"
	// BlockBody is straight-line code: no branch leaves it except the
	// unconditional one to its successor.
	BlockBody BlockKind = "body"
	// BlockBranch evaluates a condition and has (at least) a true and a
	// false successor. Short-circuit operands get one of these each.
	BlockBranch BlockKind = "branch"
	// BlockLoop is a loop header: the node a back edge returns to.
	BlockLoop BlockKind = "loop"
	// BlockExit is the synthetic single exit node. Every return, and the
	// fall-off-the-end path, edges into it.
	BlockExit BlockKind = "exit"
)

// EdgeKind classifies a CFG edge, persisted in `cfg_edges.kind`.
//
// EdgeSeq is not in the issue's sketch of the schema but is unavoidable:
// most edges in a real function are unconditional continuations, and giving
// them a "true" kind would make every straight line look like a taken
// branch to anything counting branch arms.
type EdgeKind string

const (
	EdgeSeq         EdgeKind = "seq"
	EdgeTrue        EdgeKind = "true"
	EdgeFalse       EdgeKind = "false"
	EdgeLoopBack    EdgeKind = "loop-back"
	EdgeFallthrough EdgeKind = "fallthrough"
)

// Block is one CFG node.
type Block struct {
	Index     int       `json:"index"`
	Kind      BlockKind `json:"kind"`
	StartLine int       `json:"start_line"`
	EndLine   int       `json:"end_line"`
}

// Edge is one directed CFG edge. Condition carries the source text of the
// expression that gates it (empty for EdgeSeq / EdgeLoopBack), which is what
// makes a rendered flowchart readable without re-parsing the file.
type Edge struct {
	From      int      `json:"from"`
	To        int      `json:"to"`
	Kind      EdgeKind `json:"kind"`
	Condition string   `json:"condition,omitempty"`
}

// DecisionKind names the source construct a Decision came from. Persisted in
// `cfg_decisions.kind`.
type DecisionKind string

const (
	DecisionIf         DecisionKind = "if"
	DecisionFor        DecisionKind = "for"
	DecisionRange      DecisionKind = "range"
	DecisionSwitch     DecisionKind = "switch"
	DecisionTypeSwitch DecisionKind = "type-switch"
	DecisionSelect     DecisionKind = "select"
	// DecisionAnd / DecisionOr are the short-circuit operators. They are
	// decisions in exactly the sense that matters — control reaches the
	// right-hand operand only on one outcome of the left — and they are the
	// ones a statement-level model drops on the floor.
	DecisionAnd DecisionKind = "and"
	DecisionOr  DecisionKind = "or"
)

// Condition is one atomic boolean sub-expression of a decision: a leaf of
// the &&/|| tree with parentheses and leading `!` stripped, because MC/DC
// reasons about the operand, not about its negation.
type Condition struct {
	Text string `json:"text"`
	Line int    `json:"line"`
}

// Arm is one outcome of a decision, together with the source span that
// executes when it is taken.
//
// Start/End are 0 when the outcome has no body of its own — the false arm of
// an `if` with no `else`, or either operand outcome of a `&&`. That zero is
// load-bearing: it is precisely the case where statement coverage records no
// counter, and Analyze must refuse to claim the arm was or was not taken
// rather than guess from a neighbouring block.
type Arm struct {
	Label string `json:"label"`
	Start int    `json:"start_line,omitempty"`
	End   int    `json:"end_line,omitempty"`
	// Chained marks an `else if`: the arm's body is another if statement,
	// which carries a counter for its own condition. Any counter inside the
	// chain proves the arm was entered, which is not true of an ordinary
	// body (where a nested false condition can leave every inner counter at
	// zero).
	Chained bool `json:"chained,omitempty"`
	// Terminates reports that the END of the arm cannot fall through to the
	// statement after the decision (it returns, breaks, continues, gotos or
	// panics).
	//
	// It is deliberately NOT what Analyze reasons about. It describes only
	// the last statement of the arm: an arm that falls through at its end can
	// still leave the function on a nested path (a guard `return`, a
	// `panic`), and an arm that "terminates" by `break`ing out of a switch
	// reaches the successor exactly as a non-matching case would. Both make
	// the successor-difference inference wrong in opposite directions, so
	// Analyze walks the CFG from Block instead. Terminates is kept because it
	// is a true and useful fact about the arm's shape, not because the
	// analysis can lean on it.
	Terminates bool `json:"terminates,omitempty"`
	// Block is the CFG node control enters when the arm is taken, or 0 when
	// the arm has no body of its own. Zero is unambiguous: block 0 is always
	// the synthetic entry and can never be an arm body.
	//
	// It exists so the coverage analysis can ask what a single flag cannot
	// answer -- does EVERY path out of this arm reach the statement after the
	// construct, or does some path leave the function first.
	Block int `json:"block,omitempty"`
}

// Decision is one branching construct in the source, with everything the
// coverage and complexity analyses need attached.
type Decision struct {
	Index int          `json:"index"`
	Kind  DecisionKind `json:"kind"`
	Line  int          `json:"line"`
	// Block is the CFG node that evaluates the condition.
	Block int `json:"block"`
	// Text is the condition source, trimmed. Empty for `for {}` and for a
	// type switch on a value with no comparison text worth showing.
	Text string `json:"text,omitempty"`
	// Conditions are the atomic operands MC/DC would have to exercise
	// independently. A decision with no boolean expression (range, select,
	// switch on a value) has none.
	Conditions []Condition `json:"conditions,omitempty"`
	Arms       []Arm       `json:"arms"`
	// InLoop marks a decision lexically inside a for/range in the same
	// function. Counts inside a loop accumulate across iterations, which is
	// what makes the successor-difference inference in Analyze invalid
	// there — see the ReasonInLoop path.
	InLoop bool `json:"in_loop,omitempty"`
	// SuccessorLine is the first line of the statement that follows the
	// whole construct in its enclosing statement list, or 0 when the
	// construct is last. Analyze uses it to find the block execution
	// resumes in.
	SuccessorLine int `json:"successor_line,omitempty"`
	// JoinBlock is the CFG node the construct's arms rejoin at -- the node
	// that spans SuccessorLine -- or 0 when nothing reaches it. Zero is
	// unambiguous for the same reason Arm.Block's is.
	//
	// Analyze needs the node and not just the line: "did this arm reach the
	// successor" is a reachability question over the graph, and answering it
	// from line numbers would re-import the very guesswork the graph exists
	// to replace.
	JoinBlock int `json:"join_block,omitempty"`
	// BreaksOut marks a loop whose body can `break`. It kills the one
	// inference available for a loop's exit outcome: a break reaches the
	// statement after the loop without the loop condition ever going false,
	// so a positive count there proves nothing about the arm.
	BreaksOut bool `json:"breaks_out,omitempty"`
	// Collection marks a `range` over something with elements, as opposed
	// to Go 1.22's `range n`. The N+1 detector needs the distinction: a
	// query per element of a collection is the smell; a query in a retry
	// loop is not.
	Collection bool `json:"collection,omitempty"`
}

// Graph is a function's control-flow graph plus its derived structure.
type Graph struct {
	// Func is the function's name as written (`Sum`, `(*Repo).Load`).
	Func      string     `json:"func"`
	File      string     `json:"file"`
	StartLine int        `json:"start_line"`
	EndLine   int        `json:"end_line"`
	Blocks    []Block    `json:"blocks"`
	Edges     []Edge     `json:"edges"`
	Decisions []Decision `json:"decisions"`
	// Defers counts `defer` statements. They are recorded but NOT wired as
	// edges: a deferred call runs at every exit, so edging it in would make
	// the deferred body look like a branch arm on every path and inflate
	// complexity for what is not a decision at all.
	Defers int `json:"defers"`
	// Warnings records constructs the builder could not model exactly (an
	// unresolved `goto`, most often). They travel with the graph rather
	// than being logged and lost, because they bound how much the coverage
	// numbers below can be trusted.
	Warnings []string `json:"warnings,omitempty"`
	// HasGoto reports that the function contains a `goto`, whose edge the
	// builder does not draw. It is a separate flag rather than a string
	// match on Warnings because two analyses have to REFUSE TO ANSWER over a
	// graph missing edges -- reachability (a label reached only by a goto is
	// not unreachable code) and the successor-difference coverage inference
	// (a goto into the successor is an unaccounted-for way to reach it) --
	// and a refusal that depends on parsing a human-readable warning is a
	// refusal that will stop happening.
	HasGoto bool `json:"has_goto,omitempty"`
}

// Complexity is the cyclomatic complexity of the function: one, plus one for
// every branch arm beyond the first on every decision.
//
// This is E - N + 2 over the graph this package builds, which is the textbook
// definition, and it counts && / || because they add real edges here. It can
// differ by one per `select` from what gocyclo reports (gocyclo charges N for
// N comm clauses; the graph has N-1 extra edges), and that difference is a
// property of the definition, not a bug — see docs/commands/flow.md.
func (g *Graph) Complexity() int {
	c := 1
	for _, d := range g.Decisions {
		if len(d.Arms) > 1 {
			c += len(d.Arms) - 1
		}
	}
	return c
}

// ConditionCount is the number of atomic conditions across every decision —
// the size of the set MC/DC would have to exercise independently.
func (g *Graph) ConditionCount() int {
	n := 0
	for _, d := range g.Decisions {
		n += len(d.Conditions)
	}
	return n
}

// BranchArms is the total number of outcomes across every decision: the
// denominator decision coverage would have if every one of them were
// observable. Analyze reports how many actually are.
func (g *Graph) BranchArms() int {
	n := 0
	for _, d := range g.Decisions {
		n += len(d.Arms)
	}
	return n
}

// Successors is the graph's adjacency list, indexed by block. It is built on
// demand rather than stored because the graph is rewritten as it is built and
// a cached adjacency list would go stale mid-construction.
func (g *Graph) Successors() [][]int {
	succ := make([][]int, len(g.Blocks))
	for _, e := range g.Edges {
		if e.From < 0 || e.From >= len(g.Blocks) || e.To < 0 || e.To >= len(g.Blocks) {
			continue
		}
		succ[e.From] = append(succ[e.From], e.To)
	}
	return succ
}

// Unreachable returns the indices of blocks no path from the entry can
// reach — the `flow.unreachable` diagnostic — and whether that answer can be
// trusted. This is a structural fact ("no execution can get here"),
// categorically different from "no test got here", and the two must never be
// reported as the same finding.
//
// The second return is the honest half. The builder does not draw `goto`
// edges, so in a function containing one a label reached ONLY by that goto
// has no incoming edge in the graph and would be reported as dead code that
// runs on every call. Rather than emit a confident wrong answer over a graph
// known to be missing edges, Unreachable declines: it returns no blocks and
// sound=false, and every caller must say "not determined" rather than "none".
func (g *Graph) Unreachable() (blocks []int, sound bool) {
	if len(g.Blocks) == 0 {
		return nil, true
	}
	if g.HasGoto {
		return nil, false
	}
	seen := make([]bool, len(g.Blocks))
	succ := g.Successors()
	queue := []int{entryBlock}
	seen[entryBlock] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range succ[cur] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	var out []int
	for i, ok := range seen {
		// The exit node is reachable by construction in any function with a
		// return path; one that never returns (an infinite loop) leaves it
		// unreached, and reporting THAT as unreachable code would be a lie.
		if !ok && g.Blocks[i].Kind != BlockExit {
			out = append(out, i)
		}
	}
	return out, true
}

// String renders a compact one-line summary, used in CLI text output.
func (g *Graph) String() string {
	return fmt.Sprintf("%s (%s:%d-%d) blocks=%d edges=%d decisions=%d complexity=%d",
		g.Func, g.File, g.StartLine, g.EndLine,
		len(g.Blocks), len(g.Edges), len(g.Decisions), g.Complexity())
}
