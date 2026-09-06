package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// carryCovBase pins the seeded runs on a fixed clock so "which build is
// newest" is a property of the fixture, not of when the test ran. Recent
// enough that the default 72h wall-clock bound never rejects a carry.
var carryCovBase = time.Now().UTC().Add(-90 * time.Minute).Truncate(time.Second)

// seedCarryRun writes one coverage run, optionally tagged with a run group,
// measuring the given symbols with statement counts.
func (f *covFixture) seedCarryRun(
	t *testing.T,
	group string,
	offset time.Duration,
	stmts map[int64][2]int,
) int64 {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	finished := carryCovBase.Add(offset)
	run := store.CoverageRun{Framework: store.FrameworkGoTest, StartedAt: finished, FinishedAt: finished}
	if group != "" {
		run.RunGroup = &group
	}
	results := make([]store.CoverageResult, 0, len(stmts))
	for sid, c := range stmts {
		v := sid
		results = append(results, store.CoverageResult{
			SymbolID: &v, Status: store.StatusPass,
			CoveredStmts: c[0], TotalStmts: c[1],
		})
	}
	id, err := s.Coverage().InsertRunWithResults(ctx, run, results)
	if err != nil {
		t.Fatalf("InsertRunWithResults(group=%q): %v", group, err)
	}
	return id
}

// seedCarrySymbol inserts a symbol with a pinned span, which is what the carry
// validity check compares against.
func (f *covFixture) seedCarrySymbol(t *testing.T, name, file string, line, endLine int) int64 {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	end := endLine
	id, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: shared.SymbolID(name), Kind: shared.KindFunc,
		FilePath: file, Line: line, EndLine: &end,
	})
	if err != nil {
		t.Fatalf("Insert symbol %q: %v", name, err)
	}
	return id
}

// The carry window is three flags' worth of behaviour on `cov status`, and
// nothing asserted that any of them existed. A flag that silently disappears
// in a refactor takes the whole feature's inspectability with it.
func TestCovStatus_CarryFlagsWired(t *testing.T) {
	flagsOn := newCovStatusCmd().Flags()
	for _, name := range []string{"carry", "carry-builds", "carry-max-age"} {
		if flagsOn.Lookup(name) == nil {
			t.Errorf("atlas cov status is missing --%s", name)
		}
	}
	if f := flagsOn.Lookup("carry"); f != nil && f.DefValue != "true" {
		t.Errorf("--carry default = %q, want \"true\" -- carrying is the reading that "+
			"cannot silently inflate coverage, so it is what you get without asking", f.DefValue)
	}
	// The window flags default to the zero value and take the package defaults
	// downstream, so the CLI has exactly one place where the default lives.
	if f := flagsOn.Lookup("carry-builds"); f != nil && f.DefValue != "0" {
		t.Errorf("--carry-builds default = %q, want \"0\"", f.DefValue)
	}
	if f := flagsOn.Lookup("carry-max-age"); f != nil && f.DefValue != "0s" {
		t.Errorf("--carry-max-age default = %q, want \"0s\"", f.DefValue)
	}
}

// --carry-builds has to reach the resolution, not just parse. A window of one
// build makes the two-builds-back source stale, which is visible in the JSON.
func TestCovStatus_CarryBuildsFlagNarrowsTheWindow(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := fix.seedCarrySymbol(t, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	fix.seedCarryRun(t, "build-1", 0, map[int64][2]int{goSym: {5, 100}})
	fix.seedCarryRun(t, "build-2", 10*time.Minute, map[int64][2]int{feSym: {10, 10}})
	fix.seedCarryRun(t, "build-3", 20*time.Minute, map[int64][2]int{feSym: {10, 10}})

	wide := decodeCarryJSON(t, fix)
	if wide.Evidence != 1 || wide.DenominatorOnly != 0 {
		t.Fatalf("default window: evidence=%d denominator_only=%d, want 1/0",
			wide.Evidence, wide.DenominatorOnly)
	}
	narrow := decodeCarryJSON(t, fix, "--carry-builds", "1")
	if narrow.MaxBuilds != 1 {
		t.Errorf("--carry-builds=1 reported max_builds=%d, want 1", narrow.MaxBuilds)
	}
	if narrow.Evidence != 0 || narrow.DenominatorOnly != 1 {
		t.Errorf("--carry-builds=1: evidence=%d denominator_only=%d, want 0/1 -- "+
			"two builds back is outside a one-build window", narrow.Evidence, narrow.DenominatorOnly)
	}
}

// --carry-max-age is the other half of the same window, and it must reach the
// resolution too.
func TestCovStatus_CarryMaxAgeFlagNarrowsTheWindow(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := fix.seedCarrySymbol(t, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	fix.seedCarryRun(t, "build-1", 0, map[int64][2]int{goSym: {5, 100}})
	fix.seedCarryRun(t, "build-2", 10*time.Minute, map[int64][2]int{feSym: {10, 10}})

	// The source build finished ~90 minutes ago; a one-minute bound rejects it.
	got := decodeCarryJSON(t, fix, "--carry-max-age", "1m")
	if got.MaxAge != "1m0s" {
		t.Errorf("--carry-max-age=1m reported max_age=%q, want \"1m0s\"", got.MaxAge)
	}
	if got.Evidence != 0 || got.DenominatorOnly != 1 {
		t.Errorf("evidence=%d denominator_only=%d, want 0/1 -- the measurement is older "+
			"than the wall-clock bound", got.Evidence, got.DenominatorOnly)
	}
}

// --carry=false has to switch the resolution off, not merely be accepted.
func TestCovStatus_CarryOffFlagIsHonoured(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := fix.seedCarrySymbol(t, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	fix.seedCarryRun(t, "build-1", 0, map[int64][2]int{goSym: {5, 100}})
	fix.seedCarryRun(t, "build-2", 10*time.Minute, map[int64][2]int{feSym: {10, 10}})

	got := decodeCarryJSON(t, fix, "--carry=false")
	if got.Enabled || got.Results != 0 {
		t.Errorf("--carry=false: enabled=%v results=%d, want false/0", got.Enabled, got.Results)
	}
	out, err := runCovStatusCmd(t, fix, "--carry=false")
	if err != nil {
		t.Fatalf("cov status --carry=false: %v", err)
	}
	if !strings.Contains(out, "carryforward: off") {
		t.Errorf("output does not say the carry was switched off:\n%s", out)
	}
}

// The default store passes no --run-group, so Resolve returns before looking
// for anything to carry. Reporting that as "nothing carried; every symbol in
// the picture was measured by this build" states an unknown as a measurement --
// and it is the sentence almost every user sees, because run groups are opt-in.
func TestCovStatus_UngroupedFrontierSaysCarryDidNotRun(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	fix.seedCarryRun(t, "", 0, map[int64][2]int{goSym: {5, 100}})

	out, err := runCovStatusCmd(t, fix)
	if err != nil {
		t.Fatalf("cov status: %v", err)
	}
	if strings.Contains(out, "every symbol in the picture was measured by this build") {
		t.Errorf("an ungrouped frontier reported an unmeasured claim as a measurement:\n%s", out)
	}
	if !strings.Contains(out, "carryforward: did not run") {
		t.Errorf("output does not say carryforward did not run:\n%s", out)
	}
	if !strings.Contains(out, "--run-group") {
		t.Errorf("output does not say WHY it did not run (no run group):\n%s", out)
	}

	got := decodeCarryJSON(t, fix)
	if got.Ran {
		t.Error("carry.ran = true for an ungrouped frontier, want false")
	}
	if got.SkipReason != string(store.CarrySkippedUngrouped) {
		t.Errorf("carry.skip_reason = %q, want %q", got.SkipReason, store.CarrySkippedUngrouped)
	}
}

// A grouped frontier that genuinely had nothing to carry keeps the reassuring
// sentence, because there it IS a measurement.
func TestCovStatus_GroupedFrontierWithNothingToCarrySaysSo(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	fix.seedCarryRun(t, "build-1", 0, map[int64][2]int{goSym: {5, 100}})

	out, err := runCovStatusCmd(t, fix)
	if err != nil {
		t.Fatalf("cov status: %v", err)
	}
	if !strings.Contains(out, "nothing carried") {
		t.Errorf("output does not report the (real) empty carry:\n%s", out)
	}
	if got := decodeCarryJSON(t, fix); !got.Ran {
		t.Error("carry.ran = false for a grouped frontier, want true")
	}
}

// The source's distance renders as a distance, never as the sentinel. A carry
// from beyond the window used to print "0 build(s) back", which reads as "this
// build measured it" -- the one thing a carry can never mean.
func TestCovStatus_BuildsBackIsNeverRenderedAsZero(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := fix.seedCarrySymbol(t, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	fix.seedCarryRun(t, "build-1", 0, map[int64][2]int{goSym: {5, 100}})
	for i, group := range []string{"build-2", "build-3", "build-4"} {
		fix.seedCarryRun(t, group, time.Duration(10+i*10)*time.Minute, map[int64][2]int{feSym: {10, 10}})
	}

	// A window of one build makes the ordinal scan read two frontiers, so
	// build-1 never gets an ordinal.
	out, err := runCovStatusCmd(t, fix, "--carry-builds", "1")
	if err != nil {
		t.Fatalf("cov status: %v", err)
	}
	if strings.Contains(out, "0 build(s) back") || strings.Contains(out, "0 builds back") {
		t.Errorf("the beyond-the-window sentinel rendered as a distance:\n%s", out)
	}
	if !strings.Contains(out, "beyond the carry window") {
		t.Errorf("output does not name the source as beyond the window:\n%s", out)
	}
	if got := decodeCarryJSON(t, fix, "--carry-builds", "1"); len(got.Sources) != 1 ||
		got.Sources[0].BuildsBack != store.CarryBuildsBackBeyondWindow {
		t.Errorf("carry.sources = %+v, want one source with builds_back=%d",
			got.Sources, store.CarryBuildsBackBeyondWindow)
	}
}

// A carry one build back still renders as a distance.
func TestCovStatus_ReportsTheSourceBuildAndDistance(t *testing.T) {
	fix := newCovFixture(t)
	goSym := fix.seedCarrySymbol(t, "billing.Charge", "src/billing/charge.go", 10, 120)
	feSym := fix.seedCarrySymbol(t, "billing.Checkout", "web/src/checkout.ts", 1, 12)

	fix.seedCarryRun(t, "build-1", 0, map[int64][2]int{goSym: {5, 100}})
	fix.seedCarryRun(t, "build-2", 10*time.Minute, map[int64][2]int{feSym: {10, 10}})

	out, err := runCovStatusCmd(t, fix)
	if err != nil {
		t.Fatalf("cov status: %v", err)
	}
	if !strings.Contains(out, `1 from build "build-1" (1 build back)`) {
		t.Errorf("output does not name the source build and its distance:\n%s", out)
	}
}

// decodeCarryJSON runs `cov status --json` and returns the carry accounting.
func decodeCarryJSON(t *testing.T, fix *covFixture, args ...string) covStatusCarry {
	t.Helper()
	prev := flags.JSON
	flags.JSON = true
	defer func() { flags.JSON = prev }()

	out, err := runCovStatusCmd(t, fix, append([]string{"--json"}, args...)...)
	if err != nil {
		t.Fatalf("cov status --json: %v", err)
	}
	var env struct {
		Result covStatusResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode cov status envelope: %v\n%s", err, out)
	}
	return env.Result.Carry
}
