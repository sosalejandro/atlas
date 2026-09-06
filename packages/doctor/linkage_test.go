package doctor

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
	"github.com/sosalejandro/atlas/packages/store"
)

func (f *fixture) upsertFeature(t *testing.T, id shared.FeatureID) {
	t.Helper()
	err := f.store.Features().Upsert(context.Background(), store.Feature{
		ID:    id,
		Title: string(id),
		Kind:  store.FeatureKindFeature,
	})
	if err != nil {
		t.Fatalf("upsert feature %s: %v", id, err)
	}
}

func (f *fixture) linkFeature(t *testing.T, id shared.FeatureID, symbolID int64) {
	t.Helper()
	err := f.store.FeatureSymbols().Link(context.Background(), store.FeatureSymbolLink{
		FeatureID: id,
		SymbolID:  symbolID,
		Role:      store.RoleImpl,
	})
	if err != nil {
		t.Fatalf("link feature %s: %v", id, err)
	}
}

func (f *fixture) upsertAnnotation(t *testing.T, relPath string, line int, value string) {
	t.Helper()
	err := f.store.Annotations().Upsert(context.Background(), store.AnnotationRow{
		FilePath: relPath,
		Line:     line,
		Kind:     shared.AnnFeature,
		Value:    value,
	})
	if err != nil {
		t.Fatalf("upsert annotation %s:%d: %v", relPath, line, err)
	}
}

func TestFeatureLinkage_AllLinked_OK(t *testing.T) {
	f := newFixture(t)
	id := f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)
	f.upsertFeature(t, "auth.login")
	f.linkFeature(t, "auth.login", id)
	f.upsertAnnotation(t, "pkg/a.go", 2, "auth.login")

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
}

// A feature whose only symbol was deleted survives the cascade as an
// empty shell: it still ranks in `atlas sprint` and still scores in
// `atlas health`, about nothing.
func TestFeatureLinkage_FeatureWithoutSymbols_Warns(t *testing.T) {
	f := newFixture(t)
	f.upsertFeature(t, "auth.login")

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if got := res.Details["features_without_symbols"]; got != 1 {
		t.Errorf("features_without_symbols = %v, want 1", got)
	}
	if ids, _ := res.Details["unlinked_features"].([]string); len(ids) != 1 || ids[0] != "auth.login" {
		t.Errorf("unlinked_features = %v, want [auth.login]", res.Details["unlinked_features"])
	}
}

// An annotation that DOES resolve to an indexed symbol and still names a
// feature with no row is a genuine break: the ingest materializes a
// feature for exactly this shape, so its absence means the annotation
// layer and the feature layer were written by different passes.
func TestFeatureLinkage_AnchoredAnnotationForUnknownFeature_Warns(t *testing.T) {
	f := newFixture(t)
	id := f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)
	f.upsertFeature(t, "auth.login")
	f.linkFeature(t, "auth.login", id)
	// Line 2, directly above the symbol on line 3: the doc-comment shape
	// the ingest resolves.
	f.upsertAnnotation(t, "pkg/a.go", 2, "auth.logout")

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if got := res.Details["dangling_annotations"]; got != 1 {
		t.Errorf("dangling_annotations = %v, want 1", got)
	}
}

// The defect this check shipped with: packages/store/ingest.go documents
// an annotation that resolves to no symbol within the LookupAtPosition
// window as an INTENTIONAL orphan -- markdown, package docs,
// end-of-file markers -- that materializes no feature by design. The
// check could not tell those from a real break, so it reported the
// everyday state of any annotated repo as a problem.
//
// The narrowed check re-asks the ingest's own question and stays quiet.
func TestFeatureLinkage_UnanchoredAnnotationIsNotReported(t *testing.T) {
	f := newFixture(t)
	id := f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)
	f.upsertFeature(t, "auth.login")
	f.linkFeature(t, "auth.login", id)
	// No symbol anywhere near this line, in a file with no symbols at
	// all: the ingest skipped it silently and wrote no feature row.
	f.upsertAnnotation(t, "docs/intro.md", 9, "auth.logout")

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
	if got := res.Details["dangling_annotations"]; got != 0 {
		t.Errorf("dangling_annotations = %v, want 0 -- an orphan by design is not a dangle", got)
	}
	if got := res.Details["unanchored_annotations"]; got != 1 {
		t.Errorf("unanchored_annotations = %v, want 1 (counted, not complained about)", got)
	}
}

// When the read-only probe does not open, the annotation half of this
// check never runs. It used to return nil, which Details rendered as
// `dangling_annotations: 0` beside an empty list -- a half that did not
// happen, printed exactly like a half that happened and found nothing.
func TestFeatureLinkage_SkippedSweepIsReportedAsSkippedNotZero(t *testing.T) {
	f := newFixture(t)
	id := f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)
	f.upsertFeature(t, "auth.login")
	f.linkFeature(t, "auth.login", id)

	env := f.env(t)
	env.closeProbe() // the state the schema check reports separately

	res := runCheck(t, featureLinkage{}, env)

	assertSeverity(t, res, SeverityNotApplicable)
	if got, ok := res.Details["dangling_annotations"]; ok {
		t.Errorf("dangling_annotations = %v, want the key absent: the sweep never ran", got)
	}
	if got := res.Details["annotation_sweep"]; got != "skipped" {
		t.Errorf("annotation_sweep = %v, want \"skipped\"", got)
	}
	if res.Remediation == "" {
		t.Error("a check that could not run must still say what to do about it")
	}
}

// The payload grammar carries tags and tiers alongside ids. Classifying
// one of those as a missing feature would report a problem that is not
// there, so the check uses the same id rule the store's materialization
// uses and stays silent on anything it cannot classify.
func TestFeatureLinkage_TagsAndTiersAreNotFeatureIDs(t *testing.T) {
	f := newFixture(t)
	id := f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)
	f.upsertFeature(t, "auth.login")
	f.linkFeature(t, "auth.login", id)
	f.upsertAnnotation(t, "pkg/a.go", 2, "auth.login #mocked integration")

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityOK)
}

// A repo that has not adopted annotations yet has nothing to check. That
// is not health, and it is not a failure either.
func TestFeatureLinkage_NoFeatures_NotApplicable(t *testing.T) {
	f := newFixture(t)

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityNotApplicable)
}

func TestFeatureLinkage_NoStore_NotApplicable(t *testing.T) {
	f := newFixture(t)
	env := f.env(t)
	env.Store = nil

	res := runCheck(t, featureLinkage{}, env)

	assertSeverity(t, res, SeverityNotApplicable)
}
