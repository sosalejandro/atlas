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

// findLimit returns the limit with this code, failing if it is absent.
func findLimit(t *testing.T, res Result, code string) Limit {
	t.Helper()
	for _, l := range res.Limits {
		if l.Code == code {
			return l
		}
	}
	t.Fatalf("no limit %q", code)
	return Limit{}
}

// A coverage run that measured a capability's symbols and recorded none of
// them executing is a MEASUREMENT, and the strongest thing this report can
// say about that capability. Downgrading it to "a test file sits in the same
// directory" -- which the code did, by consulting coverage only for a
// positive -- turns the measured negative into a vague positive and loses
// the only finding the reader could have acted on.
func TestInfer_MeasuredNonExecutionOutranksColocation(t *testing.T) {
	in := Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "orders.Place", "internal/orders/place.go"),
			// A test file sits right beside it -- the colocation signal
			// that used to win.
			sym(2, "orders.TestPlace", "internal/orders/place_test.go"),
		},
		Coverage: CoverageEvidence{
			Available: true,
			Executed:  map[int64]bool{},
			Measured:  map[int64]bool{1: true},
		},
	}
	c := findCap(t, Infer(in), "internal.orders")
	if c.TestEvidence != TestEvidenceNotExecuted {
		t.Errorf("test evidence = %q, want %q -- a measured negative was downgraded to colocation",
			c.TestEvidence, TestEvidenceNotExecuted)
	}

	// The positive is still read off the same run.
	in.Coverage.Executed = map[int64]bool{1: true}
	if c := findCap(t, Infer(in), "internal.orders"); c.TestEvidence != TestEvidenceExecution {
		t.Errorf("test evidence = %q, want %q", c.TestEvidence, TestEvidenceExecution)
	}
}

// The negative is only a measurement for symbols the run actually reported
// on. A Go coverprofile ingested into a Go+TypeScript repo measures half the
// tree, and claiming the other half was measured and dead would be an
// assertion about something nothing looked at.
func TestInfer_UnmeasuredSymbolsAreNotReportedAsNotExecuted(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "web.Render", "web/render.ts"),
			sym(2, "web.TestRender", "web/render.test.ts"),
		},
		Coverage: CoverageEvidence{
			Available: true,
			Executed:  map[int64]bool{99: true},
			Measured:  map[int64]bool{99: true},
		},
	})
	if c := findCap(t, res, "root.web"); c.TestEvidence != TestEvidenceColocated {
		t.Errorf("test evidence = %q, want %q -- nothing measured this capability",
			c.TestEvidence, TestEvidenceColocated)
	}
}

// A route the coverage run measured and found dead belongs in the untested
// finding, and it is the strongest entry in it. Filtering on "none" alone
// dropped exactly the endpoints atlas had a measurement for.
func TestFindings_UntestedRoutesIncludeMeasuredNonExecution(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			sym(1, "api.CreateMeasurement", "internal/api/measurements.go"),
			sym(2, "api.TestCreateMeasurement", "internal/api/measurements_test.go"),
		},
		Routes: []Route{{
			Method: "POST", Path: "/measurements",
			HandlerSymbolID: 1, HandlerName: "api.CreateMeasurement",
			FilePath: "internal/api/router.go", Line: 42,
		}},
		Coverage: CoverageEvidence{
			Available: true,
			Executed:  map[int64]bool{},
			Measured:  map[int64]bool{1: true},
		},
	})
	f := findFinding(t, res, "untested-routes")
	if f.Count != 1 {
		t.Fatalf("untested-routes Count = %d, want 1", f.Count)
	}
	// The citation has to say WHICH claim it is: measured-and-dead and
	// nothing-known-at-all are both on this list and are not the same fact.
	details := strings.Join(evidenceDetails(f), " | ")
	if !strings.Contains(details, string(TestEvidenceNotExecuted)) {
		t.Errorf("citation does not record the grade of the evidence: %s", details)
	}
}

// The scan reports two different things and must not add them together. A
// scanner warning is a diagnostic -- on this repository most of them are
// name-collision notices about symbols that WERE indexed -- and presenting
// the warning count as "files atlas could not read" states a number nothing
// measured.
func TestLimits_ScannerWarningsAreNotReportedAsUnreadFiles(t *testing.T) {
	res := Infer(Input{
		Root:          "/repo",
		Symbols:       []store.SymbolRow{sym(1, "orders.Place", "internal/orders/place.go")},
		FilesExcluded: 26,
		ScannerWarnings: []string{
			"symbol name collision: cmd.init declared in both cmd/a.go and cmd/b.go — the second is indexed under a package-qualified id",
			"no router signal detected (react-router, tanstack, or expo)",
		},
	})
	l := findLimit(t, res, "scan-incomplete")
	lower := strings.ToLower(l.Detail)
	for _, phrase := range []string{"could not read 2", "and could not read", "2 files"} {
		if strings.Contains(lower, phrase) {
			t.Errorf("scan limit presents the warning count as unread files (%q): %s", phrase, l.Detail)
		}
	}
	if !strings.Contains(l.Detail, "26 files") {
		t.Errorf("scan limit lost the excluded-file count, which IS measured: %s", l.Detail)
	}
	if !strings.Contains(l.Detail, "2 scanner warnings") {
		t.Errorf("scan limit does not report the warnings as warnings: %s", l.Detail)
	}
}

// Claiming a directory whose derived id already belongs to a capability
// proposed from another signal attaches its symbols to that capability. That
// merge is defensible; doing it invisibly is not, because the reader is then
// shown route or test-name evidence above a symbol list a directory sweep
// filled in.
func TestInfer_DirectoryMergeIntoAnExistingCapabilityIsRecorded(t *testing.T) {
	res := Infer(Input{
		Root: "/repo",
		Symbols: []store.SymbolRow{
			// Two tests agreeing on "place" propose orders.place from the
			// directory internal/orders, claiming only the matching symbol.
			sym(1, "orders.PlaceOrder", "internal/orders/place.go"),
			sym(2, "orders.TestPlaceIdempotent", "internal/orders/place_test.go"),
			sym(3, "orders.TestPlaceRetry", "internal/orders/place_test.go"),
			// This one no cluster claims, and its directory-derived id is
			// also "orders.place" -- so the fallback stage merges it in.
			sym(4, "orders.Settle", "internal/orders/place/settle.go"),
		},
	})
	c := findCap(t, res, "orders.place")
	if c.Source != SourceTestName {
		t.Fatalf("capability source = %q, want %q (the stronger signal keeps the proposal)", c.Source, SourceTestName)
	}
	if c.Symbols != 2 {
		t.Fatalf("capability holds %d symbols, want 2 (the cluster's plus the merged directory's)", c.Symbols)
	}
	var merged bool
	for _, e := range c.Evidence {
		if strings.HasPrefix(e.Detail, "merged in:") {
			merged = true
		}
	}
	if !merged {
		t.Errorf("directory symbols were merged into a %s capability with no trace in the evidence: %v",
			c.Source, evidenceDetailsOf(c))
	}
}

func evidenceDetailsOf(c Capability) []string {
	out := make([]string, 0, len(c.Evidence))
	for _, e := range c.Evidence {
		out = append(out, e.Detail)
	}
	return out
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
