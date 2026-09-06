package doctor

import (
	"context"
	"fmt"
	"testing"
)

// stubCheck lets the aggregation tests exercise Run without needing a
// store in any particular state.
type stubCheck struct {
	name string
	res  Result
	err  error
}

func (s stubCheck) Name() string     { return s.name }
func (s stubCheck) Examines() string { return "a stub" }
func (s stubCheck) Run(context.Context, *Env) (Result, error) {
	return s.res, s.err
}

// The report is the whole point: a doctor that prints only failures
// leaves the user unable to tell "checked and fine" from "never looked".
func TestRun_ReportsEveryCheckIncludingThePassingOnes(t *testing.T) {
	f := newFixture(t)
	env := &Env{Store: f.store, DBPath: f.dbPath, Root: f.root}

	rep, err := Run(context.Background(), env, DefaultChecks())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Checks) != len(DefaultChecks()) {
		t.Fatalf("reported %d checks, want %d", len(rep.Checks), len(DefaultChecks()))
	}
	for _, c := range rep.Checks {
		if c.Name == "" || c.Examines == "" {
			t.Errorf("check %+v is missing its identity", c)
		}
		if c.Severity == "" {
			t.Errorf("check %q returned no severity", c.Name)
		}
		if c.Finding == "" {
			t.Errorf("check %q returned no finding", c.Name)
		}
	}
}

// A check that blows up must not be silently absent from the report, and
// must not read as healthy: atlas failing to verify its own state is
// indistinguishable, from the user's seat, from a broken state.
func TestRun_ErroredCheckIsReportedAsFail(t *testing.T) {
	env := &Env{Root: t.TempDir()}
	rep, err := Run(context.Background(), env, []Check{
		stubCheck{name: "boom", err: fmt.Errorf("disk on fire")},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Checks) != 1 {
		t.Fatalf("reported %d checks, want 1", len(rep.Checks))
	}
	if rep.Checks[0].Severity != SeverityFail {
		t.Errorf("severity = %q, want fail", rep.Checks[0].Severity)
	}
	if rep.Worst != SeverityFail {
		t.Errorf("worst = %q, want fail", rep.Worst)
	}
}

// Names and descriptions come from the Check, so a Result cannot mislabel
// itself on one branch and not another.
func TestRun_StampsNameAndExaminesFromTheCheck(t *testing.T) {
	rep, err := Run(context.Background(), &Env{Root: t.TempDir()}, []Check{
		stubCheck{name: "x.y", res: Result{
			Name: "lying", Examines: "lying", Severity: SeverityOK, Finding: "fine",
		}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Checks[0].Name != "x.y" || rep.Checks[0].Examines != "a stub" {
		t.Errorf("got %q / %q, want \"x.y\" / \"a stub\"", rep.Checks[0].Name, rep.Checks[0].Examines)
	}
}

func TestRun_CountsAndWorst(t *testing.T) {
	rep, err := Run(context.Background(), &Env{Root: t.TempDir()}, []Check{
		stubCheck{name: "a", res: Result{Severity: SeverityOK, Finding: "."}},
		stubCheck{name: "b", res: Result{Severity: SeverityNotApplicable, Finding: "."}},
		stubCheck{name: "c", res: Result{Severity: SeverityWarn, Finding: "."}},
		stubCheck{name: "d", res: Result{Severity: SeverityOK, Finding: "."}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Counts{OK: 2, Warn: 1, Fail: 0, NotApplicable: 1}
	if rep.Counts != want {
		t.Errorf("counts = %+v, want %+v", rep.Counts, want)
	}
	if rep.Worst != SeverityWarn {
		t.Errorf("worst = %q, want warn", rep.Worst)
	}
}

func TestReport_TrippedBy(t *testing.T) {
	cases := []struct {
		worst     Severity
		threshold Severity
		want      bool
	}{
		{SeverityOK, SeverityFail, false},
		{SeverityOK, SeverityWarn, false},
		{SeverityWarn, SeverityFail, false},
		{SeverityWarn, SeverityWarn, true},
		{SeverityFail, SeverityFail, true},
		{SeverityFail, SeverityWarn, true},
		// "n/a" is a normal state for a repo mid-adoption. A gate that
		// fired on it would be switched off within the week.
		{SeverityNotApplicable, SeverityWarn, false},
		{SeverityNotApplicable, SeverityFail, false},
	}
	for _, c := range cases {
		got := Report{Worst: c.worst}.TrippedBy(c.threshold)
		if got != c.want {
			t.Errorf("worst=%q threshold=%q: TrippedBy = %v, want %v",
				c.worst, c.threshold, got, c.want)
		}
	}
}

func TestParseSeverity(t *testing.T) {
	if _, err := ParseSeverity("warn"); err != nil {
		t.Errorf("warn: %v", err)
	}
	if _, err := ParseSeverity("fail"); err != nil {
		t.Errorf("fail: %v", err)
	}
	for _, bad := range []string{"ok", "n/a", "", "FAIL", "error"} {
		if _, err := ParseSeverity(bad); err == nil {
			t.Errorf("ParseSeverity(%q) = nil error, want rejection", bad)
		}
	}
}

func TestRun_NilEnv(t *testing.T) {
	if _, err := Run(context.Background(), nil, DefaultChecks()); err == nil {
		t.Error("Run(nil env) = nil error, want rejection")
	}
}

// Env.withDefaults must not reach back into the caller's value: the CLI
// builds one Env and Run is free to be called twice with it.
func TestEnv_WithDefaultsDoesNotMutateTheCaller(t *testing.T) {
	orig := &Env{}
	_ = orig.withDefaults()
	if orig.Now != nil || orig.CoverageMaxAge != 0 {
		t.Error("withDefaults mutated the caller's Env")
	}
}
