package cfg

import (
	"fmt"
	"path"
	"strings"

	"github.com/sosalejandro/atlas/packages/coverage/gocover"
)

// ExecBlock is one basic block's execution record: the shape a Go coverage
// profile carries, reduced to what the analysis can actually use.
//
// Note what is NOT here: any notion of which way a condition went. That
// absence is the whole subject of this file.
type ExecBlock struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
	NumStmts  int `json:"num_stmts"`
	Count     int `json:"count"`
}

// ExecBlocksForFile narrows a parsed coverprofile to one source file.
//
// Profile paths are import-path-qualified (github.com/org/repo/pkg/f.go) and
// atlas file paths are repo-relative, so they are reconciled by suffix on a
// path-segment boundary — the same rule the coverage ingest uses. Duplicate
// spans (a `-coverpkg=./...` profile repeats each block once per tested
// package) are merged to their maximum count first; summing them instead
// would multiply every count by the number of packages and make the
// successor-difference inferences below nonsense.
func ExecBlocksForFile(blocks []gocover.Block, file string) []ExecBlock {
	merged := gocover.MergeBlocks(blocks)
	want := path.Clean(strings.ReplaceAll(file, "\\", "/"))
	out := make([]ExecBlock, 0, len(merged))
	for _, b := range merged {
		got := path.Clean(strings.ReplaceAll(b.File, "\\", "/"))
		if !sameFile(got, want) {
			continue
		}
		out = append(out, ExecBlock{
			StartLine: b.StartLine, EndLine: b.EndLine,
			NumStmts: b.NumStmts, Count: b.Count,
		})
	}
	return out
}

func sameFile(a, b string) bool {
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

// Reasons an outcome is decidable, or is not. They are exported because every
// surface that prints a decision-coverage number must be able to print WHY a
// missing outcome is missing — "we did not observe it" and "nothing could
// observe it" are different facts and a reader acting on the number needs the
// difference.
const (
	ReasonOwnCounter = "the arm has its own coverage counter"
	ReasonSuccessor  = "inferred from the execution count of the statement following the branch"
	// ReasonShortCircuit is the MC/DC ceiling in one sentence: `a && b` is a
	// single instrumented block, so which operand decided the outcome is not
	// recorded anywhere.
	ReasonShortCircuit = "statement coverage instruments both operands of a short-circuit operator as one block"
	ReasonInLoop       = "the branch is inside a loop, where block counts accumulate across iterations and cannot be differenced"
	ReasonNoSuccessor  = "the branch has no else and no following statement, so its false outcome increments no counter"
	ReasonNoCounter    = "no coverage block covers this arm"
	ReasonLoopBreaks   = "the loop body can break, so the statement after the loop does not witness the exit condition"
	// ReasonClauseReachesSuccessor is the switch analogue of ReasonArmEscapes,
	// and the wording matters: a clause reaches the statement after the switch
	// whether it falls out of the clause OR breaks out of it. `break` is the
	// case that looks like an exit and is not.
	ReasonClauseReachesSuccessor = "a clause reaches the statement after the switch (by falling out of it or by breaking out of it), " +
		"so that statement does not witness the no-match outcome"
	// ReasonArmEscapes is the narrowed claim for an `if` with no `else`.
	// Differencing the successor's count against the then arm's is valid only
	// when the then arm reaches the successor on EVERY path or on NONE. When
	// some paths reach it and others leave the function first (a guard
	// `return`, a `panic`, a nested `if` that returns), the successor's count
	// is neither "both arms" nor "the false arm only" and no arithmetic over
	// it recovers the false outcome.
	ReasonArmEscapes = "some but not all paths through the other arm reach the statement after the branch, " +
		"so its execution count is neither the sum of both arms nor the false arm alone"
	// ReasonUnmodelledGoto: the successor-difference inferences all assume the
	// graph names every way to reach the successor. A `goto` the builder did
	// not draw is a way it does not name.
	ReasonUnmodelledGoto = "the function contains a `goto` whose edge the graph does not model, " +
		"so the statement after the branch may be reached by a path the analysis cannot see"
	ReasonNoProfile = "no coverage profile was supplied"
)

// MCDCNotDerivable is the sentence every surface reporting condition counts
// must carry. It is a constant rather than prose in each renderer so it
// cannot drift into something weaker.
const MCDCNotDerivable = "MC/DC is NOT derivable from Go's statement coverage: the operands of a " +
	"short-circuit expression share one counter, so no profile can show that a condition " +
	"independently affected the outcome. The condition counts below describe the SOURCE " +
	"(how many conditions exist, and how many could in principle be varied independently); " +
	"they are not an MC/DC result. A DO-178C DAL A verdict needs condition-level " +
	"instrumentation that atlas does not have."

// Verdict is one outcome's answer. It is three-valued and the third value is
// not a formality: "the tests never took this branch" and "no profile could
// tell you whether they did" lead to opposite actions, and collapsing the
// second into a `taken=false` boolean is how a tool ends up reporting an
// untested branch that was in fact exercised on every call.
type Verdict string

const (
	VerdictTaken    Verdict = "taken"
	VerdictNotTaken Verdict = "not-taken"
	// VerdictUndetermined is the answer whenever the evidence does not reach.
	// It is always available, and it is always better than a confident wrong
	// answer: this analysis would rather answer less and be right.
	VerdictUndetermined Verdict = "undetermined"
)

// ArmResult is one decision outcome's verdict.
//
// Verdict is the single source of truth; Decidable and Taken are derived from
// it rather than stored beside it, so the pair cannot drift into the
// impossible "not decidable but taken" state.
type ArmResult struct {
	Decision int          `json:"decision"`
	Kind     DecisionKind `json:"kind"`
	Line     int          `json:"line"`
	Label    string       `json:"label"`
	Verdict  Verdict      `json:"verdict"`
	Reason   string       `json:"reason"`
}

// Decidable reports whether the profile could answer at all.
func (r ArmResult) Decidable() bool { return r.Verdict != VerdictUndetermined }

// Taken reports an outcome the profile shows was exercised. It is false for an
// undetermined outcome too, so never read it without Decidable.
func (r ArmResult) Taken() bool { return r.Verdict == VerdictTaken }

func (r ArmResult) String() string {
	return fmt.Sprintf("line %d %s[%s]: %s (%s)",
		r.Line, r.Kind, r.Label, strings.ToUpper(string(r.Verdict)), r.Reason)
}

// Analysis is the per-symbol decision-coverage result.
type Analysis struct {
	Func string      `json:"func"`
	Arms []ArmResult `json:"arms"`

	// OutcomesTotal is every branch outcome the source has.
	OutcomesTotal int `json:"outcomes_total"`
	// OutcomesDecidable is how many of those a statement-coverage profile can
	// judge. The gap between the two is the honest size of the blind spot,
	// and it is reported rather than divided away.
	OutcomesDecidable int `json:"outcomes_decidable"`
	OutcomesTaken     int `json:"outcomes_taken"`
	// OutcomesUndetermined is that gap, counted rather than left to be
	// subtracted. It is carried explicitly because a reader who has to
	// compute it is a reader who will forget it exists.
	OutcomesUndetermined int `json:"outcomes_undetermined"`

	Complexity int `json:"complexity"`

	// Conditions / ConditionsIndependent describe the source, not the tests:
	// how many atomic conditions exist and how many could be varied on their
	// own (a condition repeated inside one decision cannot). See
	// MCDCNotDerivable — these are NOT an MC/DC verdict.
	Conditions            int `json:"conditions"`
	ConditionsIndependent int `json:"conditions_independent"`
}

// DecisionCoverage is taken/decidable as a percentage, and whether it is
// available at all.
//
// The `ok` return is not ceremony. A function whose only branch is a `&&`, or
// one measured without a profile, has NO judgeable outcome; reporting 0% for
// it would be indistinguishable from a fully untested function, and a reader
// would act on the difference.
func (a Analysis) DecisionCoverage() (float64, bool) {
	if a.OutcomesDecidable == 0 {
		return 0, false
	}
	return 100 * float64(a.OutcomesTaken) / float64(a.OutcomesDecidable), true
}

// Analyze computes decision coverage for one function from its CFG and the
// execution blocks of its file. Pass nil blocks to get the structural half
// (complexity, conditions) with every outcome unjudged.
func Analyze(g *Graph, blocks []ExecBlock) Analysis {
	a := Analysis{
		Func:                  g.Func,
		Complexity:            g.Complexity(),
		Conditions:            g.ConditionCount(),
		ConditionsIndependent: independentConditions(g),
	}
	jc := &judgeCtx{g: g, blocks: blocks, succ: g.Successors()}
	for i := range g.Decisions {
		d := &g.Decisions[i]
		for j, arm := range d.Arms {
			r := jc.judge(d, j, arm)
			a.Arms = append(a.Arms, r)
			a.OutcomesTotal++
			switch r.Verdict {
			case VerdictTaken:
				a.OutcomesDecidable++
				a.OutcomesTaken++
			case VerdictNotTaken:
				a.OutcomesDecidable++
			case VerdictUndetermined:
				a.OutcomesUndetermined++
			}
		}
	}
	return a
}

// judgeCtx carries what judging one arm needs: the graph (because "did this
// arm reach the statement after the construct" is a reachability question, not
// a property of a single flag), its adjacency list built once, and the file's
// execution blocks.
type judgeCtx struct {
	g      *Graph
	blocks []ExecBlock
	succ   [][]int
}

// judge decides one arm. The split by kind is the honest part of this
// package: each construct leaves a different amount of evidence behind.
func (jc *judgeCtx) judge(d *Decision, armIdx int, arm Arm) ArmResult {
	r := ArmResult{
		Decision: d.Index, Kind: d.Kind, Line: d.Line,
		Label: arm.Label, Verdict: VerdictUndetermined,
	}
	switch {
	case d.Kind == DecisionAnd || d.Kind == DecisionOr:
		r.Reason = ReasonShortCircuit
		return r
	case len(jc.blocks) == 0:
		r.Reason = ReasonNoProfile
		return r
	case arm.Start > 0:
		// The arm has a body, so it has a counter of its own: the strongest
		// evidence available, and it holds inside loops too.
		count, ok := entryCount(jc.blocks, arm)
		if !ok {
			r.Reason = ReasonNoCounter
			return r
		}
		return decide(r, count > 0, ReasonOwnCounter)
	}
	return jc.judgeBodilessArm(d, armIdx, r)
}

// decide stamps a determined verdict. Going through one function keeps the
// three-valued type from being set field-by-field at eleven call sites.
func decide(r ArmResult, taken bool, reason string) ArmResult {
	r.Verdict, r.Reason = VerdictNotTaken, reason
	if taken {
		r.Verdict = VerdictTaken
	}
	return r
}

// judgeBodilessArm handles the outcomes that execute no statement of their
// own: the false arm of an `if` with no `else`, a loop's exit, and the
// "nothing matched" arm of a switch with no default. All three are witnessed
// only by the statement that follows the whole construct — when one exists,
// when the construct is not inside a loop that scrambles the counts, and when
// the graph names every way that statement can be reached.
func (jc *judgeCtx) judgeBodilessArm(d *Decision, armIdx int, r ArmResult) ArmResult {
	if d.SuccessorLine == 0 {
		r.Reason = ReasonNoSuccessor
		return r
	}
	if d.InLoop {
		// Inside a loop the successor's count is the sum over iterations and
		// the arm's count is too; their difference conflates "the false path
		// was taken once" with "the loop ran twice". Refusing is the only
		// honest answer, and it is what the issue asks for.
		r.Reason = ReasonInLoop
		return r
	}
	if jc.g.HasGoto {
		// Every inference below reads the successor's counter as the sum of a
		// known set of paths. An unmodelled `goto` is a path outside that set.
		r.Reason = ReasonUnmodelledGoto
		return r
	}
	succ, ok := blockAt(jc.blocks, d.SuccessorLine)
	if !ok {
		r.Reason = ReasonNoCounter
		return r
	}
	switch d.Kind {
	case DecisionFor, DecisionRange:
		if d.BreaksOut {
			r.Reason = ReasonLoopBreaks
			return r
		}
		// With no break, the only way past the loop is the condition going
		// false. (A `return` inside the body leaves the function without
		// reaching the successor, so it does not confound this.)
		return decide(r, succ.Count > 0, ReasonSuccessor)
	case DecisionSwitch, DecisionTypeSwitch:
		return jc.judgeImplicitDefault(d, r, succ)
	}
	return jc.judgeIfFalseArm(d, armIdx, r, succ)
}

// judgeImplicitDefault handles the "no case matched" arm of a switch with no
// default. It is witnessed only by the statement after the switch, so it is
// knowable only when NO clause can reach that statement — that is, when every
// clause leaves the function.
//
// The tempting condition, "every clause terminates", is exactly backwards for
// the case that matters. A clause ending in `break` cannot fall out of its own
// body, so it looks like a terminating clause, and breaking out is precisely
// how a matched clause arrives at the successor. So the question is asked of
// the graph — can this clause reach the join — and not of a per-clause flag.
func (jc *judgeCtx) judgeImplicitDefault(d *Decision, r ArmResult, succ ExecBlock) ArmResult {
	if d.JoinBlock == 0 {
		r.Reason = ReasonNoCounter
		return r
	}
	for _, arm := range d.Arms {
		if arm.Block == 0 {
			continue // the implicit-default arm itself; it has no clause body
		}
		if jc.reaches(arm.Block, d.JoinBlock) {
			r.Reason = ReasonClauseReachesSuccessor
			return r
		}
	}
	return decide(r, succ.Count > 0, ReasonSuccessor)
}

// judgeIfFalseArm recovers the false outcome of an `if` with no `else` from
// the count of the statement after the if — but only in the two shapes where
// the arithmetic is exact.
//
//   - NO path through the then arm reaches the successor (the arm returns, or
//     panics, on every path): the successor is reached only on the false path,
//     so any count there proves the false outcome was taken.
//   - EVERY path through the then arm reaches the successor: the successor's
//     count is both paths summed, so the false path was taken exactly when it
//     EXCEEDS the then arm's count.
//
// In between — a then arm that usually falls through but sometimes returns
// from a nested guard — the successor's count is neither quantity, and the
// difference is off by however many times the nested path fired. That is the
// case this function refuses, because a verdict computed there is not
// conservative: it reports the false arm as untaken when it was taken, and as
// taken when it was not, depending only on which way the nested path went.
func (jc *judgeCtx) judgeIfFalseArm(d *Decision, armIdx int, r ArmResult, succ ExecBlock) ArmResult {
	if armIdx == 0 || len(d.Arms) == 0 {
		r.Reason = ReasonNoCounter
		return r
	}
	thenArm := d.Arms[0]
	if thenArm.Start == 0 || thenArm.Block == 0 || d.JoinBlock == 0 {
		// Not the shape this function reasons about (an if whose THEN arm has
		// no body cannot occur in Go, so this is defensive).
		r.Reason = ReasonNoCounter
		return r
	}
	if !jc.reaches(thenArm.Block, d.JoinBlock) {
		return decide(r, succ.Count > 0, ReasonSuccessor)
	}
	if !jc.alwaysReaches(thenArm.Block, d.JoinBlock) {
		r.Reason = ReasonArmEscapes
		return r
	}
	thenCount, ok := entryCount(jc.blocks, thenArm)
	if !ok {
		r.Reason = ReasonNoCounter
		return r
	}
	return decide(r, succ.Count > thenCount, ReasonSuccessor)
}

// reaches reports whether SOME path from `from` arrives at `join`.
func (jc *judgeCtx) reaches(from, join int) bool {
	if from < 0 || from >= len(jc.succ) {
		return false
	}
	seen := make([]bool, len(jc.succ))
	queue := []int{from}
	seen[from] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == join {
			return true
		}
		for _, n := range jc.succ[cur] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return false
}

// alwaysReaches reports whether EVERY path leaving `from` arrives at `join` —
// the condition that makes "the successor's count is both arms summed" true.
//
// The walk stops at `join` (arriving there is the success case) and fails on
// two things: reaching the function's exit node, which is a path that left
// without joining, and reaching a block with no successors at all, which is
// where control went somewhere the graph does not model. Both are answered
// "no" rather than assumed away.
func (jc *judgeCtx) alwaysReaches(from, join int) bool {
	if from < 0 || from >= len(jc.succ) || join < 0 || join >= len(jc.succ) {
		return false
	}
	seen := make([]bool, len(jc.succ))
	queue := []int{from}
	seen[from] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == join {
			continue
		}
		if cur == exitBlock || len(jc.succ[cur]) == 0 {
			return false
		}
		for _, n := range jc.succ[cur] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return true
}

// entryCount returns the execution count of the block that runs FIRST when
// the arm is entered.
//
// Selection is fiddly for a reason worth recording. Go's instrumentation
// opens a counter just after the `{` and closes it at the `}`, so a block
// that STARTS on the arm's closing-brace line belongs to whatever follows,
// and a single-line block on the opening line is the construct's own header
// counter (`for i < n {` has one) rather than the body. Getting either wrong
// makes a never-entered loop body read as entered.
func entryCount(blocks []ExecBlock, arm Arm) (int, bool) {
	lo, hi := arm.Start, arm.End
	if hi > lo {
		hi--
	}
	var cands []ExecBlock
	for _, b := range blocks {
		if b.StartLine >= lo && b.StartLine <= hi {
			cands = append(cands, b)
		}
	}
	if len(cands) == 0 {
		return 0, false
	}
	if arm.Chained {
		// An `else if` arm's body is another if statement, whose condition
		// carries its own counter. Any counter inside the chain proves the
		// arm was entered — the nested condition may have been false with no
		// nested else, leaving the inner body at zero.
		return maxCount(cands), true
	}
	first := cands[0].StartLine
	for _, b := range cands {
		if b.StartLine < first {
			first = b.StartLine
		}
	}
	var extends, sameLine []ExecBlock
	for _, b := range cands {
		if b.StartLine != first {
			continue
		}
		if b.EndLine > b.StartLine {
			extends = append(extends, b)
		} else {
			sameLine = append(sameLine, b)
		}
	}
	if len(extends) > 0 {
		return maxCount(extends), true
	}
	return maxCount(sameLine), true
}

func maxCount(bs []ExecBlock) int {
	n := 0
	for _, b := range bs {
		if b.Count > n {
			n = b.Count
		}
	}
	return n
}

// blockAt returns the tightest block containing a line — the counter that
// belongs to the statement there, rather than an enclosing span that happens
// to cover it.
func blockAt(blocks []ExecBlock, line int) (ExecBlock, bool) {
	var best ExecBlock
	found := false
	for _, b := range blocks {
		if line < b.StartLine || line > b.EndLine {
			continue
		}
		if !found || (b.EndLine-b.StartLine) < (best.EndLine-best.StartLine) {
			best, found = b, true
		}
	}
	return best, found
}

// independentConditions counts the atomic conditions that could be varied on
// their own. A condition repeated inside one decision (`a && (b || a)`) is
// coupled: no test case can flip one occurrence without flipping the other,
// so MC/DC for it is unreachable by construction — a fact about the code, not
// about the tests, and one worth surfacing before anyone tries.
func independentConditions(g *Graph) int {
	total := 0
	for _, d := range g.Decisions {
		seen := map[string]int{}
		for _, c := range d.Conditions {
			seen[c.Text]++
		}
		for _, c := range d.Conditions {
			if seen[c.Text] == 1 {
				total++
			}
		}
	}
	return total
}
