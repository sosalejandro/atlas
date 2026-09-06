package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// trendFixture owns a tempdir + .atlas/atlas.db and resets the package
// singletons, mirroring deadFixture. NOT parallel-safe — see NewRootCmd.
type trendFixture struct {
	root   string
	dbPath string
}

func newTrendFixture(t *testing.T) *trendFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	dbPath := filepath.Join(dir, ".atlas", "atlas.db")
	loaded = Config{repoRoot: dir, DBPath: dbPath}
	flags = globalFlags{DBPath: dbPath}
	return &trendFixture{root: dir, dbPath: dbPath}
}

func trendDay(n int) time.Time {
	return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

func ptr(v float64) *float64 { return &v }

func (f *trendFixture) record(t *testing.T, p store.HistoryPoint) {
	t.Helper()
	s, err := store.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.History().Record(context.Background(), p); err != nil {
		t.Fatalf("Record %s: %v", p.CommitSHA, err)
	}
}

// feature registers a feature id so `--feature` accepts it. `atlas trend`
// validates the id against the features table, so a series test that wants a
// feature scope has to declare the feature exists.
func (f *trendFixture) feature(t *testing.T, ids ...string) {
	t.Helper()
	s, err := store.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	for _, id := range ids {
		if err := s.Features().Upsert(context.Background(), store.Feature{
			ID: shared.FeatureID(id), Title: id, Kind: store.FeatureKindFeature,
		}); err != nil {
			t.Fatalf("upsert feature %s: %v", id, err)
		}
	}
}

// lineContaining returns the single output line holding needle, failing the
// test when there is not exactly one. Asserting on a whole-output substring
// would let "70.00" satisfy a check meant for a zero score.
func lineContaining(t *testing.T, out, needle string) string {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one line containing %q, got %d:\n%s", needle, len(hits), out)
	}
	return hits[0]
}

func runTrendCmd(t *testing.T, fix *trendFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	loaded = Config{repoRoot: fix.root, DBPath: fix.dbPath}
	flags = globalFlags{DBPath: fix.dbPath}

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"trend", "--db-path", fix.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// The command must be reachable from the root tree; a feature the user
// cannot invoke is not delivered.
func TestTrend_RegisteredOnRoot(t *testing.T) {
	var found bool
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "trend" {
			found = true
		}
	}
	if !found {
		t.Fatal("atlas trend is not registered on the root command")
	}
}

func TestTrend_FlagsWired(t *testing.T) {
	c := newTrendCmd()
	for _, name := range []string{"feature", "since", "limit", "compare-to", "max-regression", "denominator-tolerance"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("atlas trend is missing --%s", name)
		}
	}
	var recordFound bool
	for _, sub := range c.Commands() {
		if sub.Name() == "record" {
			recordFound = true
			for _, name := range []string{"commit", "note", "retain"} {
				if sub.Flags().Lookup(name) == nil {
					t.Errorf("atlas trend record is missing --%s", name)
				}
			}
		}
	}
	if !recordFound {
		t.Error("atlas trend has no `record` subcommand")
	}
}

func TestTrend_EmptySeriesIsNotAnError(t *testing.T) {
	fix := newTrendFixture(t)
	stdout, _, err := runTrendCmd(t, fix)
	if err != nil {
		t.Fatalf("trend on an empty store: %v", err)
	}
	if !strings.Contains(stdout, "no history") {
		t.Errorf("stdout did not explain the empty series:\n%s", stdout)
	}
}

func TestTrend_TextSeriesShowsGapsAsGapsNotZeros(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "aaa1", MeasuredAt: trendDay(0), Score: ptr(70), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "bbb2", MeasuredAt: trendDay(1), Score: nil, Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "ccc3", MeasuredAt: trendDay(2), Score: ptr(75), Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix)
	if err != nil {
		t.Fatalf("trend: %v", err)
	}
	for _, sha := range []string{"aaa1", "bbb2", "ccc3"} {
		if !strings.Contains(stdout, sha) {
			t.Errorf("stdout missing commit %s:\n%s", sha, stdout)
		}
	}
	// The unmeasured commit must never print as a score of zero.
	gapLine := lineContaining(t, stdout, "bbb2")
	if strings.Contains(gapLine, "0.00") {
		t.Errorf("an unmeasured point was rendered as a zero score: %q", gapLine)
	}
	if !strings.Contains(gapLine, "no evidence") {
		t.Errorf("the gap was not labelled: %q", gapLine)
	}
}

func TestTrend_JSONEmitsTheSeries(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "aaa1", MeasuredAt: trendDay(0), Score: ptr(70), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "bbb2", MeasuredAt: trendDay(1), Score: nil, Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix, "--json")
	if err != nil {
		t.Fatalf("trend --json: %v", err)
	}
	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Series struct {
				Scope  string `json:"scope"`
				Points []struct {
					CommitSHA   string   `json:"commit_sha"`
					Score       *float64 `json:"score"`
					Denominator int64    `json:"denominator"`
				} `json:"points"`
			} `json:"series"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err, stdout)
	}
	if env.SchemaVersion != "v1" || env.Command != "trend" {
		t.Errorf("envelope = %q/%q, want v1/trend", env.SchemaVersion, env.Command)
	}
	if len(env.Result.Series.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(env.Result.Series.Points))
	}
	// null, not 0 — a JSON consumer plotting this must see the hole.
	if env.Result.Series.Points[1].Score != nil {
		t.Errorf("unmeasured point serialised as %v, want null", *env.Result.Series.Points[1].Score)
	}
}

func TestTrend_FeatureSeriesSelectsTheFeature(t *testing.T) {
	fix := newTrendFixture(t)
	fix.feature(t, "f.x", "f.y")
	fix.record(t, store.HistoryPoint{
		CommitSHA: "aaa1", MeasuredAt: trendDay(0), Score: ptr(70), Denominator: 100,
		Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.x", Score: ptr(60), Denominator: 40},
			{FeatureID: "f.y", Score: ptr(90), Denominator: 60},
		},
	})

	stdout, _, err := runTrendCmd(t, fix, "--feature", "f.x", "--json")
	if err != nil {
		t.Fatalf("trend --feature: %v", err)
	}
	if !strings.Contains(stdout, `"scope": "f.x"`) {
		t.Errorf("scope is not the requested feature:\n%s", stdout)
	}
	if !strings.Contains(stdout, "60") || strings.Contains(stdout, `"score": 90`) {
		t.Errorf("feature series carried the wrong scores:\n%s", stdout)
	}
}

func TestTrend_CompareToRegressionFailsTheCommand(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(60), Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111")
	if err == nil {
		t.Fatalf("a 20-point regression did not fail the command:\n%s", stdout)
	}
	if !strings.Contains(err.Error(), "regress") {
		t.Errorf("error did not name the regression: %v", err)
	}
}

// The prefix is how a human types a sha; resolving it is not optional.
func TestTrend_CompareToAcceptsAShaPrefix(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(60), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(75), Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1")
	if err != nil {
		t.Fatalf("trend --compare-to base1: %v", err)
	}
	if !strings.Contains(stdout, "base1111") {
		t.Errorf("resolved baseline not reported:\n%s", stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "improved") {
		t.Errorf("a 15-point gain was not reported as an improvement:\n%s", stdout)
	}
}

// --feature narrows the table a human reads. It must NOT narrow the gate: a
// gate quietly scoped away by a display flag is worse than no gate.
func TestTrend_FeatureFlagDoesNotNarrowTheGate(t *testing.T) {
	fix := newTrendFixture(t)
	fix.feature(t, "f.watched", "f.other")
	fix.record(t, store.HistoryPoint{
		CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 200,
		Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.watched", Score: ptr(80), Denominator: 100},
			{FeatureID: "f.other", Score: ptr(80), Denominator: 100},
		},
	})
	fix.record(t, store.HistoryPoint{
		CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(80), Denominator: 200,
		Features: []store.HistoryFeaturePoint{
			{FeatureID: "f.watched", Score: ptr(120), Denominator: 100},
			{FeatureID: "f.other", Score: ptr(40), Denominator: 100},
		},
	})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--feature", "f.watched", "--compare-to", "base1111")
	if err == nil {
		t.Fatalf("f.other fell 80 -> 40 and the gate passed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "f.other") {
		t.Errorf("the regressing feature was not named in the report:\n%s", stdout)
	}
}

func TestTrend_CompareToUnknownRefIsAnError(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(75), Denominator: 100})

	if _, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "nothing-like-this"); err == nil {
		t.Fatal("comparing against an unrecorded ref succeeded, want an error")
	}
}

// A tolerance that absorbs jitter must be honoured, and overridable.
func TestTrend_MaxRegressionIsHonoured(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(80), Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(78), Denominator: 100})

	if _, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111"); err == nil {
		t.Fatal("a 2-point drop passed the default 0.5 tolerance")
	}
	if _, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111", "--max-regression", "5"); err != nil {
		t.Fatalf("a 2-point drop failed a 5-point tolerance: %v", err)
	}
}

// A denominator move makes the delta a fact about the measurement, not the
// code. It must be said out loud.
func TestTrend_DenominatorShiftIsReported(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: ptr(50), Denominator: 1000})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(70), Denominator: 400})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111")
	if err != nil {
		t.Fatalf("trend --compare-to: %v", err)
	}
	if !strings.Contains(stdout, "1000") || !strings.Contains(stdout, "400") {
		t.Errorf("both denominators must be printed:\n%s", stdout)
	}
	if !strings.Contains(strings.ToLower(stdout), "surface") {
		t.Errorf("the denominator shift was not called out:\n%s", stdout)
	}
}

// Comparing against a commit with no evidence must not fabricate a
// regression out of a missing measurement.
func TestTrend_CompareAgainstUnmeasuredBaselineDoesNotFail(t *testing.T) {
	fix := newTrendFixture(t)
	fix.record(t, store.HistoryPoint{CommitSHA: "base1111", MeasuredAt: trendDay(0), Score: nil, Denominator: 100})
	fix.record(t, store.HistoryPoint{CommitSHA: "head2222", MeasuredAt: trendDay(1), Score: ptr(75), Denominator: 100})

	stdout, _, err := runTrendCmd(t, fix, "--head", "head2222", "--compare-to", "base1111")
	if err != nil {
		t.Fatalf("comparing against an unmeasured baseline failed the build: %v", err)
	}
	if !strings.Contains(strings.ToLower(stdout), "no coverage evidence") {
		t.Errorf("the missing baseline was not explained:\n%s", stdout)
	}
}

func TestTrend_SinceWindowsTheSeries(t *testing.T) {
	fix := newTrendFixture(t)
	old := time.Now().UTC().AddDate(0, 0, -60)
	recent := time.Now().UTC().AddDate(0, 0, -1)
	fix.record(t, store.HistoryPoint{CommitSHA: "old1", MeasuredAt: old, Score: ptr(10), Denominator: 1})
	fix.record(t, store.HistoryPoint{CommitSHA: "new1", MeasuredAt: recent, Score: ptr(20), Denominator: 1})

	stdout, _, err := runTrendCmd(t, fix, "--since", "168h")
	if err != nil {
		t.Fatalf("trend --since: %v", err)
	}
	if strings.Contains(stdout, "old1") {
		t.Errorf("--since 168h kept a 60-day-old point:\n%s", stdout)
	}
	if !strings.Contains(stdout, "new1") {
		t.Errorf("--since 168h dropped a 1-day-old point:\n%s", stdout)
	}
}
