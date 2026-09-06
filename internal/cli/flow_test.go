package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// flowFixture is a tiny repo on disk plus a store seeded with the symbols the
// scanner would have produced for it. `atlas flow` reads symbols from the
// store and re-parses the files they name, so both halves have to exist.
type flowFixture struct {
	root   string
	dbPath string
}

const flowSourceFile = "svc/handler.go"

// flowSource is the fixture under analysis. The shapes matter:
//   - Handle has an if with no else whose then-arm returns (its false outcome
//     is knowable only from the statement after it), and a `&&` (whose
//     outcomes are knowable from nothing at all).
//   - LoadAll issues a query per element of a collection: the N+1.
const flowSource = `package svc

func Handle(n int, ok bool) error {
	if n < 0 && ok {
		return nil
	}
	return nil
}

func LoadAll(db DB, ids []int) []Row {
	var out []Row
	for _, id := range ids {
		r, _ := db.GetUser(id)
		out = append(out, r)
	}
	return out
}
`

func newFlowFixture(t *testing.T) *flowFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, flowSourceFile), []byte(flowSource), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	fix := &flowFixture{root: dir, dbPath: filepath.Join(dir, ".atlas", "atlas.db")}

	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	for _, sym := range []store.SymbolRow{
		{QualifiedName: "svc.Handle", Kind: shared.KindFunc, FilePath: flowSourceFile, Line: 3},
		{QualifiedName: "svc.LoadAll", Kind: shared.KindFunc, FilePath: flowSourceFile, Line: 10},
	} {
		if _, err := s.Symbols().Insert(ctx, sym); err != nil {
			t.Fatalf("insert symbol %s: %v", sym.QualifiedName, err)
		}
	}
	return fix
}

// addFile writes another source file into the fixture and seeds the symbols
// the scanner would have produced for it, keyed by declaration line.
//
// It is opt-in rather than part of newFlowFixture because most tests here
// assert over the shared two-symbol fixture, and a file that silently joined
// every run would change counts those tests read.
func (f *flowFixture) addFile(t *testing.T, rel, src string, symLines map[string]int) {
	t.Helper()
	abs := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	for name, line := range symLines {
		if _, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(name), Kind: shared.KindFunc,
			FilePath: rel, Line: line,
		}); err != nil {
			t.Fatalf("insert symbol %s: %v", name, err)
		}
	}
}

// flowGotoSource is a function whose only path to `bad:` is a goto. The
// builder does not draw that edge, so over the graph as built the label looks
// like dead code — which is exactly the finding that must NOT be emitted.
const flowGotoSource = `package svc

func Jumpy(n int) string {
	if n < 0 {
		goto bad
	}
	return "ok"
bad:
	return "bad"
}
`

// flowOtherSource is a second measured-nothing file: a package the profile
// below never looked at.
const flowOtherSource = `package svc

func Other(n int) int {
	if n > 0 {
		return n
	}
	return 0
}
`

// writeProfile drops a coverprofile next to the fixture. Handle's then arm
// never ran; the statement after it did, which is the only witness the false
// outcome leaves behind.
func (f *flowFixture) writeProfile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(f.root, "cover.out")
	body := "mode: count\n" +
		"example.com/repo/svc/handler.go:3.36,4.19 1 4\n" +
		"example.com/repo/svc/handler.go:4.19,6.3 1 0\n" +
		"example.com/repo/svc/handler.go:7.2,7.13 1 4\n" +
		"example.com/repo/svc/handler.go:10.39,12.23 2 0\n" +
		"example.com/repo/svc/handler.go:12.23,15.3 2 0\n" +
		"example.com/repo/svc/handler.go:16.2,16.12 1 0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

func runFlowCmd(t *testing.T, fix *flowFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	// --root is passed explicitly: the root command's PersistentPreRunE
	// re-loads the config and overwrites the fixture's repoRoot with whatever
	// the test binary's working directory resolves to, which is the atlas
	// repo, not the tempdir.
	if len(args) > 0 && args[0] == "build" {
		args = append([]string{"build", "--root", fix.root}, args[1:]...)
	}
	root.SetArgs(append([]string{"flow", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestFlow_RegisteredOnRoot(t *testing.T) {
	found := false
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "flow" {
			found = true
		}
	}
	if !found {
		t.Fatal("`atlas flow` is not registered on the root command")
	}
}

// TestFlowBuild_PersistsGraphAndComplexity is the end-to-end wiring check:
// build parses the files the store's symbols name, and every result lands
// keyed by symbol so it joins the graph.
func TestFlowBuild_PersistsGraphAndComplexity(t *testing.T) {
	fix := newFlowFixture(t)
	stdout, stderr, err := runFlowCmd(t, fix, "build")
	if err != nil {
		t.Fatalf("flow build: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "2 symbol") {
		t.Errorf("expected both symbols analysed; got:\n%s", stdout)
	}

	s, err := store.Open(context.Background(), fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	sym, err := s.Symbols().FindByQualifiedName(ctx, "svc.Handle")
	if err != nil {
		t.Fatalf("find symbol: %v", err)
	}
	flow, err := s.ControlFlow().Get(ctx, sym.ID)
	if err != nil {
		t.Fatalf("ControlFlow().Get: %v", err)
	}
	// if + && = complexity 3.
	if flow.Metrics.Complexity != 3 {
		t.Errorf("complexity = %d, want 3", flow.Metrics.Complexity)
	}
	if len(flow.Blocks) == 0 || flow.Blocks[0].Kind != "entry" {
		t.Errorf("blocks not persisted with the entry first: %+v", flow.Blocks)
	}
	if flow.Metrics.Conditions != 2 {
		t.Errorf("conditions = %d, want 2", flow.Metrics.Conditions)
	}
}

// TestFlowBuild_QueryInLoopFinding: the N+1 fires, carries both sites and a
// confidence, and is keyed to its symbol.
func TestFlowBuild_QueryInLoopFinding(t *testing.T) {
	fix := newFlowFixture(t)
	if _, stderr, err := runFlowCmd(t, fix, "build"); err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	s, err := store.Open(context.Background(), fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	got, err := s.ControlFlow().Findings(ctx, store.FindingQueryInLoop)
	if err != nil {
		t.Fatalf("Findings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("query-in-loop findings = %d, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Line != 13 || f.RelatedLine != 12 {
		t.Errorf("finding sites: query line %d, loop line %d; want 13 and 12", f.Line, f.RelatedLine)
	}
	if f.Confidence == "" {
		t.Error("every finding must state its confidence")
	}
	sym, err := s.Symbols().FindByQualifiedName(ctx, "svc.LoadAll")
	if err != nil {
		t.Fatalf("find symbol: %v", err)
	}
	if f.SymbolID != sym.ID {
		t.Errorf("finding symbol_id = %d, want %d (it must join the graph)", f.SymbolID, sym.ID)
	}
}

// TestFlowBuild_DecisionCoverageSeparateFromStatements is the acceptance
// criterion about not blending the two metrics: the JSON payload reports
// decision coverage with its own decidable denominator, and says in words
// that statement coverage is a different number.
func TestFlowBuild_DecisionCoverageSeparateFromStatements(t *testing.T) {
	fix := newFlowFixture(t)
	profile := fix.writeProfile(t)

	stdout, stderr, err := runFlowCmd(t, fix, "build", "--profile", profile, "--json")
	if err != nil {
		t.Fatalf("flow build --profile: %v\n%s", err, stderr)
	}
	var env struct {
		Result struct {
			SymbolsAnalyzed  int `json:"symbols_analyzed"`
			DecisionCoverage *struct {
				OutcomesTotal     int      `json:"outcomes_total"`
				OutcomesDecidable int      `json:"outcomes_decidable"`
				OutcomesTaken     int      `json:"outcomes_taken"`
				Percent           *float64 `json:"percent"`
			} `json:"decision_coverage"`
			MCDC string `json:"mcdc"`
		} `json:"result"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, stdout)
	}
	dc := env.Result.DecisionCoverage
	if dc == nil {
		t.Fatal("no decision_coverage in the payload")
	}
	// Handle: if(true not taken, false taken) + &&(2 undecidable).
	// LoadAll: range(body not entered, exhausted not taken) + nothing else.
	if dc.OutcomesDecidable >= dc.OutcomesTotal {
		t.Errorf("decidable (%d) should be less than total (%d): the && outcomes are unjudgeable",
			dc.OutcomesDecidable, dc.OutcomesTotal)
	}
	if dc.Percent == nil {
		t.Fatal("percent should be present when some outcome is decidable")
	}
	if !strings.Contains(strings.ToLower(env.Result.MCDC), "not derivable") {
		t.Errorf("the payload must state the MC/DC limit; got %q", env.Result.MCDC)
	}
	joined := strings.Join(env.Warnings, " ")
	if !strings.Contains(joined, "statement coverage") {
		t.Errorf("the output must say decision coverage is not statement coverage; warnings: %v", env.Warnings)
	}
}

// TestFlowShow_ReportsBothMetricsSeparately: the human surface never prints a
// blended number, and always prints the MC/DC caveat next to the condition
// counts.
func TestFlowShow_ReportsBothMetricsSeparately(t *testing.T) {
	fix := newFlowFixture(t)
	profile := fix.writeProfile(t)
	if _, stderr, err := runFlowCmd(t, fix, "build", "--profile", profile); err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	stdout, stderr, err := runFlowCmd(t, fix, "show", "svc.Handle")
	if err != nil {
		t.Fatalf("flow show: %v\n%s", err, stderr)
	}
	for _, want := range []string{"complexity", "decision coverage", "MC/DC", "not derivable"} {
		if !strings.Contains(strings.ToLower(stdout), strings.ToLower(want)) {
			t.Errorf("`flow show` output is missing %q:\n%s", want, stdout)
		}
	}
	// The one thing this surface must never do is print a statement-coverage
	// percentage as though it were the branch story.
	if regexp.MustCompile(`(?i)statement coverage\s*[:=]\s*[0-9]`).MatchString(stdout) {
		t.Errorf("`flow show` must not present a statement-coverage number as its own:\n%s", stdout)
	}
	if !strings.Contains(stdout, "never blended") {
		t.Errorf("`flow show` must say the two metrics are not blended:\n%s", stdout)
	}
}

func TestFlowShow_UnknownSymbolIsAnError(t *testing.T) {
	fix := newFlowFixture(t)
	if _, _, err := runFlowCmd(t, fix, "show", "svc.NoSuchThing"); err == nil {
		t.Fatal("expected an error for an unknown symbol")
	}
}

// TestFlowFindings_ListsWithConfidence: the findings surface carries the
// confidence and the caveat, so nobody reads a smell as a proof.
func TestFlowFindings_ListsWithConfidence(t *testing.T) {
	fix := newFlowFixture(t)
	if _, stderr, err := runFlowCmd(t, fix, "build"); err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	stdout, stderr, err := runFlowCmd(t, fix, "findings")
	if err != nil {
		t.Fatalf("flow findings: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "flow.query-in-loop") {
		t.Errorf("expected the N+1 finding; got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "svc.LoadAll") {
		t.Errorf("findings must name their symbol; got:\n%s", stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "smell") {
		t.Errorf("findings output must carry the smell-not-proof caveat; got:\n%s", stdout)
	}
}

// TestFlowBuild_NoProfileDoesNotReportZeroCoverage: building without a
// profile must leave decision coverage absent, not zero.
func TestFlowBuild_NoProfileDoesNotReportZeroCoverage(t *testing.T) {
	fix := newFlowFixture(t)
	stdout, stderr, err := runFlowCmd(t, fix, "build", "--json")
	if err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	var env struct {
		Result struct {
			DecisionCoverage any `json:"decision_coverage"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if env.Result.DecisionCoverage != nil {
		t.Errorf("decision_coverage must be absent without a profile, got %v", env.Result.DecisionCoverage)
	}
}

// TestFlowShow_StaleMeasurementIsFlagged: a measurement taken over an older
// version of a function must not be printed beside the new graph as though it
// described it. The outcome count is a property of the graph, so a mismatch is
// proof the two do not belong together.
func TestFlowShow_StaleMeasurementIsFlagged(t *testing.T) {
	fix := newFlowFixture(t)
	profile := fix.writeProfile(t)
	if _, stderr, err := runFlowCmd(t, fix, "build", "--profile", profile); err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	// Add a branch to Handle, keeping its declaration line, then re-analyse
	// the source WITHOUT a profile: the stored measurement is now stale.
	grown := strings.Replace(flowSource,
		"func Handle(n int, ok bool) error {\n\tif n < 0 && ok {",
		"func Handle(n int, ok bool) error {\n\tif n > 99 {\n\t\treturn nil\n\t}\n\tif n < 0 && ok {", 1)
	if grown == flowSource {
		t.Fatal("fixture rewrite did not apply")
	}
	if err := os.WriteFile(filepath.Join(fix.root, flowSourceFile), []byte(grown), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	if _, stderr, err := runFlowCmd(t, fix, "build"); err != nil {
		t.Fatalf("flow rebuild: %v\n%s", err, stderr)
	}
	stdout, stderr, err := runFlowCmd(t, fix, "show", "svc.Handle")
	if err != nil {
		t.Fatalf("flow show: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "different version of this function") {
		t.Errorf("a stale measurement must be flagged:\n%s", stdout)
	}
}

// TestFlowBuild_UnmeasuredFileIsNotRecordedAsZero is the per-file guard.
//
// Supplying a profile is not the same as that profile covering a given file:
// profiling one package, running an integration-test profile, or analysing a
// package with no tests all produce a profile that names other files. The
// symbols in those files were NOT measured, and an absent row is the only
// honest record of that — a zero-valued row reads as "no branch was taken",
// which is a claim about the tests that this run cannot support.
func TestFlowBuild_UnmeasuredFileIsNotRecordedAsZero(t *testing.T) {
	fix := newFlowFixture(t)
	fix.addFile(t, "svc/other.go", flowOtherSource, map[string]int{"svc.Other": 3})
	// The profile covers svc/handler.go and nothing else.
	profile := fix.writeProfile(t)

	if _, stderr, err := runFlowCmd(t, fix, "build", "--profile", profile); err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	measured, err := s.Symbols().FindByQualifiedName(ctx, "svc.Handle")
	if err != nil {
		t.Fatalf("find svc.Handle: %v", err)
	}
	if _, err := s.ControlFlow().GetDecisionCoverage(ctx, measured.ID); err != nil {
		t.Fatalf("svc.Handle's file IS in the profile, so it must have a measurement: %v", err)
	}

	unmeasured, err := s.Symbols().FindByQualifiedName(ctx, "svc.Other")
	if err != nil {
		t.Fatalf("find svc.Other: %v", err)
	}
	dc, err := s.ControlFlow().GetDecisionCoverage(ctx, unmeasured.ID)
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("svc.Other's file is not in the profile, so it must have NO row; got %+v (err %v)", dc, err)
	}
}

// TestFlowBuild_ProfileCoveringNothingSaysSo: when the profile names no file
// the run analysed, the payload carries no decision coverage at all and the
// output explains why, rather than reporting a 0% that would be read as a
// verdict on the tests.
func TestFlowBuild_ProfileCoveringNothingSaysSo(t *testing.T) {
	fix := newFlowFixture(t)
	path := filepath.Join(fix.root, "elsewhere.out")
	body := "mode: count\n" +
		"example.com/repo/other/pkg/thing.go:3.20,5.10 1 7\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	stdout, stderr, err := runFlowCmd(t, fix, "build", "--profile", path, "--json")
	if err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	var env struct {
		Result struct {
			DecisionCoverage any `json:"decision_coverage"`
		} `json:"result"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if env.Result.DecisionCoverage != nil {
		t.Errorf("a profile that covers none of these files measured nothing; got %v",
			env.Result.DecisionCoverage)
	}
	if !strings.Contains(strings.Join(env.Warnings, " "), "covers none of the files") {
		t.Errorf("the run must say the profile covered nothing; warnings: %v", env.Warnings)
	}

	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	sym, err := s.Symbols().FindByQualifiedName(ctx, "svc.Handle")
	if err != nil {
		t.Fatalf("find symbol: %v", err)
	}
	if _, err := s.ControlFlow().GetDecisionCoverage(ctx, sym.ID); !errors.Is(err, shared.ErrNotFound) {
		t.Errorf("no row may be written for a file the profile never covered; err = %v", err)
	}
}

// TestFlowBuild_UnreachableSuppressedForGoto: the builder does not model goto
// edges, so `bad:` in flowGotoSource has no predecessor in the graph even
// though every negative input reaches it. Reporting it as unreachable at
// confidence "high" would be a confident wrong answer over a graph known to be
// incomplete, so the finding is suppressed and the run says why.
func TestFlowBuild_UnreachableSuppressedForGoto(t *testing.T) {
	fix := newFlowFixture(t)
	fix.addFile(t, "svc/jump.go", flowGotoSource, map[string]int{"svc.Jumpy": 3})

	stdout, stderr, err := runFlowCmd(t, fix, "build")
	if err != nil {
		t.Fatalf("flow build: %v\n%s", err, stderr)
	}
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	sym, err := s.Symbols().FindByQualifiedName(ctx, "svc.Jumpy")
	if err != nil {
		t.Fatalf("find svc.Jumpy: %v", err)
	}
	found, err := s.ControlFlow().Findings(ctx, store.FindingUnreachable)
	if err != nil {
		t.Fatalf("Findings: %v", err)
	}
	for _, f := range found {
		if f.SymbolID == sym.ID {
			t.Errorf("a label reached only by a goto is not unreachable code: %+v", f)
		}
	}
	flow, err := s.ControlFlow().Get(ctx, sym.ID)
	if err != nil {
		t.Fatalf("ControlFlow().Get: %v", err)
	}
	if flow.Metrics.UnreachableBlocks != 0 {
		t.Errorf("nothing may be claimed about reachability here; unreachable_blocks = %d",
			flow.Metrics.UnreachableBlocks)
	}
	// Suppressed is not the same as silent: a diagnostic that quietly stops
	// running for part of the code is worse than one that never ran.
	if !strings.Contains(stdout, "flow.unreachable was NOT computed") {
		t.Errorf("the run must report that reachability was not computed, and why:\n%s", stdout)
	}
	if !strings.Contains(stdout, "svc.Jumpy") {
		t.Errorf("the note must name at least one affected symbol:\n%s", stdout)
	}
}
