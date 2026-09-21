package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/sosalejandro/grunnr/packages/doctor"
)

// stubDoctor returns a fixed report, so these tests are about the PROJECTION
// and the loop contract rather than about the checks themselves.
func stubDoctor(rep doctor.Report) DoctorFunc {
	return func(context.Context) (doctor.Report, error) { return rep, nil }
}

// reportOf assembles a Report the way doctor.Run does.
//
// Worst is computed with an explicit precedence table rather than with
// Severity.AtLeast: AtLeast answers "does this trip a GATE set here", and
// returns false for any threshold below warn -- so seeding an accumulator with
// SeverityOK and folding AtLeast over it never updates, and every fixture
// silently claims a clean report. That is how the first draft of this helper
// made two tests assert against the wrong branch.
func reportOf(results ...doctor.Result) doctor.Report {
	rank := map[doctor.Severity]int{
		doctor.SeverityNotApplicable: 0, doctor.SeverityOK: 1,
		doctor.SeverityWarn: 2, doctor.SeverityFail: 3,
	}
	worst := doctor.SeverityOK
	for _, r := range results {
		if rank[r.Severity] > rank[worst] {
			worst = r.Severity
		}
	}
	return doctor.Report{Checks: results, Worst: worst}
}

// The gap #160 names: packages/mcp did not expose doctor at all, so an agent
// driving grunnr over MCP had no way to ask whether the index was trustworthy
// before asking for a number derived from it.
func TestDoctor_IsOnTheSurfaceAndReportsFindings(t *testing.T) {
	r := seedRepo(t)
	ts := &toolset{doctor: stubDoctor(reportOf(doctor.Result{
		Name: "index.freshness", Severity: doctor.SeverityFail,
		Finding: "the index is stale", Remediation: "grunnr scan",
		Fixes: []doctor.Fix{{ID: doctor.FixScan, Argv: []string{"grunnr", "scan"}, MutatesIndex: true}},
	}))}
	_ = r

	got, err := ts.doctorReport(context.Background(), nil)
	if err != nil {
		t.Fatalf("doctorReport: %v", err)
	}
	res, ok := got.(doctorResult)
	if !ok {
		t.Fatalf("doctorReport returned %T, want doctorResult", got)
	}
	if len(res.Checks) != 1 || res.Checks[0].Name != "index.freshness" {
		t.Errorf("checks = %+v, want the freshness finding", res.Checks)
	}
	if !res.Fixable {
		t.Error("fixable = false with a finding that carries a Fix")
	}
}

// A Fix must be executable without parsing English. That is the whole
// difference between this and the Remediation string it sits beside.
func TestDoctor_FixesCarryArgvAndSayWhetherTheyWrite(t *testing.T) {
	ts := &toolset{doctor: stubDoctor(reportOf(doctor.Result{
		Name: "index.freshness", Severity: doctor.SeverityFail,
		Fixes: doctorFixes(t, doctor.FixScan),
	}))}
	res := mustDoctor(t, ts)

	f := res.Checks[0].Fixes[0]
	if len(f.Argv) < 2 || f.Argv[0] != "grunnr" {
		t.Errorf("argv = %v, want a runnable grunnr command", f.Argv)
	}
	if !f.MutatesIndex {
		t.Error("grunnr scan is reported as not mutating the index")
	}
	// The prose and the argv must render from the same declaration, or an
	// agent runs one thing while a reviewer reads another.
	if f.Command() != "grunnr scan" {
		t.Errorf("Command() = %q, want it to match the argv", f.Command())
	}
}

// An empty Fixes is information: this finding is not mechanically fixable.
// An agent that cannot tell will re-run `scan` at a diagnosis no scan moves.
func TestDoctor_UnfixableFindingsSayStopRatherThanRetry(t *testing.T) {
	ts := &toolset{doctor: stubDoctor(reportOf(doctor.Result{
		Name: "store.schema", Severity: doctor.SeverityFail,
		Finding:     "the store was written by a newer grunnr",
		Remediation: "upgrade grunnr to the version that wrote this store",
	}))}
	res := mustDoctor(t, ts)

	if res.Fixable {
		t.Error("fixable = true with no Fix anywhere in the report")
	}
	if !strings.Contains(res.Note, "no command") && !strings.Contains(res.Note, "Do not") {
		t.Errorf("note does not tell the caller to stop: %q", res.Note)
	}
	if strings.Contains(res.Note, "call doctor again") {
		t.Errorf("note invites a retry loop it has no fix to make progress with: %q", res.Note)
	}
}

// The termination contract. Without it an agent burns a session re-running a
// command against a diagnosis that will not move.
func TestDoctor_FixableReportStatesTheStoppingRule(t *testing.T) {
	ts := &toolset{doctor: stubDoctor(reportOf(doctor.Result{
		Name: "index.freshness", Severity: doctor.SeverityFail,
		Fixes: doctorFixes(t, doctor.FixScan),
	}))}
	res := mustDoctor(t, ts)

	for _, must := range []string{"two rounds", "Stop", "fixes"} {
		if !strings.Contains(res.Note, must) {
			t.Errorf("note never says %q: %q", must, res.Note)
		}
	}
}

// A clean report must not be phrased like a fixable one, and must still
// refuse to claim more than doctor checked.
func TestDoctor_CleanReportDoesNotInviteAFixLoop(t *testing.T) {
	ts := &toolset{doctor: stubDoctor(reportOf(doctor.Result{
		Name: "index.freshness", Severity: doctor.SeverityOK, Finding: "the index matches the tree",
	}))}
	res := mustDoctor(t, ts)

	if strings.Contains(res.Note, "Stop after two") {
		t.Errorf("a clean report carries the repair loop: %q", res.Note)
	}
	if res.Fixable {
		t.Error("a clean report reports itself fixable")
	}
}

// No repo context is not a clean bill of health. This is the distinction the
// whole package is built around and the easiest one to lose.
func TestDoctor_WithoutARepoRefusesRatherThanReportingHealthy(t *testing.T) {
	ts := &toolset{doctor: nil}
	got, err := ts.doctorReport(context.Background(), nil)
	if err != nil {
		t.Fatalf("doctorReport: %v", err)
	}
	nd, ok := got.(NoData)
	if !ok {
		t.Fatalf("a server with no repo returned %T, want NoData", got)
	}
	if nd.Reason != ReasonNoRepo {
		t.Errorf("reason = %q, want %q", nd.Reason, ReasonNoRepo)
	}
	if nd.Detail == "" {
		t.Error("NoData carries no detail; the agent cannot tell why it got nothing")
	}
}

// The description is the only thing an agent reads before choosing a tool.
// #163 will measure the behaviour; this pins that the contract is stated.
func TestCatalog_DoctorStatesTheLoopAndTheOrdering(t *testing.T) {
	ts := &toolset{limits: Limits{}.withDefaults()}
	var d string
	for _, tl := range ts.catalog() {
		if tl.Name == "doctor" {
			d = tl.Description
		}
	}
	if d == "" {
		t.Fatal("doctor is not in the catalog")
	}
	for _, must := range []string{"FIRST", "two rounds", "mutates_index", "fixes"} {
		if !strings.Contains(d, must) {
			t.Errorf("doctor description never says %q: %s", must, d)
		}
	}
	// It must be the first tool offered: an agent reading the catalogue
	// top-down should meet "is this trustworthy" before anything returning a
	// number.
	if first := ts.catalog()[0].Name; first != "doctor" {
		t.Errorf("first tool is %q, want doctor", first)
	}
}

func doctorFixes(t *testing.T, id string) []doctor.Fix {
	t.Helper()
	switch id {
	case doctor.FixScan:
		return []doctor.Fix{{ID: doctor.FixScan, Argv: []string{"grunnr", "scan"}, MutatesIndex: true}}
	}
	t.Fatalf("no fixture fix for %q", id)
	return nil
}

func mustDoctor(t *testing.T, ts *toolset) doctorResult {
	t.Helper()
	got, err := ts.doctorReport(context.Background(), nil)
	if err != nil {
		t.Fatalf("doctorReport: %v", err)
	}
	res, ok := got.(doctorResult)
	if !ok {
		t.Fatalf("doctorReport returned %T, want doctorResult", got)
	}
	return res
}
