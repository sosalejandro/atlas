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
	f, err := os.Open(filepath.Join("testdata", "flowfixture", "fixture.cover.out"))
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
// numbers worked out by hand from fixture.cover.out.
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
			if r.Decidable {
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
			if r.Decidable {
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
