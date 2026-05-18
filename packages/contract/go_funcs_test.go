package contract

import (
	"context"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// indexTestProject is a tiny helper that runs codeindex.IndexProject on a
// testdata subdir and returns the resulting Index.
func indexTestProject(t *testing.T, root string) *codeindex.Index {
	t.Helper()
	idx, err := codeindex.IndexProject(context.Background(), root, codeindex.Options{
		HashFiles: false,
		SkipTS:    true, // tests are pure Go; skip the Node subprocess
	})
	if err != nil {
		t.Fatalf("IndexProject(%q): %v", root, err)
	}
	return idx
}

func TestGoFuncs_PairsAnnotationWithDeclaration(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/gofuncs")
	e := NewExtractor(Options{ProjectRoot: "testdata/gofuncs", SkipGraphQL: true, SkipTS: true})

	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	want := map[string]string{
		"Login":    "auth.login",    // @atlas:contract auth.login
		"Register": "auth.register", // legacy @testreg auth.register
	}
	got := map[string]string{}
	for _, d := range res.Defs {
		if d.Kind != KindFunc {
			continue
		}
		if d.FeatureID != nil {
			got[d.Name] = string(*d.FeatureID)
		} else {
			got[d.Name] = ""
		}
	}

	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("Go funcs: %s feature_id = %q, want %q (got map=%v)",
				name, got[name], expected, got)
		}
	}

	// NoAnnotation must appear with no FeatureID.
	if v, ok := got["NoAnnotation"]; !ok {
		t.Error("NoAnnotation contract def missing from result")
	} else if v != "" {
		t.Errorf("NoAnnotation feature_id = %q, want empty", v)
	}
}

func TestGoFuncs_SkipsGeneratedAndTestPaths(t *testing.T) {
	t.Parallel()

	e := NewExtractor(Options{ProjectRoot: "."})
	// Hand-craft an index with generated/test paths so we don't have to
	// stuff fake _test.go files into testdata.
	idx := &codeindex.Index{Symbols: nil, SymbolLangs: map[shared.SymbolID]string{}}
	for _, p := range []string{
		"src/generated/foo.go",
		"src/mocks/bar.go",
		"src/services/baz_test.go",
		"src/services/quux.gen.go",
	} {
		ann := newSymbolForPathTest(p)
		idx.Symbols = append(idx.Symbols, ann)
	}
	defs, _ := e.extractGoFuncs(idx, newAnnotationIndex(nil, 10))
	if len(defs) != 0 {
		t.Errorf("expected 0 defs for generated/test paths, got %d: %+v", len(defs), defs)
	}
}
