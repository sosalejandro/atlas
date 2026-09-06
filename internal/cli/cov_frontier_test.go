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

// seedGroupedBuild writes the two runs a polyglot CI build leaves behind: a
// Go one and a front-end one, each with its own covered symbol, tied together
// by one run group. Returns the ids in insertion order.
//
// The Go run is deliberately the OLDER of the two, because the bug #86 exists
// to fix is precisely that the later front-end sync used to erase it.
func seedGroupedBuild(t *testing.T, fix *covFixture, group string) (goRun, feRun int64) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, fix.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	var grp *string
	if group != "" {
		grp = &group
	}
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	billing := shared.FeatureID("billing")
	web := shared.FeatureID("web")
	for _, id := range []shared.FeatureID{billing, web} {
		if err := s.Features().Upsert(ctx, store.Feature{ID: id, Title: string(id)}); err != nil {
			t.Fatalf("Features.Upsert(%s): %v", id, err)
		}
	}

	goRun, err = s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework:         store.FrameworkGoTest,
		StartedAt:         base,
		FinishedAt:        base,
		RunGroup:          grp,
		FilesInReport:     4,
		FilesMatched:      3,
		FilesUnmatched:    1,
		StmtsAttributed:   90,
		StmtsUnattributed: 10,
	}, []store.CoverageResult{
		{FeatureID: &billing, Status: store.StatusPass, CoveredStmts: 9, TotalStmts: 10},
	})
	if err != nil {
		t.Fatalf("InsertRunWithResults(go): %v", err)
	}

	feRun, err = s.Coverage().InsertRunWithResults(ctx, store.CoverageRun{
		Framework:         store.FrameworkVitest,
		StartedAt:         base.Add(time.Minute),
		FinishedAt:        base.Add(time.Minute),
		RunGroup:          grp,
		FilesInReport:     2,
		FilesMatched:      2,
		FilesUnmatched:    0,
		StmtsAttributed:   30,
		StmtsUnattributed: 5,
	}, []store.CoverageResult{
		{FeatureID: &web, Status: store.StatusPass, CoveredStmts: 3, TotalStmts: 4},
	})
	if err != nil {
		t.Fatalf("InsertRunWithResults(fe): %v", err)
	}
	return goRun, feRun
}

func TestCovStatus_GroupFlagWired(t *testing.T) {
	if newCovStatusCmd().Flags().Lookup("group") == nil {
		t.Fatal("atlas cov status is missing --group")
	}
	if newCovSyncCmd().Flags().Lookup("run-group") == nil {
		t.Fatal("atlas cov sync is missing --run-group")
	}
	if newCovRunCmd().Flags().Lookup("run-group") == nil {
		t.Fatal("atlas cov run is missing --run-group")
	}
}

// The reason #86 exists: with both syncs tagged, status reports on BOTH
// features. Untagged, the last sync is the whole frontier and the Go feature
// vanishes -- which is the pre-#86 behaviour, and must stay reachable so an
// operator who forgets the flag is not silently merged into a stale group.
func TestCovStatus_FrontierSpansTheGroup(t *testing.T) {
	t.Run("grouped", func(t *testing.T) {
		fix := newCovFixture(t)
		seedGroupedBuild(t, fix, "ci-abc123")

		out, err := runCovStatusCmd(t, fix)
		if err != nil {
			t.Fatalf("cov status: %v", err)
		}
		for _, want := range []string{"billing", "web", "ci-abc123"} {
			if !strings.Contains(out, want) {
				t.Errorf("output is missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("ungrouped", func(t *testing.T) {
		fix := newCovFixture(t)
		seedGroupedBuild(t, fix, "")

		out, err := runCovStatusCmd(t, fix)
		if err != nil {
			t.Fatalf("cov status: %v", err)
		}
		if !strings.Contains(out, "web") {
			t.Errorf("newest run's feature is missing:\n%s", out)
		}
		if strings.Contains(out, "billing") {
			t.Errorf("ungrouped frontier reached past the newest run:\n%s", out)
		}
	})
}

// --group enumerates the runs behind the pooled number, which is how an
// operator checks that every framework in a build landed under the same key.
func TestCovStatus_GroupEnumeratesTheRuns(t *testing.T) {
	fix := newCovFixture(t)
	goRun, feRun := seedGroupedBuild(t, fix, "ci-abc123")

	out, err := runCovStatusCmd(t, fix, "--group")
	if err != nil {
		t.Fatalf("cov status --group: %v", err)
	}
	if !strings.Contains(out, "frontier runs (2)") {
		t.Errorf("run breakdown missing:\n%s", out)
	}
	for _, fw := range []string{"go-test", "vitest"} {
		if !strings.Contains(out, fw) {
			t.Errorf("output does not name framework %q:\n%s", fw, out)
		}
	}

	t.Cleanup(func() { flags.JSON = false })
	raw, err := runCovStatusCmd(t, fix, "--group", "--json")
	if err != nil {
		t.Fatalf("cov status --group --json: %v", err)
	}
	var env struct {
		Result covStatusResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, raw)
	}
	if env.Result.Group == nil || *env.Result.Group != "ci-abc123" {
		t.Errorf("Group = %v, want ci-abc123", env.Result.Group)
	}
	if env.Result.RunID != feRun {
		t.Errorf("RunID = %d, want the newest run %d", env.Result.RunID, feRun)
	}
	if len(env.Result.RunIDs) != 2 || env.Result.RunIDs[0] != goRun {
		t.Errorf("RunIDs = %v, want [%d %d]", env.Result.RunIDs, goRun, feRun)
	}
	for _, r := range env.Result.Runs {
		if r.Results != 1 {
			t.Errorf("run %d contributed %d results, want 1", r.RunID, r.Results)
		}
	}
}

// Attribution sums across the frontier. The reports do not overlap -- go-cover
// measures Go files, istanbul the front end -- so the sum is the build's total
// blind spot, which is the number a CI gate reads.
func TestCovStatus_GapsSumAcrossTheFrontier(t *testing.T) {
	fix := newCovFixture(t)
	seedGroupedBuild(t, fix, "ci-abc123")

	t.Cleanup(func() { flags.JSON = false })
	raw, err := runCovStatusCmd(t, fix, "--gaps", "--json")
	if err != nil {
		t.Fatalf("cov status --gaps --json: %v", err)
	}
	var env struct {
		Result covStatusResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, raw)
	}
	a := env.Result.Attribution
	if a == nil || !a.Recorded {
		t.Fatalf("attribution absent or unrecorded: %+v", a)
	}
	if a.StmtsAttributed != 120 || a.StmtsUnattributed != 15 {
		t.Errorf("stmts = %d/%d, want 120 attributed and 15 unattributed",
			a.StmtsAttributed, a.StmtsUnattributed)
	}
	if a.FilesInReport != 6 || a.FilesUnmatched != 1 {
		t.Errorf("files = %d in report, %d unmatched; want 6 and 1",
			a.FilesInReport, a.FilesUnmatched)
	}
}
