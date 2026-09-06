package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/coverage"
	"github.com/sosalejandro/atlas/packages/store"
)

// fixtureDir is the repo the whole acceptance layer runs against. It has its
// own go.mod so `go test` can build and measure it independently, and lives
// under testdata/ so the parent module's package patterns never reach it.
const fixtureDir = "testdata/shopfixture"

// profileName is the committed output of, run inside fixtureDir:
//
//	go test ./... -coverprofile=cover.coverprofile -coverpkg=./...
//
// Committed rather than regenerated per run, for two reasons. It is evidence:
// the numbers below are about THIS measurement, and a profile regenerated on
// every CI machine would let a fixture change move the expected values
// silently. And the .gitignore in this repo un-ignores everything under a
// testdata/ directory precisely so a fixture like this is tracked — a
// coverprofile that is present locally and missing in CI has cost this repo
// two debugging sessions, so the extension avoids `*.out` as well.
const profileName = "cover.coverprofile"

// pipelineResult is one full scan-ingest-attribute run over the fixture.
type pipelineResult struct {
	store *store.Store
	index *codeindex.Index
	stats coverage.ProfileIngestStats
}

// runPipeline executes the real pipeline: index the fixture, persist it,
// ingest the committed coverprofile. No stubs — the point of this layer is
// that the layers compose, and a stub is a claim that they do.
func runPipeline(t *testing.T) pipelineResult {
	t.Helper()
	ctx := context.Background()

	idx, err := codeindex.IndexProject(ctx, fixtureDir, codeindex.Options{
		SkipTS: true, SkipPY: true,
	})
	if err != nil {
		t.Fatalf("IndexProject(%s): %v", fixtureDir, err)
	}

	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Ingest(ctx, idx); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	profile, err := os.Open(filepath.Join(fixtureDir, profileName))
	if err != nil {
		t.Fatalf("open profile: %v", err)
	}
	defer func() { _ = profile.Close() }()

	stats, err := coverage.IngestGoProfile(ctx, s,
		coverage.RunMeta{Framework: store.FrameworkGoTest}, profile)
	if err != nil {
		t.Fatalf("IngestGoProfile: %v", err)
	}
	return pipelineResult{store: s, index: idx, stats: stats}
}

// TestAcceptance_PerSymbolCoverageMatchesGoToolCover is the layer's anchor:
// atlas's per-symbol statement fractions, compared against `go tool cover
// -func` run live on the same profile.
//
// Live rather than a checked-in expectation. A committed expected-output file
// pins what the Go toolchain said on the day it was written; invoking the
// toolchain pins what it says now. The distinction matters because the claim
// atlas makes is not "these seven numbers" but "our arithmetic agrees with
// the compiler's" — and when a toolchain upgrade changes how statements are
// counted, the honest outcome is this test failing, not a stale file agreeing
// with a stale belief.
//
// Every clause of the fixture maps to a bug this shape has caused: the
// colliding Order.Total made one of the two files invisible (#85); the
// package-private helpers had no symbol to be charged to; and without
// end_line every span was a guess at where the next symbol started.
func TestAcceptance_PerSymbolCoverageMatchesGoToolCover(t *testing.T) {
	res := runPipeline(t)
	ctx := context.Background()

	// Seven is what the fixture declares, and asserting it stops the loop
	// below from passing by comparing nothing: a profile that failed to parse
	// or a regex that stopped matching would otherwise be indistinguishable
	// from perfect agreement.
	want := goToolCoverFunc(t)
	if len(want) != 7 {
		t.Fatalf("go tool cover reported %d functions, want 7: %+v", len(want), want)
	}

	syms, err := res.store.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		t.Fatalf("Symbols().List: %v", err)
	}
	byPos := map[string]store.SymbolRow{}
	for _, s := range syms {
		byPos[fmt.Sprintf("%s:%d", s.FilePath, s.Line)] = s
	}

	frontier, err := res.store.Coverage().LatestFrontier(ctx)
	if err != nil {
		t.Fatalf("LatestFrontier: %v", err)
	}
	results, err := res.store.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		t.Fatalf("ListFrontierResults: %v", err)
	}
	bySymbol := map[int64]store.CoverageResult{}
	for _, r := range results {
		if r.SymbolID != nil {
			bySymbol[*r.SymbolID] = r
		}
	}

	for _, fn := range want {
		sym, ok := byPos[fmt.Sprintf("%s:%d", fn.file, fn.line)]
		if !ok {
			t.Errorf("%s at %s:%d has no atlas symbol; its coverage is charged to nobody",
				fn.name, fn.file, fn.line)
			continue
		}
		got, ok := bySymbol[sym.ID]
		if !ok {
			t.Errorf("%s (%s) is indexed but carries no coverage result", fn.name, sym.QualifiedName)
			continue
		}
		if got.TotalStmts == 0 {
			t.Errorf("%s (%s): atlas charged it 0 statements; go tool cover measured it at %.1f%%",
				fn.name, sym.QualifiedName, fn.percent)
			continue
		}
		gotPct := 100 * float64(got.CoveredStmts) / float64(got.TotalStmts)
		// One decimal place is the precision go tool cover prints, so
		// comparing at half of that is comparing what both sides actually
		// claim rather than the float noise underneath it.
		if diff := gotPct - fn.percent; diff > 0.05 || diff < -0.05 {
			t.Errorf("%s (%s): atlas says %d/%d = %.1f%%, go tool cover says %.1f%%",
				fn.name, sym.QualifiedName, got.CoveredStmts, got.TotalStmts, gotPct, fn.percent)
		}
	}
}

// TestAcceptance_TheBlindSpotIsReportedNotAbsorbed pins the accounting a user
// reads at the top of `atlas cov sync`, including the part that is missing.
//
// The fixture measures 14 statements. Eleven of them belong to the seven
// declarations `go tool cover -func` lists, and atlas charges those. The other
// three are the body of shipping.Surcharge, a function VALUE the compiler
// instruments and the scanner does not index — so they are reported as an
// "outside-symbol-spans" gap.
//
// Reported, not absorbed, is the whole assertion. Surcharge is the last thing
// in its file, so if a future change stopped trusting rate's end_line the
// span fallback would extend rate to end-of-file and quietly hand it those
// three statements. Every count would still balance; rate's percentage would
// simply start describing code rate does not contain. Issue #85 is what that
// looks like at scale.
//
// The numbers are exact rather than approximate because the fixture is small
// enough to hold in your head — two production files, fourteen statements,
// nothing generated, nothing excluded — so drift here is a regression rather
// than noise. That is the only condition under which pinning an exact number
// is honest.
func TestAcceptance_TheBlindSpotIsReportedNotAbsorbed(t *testing.T) {
	res := runPipeline(t)

	if res.stats.FilesInProfile != 2 {
		t.Errorf("files_in_profile = %d, want 2", res.stats.FilesInProfile)
	}
	if res.stats.FilesMatched != 2 || res.stats.FilesUnmatched != 0 {
		t.Errorf("files matched/unmatched = %d/%d, want 2/0 (gaps: %+v)",
			res.stats.FilesMatched, res.stats.FilesUnmatched, res.stats.Gaps)
	}
	if res.stats.StmtsAttributed != 11 {
		t.Errorf("stmts_attributed = %d, want 11 (the seven declarations go tool cover lists)",
			res.stats.StmtsAttributed)
	}
	if res.stats.StmtsUnattributed != 3 {
		t.Errorf("stmts_unattributed = %d, want 3 (the body of the Surcharge function value); gaps: %+v",
			res.stats.StmtsUnattributed, res.stats.Gaps)
	}
	if res.stats.StmtsAttributed+res.stats.StmtsUnattributed != res.stats.BlocksParsed {
		t.Errorf("the run accounts for %d+%d statements but parsed %d blocks of one statement each",
			res.stats.StmtsAttributed, res.stats.StmtsUnattributed, res.stats.BlocksParsed)
	}
	if res.stats.SymbolsExecuted != 7 {
		t.Errorf("symbols_executed = %d, want 7", res.stats.SymbolsExecuted)
	}

	// The gap has to NAME the file, because a total with no location is a
	// number the reader cannot act on.
	if len(res.stats.Gaps) != 1 {
		t.Fatalf("got %d gaps, want 1: %+v", len(res.stats.Gaps), res.stats.Gaps)
	}
	gap := res.stats.Gaps[0]
	if !strings.HasSuffix(gap.Path, "shipping/order.go") {
		t.Errorf("gap names %q, want the shipping file", gap.Path)
	}
	if gap.Reason != coverage.ReasonOutsideSymbolSpans {
		t.Errorf("gap reason = %q, want %q — the file IS indexed, the statements just belong to no declaration",
			gap.Reason, coverage.ReasonOutsideSymbolSpans)
	}
	if gap.Stmts != 3 {
		t.Errorf("gap covers %d statements, want 3", gap.Stmts)
	}
}

// TestAcceptance_CollidingDeclarationsBothSurvive states issue #85 as an
// end-to-end claim rather than a scanner-level one.
//
// billing.Order.Total and shipping.Order.Total compute the same short id.
// Exactly one keeps it; the other is indexed package-qualified. What the user
// must see is BOTH, at their own positions, each with its own coverage row —
// because the failure mode was never "a symbol has the wrong name", it was
// "an entire file's execution is invisible".
func TestAcceptance_CollidingDeclarationsBothSurvive(t *testing.T) {
	res := runPipeline(t)
	ctx := context.Background()

	syms, err := res.store.Symbols().List(ctx, store.SymbolFilter{})
	if err != nil {
		t.Fatalf("Symbols().List: %v", err)
	}
	perFile := map[string][]string{}
	for _, s := range syms {
		perFile[s.FilePath] = append(perFile[s.FilePath], string(s.QualifiedName))
	}
	for _, file := range []string{"billing/order.go", "shipping/order.go"} {
		if len(perFile[file]) == 0 {
			t.Fatalf("no symbols indexed for %s; the collision swallowed the file", file)
		}
	}

	// Both Totals, at their own positions.
	totals := 0
	for _, s := range syms {
		if strings.HasSuffix(string(s.QualifiedName), "Order.Total") {
			totals++
		}
	}
	if totals != 2 {
		var names []string
		for _, s := range syms {
			names = append(names, string(s.QualifiedName))
		}
		sort.Strings(names)
		t.Fatalf("found %d Order.Total symbols, want 2 (billing's and shipping's); indexed: %v", totals, names)
	}
}

// TestAcceptance_AnnotationsBecomeFeaturesWithCoverage walks the last hop of
// the chain: annotation to feature to symbol to statements.
//
// This is the one every other layer is in service of. A coverage number that
// cannot be attached to a feature is a repo-wide percentage, which is the
// metric atlas exists to replace; the value is only there if the annotation
// in the source ends up scoring the statements the compiler measured.
func TestAcceptance_AnnotationsBecomeFeaturesWithCoverage(t *testing.T) {
	res := runPipeline(t)
	ctx := context.Background()

	features, err := res.store.Features().List(ctx, store.FeatureFilter{})
	if err != nil {
		t.Fatalf("Features().List: %v", err)
	}
	got := map[string]bool{}
	for _, f := range features {
		got[string(f.ID)] = true
	}
	for _, want := range []string{"checkout.total", "checkout.pay", "shipping.quote"} {
		if !got[want] {
			t.Errorf("feature %q was annotated in the fixture but did not materialise; got %v", want, keysOf(got))
		}
	}

	// checkout.pay covers Pay and Paid, neither of which the fixture's tests
	// call. Its statements must all be present and none of them covered — a
	// feature that reports coverage its tests did not produce is the single
	// worst output this tool can emit.
	links, err := res.store.FeatureSymbols().ListByFeature(ctx, "checkout.pay")
	if err != nil {
		t.Fatalf("FeatureSymbols().ListByFeature: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("checkout.pay links %d symbols, want 2 (Pay and Paid)", len(links))
	}

	frontier, err := res.store.Coverage().LatestFrontier(ctx)
	if err != nil {
		t.Fatalf("LatestFrontier: %v", err)
	}
	results, err := res.store.Coverage().ListFrontierResults(ctx, frontier)
	if err != nil {
		t.Fatalf("ListFrontierResults: %v", err)
	}
	linked := map[int64]bool{}
	for _, l := range links {
		linked[l.SymbolID] = true
	}
	covered, total := 0, 0
	for _, r := range results {
		if r.SymbolID != nil && linked[*r.SymbolID] {
			covered += r.CoveredStmts
			total += r.TotalStmts
		}
	}
	if total != 2 {
		t.Errorf("checkout.pay covers %d statements, want 2 (one in Pay, one in Paid)", total)
	}
	if covered != 0 {
		t.Errorf("checkout.pay reports %d covered statements; the fixture's tests never call either method", covered)
	}
}

// coveredFunc is one line of `go tool cover -func` output.
type coveredFunc struct {
	file    string // repo-relative to the fixture, e.g. "billing/order.go"
	line    int
	name    string
	percent float64
}

// funcLine matches `go tool cover -func` rows:
//
//	github.com/example/shopfixture/billing/order.go:17:	Total	100.0%
var funcLine = regexp.MustCompile(`^(\S+):(\d+):\s+(\S+)\s+([0-9.]+)%$`)

// goToolCoverFunc runs the Go toolchain's own reporter over the committed
// profile and returns what it says, with paths reduced to the fixture-
// relative form atlas uses.
//
// The command runs with Dir set to the fixture because `go tool cover -func`
// resolves the profile's import paths against the surrounding module; from
// the atlas module it cannot find github.com/example/shopfixture and fails.
func goToolCoverFunc(t *testing.T) []coveredFunc {
	t.Helper()
	cmd := exec.Command("go", "tool", "cover", "-func="+profileName)
	cmd.Dir = fixtureDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go tool cover -func: %v\n%s", err, stderr.String())
	}

	const modulePrefix = "github.com/example/shopfixture/"
	var out []coveredFunc
	for _, line := range strings.Split(stdout.String(), "\n") {
		m := funcLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue // the trailing "total:" row, and blanks
		}
		ln, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("go tool cover printed a non-numeric line in %q", line)
		}
		pct, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			t.Fatalf("go tool cover printed a non-numeric percentage in %q", line)
		}
		out = append(out, coveredFunc{
			file:    strings.TrimPrefix(m[1], modulePrefix),
			line:    ln,
			name:    m[3],
			percent: pct,
		})
	}
	return out
}

// TestAcceptance_CallEdgesReachTheColliderOnBothSides checks that the call
// graph a user walks with `atlas trace` reaches the RIGHT Total.
//
// Both packages declare Order.Total and both call a package-private helper.
// The scanner resolves a call by preferring the declaration in the CALLER's
// own package, so billing's Total must reach billing's normalize and
// shipping's must reach shipping's rate. Getting this wrong does not lose a
// symbol or change a count — it silently routes one context's impl surface
// through another's, and the coverage a feature is scored on comes from
// functions it never calls (issue #84).
func TestAcceptance_CallEdgesReachTheColliderOnBothSides(t *testing.T) {
	res := runPipeline(t)

	edges := map[string]bool{}
	for _, e := range res.index.Graph.Edges {
		edges[string(e.From)+" -> "+string(e.To)] = true
	}
	// Ids are asymmetric on purpose: the first Order.Total walked keeps the
	// bare short id, the second is package-qualified. That asymmetry is what
	// the scanner does to keep BOTH, and pinning it here is what would make a
	// regression to the old first-wins collapse visible.
	for _, want := range []string{
		"Order.Total -> billing.normalize",
		"shipping.Order.Total -> shipping.rate",
	} {
		if !edges[want] {
			t.Errorf("call edge %q is missing; edges found: %v", want, keysOf(edges))
		}
	}
	// And the inverse: neither context may reach the other's helper.
	for _, unwanted := range []string{
		"Order.Total -> shipping.rate",
		"shipping.Order.Total -> billing.normalize",
	} {
		if edges[unwanted] {
			t.Errorf("call edge %q crosses bounded contexts; a feature would be scored on code it never calls", unwanted)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
