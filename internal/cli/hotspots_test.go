package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

// hotspotsFixture is a real git repository with a real Atlas state file.
// It has to be real git: the churn factor exists to be mined from history,
// and a fixture that stubs the mining would leave the one thing this
// command does untested.
type hotspotsFixture struct {
	root   string
	dbPath string
}

func newHotspotsFixture(t *testing.T) *hotspotsFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	// macOS temp dirs are symlinks (/var -> /private/var); resolve so the
	// path the fixture writes and the path git reports agree.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := os.MkdirAll(filepath.Join(dir, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir .atlas: %v", err)
	}
	f := &hotspotsFixture{root: dir, dbPath: filepath.Join(dir, ".atlas", "atlas.db")}
	f.initRepo(t)
	t.Chdir(dir) // so findRepoRoot() resolves to the fixture, not the atlas checkout
	return f
}

func (f *hotspotsFixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com",
		"GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (f *hotspotsFixture) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// initRepo builds a history where hot.go moves and dead.go does not.
func (f *hotspotsFixture) initRepo(t *testing.T) {
	t.Helper()
	f.git(t, "init", "-q", "-b", "main")
	f.git(t, "config", "user.email", "ann@example.com")
	f.git(t, "config", "user.name", "Ann")

	f.write(t, "dead.go", "package p\n")
	f.write(t, "hot.go", "package p\n")
	f.git(t, "add", "-A")
	f.git(t, "commit", "-q", "-m", "feat: seed")

	for i := 0; i < 6; i++ {
		f.write(t, "hot.go", strings.Repeat("// edit\n", i+1)+"package p\n")
		f.git(t, "add", "-A")
		f.git(t, "commit", "-q", "-m", "feat: keep working on hot")
	}
}

// seed links two equally-unhealthy features, one per file, so churn is the
// only thing that can order them.
func (f *hotspotsFixture) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, spec := range []struct{ feature, file string }{
		{"hot.feature", "hot.go"},
		{"dead.feature", "dead.go"},
	} {
		fid := shared.FeatureID(spec.feature)
		if err := s.Features().Upsert(ctx, store.Feature{ID: fid, Title: spec.feature}); err != nil {
			t.Fatalf("Upsert %s: %v", spec.feature, err)
		}
		sid, err := s.Symbols().Insert(ctx, store.SymbolRow{
			QualifiedName: shared.SymbolID(spec.feature + ".Run"),
			Kind:          shared.KindFunc, FilePath: spec.file, Line: 1,
		})
		if err != nil {
			t.Fatalf("Insert %s: %v", spec.feature, err)
		}
		if err := s.FeatureSymbols().Link(ctx, store.FeatureSymbolLink{
			FeatureID: fid, SymbolID: sid,
			Role: store.RoleImpl, Source: store.SourceAnnotation,
		}); err != nil {
			t.Fatalf("Link %s: %v", spec.feature, err)
		}
	}
}

func runHotspotsCmd(t *testing.T, f *hotspotsFixture, args ...string) (string, string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"hotspots", "--db-path", f.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// -----------------------------------------------------------------------------

func TestHotspots_FlagsWired(t *testing.T) {
	c := newHotspotsCmd()
	for _, name := range []string{
		"top", "window-days", "half-life-days", "max-files-per-commit",
		"exclude-message", "no-default-exclusions", "no-author-diversity",
	} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("atlas hotspots is missing --%s", name)
		}
	}
}

func TestHotspots_RegisteredOnRoot(t *testing.T) {
	found := false
	for _, c := range NewRootCmd().Commands() {
		if c.Name() == "hotspots" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hotspots is not reachable from the root command")
	}
}

func TestHotspots_ChurningFeatureOutranksDeadOne(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)

	out, _, err := runHotspotsCmd(t, f)
	if err != nil {
		t.Fatalf("hotspots: %v\n%s", err, out)
	}
	hot := strings.Index(out, "hot.feature")
	dead := strings.Index(out, "dead.feature")
	if hot < 0 || dead < 0 {
		t.Fatalf("both features should be listed:\n%s", out)
	}
	if hot > dead {
		t.Errorf("dead.feature outranked hot.feature:\n%s", out)
	}
}

func TestHotspots_TextShowsBothFactors(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)

	out, _, err := runHotspotsCmd(t, f)
	if err != nil {
		t.Fatalf("hotspots: %v", err)
	}
	for _, want := range []string{"gap=", "churn=", "hotspot=", "hot.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must expose %q so the score can be decomposed:\n%s", want, out)
		}
	}
}

func TestHotspots_JSONEnvelope(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)

	out, _, err := runHotspotsCmd(t, f, "--json")
	if err != nil {
		t.Fatalf("hotspots --json: %v", err)
	}
	var env struct {
		SchemaVersion string `json:"schema_version"`
		Command       string `json:"command"`
		Result        struct {
			Items []struct {
				FeatureID string  `json:"feature_id"`
				Score     float64 `json:"score"`
				Gap       float64 `json:"gap"`
				Churn     struct {
					Score   float64 `json:"score"`
					Status  string  `json:"status"`
					HotFile string  `json:"hot_file"`
					Commits int     `json:"commits"`
				} `json:"churn"`
			} `json:"items"`
			Churn struct {
				WindowDays     int  `json:"window_days"`
				HalfLifeDays   int  `json:"half_life_days"`
				Shallow        bool `json:"shallow"`
				CommitsScanned int  `json:"commits_scanned"`
			} `json:"churn"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if env.SchemaVersion != "v1" || env.Command != "hotspots" {
		t.Errorf("envelope = %q/%q, want v1/hotspots", env.SchemaVersion, env.Command)
	}
	if len(env.Result.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(env.Result.Items))
	}
	top := env.Result.Items[0]
	if top.FeatureID != "hot.feature" {
		t.Errorf("top item = %q, want hot.feature", top.FeatureID)
	}
	if top.Churn.HotFile != "hot.go" || top.Churn.Commits == 0 {
		t.Errorf("churn factor not decomposed: %+v", top.Churn)
	}
	if top.Churn.Status != "known" {
		t.Errorf("churn status = %q, want known", top.Churn.Status)
	}
	if env.Result.Churn.WindowDays == 0 || env.Result.Churn.HalfLifeDays == 0 {
		t.Errorf("the window the scores were taken under must be reported: %+v", env.Result.Churn)
	}
	if env.Result.Churn.CommitsScanned == 0 {
		t.Errorf("no commits scanned; the fixture has seven")
	}
}

func TestHotspots_TopCapsOutput(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)

	out, _, err := runHotspotsCmd(t, f, "--json", "--top", "1")
	if err != nil {
		t.Fatalf("hotspots --top 1: %v", err)
	}
	var env struct {
		Result struct {
			Items []json.RawMessage `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Result.Items) != 1 {
		t.Errorf("--top 1 returned %d items", len(env.Result.Items))
	}
}

func TestHotspots_ShallowCloneWarns(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)
	// A shallow clone of the fixture, cloned into place as its own repo.
	shallow := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(shallow); err == nil {
		shallow = resolved
	}
	clone := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+f.root, shallow)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Skipf("shallow clone unavailable in this environment: %v\n%s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(shallow, ".atlas"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(f.dbPath, filepath.Join(shallow, ".atlas", "atlas.db")); err != nil {
		t.Fatalf("move db: %v", err)
	}
	sf := &hotspotsFixture{root: shallow, dbPath: filepath.Join(shallow, ".atlas", "atlas.db")}
	t.Chdir(shallow)

	out, _, err := runHotspotsCmd(t, sf, "--json")
	if err != nil {
		t.Fatalf("hotspots: %v", err)
	}
	if !strings.Contains(out, "shallow") {
		t.Errorf("a shallow clone must be reported; every ranking taken in CI depends on it:\n%s", out)
	}
	if !strings.Contains(out, `"status": "unknown"`) {
		t.Errorf("shallow churn must be unknown, not zero:\n%s", out)
	}
}

func TestHotspots_InvalidExcludePatternIsRejected(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)
	if _, _, err := runHotspotsCmd(t, f, "--exclude-message", "("); err == nil {
		t.Fatalf("want an error for an unparseable --exclude-message")
	}
}

func TestHotspots_EmptyBacklogIsNotAnError(t *testing.T) {
	f := newHotspotsFixture(t)
	// No seed: an initialised store with no features.
	s, err := store.Open(context.Background(), f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	_ = s.Close()

	out, _, err := runHotspotsCmd(t, f)
	if err != nil {
		t.Fatalf("hotspots on an empty store: %v", err)
	}
	if !strings.Contains(out, "no hotspots") {
		t.Errorf("empty backlog should say so plainly:\n%s", out)
	}
}

// -----------------------------------------------------------------------------
// `atlas sprint --rank churn`
// -----------------------------------------------------------------------------

func runSprintCmd(t *testing.T, f *hotspotsFixture, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"sprint", "--db-path", f.dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), err
}

func TestSprint_RankChurnIsOptIn(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)

	plain, err := runSprintCmd(t, f, "--json")
	if err != nil {
		t.Fatalf("sprint: %v", err)
	}
	if strings.Contains(plain, `"churn"`) {
		t.Errorf("default sprint must not change shape:\n%s", plain)
	}

	weighted, err := runSprintCmd(t, f, "--json", "--rank", "churn")
	if err != nil {
		t.Fatalf("sprint --rank churn: %v", err)
	}
	if !strings.Contains(weighted, `"churn"`) {
		t.Errorf("--rank churn must surface the churn factor:\n%s", weighted)
	}
	if !strings.Contains(weighted, `"weighted_priority"`) {
		t.Errorf("--rank churn must surface the weighted priority:\n%s", weighted)
	}
	var env struct {
		Result struct {
			Items []struct {
				FeatureID string `json:"feature_id"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(weighted), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Result.Items) == 0 || env.Result.Items[0].FeatureID != "hot.feature" {
		t.Errorf("churn-weighted sprint did not put hot.feature first: %+v", env.Result.Items)
	}
}

func TestSprint_RankFlagRejectsUnknownValues(t *testing.T) {
	f := newHotspotsFixture(t)
	f.seed(t)
	if _, err := runSprintCmd(t, f, "--rank", "vibes"); err == nil {
		t.Fatalf("want an error for an unknown --rank value")
	}
}
