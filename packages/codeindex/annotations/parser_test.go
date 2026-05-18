package annotations

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// readFixture loads a *.fixture file from testdata/. We use the .fixture
// suffix to keep go test from compiling/testing the embedded snippets.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestParseBytes_GoFixture(t *testing.T) {
	t.Parallel()

	content := readFixture(t, "login_test.go.fixture")
	got := ParseBytes("auth/login_test.go", content, styleGoTS)

	// Expectations (order = file order):
	//   1. @atlas:feature auth.login #real
	//   2. @atlas:contract auth.login
	//   3. @atlas:owner platform-team
	//   4. @testreg auth.login,auth.logout #real
	//   5. @api POST /api/v1/auth/login
	// Skipped:
	//   - @atlas:Feature (uppercase kind → unknown kind, skipped)
	//   - @atlas:feature (no IDs, skipped)
	//   - prose mention without @ prefix
	expectKinds := []shared.AnnotationKind{
		shared.AnnFeature, shared.AnnContract, shared.AnnOwner,
		shared.AnnFeature, shared.AnnAPI,
	}
	if len(got) != len(expectKinds) {
		t.Fatalf("expected %d annotations, got %d: %+v", len(expectKinds), len(got), got)
	}
	for i, ann := range got {
		if ann.Kind != expectKinds[i] {
			t.Fatalf("ann[%d].Kind = %s; want %s", i, ann.Kind, expectKinds[i])
		}
	}

	// Legacy multi-id with comma must canonicalise to two IDs.
	legacy := got[3]
	if legacy.Source != shared.SourceTestreg {
		t.Fatalf("legacy ann.Source = %s; want testreg", legacy.Source)
	}
	if !reflect.DeepEqual(legacy.IDs, []string{"auth.login", "auth.logout"}) {
		t.Fatalf("legacy IDs = %v; want [auth.login auth.logout]", legacy.IDs)
	}
	if !reflect.DeepEqual(legacy.Tags, []string{"#real"}) {
		t.Fatalf("legacy Tags = %v; want [#real]", legacy.Tags)
	}

	// @api annotation carries Method + Path.
	api := got[4]
	if api.Method != "POST" || api.Path != "/api/v1/auth/login" {
		t.Fatalf("api annotation wrong: method=%s path=%s", api.Method, api.Path)
	}
}

func TestParseBytes_TSFixtureWithBlockComments(t *testing.T) {
	t.Parallel()

	content := readFixture(t, "dashboard.test.tsx.fixture")
	got := ParseBytes("apps/web/dashboard.test.tsx", content, styleGoTS)

	// Expect: 1 feature, 1 feature (multi-id in block), 1 feature (jsdoc block),
	// 1 owner (jsdoc block), 1 legacy feature (multi-id testreg). Order can
	// vary slightly due to block-comment processing emitting one logicalLine
	// per inner line.
	kindCounts := map[shared.AnnotationKind]int{}
	for _, a := range got {
		kindCounts[a.Kind]++
	}
	if kindCounts[shared.AnnFeature] < 4 {
		t.Fatalf("expected at least 4 feature annotations; counts=%v records=%+v", kindCounts, got)
	}
	if kindCounts[shared.AnnOwner] < 1 {
		t.Fatalf("expected at least 1 owner annotation; counts=%v", kindCounts)
	}

	// Multi-id in block comment must produce 2 IDs.
	var multi shared.Annotation
	for _, a := range got {
		if len(a.IDs) == 2 && a.Source == shared.SourceAtlas {
			multi = a
			break
		}
	}
	if multi.Kind != shared.AnnFeature {
		t.Fatalf("expected multi-id block annotation to parse; got %+v", got)
	}
	sort.Strings(multi.IDs)
	want := []string{"web.dashboard", "web.notifications"}
	if !reflect.DeepEqual(multi.IDs, want) {
		t.Fatalf("multi IDs = %v; want %v", multi.IDs, want)
	}
}

func TestParseBytes_MarkdownFixture(t *testing.T) {
	t.Parallel()

	content := readFixture(t, "runbook.md.fixture")
	got := ParseBytes("docs/runbook.md", content, styleMarkdown)

	// Expect: 1 feature + 1 owner.
	var (
		hasFeature bool
		hasOwner   bool
	)
	for _, a := range got {
		if a.Kind == shared.AnnFeature && len(a.IDs) == 1 && a.IDs[0] == "ops.incident_response" {
			hasFeature = true
		}
		if a.Kind == shared.AnnOwner {
			hasOwner = true
		}
	}
	if !hasFeature {
		t.Fatalf("expected feature annotation; got %+v", got)
	}
	if !hasOwner {
		t.Fatalf("expected owner annotation; got %+v", got)
	}
}

func TestParseBytes_PythonFixture(t *testing.T) {
	t.Parallel()

	content := readFixture(t, "rollup.py.fixture")
	got := ParseBytes("scripts/rollup.py", content, stylePython)
	if len(got) != 1 || got[0].Kind != shared.AnnFeature {
		t.Fatalf("expected one feature annotation; got %+v", got)
	}
	if got[0].IDs[0] != "analytics.daily_rollup" {
		t.Fatalf("expected analytics.daily_rollup; got %v", got[0].IDs)
	}
}

func TestParseBytes_RejectsInvalidIDFormat(t *testing.T) {
	t.Parallel()

	content := []byte(`// @atlas:feature Auth.Login
// @atlas:feature auth.login
`)
	got := ParseBytes("x.go", content, styleGoTS)
	// First line rejected (capitalised); second accepted.
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 accepted annotation; got %d (%v)", len(got), got)
	}
	if got[0].IDs[0] != "auth.login" {
		t.Fatalf("expected auth.login to be accepted; got %v", got[0].IDs)
	}
}

func TestParseBytes_UnknownKindSkipped(t *testing.T) {
	t.Parallel()

	content := []byte(`// @atlas:experimental foo.bar
// @atlas:feature foo.bar
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 {
		t.Fatalf("expected unknown kind skipped, known kept; got %d (%v)", len(got), got)
	}
}

func TestParseBytes_LegacyCommaSeparatedIDs(t *testing.T) {
	t.Parallel()

	content := []byte(`// @testreg a.b,c.d,e.f #real
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 {
		t.Fatalf("expected 1; got %d", len(got))
	}
	if !reflect.DeepEqual(got[0].IDs, []string{"a.b", "c.d", "e.f"}) {
		t.Fatalf("expected 3 split IDs; got %v", got[0].IDs)
	}
}

func TestParse_FileIO(t *testing.T) {
	t.Parallel()

	// Use the Go fixture renamed to .go so Parse() picks the right style.
	dir := t.TempDir()
	dst := filepath.Join(dir, "sample.go")
	src := readFixture(t, "login_test.go.fixture")
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	got, err := Parse(context.Background(), dst)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected non-empty annotations")
	}
}

func TestParse_ContextCancelled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dst := filepath.Join(dir, "x.go")
	if err := os.WriteFile(dst, []byte("// @atlas:feature x.y\n"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Parse(ctx, dst)
	if err == nil {
		t.Fatalf("expected ctx.Err propagation")
	}
}

func TestParse_UnsupportedExtReturnsNil(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dst := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(dst, []byte("// @atlas:feature x.y\n"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	got, err := Parse(context.Background(), dst)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for unsupported ext; got %v", got)
	}
}

func TestParseBytes_StringLiteralWithSlashes(t *testing.T) {
	t.Parallel()

	// A `//` inside a string literal must NOT be treated as a comment.
	content := []byte(`var s = "http://example.com" // @atlas:feature s.x
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 {
		t.Fatalf("expected 1 (trailing comment annotation); got %d (%v)", len(got), got)
	}
	if got[0].IDs[0] != "s.x" {
		t.Fatalf("expected s.x; got %v", got[0].IDs)
	}
}

func TestParseBytes_TagsAfterIDs(t *testing.T) {
	t.Parallel()

	content := []byte(`// @atlas:feature foo.bar #real bad.id #flaky
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 {
		t.Fatalf("expected 1; got %d", len(got))
	}
	// Per docs §Parser rules #4: first #tag terminates ID list.
	if !reflect.DeepEqual(got[0].IDs, []string{"foo.bar"}) {
		t.Fatalf("expected ids=[foo.bar]; got %v", got[0].IDs)
	}
	if !strings.Contains(strings.Join(got[0].Tags, " "), "#real") {
		t.Fatalf("expected #real tag; got %v", got[0].Tags)
	}
}
