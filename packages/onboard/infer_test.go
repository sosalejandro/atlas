package onboard

import (
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// sym is a terse SymbolRow builder. ids are assigned by position so a test
// can refer to them without bookkeeping.
func sym(id int64, qn, path string) store.SymbolRow {
	return store.SymbolRow{
		ID: id, QualifiedName: shared.SymbolID(qn),
		Kind: shared.KindFunc, FilePath: path, Line: int(id),
	}
}

func findCap(t *testing.T, res Result, id string) Capability {
	t.Helper()
	for _, c := range res.Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no provisional capability %q; got %v", id, capIDs(res))
	return Capability{}
}

func capIDs(res Result) []string {
	out := make([]string, 0, len(res.Capabilities))
	for _, c := range res.Capabilities {
		out = append(out, c.ID)
	}
	return out
}

// The whole product rests on this: nothing Infer produces may be mistakable
// for something a human declared. Infer takes no store handle at all, so it
// physically cannot write the registry -- what this test pins is the weaker
// but still load-bearing half, that every record it hands back is labelled.
func TestInfer_EverythingIsLabelledProvisional(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "store.Open", "packages/store/store.go"),
			sym(2, "cli.Run", "internal/cli/root.go"),
		},
	})
	if len(res.Capabilities) == 0 {
		t.Fatal("expected provisional capabilities from directory structure alone")
	}
	for _, c := range res.Capabilities {
		if !c.Provisional {
			t.Errorf("capability %s is not marked provisional", c.ID)
		}
		if !strings.HasPrefix(c.Ref(), ProvisionalPrefix) {
			t.Errorf("capability %s Ref()=%q lacks the %q namespace", c.ID, c.Ref(), ProvisionalPrefix)
		}
	}
}

// Existing annotations are adopted, never duplicated: a symbol that already
// belongs to a declared feature must not reappear inside a proposal, or the
// user is asked to accept a grouping they already made.
func TestInfer_DeclaredSymbolsAreNotReproposed(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "billing.Charge", "internal/billing/charge.go"),
			sym(2, "billing.Refund", "internal/billing/refund.go"),
		},
		Declared: []DeclaredFeature{{
			ID: "billing.charge", Title: "Charge a card",
			SymbolIDs: []int64{1},
		}},
	})
	c := findCap(t, res, "internal.billing")
	for _, s := range c.SymbolIDs {
		if s == 1 {
			t.Fatalf("declared symbol 1 was re-proposed inside %s", c.ID)
		}
	}
	if c.Symbols != 1 {
		t.Errorf("capability %s claims %d symbols, want 1 (the undeclared one)", c.ID, c.Symbols)
	}
	if res.Stats.DeclaredFeatures != 1 {
		t.Errorf("DeclaredFeatures = %d, want 1", res.Stats.DeclaredFeatures)
	}
	// The coverage fraction the report prints is proposed/undeclared, and a
	// denominator that counts declared or test symbols makes it exceed 100%.
	if res.Stats.UndeclaredSymbols != 1 {
		t.Errorf("UndeclaredSymbols = %d, want 1", res.Stats.UndeclaredSymbols)
	}
	if res.Stats.SymbolsProposed > res.Stats.UndeclaredSymbols {
		t.Errorf("proposed %d of %d undeclared symbols -- the fraction is impossible",
			res.Stats.SymbolsProposed, res.Stats.UndeclaredSymbols)
	}
}

// A route is the most legible capability a newcomer can be shown, so it wins
// over the directory grouping and takes its handler with it.
func TestInfer_RoutesClaimTheirHandlers(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "api.CreateMeasurement", "internal/api/measurements.go"),
			sym(2, "api.helper", "internal/api/helper.go"),
		},
		Routes: []Route{{
			Method: "POST", Path: "/measurements",
			HandlerSymbolID: 1, HandlerName: "api.CreateMeasurement",
			FilePath: "internal/api/router.go", Line: 42,
		}},
	})
	rc := findCap(t, res, "measurements.create")
	if rc.Source != SourceRoute {
		t.Errorf("route capability source = %q, want %q", rc.Source, SourceRoute)
	}
	if len(rc.SymbolIDs) != 1 || rc.SymbolIDs[0] != 1 {
		t.Errorf("route capability symbols = %v, want [1]", rc.SymbolIDs)
	}
	if len(rc.Evidence) == 0 || rc.Evidence[0].Kind != SourceRoute {
		t.Fatalf("route capability cites no route evidence: %+v", rc.Evidence)
	}
	if rc.Evidence[0].Line != 42 || rc.Evidence[0].File != "internal/api/router.go" {
		t.Errorf("route evidence does not cite the registration site: %+v", rc.Evidence[0])
	}
	// The handler must not also appear under the directory grouping.
	dc := findCap(t, res, "internal.api")
	for _, s := range dc.SymbolIDs {
		if s == 1 {
			t.Fatalf("handler symbol appears in both %s and %s", rc.ID, dc.ID)
		}
	}
}

// Test names group production code when two or more tests agree on the
// subject. A cluster that resolves to no production symbol is dropped
// before display rather than shown as an empty proposal.
func TestInfer_TestNameClusters(t *testing.T) {
	in := Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "billing.CheckoutSession", "internal/billing/checkout.go"),
			sym(2, "billing.Ledger", "internal/billing/ledger.go"),
			sym(3, "billing.TestCheckoutIdempotent", "internal/billing/checkout_test.go"),
			sym(4, "billing.TestCheckoutRetry", "internal/billing/checkout_test.go"),
			// One lone test about something with no production symbol:
			// the cluster must not survive.
			sym(5, "billing.TestPhantomOne", "internal/billing/phantom_test.go"),
			sym(6, "billing.TestPhantomTwo", "internal/billing/phantom_test.go"),
		},
	}
	res := Infer(in)
	c := findCap(t, res, "billing.checkout")
	if len(c.SymbolIDs) != 1 || c.SymbolIDs[0] != 1 {
		t.Errorf("test-name cluster claimed %v, want [1]", c.SymbolIDs)
	}
	if c.Source != SourceTestName {
		t.Errorf("cluster source = %q, want %q", c.Source, SourceTestName)
	}
	for _, got := range capIDs(res) {
		if got == "billing.phantom" {
			t.Error("a cluster that resolves to no production symbol was displayed")
		}
	}
}

// Test symbols are never proposed as capabilities of their own -- a
// capability made of tests is a grouping nobody can act on.
func TestInfer_TestFilesDoNotBecomeCapabilities(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "billing.TestOnly", "internal/billing/only_test.go")},
	})
	if len(res.Capabilities) != 0 {
		t.Errorf("expected no capabilities from a test-only tree; got %v", capIDs(res))
	}
}

// The data footprint is the half of a capability that a directory listing
// cannot show, so it has to survive the roll-up -- including the count of
// queries atlas could not read, without which the table set reads as
// complete when it is a lower bound.
func TestInfer_SQLFootprintRollsUp(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "store.Insert", "packages/store/ingest.go")},
		SQLOps: []store.SQLOperationRecord{
			{
				FilePath: "packages/store/ingest.go", Line: 10, Resolved: true, Kind: "insert",
				Tables: []store.SQLTableAccess{{Table: "symbols", Access: "write"}},
			},
			{
				FilePath: "packages/store/ingest.go", Line: 20, Resolved: true, Kind: "select",
				Tables: []store.SQLTableAccess{{Table: "features", Access: "read"}},
			},
			{FilePath: "packages/store/ingest.go", Line: 30, Resolved: false},
		},
	})
	c := findCap(t, res, "packages.store")
	if len(c.Writes) != 1 || c.Writes[0] != "symbols" {
		t.Errorf("writes = %v, want [symbols]", c.Writes)
	}
	if len(c.Reads) != 1 || c.Reads[0] != "features" {
		t.Errorf("reads = %v, want [features]", c.Reads)
	}
	if c.SQLUnresolved != 1 {
		t.Errorf("SQLUnresolved = %d, want 1 -- the table set is a lower bound and must say so", c.SQLUnresolved)
	}
}

// Generated query files (sqlc's .sql inputs) hold no indexed symbols, so an
// operation read out of one links to neither a symbol nor a file any
// capability owns. Dropping it silently costs the data layer its whole
// footprint, which is the single most useful column in the map.
func TestInfer_SQLFromQueryFilesAttachesToTheNearestCapability(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "store.Insert", "packages/store/ingest.go")},
		SQLOps: []store.SQLOperationRecord{{
			FilePath: "packages/store/queries/coverage.sql", Line: 3, Resolved: true, Kind: "insert",
			Tables: []store.SQLTableAccess{{Table: "coverage_runs", Access: "write"}},
		}},
	})
	c := findCap(t, res, "packages.store")
	if len(c.Writes) != 1 || c.Writes[0] != "coverage_runs" {
		t.Errorf("writes = %v, want [coverage_runs] attributed to the enclosing package", c.Writes)
	}
}

// Every provisional id must be promotable. This is the property the whole
// promote path depends on.
func TestInfer_EveryIDIsPromotable(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "cmd.Main", "cmd/main.go"),
			sym(2, "x.Y", "weird dir/Sub Dir/f.go"),
			sym(3, "z.W", "f.go"),
		},
	})
	if len(res.Capabilities) == 0 {
		t.Fatal("no capabilities")
	}
	for _, c := range res.Capabilities {
		if !validID(c.ID) {
			t.Errorf("capability id %q is not promotable", c.ID)
		}
		if c.Anchor == nil {
			t.Errorf("capability %s has no anchor symbol, so it cannot be promoted", c.ID)
		}
	}
}
