package sprintplan

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sosalejandro/atlas/packages/audit"
	"github.com/sosalejandro/atlas/packages/churn"
	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// fakeGit replays a recorded `git log` so the churn report under test is
// deterministic without a fixture repository.
type fakeGit struct{ out map[string]string }

func (f *fakeGit) Run(_ context.Context, args ...string) (string, error) {
	return f.out[args[0]], nil
}

const (
	rs = "\x1e"
	fs = "\x1f"
)

func commitLine(sha, email string, at time.Time, subject string, paths ...string) string {
	var b strings.Builder
	b.WriteString(rs + sha + fs + email + fs + itoa(at.Unix()) + fs + subject + "\n")
	for _, p := range paths {
		b.WriteString("M\t" + p + "\n")
	}
	return b.String()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

var churnNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// churnReport mines a report from a canned log. tracked lists the files git
// knows about; log is the recorded stream.
func churnReport(t *testing.T, shallow bool, tracked []string, log string) *churn.Report {
	t.Helper()
	shallowOut := "false\n"
	if shallow {
		shallowOut = "true\n"
	}
	rep, err := churn.Mine(context.Background(), churn.Options{
		Repo: "/repo",
		Now:  func() time.Time { return churnNow },
		Git: &fakeGit{out: map[string]string{
			"rev-parse": shallowOut,
			"ls-files":  strings.Join(tracked, "\x00") + "\x00",
			"log":       log,
		}},
	})
	if err != nil {
		t.Fatalf("churn.Mine: %v", err)
	}
	return rep
}

// seedTwoEqualFeatures gives both features an identical (bad) health score
// so churn is the ONLY thing that can separate them.
func seedTwoEqualFeatures(t *testing.T, s *store.Store) {
	t.Helper()
	seedFeatureWithSymbols(t, s, "hot.feature", 2, "hot.go")
	seedFeatureWithSymbols(t, s, "dead.feature", 2, "dead.go")
}

func hotAndDeadLog() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteString(commitLine("h"+itoa(int64(i)), "ann@x",
			churnNow.AddDate(0, 0, -(i+1)), "feat: hot", "hot.go"))
	}
	b.WriteString(commitLine("d0", "ann@x", churnNow.AddDate(-3, 0, 0), "feat: dead", "dead.go"))
	return b.String()
}

// -----------------------------------------------------------------------------
// Hotspots
// -----------------------------------------------------------------------------

func TestHotspots_ChurnSeparatesEquallyBrokenFeatures(t *testing.T) {
	s := openTestStore(t)
	seedTwoEqualFeatures(t, s)
	rep := churnReport(t, false, []string{"hot.go", "dead.go"}, hotAndDeadLog())

	p := New(s, audit.New(s, audit.Options{}), Options{Churn: rep})
	spots, err := p.Hotspots(context.Background())
	if err != nil {
		t.Fatalf("Hotspots: %v", err)
	}
	if len(spots) != 2 {
		t.Fatalf("got %d hotspots, want 2: %+v", len(spots), spots)
	}
	if spots[0].FeatureID != "hot.feature" {
		t.Errorf("ranked %q first, want hot.feature", spots[0].FeatureID)
	}
	if spots[0].Gap != spots[1].Gap {
		t.Fatalf("fixture broken: gaps differ (%.2f vs %.2f)", spots[0].Gap, spots[1].Gap)
	}
	if spots[0].Score <= spots[1].Score {
		t.Errorf("hot score %.2f should exceed dead score %.2f", spots[0].Score, spots[1].Score)
	}
}

func TestHotspots_BothFactorsAreVisibleAndMultiplyOut(t *testing.T) {
	s := openTestStore(t)
	seedTwoEqualFeatures(t, s)
	rep := churnReport(t, false, []string{"hot.go", "dead.go"}, hotAndDeadLog())

	p := New(s, audit.New(s, audit.Options{}), Options{Churn: rep})
	spots, err := p.Hotspots(context.Background())
	if err != nil {
		t.Fatalf("Hotspots: %v", err)
	}
	h := spots[0]
	if want := h.Gap * h.Churn.Score / 100; !nearly(h.Score, want) {
		t.Errorf("Score = %.4f, want Gap*Churn/100 = %.4f", h.Score, want)
	}
	if h.Gap <= 0 || h.Churn.Score <= 0 {
		t.Errorf("both factors must be non-zero for this fixture: %+v", h)
	}
	if h.Churn.HotFile != "hot.go" {
		t.Errorf("HotFile = %q, want hot.go — the score must be decomposable", h.Churn.HotFile)
	}
	if h.Health+h.Gap != 100 {
		t.Errorf("Health %.2f + Gap %.2f should be 100", h.Health, h.Gap)
	}
}

func TestHotspots_TestOnlyFilesDoNotDriveTheRanking(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedFeatureWithSymbols(t, s, "impl.feature", 1, "impl.go")
	// Link a churning test file to the same feature in the test role.
	sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
		QualifiedName: "impl.feature.Test", Kind: shared.KindFunc,
		FilePath: "impl_test.go", Line: 1,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
		FeatureID: "impl.feature", SymbolID: sid,
		Role: store.RoleTest, Source: store.SourceAnnotation,
	}); err != nil {
		t.Fatalf("Link: %v", err)
	}

	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString(commitLine("t"+itoa(int64(i)), "ann@x",
			churnNow.AddDate(0, 0, -(i+1)), "test: flake chase", "impl_test.go"))
	}
	rep := churnReport(t, false, []string{"impl.go", "impl_test.go"}, b.String())

	p := New(s, audit.New(s, audit.Options{}), Options{Churn: rep})
	spots, err := p.Hotspots(ctx)
	if err != nil {
		t.Fatalf("Hotspots: %v", err)
	}
	if spots[0].Churn.HotFile == "impl_test.go" {
		t.Errorf("test-role churn drove the ranking; a test being stabilised is not a hotspot")
	}
	if spots[0].Churn.Score != 0 {
		t.Errorf("impl.go is quiet, so churn should be 0, got %.2f", spots[0].Churn.Score)
	}
}

func TestHotspots_ShallowCloneIsUnknownNotZero(t *testing.T) {
	s := openTestStore(t)
	seedTwoEqualFeatures(t, s)
	rep := churnReport(t, true, []string{"hot.go", "dead.go"}, hotAndDeadLog())

	p := New(s, audit.New(s, audit.Options{}), Options{Churn: rep})
	spots, err := p.Hotspots(context.Background())
	if err != nil {
		t.Fatalf("Hotspots: %v", err)
	}
	for _, h := range spots {
		if h.Churn.Status != churn.StatusUnknown {
			t.Errorf("%s: status %q, want unknown under a shallow clone", h.FeatureID, h.Churn.Status)
		}
		if h.Score == 0 {
			t.Errorf("%s: unknown churn must not zero the ranking", h.FeatureID)
		}
	}
}

func TestHotspots_RequiresAChurnReport(t *testing.T) {
	s := openTestStore(t)
	seedTwoEqualFeatures(t, s)
	p := New(s, audit.New(s, audit.Options{}), Options{})
	if _, err := p.Hotspots(context.Background()); err == nil {
		t.Fatalf("want an error when no churn report is wired")
	}
}

func TestHotspots_DeterministicTieBreak(t *testing.T) {
	s := openTestStore(t)
	seedFeatureWithSymbols(t, s, "b.feature", 1, "b.go")
	seedFeatureWithSymbols(t, s, "a.feature", 1, "a.go")
	log := commitLine("c1", "ann@x", churnNow.AddDate(0, 0, -1), "feat: x", "a.go", "b.go")
	rep := churnReport(t, false, []string{"a.go", "b.go"}, log)

	p := New(s, audit.New(s, audit.Options{}), Options{Churn: rep})
	for i := 0; i < 3; i++ {
		spots, err := p.Hotspots(context.Background())
		if err != nil {
			t.Fatalf("Hotspots: %v", err)
		}
		if spots[0].FeatureID != "a.feature" {
			t.Fatalf("run %d: equal scores must break by feature id, got %q", i, spots[0].FeatureID)
		}
	}
}

// -----------------------------------------------------------------------------
// Rank, churn-weighted
// -----------------------------------------------------------------------------

func TestRank_ChurnWeightingIsOptInAndAdditive(t *testing.T) {
	s := openTestStore(t)
	seedTwoEqualFeatures(t, s)
	a := audit.New(s, audit.Options{})
	ctx := context.Background()

	plain, err := New(s, a, Options{}).Rank(ctx)
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	for _, it := range plain {
		if it.Churn != nil {
			t.Errorf("%s: churn must be absent unless asked for", it.FeatureID)
		}
		if it.WeightedPriority != 0 {
			t.Errorf("%s: WeightedPriority must stay zero without churn", it.FeatureID)
		}
	}

	rep := churnReport(t, false, []string{"hot.go", "dead.go"}, hotAndDeadLog())
	weighted, err := New(s, a, Options{Churn: rep}).Rank(ctx)
	if err != nil {
		t.Fatalf("Rank churn: %v", err)
	}
	if weighted[0].FeatureID != "hot.feature" {
		t.Errorf("churn-weighted rank put %q first, want hot.feature", weighted[0].FeatureID)
	}
	for _, it := range weighted {
		if it.Churn == nil {
			t.Fatalf("%s: churn factor missing from the weighted ranking", it.FeatureID)
		}
		if want := it.Priority * it.Churn.Score / 100; !nearly(it.WeightedPriority, want) {
			t.Errorf("%s: WeightedPriority %.4f, want %.4f", it.FeatureID, it.WeightedPriority, want)
		}
	}
	// Priority itself is untouched: the flag changes the ordering, not the
	// existing score's meaning.
	byID := map[shared.FeatureID]float64{}
	for _, it := range plain {
		byID[it.FeatureID] = it.Priority
	}
	for _, it := range weighted {
		if !nearly(byID[it.FeatureID], it.Priority) {
			t.Errorf("%s: Priority changed under churn weighting (%.4f → %.4f)",
				it.FeatureID, byID[it.FeatureID], it.Priority)
		}
	}
}

func TestRank_ChurnReasonIsExplained(t *testing.T) {
	s := openTestStore(t)
	seedTwoEqualFeatures(t, s)
	rep := churnReport(t, false, []string{"hot.go", "dead.go"}, hotAndDeadLog())
	items, err := New(s, audit.New(s, audit.Options{}), Options{Churn: rep}).Rank(context.Background())
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	found := false
	for _, r := range items[0].Reasons {
		if strings.Contains(r, "churn") {
			found = true
		}
	}
	if !found {
		t.Errorf("no churn reason in %v", items[0].Reasons)
	}
}

func nearly(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
