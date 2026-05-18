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

// ---------------------------------------------------------------------------
// EDA-pattern annotation kinds (Phase 6e).
// ---------------------------------------------------------------------------

func TestParseBytes_EDA_AllKindsRoundTrip(t *testing.T) {
	t.Parallel()

	content := []byte(`// @atlas:bc identity
// @atlas:aggregate meal_prep.batch_session
// @atlas:aggregate-service meal_prep.batch_session
// @atlas:saga meal_prep_flow step=1
// @atlas:consumer stream=meal_prep_events
// @atlas:event-emit batch_session_started
// @atlas:outbox-publish batch_session_started
`)
	got := ParseBytes("x.go", content, styleGoTS)
	wantKinds := []shared.AnnotationKind{
		shared.AnnBC,
		shared.AnnAggregate,
		shared.AnnAggregateService,
		shared.AnnSaga,
		shared.AnnConsumer,
		shared.AnnEventEmit,
		shared.AnnOutboxPublish,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("expected %d annotations, got %d: %+v", len(wantKinds), len(got), got)
	}
	for i, ann := range got {
		if ann.Kind != wantKinds[i] {
			t.Fatalf("ann[%d].Kind = %s; want %s", i, ann.Kind, wantKinds[i])
		}
		if ann.Source != shared.SourceAtlas {
			t.Fatalf("ann[%d].Source = %s; want atlas", i, ann.Source)
		}
		if len(ann.IDs) == 0 {
			t.Fatalf("ann[%d] (%s) has no IDs", i, ann.Kind)
		}
	}

	// Saga step= tag must be preserved verbatim.
	sagaAnn := got[3]
	var sawStep bool
	for _, tag := range sagaAnn.Tags {
		if tag == "step=1" {
			sawStep = true
		}
	}
	if !sawStep {
		t.Fatalf("expected step=1 tag on saga annotation; got tags=%v", sagaAnn.Tags)
	}

	// Consumer must promote stream= value to IDs.
	consumerAnn := got[4]
	if !reflect.DeepEqual(consumerAnn.IDs, []string{"meal_prep_events"}) {
		t.Fatalf("expected consumer IDs=[meal_prep_events]; got %v", consumerAnn.IDs)
	}
}

func TestParseBytes_EDA_RejectsInvalidIDFormat(t *testing.T) {
	t.Parallel()

	// Strict id-validation applies to every EDA kind.
	content := []byte(`// @atlas:bc Identity
// @atlas:aggregate Meal_Prep.batch_session
// @atlas:saga MealPrepFlow step=1
// @atlas:event-emit Batch.Session.Started
// @atlas:outbox-publish Batch.Session.Started
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 0 {
		t.Fatalf("expected all malformed EDA ids rejected; got %d (%+v)", len(got), got)
	}
}

func TestParseBytes_EDA_SagaNonNumericStepRejected(t *testing.T) {
	t.Parallel()

	// Edge case (pressure dim: data shape): step= must be a non-negative
	// integer — `step=two` and `step=` must be rejected at parse time so
	// the saga-walk query never has to defend against them.
	content := []byte(`// @atlas:saga meal_prep_flow step=two
// @atlas:saga meal_prep_flow step=
// @atlas:saga meal_prep_flow step=1
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 {
		t.Fatalf("expected only well-formed step=1 to parse; got %d (%+v)", len(got), got)
	}
	if got[0].IDs[0] != "meal_prep_flow" {
		t.Fatalf("expected ids=[meal_prep_flow]; got %v", got[0].IDs)
	}
}

func TestParseBytes_EDA_ConsumerWithoutStreamRejected(t *testing.T) {
	t.Parallel()

	// Edge case (pressure dim: data shape): consumer must carry stream=.
	// Bare `@atlas:consumer x` is rejected because the id grammar puts
	// the stream identity in the tag, not the id slot.
	content := []byte(`// @atlas:consumer
// @atlas:consumer foo
// @atlas:consumer stream=meal_prep_events
// @atlas:consumer x stream=meal_prep_events
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 {
		t.Fatalf("expected only the well-formed consumer to parse; got %d (%+v)", len(got), got)
	}
	if got[0].Kind != shared.AnnConsumer {
		t.Fatalf("expected kind=consumer; got %s", got[0].Kind)
	}
	if got[0].IDs[0] != "meal_prep_events" {
		t.Fatalf("expected ids=[meal_prep_events]; got %v", got[0].IDs)
	}
}

func TestParseBytes_EDA_AggregateWithoutServiceLinkIsValid(t *testing.T) {
	t.Parallel()

	// Edge case (pressure dim: state-shape): an aggregate annotation
	// stands on its own — there is no parser requirement that an
	// aggregate-service annotation exist somewhere too. The store query
	// FindAggregate handles "no canonical service" by returning a nil
	// pointer field, not an error.
	content := []byte(`// @atlas:aggregate meal_prep.batch_session
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 1 || got[0].Kind != shared.AnnAggregate {
		t.Fatalf("expected aggregate annotation; got %+v", got)
	}
}

func TestParseBytes_EDA_NoIDForSagaIsRejected(t *testing.T) {
	t.Parallel()

	// `@atlas:saga step=1` is ill-formed — saga's identity is the id
	// slot, not a tag.
	content := []byte(`// @atlas:saga step=1
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 0 {
		t.Fatalf("expected saga without id to be rejected; got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Issue #15 — strict-kind id regex must accept dashes.
//
// The nutrition-v2-go cutover corpus uses kebab-style segments
// (`plans-patient.export-pdf`, `email-relay.dlq`, `batch-sessions.cook`) for
// ~149 of its 1,110 testreg annotations. Pre-fix, those would be silently
// dropped at parse time. These tests cover:
//   - round-trip preservation of dashed ids across every strict kind
//   - mixed dash + underscore in a single id
//   - underscore-only ids still parse (backward compat)
//   - shapes the regex must still reject (uppercase, spaces, dot collisions)
// ---------------------------------------------------------------------------

func TestParseBytes_DashedIDs_AllStrictKindsRoundTrip(t *testing.T) {
	t.Parallel()

	content := []byte(`// @atlas:feature plans-patient.export-pdf #real
// @atlas:contract email-relay.dlq
// @atlas:bc email-relay
// @atlas:aggregate batch-sessions.cook
// @atlas:aggregate-service measurements-nutritionist.analytics-summary
// @atlas:saga email-relay.poll-startup step=1
// @atlas:event-emit batch-sessions.cooked
// @atlas:outbox-publish email-relay.dlq-event
`)
	got := ParseBytes("x.go", content, styleGoTS)

	wantIDs := []string{
		"plans-patient.export-pdf",
		"email-relay.dlq",
		"email-relay",
		"batch-sessions.cook",
		"measurements-nutritionist.analytics-summary",
		"email-relay.poll-startup",
		"batch-sessions.cooked",
		"email-relay.dlq-event",
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("expected %d annotations; got %d (%+v)", len(wantIDs), len(got), got)
	}
	for i, ann := range got {
		if len(ann.IDs) == 0 || ann.IDs[0] != wantIDs[i] {
			t.Fatalf("ann[%d] (kind=%s) IDs = %v; want first id = %q", i, ann.Kind, ann.IDs, wantIDs[i])
		}
		if ann.Source != shared.SourceAtlas {
			t.Fatalf("ann[%d].Source = %s; want atlas", i, ann.Source)
		}
	}

	// Tag preservation on the dashed-feature row.
	feature := got[0]
	if !reflect.DeepEqual(feature.Tags, []string{"#real"}) {
		t.Fatalf("feature.Tags = %v; want [#real]", feature.Tags)
	}
}

func TestParseBytes_DashedIDs_MixedWithUnderscore(t *testing.T) {
	t.Parallel()

	// One id may carry both dashes and underscores — the regex is
	// character-class permissive, dot is the only segment separator.
	content := []byte(`// @atlas:feature plans-patient.export_v2
// @atlas:feature meal_prep.batch-session
// @atlas:aggregate email-relay.dlq_table
`)
	got := ParseBytes("x.go", content, styleGoTS)
	wantIDs := []string{
		"plans-patient.export_v2",
		"meal_prep.batch-session",
		"email-relay.dlq_table",
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("expected %d annotations; got %d (%+v)", len(wantIDs), len(got), got)
	}
	for i, ann := range got {
		if ann.IDs[0] != wantIDs[i] {
			t.Fatalf("ann[%d] id = %q; want %q", i, ann.IDs[0], wantIDs[i])
		}
	}
}

func TestParseBytes_DashedIDs_UnderscoreOnlyBackCompat(t *testing.T) {
	t.Parallel()

	// Backward compat: the pre-#15 underscore-only style continues to
	// parse identically. This is a regression guard — if a future change
	// over-tightens the regex (e.g. requiring at least one dash), we
	// lose every existing canonical id.
	content := []byte(`// @atlas:feature auth.login
// @atlas:contract meal_prep.batch_session
// @atlas:aggregate meal_prep.batch_session
// @atlas:bc identity
`)
	got := ParseBytes("x.go", content, styleGoTS)
	if len(got) != 4 {
		t.Fatalf("expected 4 underscore-only ids accepted; got %d (%+v)", len(got), got)
	}
	for _, ann := range got {
		if strings.ContainsRune(ann.IDs[0], '-') {
			t.Fatalf("unexpected dash in id %q", ann.IDs[0])
		}
	}
}

func TestParseBytes_DashedIDs_NegativeShapesStillRejected(t *testing.T) {
	t.Parallel()

	// Pressure dim: data shape. The fix narrows by ADDING `-` to the
	// character class — it does NOT loosen the anchored dot-segment
	// structure. These shapes must still be rejected at parse time:
	//   - uppercase segment        (`Plans-patient.export-pdf`)
	//   - whitespace inside the id (`plans-patient. export-pdf`)
	//   - consecutive dots         (`plans-patient..export-pdf`)
	//   - leading dot              (`.plans-patient.export-pdf`)
	//   - trailing dot             (`plans-patient.export-pdf.`)
	content := []byte(`// @atlas:feature Plans-patient.export-pdf
// @atlas:feature plans-patient. export-pdf
// @atlas:feature plans-patient..export-pdf
// @atlas:feature .plans-patient.export-pdf
// @atlas:feature plans-patient.export-pdf.
`)
	got := ParseBytes("x.go", content, styleGoTS)
	// The "whitespace inside" line will not produce a malformed id — the
	// space splits into ids=[plans-patient.], tags=nothing-special. The
	// first id `plans-patient.` has a trailing dot and is rejected by
	// validateAtlasIDs, so the whole annotation is dropped. Net: 0
	// annotations from any of these 5 lines.
	if len(got) != 0 {
		t.Fatalf("expected all 5 malformed shapes rejected; got %d (%+v)", len(got), got)
	}
}
