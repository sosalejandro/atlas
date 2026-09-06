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
// `atlas audit`, about nothing.
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

// An annotation naming a feature the store does not have means its
// symbol could not be resolved at ingest: the annotation is pointing at
// code that moved or vanished.
func TestFeatureLinkage_AnnotationForUnknownFeature_Warns(t *testing.T) {
	f := newFixture(t)
	id := f.insertSymbol(t, "pkg.A", "pkg/a.go", 3)
	f.upsertFeature(t, "auth.login")
	f.linkFeature(t, "auth.login", id)
	f.upsertAnnotation(t, "pkg/ghost.go", 9, "auth.logout")

	res := runCheck(t, featureLinkage{}, f.env(t))

	assertSeverity(t, res, SeverityWarn)
	if got := res.Details["dangling_annotations"]; got != 1 {
		t.Errorf("dangling_annotations = %v, want 1", got)
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
