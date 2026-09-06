package cfg_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/cfg"
	"github.com/sosalejandro/atlas/packages/coverage/gocover"
)

// profileBlocks loads the real `go test -covermode=count` profile committed
// beside the fixture. It is a REAL profile, not a hand-written one, because
// the whole analysis turns on where Go's instrumentation actually puts its
// counters — an invented profile would only test the invention.
func profileBlocks(t *testing.T, file string) []cfg.ExecBlock {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "flowfixture", "fixture.coverprofile"))
	if err != nil {
		t.Fatalf("open profile: %v", err)
	}
	defer f.Close()
	blocks, err := gocover.Parse(f)
	if err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	return cfg.ExecBlocksForFile(blocks, file)
}

func analyze(t *testing.T, fn string) cfg.Analysis {
	t.Helper()
	g, ok := fixture(t)[fn]
	if !ok {
		t.Fatalf("fixture has no func %s", fn)
	}
	return cfg.Analyze(g, profileBlocks(t, g.File))
}

// TestAnalyze_HandWorkedFixture is the acceptance criterion: decision
// coverage computed per symbol from an existing coverprofile, checked against
// numbers worked out by hand from fixture.coverprofile.
//
// The three columns are deliberately separate. `decidable` is how many
// outcomes statement coverage can judge AT ALL; `taken` is how many of those
// were taken. Dividing taken by TOTAL instead would silently charge a symbol
// for outcomes no instrumentation could ever have observed.
func TestAnalyze_HandWorkedFixture(t *testing.T) {
	cases := []struct {
		fn                      string
		total, decidable, taken int
		note                    string
	}{
		// if(2) + else-if(2) + &&(2, never decidable). The outer true arm ran;
		// the outer false arm did not (the else-if's own counter is 0); the
		// inner then arm did not, and its false arm is knowable because the
		// `return "zero"` after it has a counter, which read 0.
		{"Classify", 6, 4, 1, "&& outcomes are never observable"},
		// The then arm never ran; the false arm is proved by the statement
		// after the if having run.
		{"Guard", 2, 2, 1, "false arm inferred from the successor block"},
		// range(entered, exhausted) both taken; the inner if's then arm did
		// not run and its false arm is REFUSED because the if is in a loop.
		{"Sum", 4, 3, 2, "in-loop false arm refused"},
		{"Label", 3, 3, 1, "every clause has a counter"},
		// Two clauses plus the "no case matched" arm, which is knowable only
		// because every clause returns.
		{"Kindly", 3, 3, 1, "implicit default inferred from the successor"},
		{"Await", 2, 2, 1, "select clauses have counters"},
		{"Retry", 2, 2, 2, "fully covered"},
		// ||(2, never decidable) + if(2) + the bare for's single arm.
		{"Either", 5, 3, 2, "|| outcomes are never observable"},
		// The only decision is a short-circuit operator, so NOTHING about
		// this function's branching is observable.
		{"Both", 2, 0, 0, "no observable outcome at all"},
		// The then arm ran on every call and fell through, so the successor's
		// count EQUALS it and the false outcome was never taken.
		{"AlwaysThen", 2, 2, 1, "successor count equals the then count"},
		// The outer if's false arm is undetermined: the then arm reaches the
		// successor on some runs and leaves the function on others.
		{"Escapes", 4, 3, 3, "a nested return escapes the then arm"},
		{"Panics", 4, 3, 2, "a nested panic escapes the then arm"},
		// The clause `break`s out, which reaches the successor exactly as a
		// non-matching value would.
		{"Breaking", 2, 1, 1, "a breaking clause witnesses no implicit default"},
		// A goto the graph does not model is a way to reach the successor the
		// analysis cannot account for.
		{"Jumping", 2, 1, 0, "an unmodelled goto blocks the successor inference"},
	}
	for _, tc := range cases {
		a := analyze(t, tc.fn)
		if a.OutcomesTotal != tc.total || a.OutcomesDecidable != tc.decidable || a.OutcomesTaken != tc.taken {
			t.Errorf("%s (%s): total/decidable/taken = %d/%d/%d, want %d/%d/%d\n%s",
				tc.fn, tc.note, a.OutcomesTotal, a.OutcomesDecidable, a.OutcomesTaken,
				tc.total, tc.decidable, tc.taken, dumpArms(a))
		}
	}
}

func dumpArms(a cfg.Analysis) string {
	s := ""
	for _, r := range a.Arms {
		s += "    " + r.String() + "\n"
	}
	return s
}

// TestAnalyze_RefusesInsideLoop pins the refusal itself, with its reason, so
// a future "simplification" that starts differencing counts inside a loop
// fails loudly rather than quietly reporting a coverage number that is wrong.
func TestAnalyze_RefusesInsideLoop(t *testing.T) {
	a := analyze(t, "Sum")
	found := false
	for _, r := range a.Arms {
		if r.Kind == cfg.DecisionIf && r.Label == "false" {
			found = true
			if r.Decidable() {
				t.Errorf("the false arm of an if inside a loop must not be decidable: %s", r)
			}
			if r.Reason != cfg.ReasonInLoop {
				t.Errorf("reason = %q, want %q", r.Reason, cfg.ReasonInLoop)
			}
		}
	}
	if !found {
		t.Fatal("no if/false arm in Sum")
	}
}

// TestAnalyze_ShortCircuitNeverDecidable is the MC/DC honesty pin at the
// outcome level: the operands of && and || share one counter, so neither
// outcome is ever reported as taken or not taken.
func TestAnalyze_ShortCircuitNeverDecidable(t *testing.T) {
	for _, fn := range []string{"Classify", "Either", "Both"} {
		a := analyze(t, fn)
		n := 0
		for _, r := range a.Arms {
			if r.Kind != cfg.DecisionAnd && r.Kind != cfg.DecisionOr {
				continue
			}
			n++
			if r.Decidable() {
				t.Errorf("%s: short-circuit arm reported as decidable: %s", fn, r)
			}
			if r.Reason != cfg.ReasonShortCircuit {
				t.Errorf("%s: reason = %q, want %q", fn, r.Reason, cfg.ReasonShortCircuit)
			}
		}
		if n == 0 {
			t.Errorf("%s: expected short-circuit arms", fn)
		}
	}
}

// TestAnalyze_CoverageIsOverDecidableOnly: the ratio never uses outcomes the
// profile cannot judge as its denominator, and it reports "unavailable"
// rather than 0% when nothing is judgeable.
func TestAnalyze_CoverageIsOverDecidableOnly(t *testing.T) {
	a := analyze(t, "Classify")
	got, ok := a.DecisionCoverage()
	if !ok {
		t.Fatal("Classify: coverage should be available")
	}
	if want := 25.0; got != want {
		t.Errorf("Classify: decision coverage = %.1f, want %.1f (1 of 4 decidable)", got, want)
	}

	b := analyze(t, "Both")
	if _, ok := b.DecisionCoverage(); ok {
		t.Error("Both: coverage must be reported as unavailable, not as 0%")
	}
}

// TestAnalyze_MCDCFeasibility: the condition enumeration is reported as a
// property of the source, and the caveat that it is not an MC/DC verdict
// travels with it as a constant no renderer can water down.
func TestAnalyze_MCDCFeasibility(t *testing.T) {
	a := analyze(t, "Classify")
	// n > 0, ok, n < 0 — three atomic conditions, none repeated within its
	// decision, so all three are independently exercisable in principle.
	if a.Conditions != 3 {
		t.Errorf("conditions = %d, want 3", a.Conditions)
	}
	if a.ConditionsIndependent != 3 {
		t.Errorf("independent conditions = %d, want 3", a.ConditionsIndependent)
	}
	if cfg.MCDCNotDerivable == "" {
		t.Error("the MC/DC caveat string must not be empty; every surface prints it")
	}
}

// TestAnalyze_CoupledConditionsAreNotIndependent: a condition repeated inside
// one decision cannot be varied on its own, so it does not count toward the
// feasible set.
func TestAnalyze_CoupledConditionsAreNotIndependent(t *testing.T) {
	g := parseFuncSource(t, `package p
func f(a, b bool) bool {
	if a && (b || a) {
		return true
	}
	return false
}`)
	a := cfg.Analyze(g, nil)
	if a.Conditions != 3 {
		t.Fatalf("conditions = %d, want 3", a.Conditions)
	}
	if a.ConditionsIndependent != 1 {
		t.Errorf("independent = %d, want 1 (only b; a appears twice)", a.ConditionsIndependent)
	}
}

// TestAnalyze_NoProfileIsNotZeroCoverage: analysing with no execution data at
// all must report every outcome as unjudged, never as untaken.
func TestAnalyze_NoProfileIsNotZeroCoverage(t *testing.T) {
	g := fixture(t)["Guard"]
	a := cfg.Analyze(g, nil)
	if a.OutcomesDecidable != 0 {
		t.Errorf("decidable = %d with no profile, want 0", a.OutcomesDecidable)
	}
	if _, ok := a.DecisionCoverage(); ok {
		t.Error("coverage must be unavailable with no profile")
	}
}

// armVerdict returns the verdict and reason for one decision outcome, found by
// the line the decision sits on and the arm's label.
func armVerdict(t *testing.T, a cfg.Analysis, line int, label string) cfg.ArmResult {
	t.Helper()
	for _, r := range a.Arms {
		if r.Line == line && r.Label == label {
			return r
		}
	}
	t.Fatalf("no arm %q at line %d in:\n%s", label, line, dumpArms(a))
	return cfg.ArmResult{}
}

func decisionLine(t *testing.T, fn string, kind cfg.DecisionKind) int {
	t.Helper()
	g, ok := fixture(t)[fn]
	if !ok {
		t.Fatalf("fixture has no func %s", fn)
	}
	for _, d := range g.Decisions {
		if d.Kind == kind {
			return d.Line
		}
	}
	t.Fatalf("%s has no %s decision", fn, kind)
	return 0
}

// TestAnalyze_DifferencingNeedsTheThenCount is the discriminating test for the
// successor-difference rule. AlwaysThen's then-arm runs on EVERY call and falls
// through, so the statement after the if has exactly the then-arm's count.
//
// This is the one shape where `succ.Count > thenCount` (correct: the false arm
// was never taken) and `succ.Count > 0` (wrong: reports it as taken) disagree.
// Every other fixture in this file passes under both. Replace the comparison
// in judgeIfFalseArm with `succ.Count > 0` and this test must fail.
func TestAnalyze_DifferencingNeedsTheThenCount(t *testing.T) {
	a := analyze(t, "AlwaysThen")
	line := decisionLine(t, "AlwaysThen", cfg.DecisionIf)

	if got := armVerdict(t, a, line, "true"); got.Verdict != cfg.VerdictTaken {
		t.Errorf("the then arm ran on every call: %s", got)
	}
	got := armVerdict(t, a, line, "false")
	if got.Verdict != cfg.VerdictNotTaken {
		t.Errorf("the false arm was never taken (the successor's count equals the then arm's): %s", got)
	}
	if got.Reason != cfg.ReasonSuccessor {
		t.Errorf("reason = %q, want %q", got.Reason, cfg.ReasonSuccessor)
	}
	if a.OutcomesTaken != 1 {
		t.Errorf("taken = %d, want 1; reading the successor's count as proof on its own gives 2:\n%s",
			a.OutcomesTaken, dumpArms(a))
	}
}

// TestAnalyze_ArmThatSometimesEscapesIsUndetermined: the then arm of Escapes
// falls through at its END -- Terminates is false -- but a nested guard leaves
// the function first on some runs. The successor's count is then neither
// "both arms summed" nor "the false arm alone", and the only correct answer is
// UNDETERMINED.
//
// The verdict is asserted alongside Terminates on purpose: it pins that the
// analysis reasons over the CFG and not over that single end-of-arm flag.
func TestAnalyze_ArmThatSometimesEscapesIsUndetermined(t *testing.T) {
	for _, fn := range []string{"Escapes", "Panics"} {
		g := fixture(t)[fn]
		outer := g.Decisions[0]
		if outer.Kind != cfg.DecisionIf {
			t.Fatalf("%s: first decision = %s, want if", fn, outer.Kind)
		}
		if outer.Arms[0].Terminates {
			t.Fatalf("%s: the then arm falls through at its end; the fixture has lost its point", fn)
		}
		got := armVerdict(t, analyze(t, fn), outer.Line, "false")
		if got.Verdict != cfg.VerdictUndetermined {
			t.Errorf("%s: a then arm with an escaping path cannot be differenced: %s", fn, got)
		}
		if got.Reason != cfg.ReasonArmEscapes {
			t.Errorf("%s: reason = %q, want %q", fn, got.Reason, cfg.ReasonArmEscapes)
		}
	}
}

// TestAnalyze_ImplicitDefaultBothShapes pins the condition that used to be
// backwards. A `break` makes a clause unable to fall out of its own body while
// still reaching the statement after the switch -- which is exactly what
// destroys the inference, not what enables it.
func TestAnalyze_ImplicitDefaultBothShapes(t *testing.T) {
	// Shape 1: every clause returns, so nothing but a no-match reaches the
	// successor and its counter is proof.
	kindly := armVerdict(t, analyze(t, "Kindly"),
		decisionLine(t, "Kindly", cfg.DecisionTypeSwitch), "no case matched")
	if !kindly.Decidable() {
		t.Errorf("every clause of Kindly returns, so the no-match arm is knowable: %s", kindly)
	}
	if kindly.Reason != cfg.ReasonSuccessor {
		t.Errorf("Kindly: reason = %q, want %q", kindly.Reason, cfg.ReasonSuccessor)
	}

	// Shape 2: the clause breaks out, and lands on the very same successor.
	breaking := armVerdict(t, analyze(t, "Breaking"),
		decisionLine(t, "Breaking", cfg.DecisionSwitch), "no case matched")
	if breaking.Verdict != cfg.VerdictUndetermined {
		t.Errorf("a clause that breaks out reaches the successor, so the no-match arm is not knowable: %s",
			breaking)
	}
	if breaking.Reason != cfg.ReasonClauseReachesSuccessor {
		t.Errorf("Breaking: reason = %q, want %q", breaking.Reason, cfg.ReasonClauseReachesSuccessor)
	}
	// And the clause that broke out must still look like a terminating clause,
	// or the fixture is not exercising the trap it exists for.
	g := fixture(t)["Breaking"]
	if !g.Decisions[0].Arms[0].Terminates {
		t.Error("Breaking's clause ends in `break`, so Terminates is true; that is the trap")
	}
}

// TestAnalyze_UnmodelledGotoBlocksTheSuccessorInference: every successor-based
// inference assumes the graph names each way the successor can be reached. A
// `goto` the builder did not draw is a way it does not name, so the answer is
// UNDETERMINED rather than a differenced number over an incomplete graph.
func TestAnalyze_UnmodelledGotoBlocksTheSuccessorInference(t *testing.T) {
	got := armVerdict(t, analyze(t, "Jumping"), decisionLine(t, "Jumping", cfg.DecisionIf), "false")
	if got.Verdict != cfg.VerdictUndetermined {
		t.Errorf("a graph missing goto edges cannot witness the false outcome: %s", got)
	}
	if got.Reason != cfg.ReasonUnmodelledGoto {
		t.Errorf("reason = %q, want %q", got.Reason, cfg.ReasonUnmodelledGoto)
	}
}

// TestAnalyze_UndeterminedIsCounted: the third verdict is carried as its own
// number, not left to be recovered by subtraction. A reader who has to compute
// the blind spot is a reader who will forget it exists.
func TestAnalyze_UndeterminedIsCounted(t *testing.T) {
	for _, fn := range []string{"Classify", "Escapes", "Breaking", "Jumping", "Both"} {
		a := analyze(t, fn)
		if want := a.OutcomesTotal - a.OutcomesDecidable; a.OutcomesUndetermined != want {
			t.Errorf("%s: undetermined = %d, want %d (total %d - decidable %d)",
				fn, a.OutcomesUndetermined, want, a.OutcomesTotal, a.OutcomesDecidable)
		}
		for _, r := range a.Arms {
			if r.Verdict == "" {
				t.Errorf("%s: an arm with no verdict is a silent zero: %+v", fn, r)
			}
			if !r.Decidable() && r.Taken() {
				t.Errorf("%s: impossible verdict pair: %s", fn, r)
			}
		}
	}
}
