package cfg_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/sosalejandro/atlas/packages/cfg"
)

// fixture parses the golden fixture once and returns the graphs by function
// name. Parsing is cheap and the map keeps each test's intent on one line.
func fixture(t *testing.T) map[string]*cfg.Graph {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*cfg.Graph{}
	for _, name := range []string{"fixture.go", "queries.go"} {
		path := filepath.Join("testdata", "flowfixture", name)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			g, err := cfg.Build(fset, fn)
			if err != nil {
				t.Fatalf("Build %s: %v", fn.Name.Name, err)
			}
			out[fn.Name.Name] = g
		}
	}
	return out
}

func decisionKinds(g *cfg.Graph) []cfg.DecisionKind {
	out := make([]cfg.DecisionKind, 0, len(g.Decisions))
	for _, d := range g.Decisions {
		out = append(out, d.Kind)
	}
	return out
}

// TestBuild_DecisionsPerConstruct is the golden assertion: every construct
// the issue names produces the decisions it should, and — the part hand-rolled
// builders get wrong — && and || each produce one of their own.
func TestBuild_DecisionsPerConstruct(t *testing.T) {
	graphs := fixture(t)

	cases := []struct {
		fn    string
		kinds []cfg.DecisionKind
	}{
		// `n > 0 && ok` is two decisions: the if, and the && itself.
		{"Classify", []cfg.DecisionKind{cfg.DecisionAnd, cfg.DecisionIf, cfg.DecisionIf}},
		{"Guard", []cfg.DecisionKind{cfg.DecisionIf}},
		{"Sum", []cfg.DecisionKind{cfg.DecisionRange, cfg.DecisionIf}},
		{"Label", []cfg.DecisionKind{cfg.DecisionSwitch}},
		{"Kindly", []cfg.DecisionKind{cfg.DecisionTypeSwitch}},
		{"Await", []cfg.DecisionKind{cfg.DecisionSelect}},
		{"Retry", []cfg.DecisionKind{cfg.DecisionFor}},
		{"Either", []cfg.DecisionKind{cfg.DecisionOr, cfg.DecisionIf, cfg.DecisionFor}},
	}
	for _, tc := range cases {
		g, ok := graphs[tc.fn]
		if !ok {
			t.Fatalf("fixture has no func %s", tc.fn)
		}
		got := decisionKinds(g)
		if len(got) != len(tc.kinds) {
			t.Fatalf("%s: decision kinds = %v, want %v", tc.fn, got, tc.kinds)
		}
		for i := range got {
			if got[i] != tc.kinds[i] {
				t.Fatalf("%s: decision kinds = %v, want %v", tc.fn, got, tc.kinds)
			}
		}
	}
}

// TestBuild_Complexity pins the numbers the backlog ranking (#93) will weight
// by. Each is 1 + (arms beyond the first), hand-worked from the fixture.
func TestBuild_Complexity(t *testing.T) {
	graphs := fixture(t)
	want := map[string]int{
		"Classify": 4, // two ifs + one &&
		"Guard":    2,
		"Sum":      3, // range + if
		"Label":    3, // three case arms
		"Kindly":   3, // two case arms + the unobservable "no case matched"
		"Await":    2, // two comm clauses
		"Retry":    2,
		"Both":     2, // one && outside condition position
		"Either":   3, // if + ||; the bare for adds no arm to choose between
	}
	for fn, w := range want {
		g, ok := graphs[fn]
		if !ok {
			t.Fatalf("fixture has no func %s", fn)
		}
		if got := g.Complexity(); got != w {
			t.Errorf("%s: complexity = %d, want %d (decisions=%d arms=%d)",
				fn, got, w, len(g.Decisions), g.BranchArms())
		}
	}
}

// TestBuild_ComplexityMatchesEuler cross-checks the decision-derived
// complexity against E - N + 2 on the graph itself. If the two ever disagree
// the builder has dropped an edge or invented an arm, and both numbers are
// then untrustworthy — which is exactly the failure a "complexity" column
// nobody verifies would hide.
func TestBuild_ComplexityMatchesEuler(t *testing.T) {
	graphs := fixture(t)
	for _, fn := range []string{
		"Classify", "Guard", "Label", "Kindly", "Await", "Retry", "Either", "Sum", "Both",
	} {
		g := graphs[fn]
		euler := len(g.Edges) - len(g.Blocks) + 2
		if got := g.Complexity(); got != euler {
			t.Errorf("%s: complexity = %d but E-N+2 = %d (edges=%d blocks=%d)",
				fn, got, euler, len(g.Edges), len(g.Blocks))
		}
	}
}

// TestBuild_Conditions checks the enumeration MC/DC would need: the atomic
// operands, with parentheses and negation stripped.
func TestBuild_Conditions(t *testing.T) {
	graphs := fixture(t)
	g := graphs["Classify"]
	var texts []string
	for _, d := range g.Decisions {
		if d.Kind != cfg.DecisionIf {
			continue
		}
		for _, c := range d.Conditions {
			texts = append(texts, c.Text)
		}
	}
	// The outer if's condition decomposes into two conditions; the else-if
	// contributes one.
	want := []string{"n > 0", "ok", "n < 0"}
	if len(texts) != len(want) {
		t.Fatalf("conditions = %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("conditions = %v, want %v", texts, want)
		}
	}
}

// TestBuild_DeferIsNotABranch pins the modelling choice: a defer is recorded,
// never wired as an arm. A deferred call runs on every exit path, so an edge
// for it would inflate complexity for something that is not a decision.
func TestBuild_DeferIsNotABranch(t *testing.T) {
	g := fixture(t)["Await"]
	if g.Defers != 1 {
		t.Errorf("Defers = %d, want 1", g.Defers)
	}
	for _, d := range g.Decisions {
		if d.Kind != cfg.DecisionSelect {
			t.Errorf("unexpected decision from defer: %+v", d)
		}
	}
}

// TestBuild_EarlyReturnReachesExit: a return edges to the single exit node
// and leaves the code after it unreachable in the `Unreachable` fixture.
func TestBuild_EarlyReturnReachesExit(t *testing.T) {
	graphs := fixture(t)
	g := graphs["Unreachable"]
	got, sound := g.Unreachable()
	if !sound {
		t.Fatal("Unreachable: the fixture has no goto, so the answer must be sound")
	}
	if len(got) != 1 {
		t.Fatalf("Unreachable() = %v, want exactly one block", got)
	}
	sum := graphs["Sum"]
	if got, sound := sum.Unreachable(); !sound || len(got) != 0 {
		t.Errorf("Sum: Unreachable() = %v (sound=%v), want none and sound", got, sound)
	}
}

// TestBuild_UnreachableDeclinesOverAGotoGraph is the refusal that keeps
// `flow.unreachable` honest. The builder does not draw goto edges, so a label
// reached only by a goto has no incoming edge in the graph — and reporting it
// as dead code would be a confident wrong answer over a graph known to be
// incomplete. "Cannot determine" is the correct answer and is always
// available.
func TestBuild_UnreachableDeclinesOverAGotoGraph(t *testing.T) {
	g := fixture(t)["Jumping"]
	if !g.HasGoto {
		t.Fatal("Jumping contains a goto; the graph must record that it is not modelled")
	}
	blocks, sound := g.Unreachable()
	if sound {
		t.Error("the unreachable analysis must decline over a graph missing goto edges")
	}
	if len(blocks) != 0 {
		t.Errorf("a declined analysis must claim nothing, got blocks %v", blocks)
	}
}

// TestBuild_PanicLeavesTheFunction: `panic` ends a path exactly as `return`
// does. Without the exit edge, an arm ending in a panic looks like it falls
// through to the statement after the branch, and the coverage analysis would
// difference two counts that never both happened.
func TestBuild_PanicLeavesTheFunction(t *testing.T) {
	g := parseFuncSource(t, `package p
func f(bad bool) int {
	if bad {
		panic("no")
	}
	return 1
}`)
	if len(g.Decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(g.Decisions))
	}
	if !g.Decisions[0].Arms[0].Terminates {
		t.Error("an arm whose only statement is a panic does not fall through to the successor")
	}
}

// TestBuild_InLoopFlagged: the if inside Sum's range body must be marked, or
// the decision-coverage analysis will difference counts that accumulate
// across iterations and report a false verdict.
func TestBuild_InLoopFlagged(t *testing.T) {
	g := fixture(t)["Sum"]
	for _, d := range g.Decisions {
		if d.Kind == cfg.DecisionIf && !d.InLoop {
			t.Errorf("if at line %d not marked InLoop", d.Line)
		}
		if d.Kind == cfg.DecisionRange && d.InLoop {
			t.Errorf("the range itself must not be marked InLoop")
		}
	}
}

// TestBuild_LoopBackEdge: a loop header must be re-entered by an edge marked
// as such, or a renderer draws a straight line where the code cycles.
func TestBuild_LoopBackEdge(t *testing.T) {
	// Either is deliberately absent: its `for {}` body always breaks, so
	// there IS no back edge and asserting one would be asserting a lie.
	for _, fn := range []string{"Sum", "Retry"} {
		g := fixture(t)[fn]
		found := false
		for _, e := range g.Edges {
			if e.Kind == cfg.EdgeLoopBack {
				found = true
				if g.Blocks[e.To].Kind != cfg.BlockLoop {
					t.Errorf("%s: loop-back edge targets a %s block", fn, g.Blocks[e.To].Kind)
				}
			}
		}
		if !found {
			t.Errorf("%s: no loop-back edge", fn)
		}
	}
}

// TestBuild_ArmSpansAbsentWhereNoCounterExists is the honesty pin: the false
// arm of an if with no else, and both arms of a short-circuit operator, have
// no source span — because statement coverage records no counter for them.
func TestBuild_ArmSpansAbsentWhereNoCounterExists(t *testing.T) {
	graphs := fixture(t)

	guard := graphs["Guard"]
	if len(guard.Decisions) != 1 {
		t.Fatalf("Guard: decisions = %d, want 1", len(guard.Decisions))
	}
	arms := guard.Decisions[0].Arms
	if len(arms) != 2 {
		t.Fatalf("Guard: arms = %d, want 2", len(arms))
	}
	if arms[0].Start == 0 {
		t.Error("Guard: the then arm must carry its body span")
	}
	if !arms[0].Terminates {
		t.Error("Guard: the then arm returns, so it must be marked Terminates")
	}
	if arms[1].Start != 0 {
		t.Errorf("Guard: the false arm has no body, want zero span, got %d", arms[1].Start)
	}
	if guard.Decisions[0].SuccessorLine == 0 {
		t.Error("Guard: the statement after the if must be recorded as the successor")
	}

	for _, d := range graphs["Classify"].Decisions {
		if d.Kind != cfg.DecisionAnd {
			continue
		}
		for _, a := range d.Arms {
			if a.Start != 0 {
				t.Errorf("&&: arm %q claims a span (%d); no counter exists for it", a.Label, a.Start)
			}
		}
	}
}

// parseFuncSource parses a one-function source snippet and returns its graph.
// Used where a fixture file would be more ceremony than the case is worth.
func parseFuncSource(t *testing.T, src string) *cfg.Graph {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		g, err := cfg.Build(fset, fn)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return g
	}
	t.Fatal("snippet has no function")
	return nil
}
