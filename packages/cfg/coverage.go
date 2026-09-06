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
	ReasonNotAllExit   = "a clause falls through to the statement after the switch, so that statement does not witness the no-match outcome"
	ReasonNoProfile    = "no coverage profile was supplied"
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

// ArmResult is one decision outcome's verdict.
type ArmResult struct {
	Decision int          `json:"decision"`
	Kind     DecisionKind `json:"kind"`
	Line     int          `json:"line"`
	Label    string       `json:"label"`
	// Decidable reports whether the profile can answer at all. When false,
	// Taken is meaningless and must not be rendered as "not taken".
	Decidable bool   `json:"decidable"`
	Taken     bool   `json:"taken"`
	Reason    string `json:"reason"`
}

func (r ArmResult) String() string {
	verdict := "UNDECIDABLE"
	if r.Decidable {
		verdict = "not taken"
		if r.Taken {
			verdict = "taken"
		}
	}
	return fmt.Sprintf("line %d %s[%s]: %s (%s)", r.Line, r.Kind, r.Label, verdict, r.Reason)
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
	for i := range g.Decisions {
		d := &g.Decisions[i]
		for j, arm := range d.Arms {
			r := judge(d, j, arm, blocks)
			a.Arms = append(a.Arms, r)
			a.OutcomesTotal++
			if r.Decidable {
				a.OutcomesDecidable++
				if r.Taken {
					a.OutcomesTaken++
				}
			}
		}
	}
	return a
}

// judge decides one arm. The split by kind is the honest part of this
// package: each construct leaves a different amount of evidence behind.
func judge(d *Decision, armIdx int, arm Arm, blocks []ExecBlock) ArmResult {
	r := ArmResult{Decision: d.Index, Kind: d.Kind, Line: d.Line, Label: arm.Label}
	switch {
	case d.Kind == DecisionAnd || d.Kind == DecisionOr:
		r.Reason = ReasonShortCircuit
		return r
	case len(blocks) == 0:
		r.Reason = ReasonNoProfile
		return r
	case arm.Start > 0:
		// The arm has a body, so it has a counter of its own: the strongest
		// evidence available, and it holds inside loops too.
		count, ok := entryCount(blocks, arm)
		if !ok {
			r.Reason = ReasonNoCounter
			return r
		}
		r.Decidable, r.Taken, r.Reason = true, count > 0, ReasonOwnCounter
		return r
	}
	return judgeBodilessArm(d, armIdx, r, blocks)
}

// judgeBodilessArm handles the outcomes that execute no statement of their
// own: the false arm of an `if` with no `else`, a loop's exit, and the
// "nothing matched" arm of a switch with no default. All three are witnessed
// only by the statement that follows the whole construct — when one exists,
// and when the construct is not inside a loop that scrambles the counts.
func judgeBodilessArm(d *Decision, armIdx int, r ArmResult, blocks []ExecBlock) ArmResult {
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
	succ, ok := blockAt(blocks, d.SuccessorLine)
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
		r.Decidable, r.Taken, r.Reason = true, succ.Count > 0, ReasonSuccessor
		return r
	case DecisionSwitch, DecisionTypeSwitch:
		return judgeImplicitDefault(d, r, succ)
	}
	return judgeIfFalseArm(d, armIdx, r, succ, blocks)
}

// judgeImplicitDefault handles the "no case matched" arm of a switch with no
// default. It is knowable only when every clause leaves the function (or
// breaks out): otherwise a matched clause also reaches the successor and the
// two are indistinguishable.
func judgeImplicitDefault(d *Decision, r ArmResult, succ ExecBlock) ArmResult {
	for _, arm := range d.Arms {
		if arm.Start > 0 && !arm.Terminates {
			r.Reason = ReasonNotAllExit
			return r
		}
	}
	r.Decidable, r.Taken, r.Reason = true, succ.Count > 0, ReasonSuccessor
	return r
}

// judgeIfFalseArm is the inference the issue spells out: with no else, the
// false outcome is visible only in the count of the statement after the if.
//
//   - When the then arm RETURNS (or otherwise leaves), the successor is
//     reached only on the false path: any count there proves it was taken.
//   - When the then arm falls through, the successor's count is the sum of
//     both paths, so the false path was taken exactly when it EXCEEDS the
//     then arm's count.
func judgeIfFalseArm(d *Decision, armIdx int, r ArmResult, succ ExecBlock, blocks []ExecBlock) ArmResult {
	if armIdx == 0 || len(d.Arms) == 0 {
		r.Reason = ReasonNoCounter
		return r
	}
	thenArm := d.Arms[0]
	if thenArm.Start == 0 {
		// Not the shape this function reasons about (an if whose THEN arm has
		// no body cannot occur in Go, so this is defensive).
		r.Reason = ReasonNoCounter
		return r
	}
	if thenArm.Terminates {
		r.Decidable, r.Taken, r.Reason = true, succ.Count > 0, ReasonSuccessor
		return r
	}
	thenCount, ok := entryCount(blocks, thenArm)
	if !ok {
		r.Reason = ReasonNoCounter
		return r
	}
	r.Decidable, r.Taken, r.Reason = true, succ.Count > thenCount, ReasonSuccessor
	return r
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
