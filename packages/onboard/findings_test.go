package onboard

import (
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/sqlops"
	"github.com/sosalejandro/atlas/packages/store"
)

// fakeChurn returns a fixed score for every file set, so the ranking can be
// tested without a git repository standing in the way.
type fakeChurn struct {
	score  float64
	status string
}

func (f fakeChurn) ForFiles(paths []string) churn.FeatureChurn {
	return churn.FeatureChurn{
		Score: f.score, Status: f.status, HotFile: paths[0],
		Commits: 9, Authors: 2, LastCommit: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
	}
}

func findFinding(t *testing.T, res Result, code string) Finding {
	t.Helper()
	for _, f := range res.Findings {
		if f.Code == code {
			return f
		}
	}
	var got []string
	for _, f := range res.Findings {
		got = append(got, f.Code)
	}
	t.Fatalf("no finding %q; got %v", code, got)
	return Finding{}
}

func hasFinding(res Result, code string) bool {
	for _, f := range res.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// Two capabilities writing one table is coupling that neither the import
// graph nor the directory tree shows. It is the finding this whole feature
// exists to produce.
func TestFindings_SharedTableWrites(t *testing.T) {
	res := Infer(Input{
		Root:       "/repo",
		SQLScanned: true,
		Symbols: []store.SymbolRow{
			sym(1, "orders.Place", "internal/orders/place.go"),
			sym(2, "billing.Settle", "internal/billing/settle.go"),
		},
		SQLOps: []store.SQLOperationRecord{
			{
				FilePath: "internal/orders/place.go", Resolved: true, Kind: "insert",
				Tables: []store.SQLTableAccess{{Table: "ledger", Access: "write"}},
			},
			{
				FilePath: "internal/billing/settle.go", Resolved: true, Kind: "update",
				Tables: []store.SQLTableAccess{
					{Table: "ledger", Access: "write"},
					{Table: "invoices", Access: "write"},
				},
			},
		},
	})
	f := findFinding(t, res, "shared-table-writes")
	if f.Count != 1 {
		t.Errorf("shared-table-writes Count = %d, want 1 (only ledger has two writers)", f.Count)
	}
	if !f.Provisional {
		t.Error("a finding computed over inferred groupings must say so")
	}
	joined := strings.Join(evidenceDetails(f), " | ")
	if !strings.Contains(joined, "ledger") {
		t.Errorf("evidence does not name the shared table: %s", joined)
	}
	if !strings.Contains(joined, ProvisionalPrefix) {
		t.Errorf("evidence names capabilities without the provisional namespace: %s", joined)
	}

	sole := findFinding(t, res, "sole-table-owners")
	if sole.Count != 1 {
		t.Errorf("sole-table-owners Count = %d, want 1 (invoices)", sole.Count)
	}
}

// The "changing and untested" claim needs both halves. With no history the
// finding must disappear entirely rather than degrade into a ranking of
// untested code, which is a different and much less interesting statement.
func TestFindings_ChangingAndUntestedNeedsHistory(t *testing.T) {
	in := Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
	}
	if hasFinding(Infer(in), "changing-and-untested") {
		t.Error("claimed code is changing with no git history mined")
	}

	in.Churn = fakeChurn{score: 88, status: churn.StatusKnown}
	res := Infer(in)
	if hasLimit(res, "shallow-history") {
		t.Error("reported history as unusable while ranking capabilities on it")
	}
	f := findFinding(t, res, "changing-and-untested")
	if f.Count != 1 {
		t.Errorf("Count = %d, want 1", f.Count)
	}
	if f.Severity != SeverityHigh {
		t.Errorf("Severity = %q, want high", f.Severity)
	}

	// A hot capability that a test file sits beside is not this finding.
	in.Symbols = append(in.Symbols, sym(2, "orders.TestPlace", "internal/orders/place_test.go"))
	if hasFinding(Infer(in), "changing-and-untested") {
		t.Error("a capability with colocated tests was reported as having none")
	}
}

// An unknown churn status is the neutral placeholder churn returns when git
// cannot speak, not a measurement. Ranking on it would put every file in a
// shallow CI clone at the top of the list.
func TestFindings_UnknownChurnIsNotAMeasurement(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
		Churn:   fakeChurn{score: 99, status: churn.StatusUnknown},
	})
	if hasFinding(res, "changing-and-untested") {
		t.Error("ranked a capability on a churn score git never measured")
	}
	if !hasLimit(res, "shallow-history") {
		t.Error("unknown churn must be stated as a limit")
	}
}

func TestFindings_UntestedRoutes(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "api.CreateMeasurement", "internal/api/measurements.go")},
		Routes: []Route{{
			Method: "POST", Path: "/measurements", HandlerSymbolID: 1,
			HandlerName: "api.CreateMeasurement", FilePath: "internal/api/router.go", Line: 7,
		}},
	})
	f := findFinding(t, res, "untested-routes")
	if f.Count != 1 || f.Severity != SeverityHigh {
		t.Errorf("untested-routes = %+v, want one high-severity hit", f)
	}
	if f.Next == "" {
		t.Error("a finding the user can act on must carry the command that acts on it")
	}
}

func TestFindings_SQLAdvisoriesRollUpByCode(t *testing.T) {
	res := Infer(Input{
		Root:       "/repo",
		SQLScanned: true,
		Symbols:    []store.SymbolRow{sym(1, "store.List", "packages/store/list.go")},
		Advisories: []sqlops.Advisory{
			{Code: sqlops.CodeUnboundedList, Message: "no LIMIT", Position: shared.FilePosition{Path: "a.go", Line: 3}},
			{Code: sqlops.CodeUnboundedList, Message: "no LIMIT", Position: shared.FilePosition{Path: "b.go", Line: 4}},
			{Code: sqlops.CodeSelectStar, Message: "SELECT *", Position: shared.FilePosition{Path: "c.go", Line: 5}},
		},
	})
	f := findFinding(t, res, "sql-advisories")
	if f.Count != 3 {
		t.Errorf("Count = %d, want 3", f.Count)
	}
	// The most frequent code leads: it is the one worth a policy decision.
	if got := evidenceDetails(f); len(got) == 0 || !strings.HasPrefix(got[0], sqlops.CodeUnboundedList) {
		t.Errorf("advisory evidence not ordered by frequency: %v", got)
	}
}

// Dead-code candidates found only in fixtures and tests are noise: a
// testdata tree exists to be unreferenced.
func TestFindings_DeadCodeSkipsFixtures(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
		Dead: []store.DeadCodeCandidate{
			{Symbol: sym(9, "fix.Unused", "packages/x/testdata/fix.go")},
			{Symbol: sym(8, "t.Helper", "internal/orders/place_test.go")},
		},
	})
	if hasFinding(res, "dead-code-candidates") {
		t.Error("reported dead code found only in fixtures and tests")
	}
}

// The headline count must be the count of what the finding is about. Counting
// every candidate while showing only the production ones makes the number
// unreproducible from the evidence beside it.
func TestFindings_DeadCodeCountsOnlyWhatItReports(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
		Dead: []store.DeadCodeCandidate{
			{Symbol: sym(9, "fix.Unused", "packages/x/testdata/fix.go")},
			{Symbol: sym(8, "t.Helper", "internal/orders/place_test.go")},
			{Symbol: sym(7, "orders.Orphan", "internal/orders/orphan.go")},
		},
	})
	f := findFinding(t, res, "dead-code-candidates")
	if f.Count != 1 {
		t.Errorf("Count = %d, want 1 (the one production candidate)", f.Count)
	}
}

// The limits section is not optional: with nothing ingested and nothing
// mined, the report must still say what it could not see.
func TestLimits_StatedEvenWhenNothingIsAvailable(t *testing.T) {
	res := Infer(Input{
		Root:    "/repo",
		Symbols: []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
	})
	for _, code := range []string{
		"no-execution-evidence", "sql-not-scanned", "no-routes-found",
		"no-history", "inference-is-not-declaration",
	} {
		if !hasLimit(res, code) {
			t.Errorf("missing limit %q", code)
		}
	}
}

func hasLimit(res Result, code string) bool {
	for _, l := range res.Limits {
		if l.Code == code {
			return true
		}
	}
	return false
}

func evidenceDetails(f Finding) []string {
	out := make([]string, 0, len(f.Evidence))
	for _, e := range f.Evidence {
		out = append(out, e.Detail)
	}
	return out
}
