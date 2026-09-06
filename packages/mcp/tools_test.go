package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// repo is a small but complete index: one feature whose annotation sits on a
// test symbol, the production symbols that test really reaches, and a logger
// that the whole suite runs. The logger is the point — a surface derivation
// that cannot exclude shared runtime hands the agent a feature that "includes"
// every package in the repo.
type repo struct {
	s    *store.Store
	ids  map[string]int64
	runs []int64
}

const (
	featPay    = shared.FeatureID("checkout.pay")
	featSearch = shared.FeatureID("search.index")
	symPay     = "pkg/checkout.Pay"
	symCharge  = "pkg/checkout.charge"
	symTestPay = "pkg/checkout.TestPay"
	symLog     = "pkg/log.Debug"
)

func seedRepo(t *testing.T) *repo {
	t.Helper()
	ctx := context.Background()
	r := &repo{s: openTestStore(t), ids: map[string]int64{}}

	for id, title := range map[shared.FeatureID]string{
		featPay:    "Checkout payment capture",
		featSearch: "Search index rebuild",
	} {
		if err := r.s.Features().Upsert(ctx, store.Feature{ID: id, Title: title, Kind: store.FeatureKindFeature}); err != nil {
			t.Fatalf("upsert feature %s: %v", id, err)
		}
	}

	pkgCheckout, pkgLog := "checkout", "log"
	r.insert(t, symTestPay, "pkg/checkout/pay_test.go", 10, &pkgCheckout)
	r.insert(t, symPay, "pkg/checkout/pay.go", 20, &pkgCheckout)
	r.insert(t, symCharge, "pkg/checkout/pay.go", 60, &pkgCheckout)
	r.insert(t, symLog, "pkg/log/log.go", 5, &pkgLog)

	r.link(t, featPay, symTestPay, store.RoleTest)
	r.edge(t, symTestPay, symPay, "pkg/checkout/pay_test.go", 12)
	r.edge(t, symPay, symCharge, "pkg/checkout/pay.go", 25)
	r.edge(t, symPay, symLog, "pkg/checkout/pay.go", 26)
	return r
}

func (r *repo) insert(t *testing.T, qn, file string, line int, pkg *string) int64 {
	t.Helper()
	end := line + 5
	id, err := r.s.Symbols().Insert(context.Background(), store.SymbolRow{
		QualifiedName: shared.SymbolID(qn), Kind: shared.KindFunc,
		FilePath: file, Line: line, EndLine: &end, Package: pkg,
	})
	if err != nil {
		t.Fatalf("insert symbol %s: %v", qn, err)
	}
	r.ids[qn] = id
	return id
}

func (r *repo) link(t *testing.T, f shared.FeatureID, qn string, role store.FeatureSymbolRole) {
	t.Helper()
	err := r.s.FeatureSymbols().Link(context.Background(), store.FeatureSymbolLink{
		FeatureID: f, SymbolID: r.ids[qn], Role: role, Source: store.SourceAnnotation,
	})
	if err != nil {
		t.Fatalf("link %s -> %s: %v", f, qn, err)
	}
}

func (r *repo) edge(t *testing.T, from, to, file string, line int) {
	t.Helper()
	_, err := r.s.Edges().Insert(context.Background(), store.EdgeRow{
		Tier: graph.TierNameResolved, FromID: r.ids[from], ToID: r.ids[to], Kind: store.EdgeKindCall, FilePath: file, Line: line,
	})
	if err != nil {
		t.Fatalf("insert edge %s -> %s: %v", from, to, err)
	}
}

// seedPerTestCoverage writes a run carrying per-test execution evidence: the
// feature's own test reaches Pay and charge, while ten unrelated tests all
// reach the logger. That ratio is what the ubiquity cutoff reads.
func (r *repo) seedPerTestCoverage(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	results := []store.CoverageResult{
		{SymbolID: ptr(r.ids[symPay]), Status: store.StatusPass, CoveredStmts: 8, TotalStmts: 10},
		{SymbolID: ptr(r.ids[symCharge]), Status: store.StatusPass, CoveredStmts: 5, TotalStmts: 10},
		{SymbolID: ptr(r.ids[symLog]), Status: store.StatusPass, CoveredStmts: 1, TotalStmts: 10},
	}
	runID, err := r.s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, results)
	if err != nil {
		t.Fatalf("insert coverage run: %v", err)
	}

	rows := []store.TestExecution{
		{TestSymbolID: r.ids[symTestPay], SymbolID: r.ids[symPay], CoveredStmts: 8, TotalStmts: 10},
		{TestSymbolID: r.ids[symTestPay], SymbolID: r.ids[symCharge], CoveredStmts: 5, TotalStmts: 10},
		{TestSymbolID: r.ids[symTestPay], SymbolID: r.ids[symLog], CoveredStmts: 1, TotalStmts: 10},
	}
	for i := 0; i < 10; i++ {
		qn := fmt.Sprintf("pkg/other.Test%d", i)
		id := r.insert(t, qn, "pkg/other/other_test.go", 10+i, nil)
		rows = append(rows, store.TestExecution{TestSymbolID: id, SymbolID: r.ids[symLog], CoveredStmts: 1, TotalStmts: 10})
	}
	if err := r.s.TestCoverage().Insert(ctx, runID, rows); err != nil {
		t.Fatalf("insert per-test coverage: %v", err)
	}
	r.runs = append(r.runs, runID)
	return runID
}

func ptr[T any](v T) *T { return &v }

func (r *repo) wire(t *testing.T) *wire {
	t.Helper()
	w := startWire(t, newServerOver(t, r.s))
	w.handshake()
	return w
}

func TestFindFeature_MatchesIDAndTitleCaseInsensitively(t *testing.T) {
	w := seedRepo(t).wire(t)

	for _, query := range []string{"checkout", "CHECKOUT", "payment capture"} {
		sc := w.structured(w.call(2, "find_feature", map[string]any{"query": query}))
		feats, ok := sc["features"].([]any)
		if !ok || len(feats) != 1 {
			t.Fatalf("find_feature(%q) = %v, want exactly the checkout feature", query, sc)
		}
		got, _ := feats[0].(map[string]any)
		if got["id"] != string(featPay) {
			t.Errorf("find_feature(%q) matched %v, want %s", query, got["id"], featPay)
		}
	}
}

// An empty result on a store that HAS features is a real answer ("no feature
// is called that"), and must not be dressed up as a missing scan.
func TestFindFeature_NoMatchOnAPopulatedStoreIsAnEmptyList(t *testing.T) {
	w := seedRepo(t).wire(t)
	sc := w.structured(w.call(2, "find_feature", map[string]any{"query": "zzz-nothing"}))
	if _, bad := sc["no_data"]; bad {
		t.Fatalf("find_feature on a populated store reported no_data: %v", sc)
	}
	if feats, _ := sc["features"].([]any); len(feats) != 0 {
		t.Fatalf("features = %v, want an empty list", feats)
	}
}

// The dynamic tier is the only one that survives interface dispatch, so when
// per-test evidence exists it must win — and the answer must say so, because a
// surface an agent cannot rank by trust is a surface it will over-trust.
func TestFeatureSurface_PrefersDynamicEvidenceAndNamesTheSource(t *testing.T) {
	r := seedRepo(t)
	r.seedPerTestCoverage(t)
	w := r.wire(t)

	sc := w.structured(w.call(2, "feature_surface", map[string]any{"feature_id": string(featPay)}))
	if sc["surface_source"] != audit.SurfaceDynamic {
		t.Fatalf("surface_source = %v, want %q", sc["surface_source"], audit.SurfaceDynamic)
	}
	if note, _ := sc["surface_source_note"].(string); note == "" {
		t.Error("surface_source_note is empty; the agent cannot tell how far to trust the list")
	}
	got := symbolNames(t, sc["symbols"])
	if !got[symPay] || !got[symCharge] {
		t.Errorf("symbols = %v, want the production symbols the test executed", got)
	}
	if got[symLog] {
		t.Errorf("symbols include %s — shared runtime leaked into the feature surface", symLog)
	}
}

// With no execution evidence the call-edge walk is the best available answer,
// and it must be labelled as the weaker thing it is.
func TestFeatureSurface_FallsBackToStaticWalk(t *testing.T) {
	w := seedRepo(t).wire(t)
	sc := w.structured(w.call(2, "feature_surface", map[string]any{"feature_id": string(featPay)}))
	if sc["surface_source"] != audit.SurfaceStatic {
		t.Fatalf("surface_source = %v, want %q", sc["surface_source"], audit.SurfaceStatic)
	}
	got := symbolNames(t, sc["symbols"])
	if !got[symPay] || !got[symCharge] {
		t.Errorf("symbols = %v, want the call-reachable production symbols", got)
	}
	if got[symTestPay] {
		t.Errorf("symbols include the test symbol %s; the surface is production code", symTestPay)
	}
}

func TestFeatureSurface_LinklessFeatureSaysWhatToRun(t *testing.T) {
	w := seedRepo(t).wire(t)
	sc := w.structured(w.call(2, "feature_surface", map[string]any{"feature_id": string(featSearch)}))
	nd, ok := sc["no_data"].(map[string]any)
	if !ok {
		t.Fatalf("feature_surface for an unlinked feature = %v, want a no_data envelope", sc)
	}
	if run, _ := nd["run"].(string); run == "" {
		t.Error("no_data.run is empty; the agent is told nothing is there but not how to fix it")
	}
	if _, bad := sc["symbols"]; bad {
		t.Errorf("no_data result also carries a symbols list: %v", sc)
	}
}

// A feature id that does not exist is the agent's mistake, not a protocol
// violation: the spec puts business-logic failures in the result with
// isError, so the model can read the message and correct itself.
func TestUnknownFeatureIsAToolErrorNotAProtocolError(t *testing.T) {
	w := seedRepo(t).wire(t)
	res := w.call(2, "feature_surface", map[string]any{"feature_id": "no.such.feature"})
	if res["isError"] != true {
		t.Fatalf("feature_surface for an unknown id = %v, want isError true", res)
	}
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatal("isError result carries no content explaining what went wrong")
	}
}

func TestSymbolInfo_ReportsSpanPackageAndOwningFeatures(t *testing.T) {
	w := seedRepo(t).wire(t)
	sc := w.structured(w.call(2, "symbol_info", map[string]any{"qualified_name": symTestPay}))

	if sc["file"] != "pkg/checkout/pay_test.go" || sc["line"] != float64(10) {
		t.Errorf("file:line = %v:%v, want pkg/checkout/pay_test.go:10", sc["file"], sc["line"])
	}
	if sc["end_line"] != float64(15) {
		t.Errorf("end_line = %v, want 15", sc["end_line"])
	}
	if sc["package"] != "checkout" || sc["kind"] != string(shared.KindFunc) {
		t.Errorf("package/kind = %v/%v, want checkout/func", sc["package"], sc["kind"])
	}
	feats, ok := sc["features"].([]any)
	if !ok || len(feats) != 1 {
		t.Fatalf("features = %v, want the one feature this symbol is annotated for", sc["features"])
	}
	f, _ := feats[0].(map[string]any)
	if f["feature_id"] != string(featPay) || f["role"] != string(store.RoleTest) {
		t.Errorf("feature link = %v, want %s/test", f, featPay)
	}
	// doc and signature are not in the index; saying so beats leaving the
	// agent to read their absence as "this symbol has no doc".
	if notes, _ := sc["notes"].([]any); len(notes) == 0 {
		t.Error("notes is empty; the agent is not told which fields the index does not carry")
	}
}

func TestSymbolInfo_UnknownSymbolIsAToolError(t *testing.T) {
	w := seedRepo(t).wire(t)
	if res := w.call(2, "symbol_info", map[string]any{"qualified_name": "nope.Nope"}); res["isError"] != true {
		t.Fatalf("symbol_info for an unknown symbol = %v, want isError true", res)
	}
}

func TestCallersAndCallees_ReportNeighboursWithCallSites(t *testing.T) {
	w := seedRepo(t).wire(t)

	out := w.structured(w.call(2, "callees", map[string]any{"qualified_name": symPay}))
	if got := edgeNames(t, out["callees"]); !got[symCharge] || !got[symLog] {
		t.Errorf("callees(%s) = %v, want charge and the logger", symPay, got)
	}
	in := w.structured(w.call(3, "callers", map[string]any{"qualified_name": symPay}))
	callers, _ := in["callers"].([]any)
	if len(callers) != 1 {
		t.Fatalf("callers(%s) = %v, want exactly the test", symPay, in["callers"])
	}
	c, _ := callers[0].(map[string]any)
	if c["qualified_name"] != symTestPay {
		t.Errorf("caller = %v, want %s", c["qualified_name"], symTestPay)
	}
	site, ok := c["call_site"].(map[string]any)
	if !ok || site["file"] != "pkg/checkout/pay_test.go" || site["line"] != float64(12) {
		t.Errorf("call_site = %v, want pkg/checkout/pay_test.go:12 so the agent can cite it", c["call_site"])
	}
}

// An unbounded callers() on a hot utility blows the agent's context; a SILENT
// truncation is worse, because the agent then reasons from a partial graph
// believing it is complete.
func TestCallers_TruncationIsBoundedAndAnnounced(t *testing.T) {
	r := seedRepo(t)
	for i := 0; i < 12; i++ {
		qn := fmt.Sprintf("pkg/many.Caller%d", i)
		r.insert(t, qn, "pkg/many/many.go", 10+i, nil)
		r.edge(t, qn, symLog, "pkg/many/many.go", 10+i)
	}
	w := r.wire(t)

	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symLog, "limit": 5}))
	callers, _ := sc["callers"].([]any)
	if len(callers) != 5 {
		t.Fatalf("callers = %d rows, want the 5 the limit allows", len(callers))
	}
	tr, ok := sc["truncated"].(map[string]any)
	if !ok {
		t.Fatalf("a cut result carries no truncated block: %v", sc)
	}
	if tr["returned"] != float64(5) || tr["total"] != float64(13) {
		t.Errorf("truncated = %v, want returned 5 of a total 13", tr)
	}
	if note, _ := tr["note"].(string); note == "" {
		t.Error("truncated.note is empty; the agent has no prose warning to carry into its reasoning")
	}

	// The uncut case must NOT carry the block, or "truncated" stops meaning
	// anything.
	full := w.structured(w.call(3, "callers", map[string]any{"qualified_name": symPay}))
	if _, bad := full["truncated"]; bad {
		t.Errorf("an untruncated result carries a truncated block: %v", full)
	}
}

// The per-call limit must not be a way around the server's own cap: an agent
// asking for 100000 rows gets the cap, and is told that it did.
func TestLimitIsClampedToTheServerCap(t *testing.T) {
	r := seedRepo(t)
	for i := 0; i < 12; i++ {
		qn := fmt.Sprintf("pkg/many.Caller%d", i)
		r.insert(t, qn, "pkg/many/many.go", 10+i, nil)
		r.edge(t, qn, symLog, "pkg/many/many.go", 10+i)
	}
	srv, err := New(Options{
		Graph: FromStore(r.s), Coverage: FromStore(r.s), Scorer: FromStore(r.s).Scorer(),
		Limits: Limits{MaxEdges: 4},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := startWire(t, srv)
	w.handshake()

	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symLog, "limit": 100000}))
	if callers, _ := sc["callers"].([]any); len(callers) != 4 {
		t.Fatalf("callers = %d rows, want the server cap of 4 regardless of the requested limit", len(callers))
	}
	if _, ok := sc["truncated"].(map[string]any); !ok {
		t.Error("a capped result carries no truncated block")
	}
}

func TestTestsCovering_NamesTheTestsThatExecutedTheSymbol(t *testing.T) {
	r := seedRepo(t)
	r.seedPerTestCoverage(t)
	w := r.wire(t)

	sc := w.structured(w.call(2, "tests_covering", map[string]any{"qualified_name": symCharge}))
	tests, ok := sc["tests"].([]any)
	if !ok || len(tests) != 1 {
		t.Fatalf("tests_covering(%s) = %v, want the one test that ran it", symCharge, sc)
	}
	got, _ := tests[0].(map[string]any)
	if got["qualified_name"] != symTestPay {
		t.Errorf("test = %v, want %s", got["qualified_name"], symTestPay)
	}
	if got["covered_stmts"] != float64(5) || got["total_stmts"] != float64(10) {
		t.Errorf("statement counts = %v/%v, want 5/10", got["covered_stmts"], got["total_stmts"])
	}
}

// Whole-run coverage records that a symbol ran, not which test ran it. An
// agent asking "what covers this" must be told to re-sync with --per-test
// rather than concluding nothing covers it.
func TestTestsCovering_DistinguishesNoCoverageFromNoPerTestEvidence(t *testing.T) {
	r := seedRepo(t)
	w := r.wire(t)
	sc := w.structured(w.call(2, "tests_covering", map[string]any{"qualified_name": symPay}))
	nd, ok := sc["no_data"].(map[string]any)
	if !ok {
		t.Fatalf("tests_covering with no coverage at all = %v, want no_data", sc)
	}
	if run, _ := nd["run"].(string); !strings.Contains(run, "cov sync") {
		t.Errorf("no_data.run = %q, want it to name `atlas cov sync`", run)
	}

	// Now a run WITHOUT per-test rows: a different gap, a different fix.
	now := time.Now().UTC()
	_, err := r.s.Coverage().InsertRunWithResults(context.Background(), store.CoverageRun{
		Framework: store.FrameworkGoTest, StartedAt: now, FinishedAt: now,
	}, []store.CoverageResult{{SymbolID: ptr(r.ids[symPay]), Status: store.StatusPass}})
	if err != nil {
		t.Fatalf("insert coverage run: %v", err)
	}
	w2 := r.wire(t)
	sc2 := w2.structured(w2.call(2, "tests_covering", map[string]any{"qualified_name": symPay}))
	nd2, ok := sc2["no_data"].(map[string]any)
	if !ok {
		t.Fatalf("tests_covering with union-only coverage = %v, want no_data", sc2)
	}
	if run, _ := nd2["run"].(string); !strings.Contains(run, "--per-test") {
		t.Errorf("no_data.run = %q, want it to name --per-test", run)
	}
}

func TestCoverageFor_ReportsTheFrontierNumbersAndTheirProvenance(t *testing.T) {
	r := seedRepo(t)
	runID := r.seedPerTestCoverage(t)
	w := r.wire(t)

	sc := w.structured(w.call(2, "coverage_for", map[string]any{"feature_id": string(featPay)}))
	score, ok := sc["score"].(float64)
	if !ok || score <= 0 {
		t.Fatalf("score = %v, want a positive coverage score", sc["score"])
	}
	if sc["surface_source"] != audit.SurfaceDynamic {
		t.Errorf("surface_source = %v, want %q", sc["surface_source"], audit.SurfaceDynamic)
	}
	frontier, ok := sc["frontier"].(map[string]any)
	if !ok {
		t.Fatalf("result carries no frontier block: %v", sc)
	}
	ids, _ := frontier["run_ids"].([]any)
	if len(ids) != 1 || ids[0] != float64(runID) {
		t.Errorf("frontier.run_ids = %v, want [%d]", frontier["run_ids"], runID)
	}
}

func TestCoverageFor_NoFrontierSaysWhatToRun(t *testing.T) {
	w := seedRepo(t).wire(t)
	sc := w.structured(w.call(2, "coverage_for", map[string]any{"feature_id": string(featPay)}))
	nd, ok := sc["no_data"].(map[string]any)
	if !ok {
		t.Fatalf("coverage_for with no coverage = %v, want no_data", sc)
	}
	if run, _ := nd["run"].(string); !strings.Contains(run, "cov sync") {
		t.Errorf("no_data.run = %q, want it to name `atlas cov sync`", run)
	}
	if _, bad := sc["score"]; bad {
		t.Errorf("a no_data coverage result still reports a score: %v — 0 reads as 'nothing is covered'", sc)
	}
}

// Every tool answer also arrives as text, because a client that ignores
// structuredContent (most do today) would otherwise show the agent nothing.
func TestEveryToolResultAlsoCarriesSerializedText(t *testing.T) {
	w := seedRepo(t).wire(t)
	res := w.call(2, "symbol_info", map[string]any{"qualified_name": symPay})
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one text block", res["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("content block type = %v, want text", block["type"])
	}
	text, _ := block["text"].(string)
	if !strings.Contains(text, symPay) {
		t.Errorf("text block = %q, want the serialized result", text)
	}
}

func symbolNames(t *testing.T, raw any) map[string]bool {
	t.Helper()
	rows, ok := raw.([]any)
	if !ok {
		t.Fatalf("expected a symbol list, got %v", raw)
	}
	out := map[string]bool{}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		name, _ := m["qualified_name"].(string)
		out[name] = true
	}
	return out
}

func edgeNames(t *testing.T, raw any) map[string]bool { return symbolNames(t, raw) }
