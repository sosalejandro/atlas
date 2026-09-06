package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/store"
)

// Symbols for the two surface tiers below `static`, which the rest of the
// suite never reaches: a test stub whose package holds production code
// (package-anchor), and one whose package is unknown (direct-links).
const (
	symTestSearch  = "pkg/search.TestIndex"
	symSearchIndex = "pkg/search.Rebuild"
	symTestOrphan  = "pkg/orphan.TestOrphan"
)

// ---------------------------------------------------------------------------
// The two weakest surface tiers
// ---------------------------------------------------------------------------

// seedPackageAnchorFeature annotates featSearch on a test stub that calls
// nothing, in a package that does hold production code. That is the exact
// shape the package-anchor tier exists for: no execution evidence and no call
// edge to walk, but the package the annotation sits in is a better answer than
// the stub on its own.
func (r *repo) seedPackageAnchorFeature(t *testing.T) {
	t.Helper()
	pkgSearch := "search"
	r.insert(t, symTestSearch, "pkg/search/index_test.go", 8, &pkgSearch)
	r.insert(t, symSearchIndex, "pkg/search/index.go", 30, &pkgSearch)
	r.link(t, featSearch, symTestSearch, store.RoleTest)
}

// With no execution evidence and no call edge to walk, the annotated symbol's
// package is the answer — and it must be labelled `package-anchor`, because
// whole-package granularity may include code the feature has nothing to do
// with, and an agent that cannot see that will read the list as precise.
func TestFeatureSurface_FallsBackToThePackageAnchor(t *testing.T) {
	r := seedRepo(t)
	r.seedPackageAnchorFeature(t)
	w := r.wire(t)

	sc := w.structured(w.call(2, "feature_surface", map[string]any{"feature_id": string(featSearch)}))
	if sc["surface_source"] != audit.SurfacePackageAnchor {
		t.Fatalf("surface_source = %v, want %q", sc["surface_source"], audit.SurfacePackageAnchor)
	}
	if note, _ := sc["surface_source_note"].(string); !strings.Contains(note, "package") {
		t.Errorf("surface_source_note = %q, want it to name the whole-package granularity", note)
	}
	got := symbolNames(t, sc["symbols"])
	if !got[symSearchIndex] {
		t.Errorf("symbols = %v, want the production symbol of the annotated package", got)
	}
	if got[symTestSearch] {
		t.Errorf("symbols include the test stub %s; the anchored surface is production code", symTestSearch)
	}
	// The anchor is per-package: another package's production code is not this
	// feature's implementation.
	if got[symPay] || got[symCharge] {
		t.Errorf("symbols = %v, want only symbols from the annotated package", got)
	}
}

// direct-links is the honest answer when nothing else is derivable: a test stub
// with no package to anchor to, no call edges and no coverage. Reporting it as
// anything stronger tells an agent that a human's annotation is evidence.
func TestFeatureSurface_FallsBackToDirectLinks(t *testing.T) {
	r := seedRepo(t)
	// No package, so the package-anchor tier has nothing to anchor to — which
	// is what leaves direct-links as the only remaining answer.
	r.insert(t, symTestOrphan, "pkg/orphan/orphan_test.go", 3, nil)
	r.link(t, featSearch, symTestOrphan, store.RoleTest)
	w := r.wire(t)

	sc := w.structured(w.call(2, "feature_surface", map[string]any{"feature_id": string(featSearch)}))
	if sc["surface_source"] != audit.SurfaceDirectLinks {
		t.Fatalf("surface_source = %v, want %q", sc["surface_source"], audit.SurfaceDirectLinks)
	}
	if note, _ := sc["surface_source_note"].(string); !strings.Contains(note, "annotated") {
		t.Errorf("surface_source_note = %q, want it to say only annotated symbols are listed", note)
	}
	got := symbolNames(t, sc["symbols"])
	if len(got) != 1 || !got[symTestOrphan] {
		t.Errorf("symbols = %v, want exactly the one annotated symbol", got)
	}
}

// ---------------------------------------------------------------------------
// no-features-annotated
// ---------------------------------------------------------------------------

// "Nothing is indexed" and "everything is indexed and none of it is annotated"
// both look like an empty feature list from here, and are repaired by entirely
// different actions: one by a scan that would change nothing, one by an
// annotation.
func TestFindFeature_IndexedButUnannotatedIsItsOwnGap(t *testing.T) {
	r := &repo{s: openTestStore(t), ids: map[string]int64{}}
	r.insert(t, symPay, "pkg/checkout/pay.go", 20, nil)
	w := r.wire(t)

	sc := w.structured(w.call(2, "find_feature", map[string]any{"query": "checkout"}))
	nd, ok := sc["no_data"].(map[string]any)
	if !ok {
		t.Fatalf("find_feature on an unannotated store = %v, want a no_data envelope", sc)
	}
	if nd["reason"] != ReasonNoFeatures {
		t.Fatalf("no_data.reason = %v, want %q — an index-empty answer sends the agent to re-run a scan "+
			"that would change nothing", nd["reason"], ReasonNoFeatures)
	}
	if run, _ := nd["run"].(string); !strings.Contains(run, "@atlas:feature") {
		t.Errorf("no_data.run = %q, want it to name the annotation that would fix this", run)
	}
	if _, bad := sc["features"]; bad {
		t.Errorf("no_data result also carries a features list: %v", sc)
	}
}

// ---------------------------------------------------------------------------
// Distinct counts, freshness on tests_covering, truncation honesty
// ---------------------------------------------------------------------------

// The edges table holds one row per call SITE. "412 callers" is read as the
// answer to "is this safe to change", and answering it with a site count
// inflates the blast radius by however often each caller happens to call.
func TestSymbolInfo_CountsDistinctSymbolsNotCallSites(t *testing.T) {
	r := seedRepo(t)
	// The same caller, three more times, from three more lines.
	r.edge(t, symTestPay, symPay, "pkg/checkout/pay_test.go", 13)
	r.edge(t, symTestPay, symPay, "pkg/checkout/pay_test.go", 14)
	r.edge(t, symTestPay, symPay, "pkg/checkout/pay_test.go", 15)
	// And the same callee twice more from Pay.
	r.edge(t, symPay, symCharge, "pkg/checkout/pay.go", 27)
	r.edge(t, symPay, symCharge, "pkg/checkout/pay.go", 28)
	w := r.wire(t)

	sc := w.structured(w.call(2, "symbol_info", map[string]any{"qualified_name": symPay}))
	if sc["caller_count"] != float64(1) {
		t.Errorf("caller_count = %v, want 1 — one function calling it four times is one caller", sc["caller_count"])
	}
	if sc["callee_count"] != float64(2) {
		t.Errorf("callee_count = %v, want 2 distinct callees (charge and the logger)", sc["callee_count"])
	}

	// The list tools still return SITES, because an agent citing a call needs
	// its file:line. The counts and the row counts therefore differ, and the
	// notes have to say which is which.
	list := w.structured(w.call(3, "callers", map[string]any{"qualified_name": symPay}))
	if rows, _ := list["callers"].([]any); len(rows) != 4 {
		t.Fatalf("callers = %d rows, want the 4 call sites", len(rows))
	}
	if !notesMention(t, sc["notes"], "DISTINCT") {
		t.Error("notes do not say the counts are of distinct symbols; a model will read them as call sites")
	}
}

func notesMention(t *testing.T, raw any, substr string) bool {
	t.Helper()
	notes, ok := raw.([]any)
	if !ok {
		t.Fatalf("notes = %v, want a list", raw)
	}
	for _, n := range notes {
		if s, _ := n.(string); strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// tests_covering hands the agent a test's file and line like every other
// span-citing answer, so it owes the same statement about whether those spans
// still describe the working tree.
func TestTestsCovering_CarriesIndexFreshnessForTheSpansItCites(t *testing.T) {
	r := seedRepo(t)
	r.seedPerTestCoverage(t)

	var asked []string
	idx := FromStore(r.s)
	srv, err := New(Options{
		Graph: idx, Coverage: idx, Scorer: idx.Scorer(),
		Freshness: func(_ context.Context, paths []string) (map[string]string, error) {
			asked = append(asked, paths...)
			return map[string]string{"pkg/checkout/pay_test.go": "stale"}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := startWire(t, srv)
	w.handshake()

	sc := w.structured(w.call(2, "tests_covering", map[string]any{"qualified_name": symCharge}))
	fresh, ok := sc["index_freshness"].(map[string]any)
	if !ok {
		t.Fatalf("tests_covering cites file:line but carries no index_freshness: %v", sc)
	}
	if bad, _ := fresh["untrustworthy_files"].([]any); len(bad) != 1 {
		t.Fatalf("untrustworthy_files = %v, want the stale test file the answer points at", fresh)
	}
	if len(asked) != 1 || asked[0] != "pkg/checkout/pay_test.go" {
		t.Errorf("freshness was asked about %v, want the files this answer cites", asked)
	}
}

// The truncation note is an instruction a model will follow. Telling it to
// "raise limit" when every inputSchema is additionalProperties:false with no
// cursor, offset or page token is telling it to do something the protocol
// cannot serve, and it burns a turn discovering that.
func TestTruncationNoteDoesNotPromiseAPageThisServerCannotServe(t *testing.T) {
	r := seedRepo(t)
	for i := 0; i < 12; i++ {
		qn := fmt.Sprintf("pkg/many.Caller%d", i)
		r.insert(t, qn, "pkg/many/many.go", 10+i, nil)
		r.edge(t, qn, symLog, "pkg/many/many.go", 10+i)
	}
	w := r.wire(t)

	sc := w.structured(w.call(2, "callers", map[string]any{"qualified_name": symLog, "limit": 5}))
	tr, ok := sc["truncated"].(map[string]any)
	if !ok {
		t.Fatalf("a cut result carries no truncated block: %v", sc)
	}
	note, _ := tr["note"].(string)
	if !strings.Contains(note, "NO cursor") {
		t.Errorf("truncated.note = %q, want it to say plainly that the remainder is not retrievable here", note)
	}
	if !strings.Contains(note, "narrower") {
		t.Errorf("truncated.note = %q, want it to name what the model should do instead", note)
	}

	// And the claim has to stay true: no tool may advertise a paging argument
	// while the note says there is none.
	w.sendJSON(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"})
	res, _ := w.recv()["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("tools/list returned nothing: %v", res)
	}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for _, paging := range []string{"cursor", "offset", "page", "page_token", "after"} {
			if _, found := props[paging]; found {
				t.Errorf("%v declares a %q argument while the truncation note says there is no cursor — "+
					"one of the two is lying to the model", tool["name"], paging)
			}
		}
	}
}
