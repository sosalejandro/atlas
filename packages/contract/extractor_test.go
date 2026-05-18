package contract

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// TestExtractor_MixedSource is the package-level integration test. It
// indexes a small project that has Go funcs + a Huma operation + a
// GraphQL schema and asserts the merged output matches the documented
// golden contract list.
func TestExtractor_MixedSource(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/mixed")
	e := NewExtractor(Options{ProjectRoot: "testdata/mixed", SkipTS: true})
	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// 1. Exactly one huma-op (loginUser).
	humas := filterByKind(res.Defs, KindHumaOp)
	if len(humas) != 1 || humas[0].Operation.OperationID != "loginUser" {
		t.Errorf("expected one Huma op loginUser, got %d: %+v", len(humas), humas)
	}

	// 2. Exactly two GraphQL defs (Mutation.loginUser is one + one type
	// inside Mutation block).
	gqls := filterByKind(res.Defs, KindGraphQL)
	gqlNames := names(gqls)
	wantGQL := []string{"Mutation.loginUser"}
	for _, w := range wantGQL {
		if !contains(gqlNames, w) {
			t.Errorf("missing GraphQL contract %q in %v", w, gqlNames)
		}
	}

	// 3. The merge pass MUST suppress the plain Go-func ContractDef for
	// AuthHandler.Login since a higher-fidelity huma-op covers the same
	// symbol. Look for KindFunc entries pointing at AuthHandler.Login.
	for _, d := range res.Defs {
		if d.Kind == KindFunc && containsSym(d.Symbols, "AuthHandler.Login") {
			t.Errorf("merge pass should drop the plain func entry for AuthHandler.Login (huma-op covers it); got %+v", d)
		}
	}

	// 4. The Huma op's FeatureID must come from @atlas:contract.
	if humas[0].FeatureID == nil || *humas[0].FeatureID != "auth.login" {
		t.Errorf("huma loginUser FeatureID = %v, want auth.login", humas[0].FeatureID)
	}

	// 5. computeToken — a plain helper func with no annotation — must
	// stay in the result as KindFunc so callers see auxiliary funcs.
	if !hasFuncNamed(res.Defs, "computeToken") {
		t.Errorf("expected computeToken KindFunc in result, missing from %v", names(res.Defs))
	}
}

func TestExtractor_NilIndex(t *testing.T) {
	t.Parallel()
	e := NewExtractor(Options{ProjectRoot: "."})
	if _, err := e.Extract(context.Background(), nil); err == nil {
		t.Fatal("Extract(nil) expected error, got nil")
	}
}

func TestExtractor_MissingProjectRoot(t *testing.T) {
	t.Parallel()
	e := NewExtractor(Options{})
	_, err := e.Extract(context.Background(), &codeindex.Index{})
	if err == nil {
		t.Fatal("Extract without ProjectRoot expected error, got nil")
	}
}

func TestExtractor_EmptyProject(t *testing.T) {
	t.Parallel()
	idx := indexTestProject(t, "testdata/emptyproj")
	e := NewExtractor(Options{ProjectRoot: "testdata/emptyproj", SkipTS: true})
	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Defs) != 0 {
		t.Errorf("emptyproj should produce zero defs; got %d: %+v", len(res.Defs), res.Defs)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("emptyproj should produce zero warnings; got %v", res.Warnings)
	}
}

func TestExtractor_StableSort(t *testing.T) {
	t.Parallel()
	idx := indexTestProject(t, "testdata/httproutes")
	e := NewExtractor(Options{ProjectRoot: "testdata/httproutes", SkipGraphQL: true, SkipTS: true})

	// Run twice; output must be byte-identical (within slice-of-defs).
	r1, _ := e.Extract(context.Background(), idx)
	r2, _ := e.Extract(context.Background(), idx)

	if len(r1.Defs) != len(r2.Defs) {
		t.Fatalf("two runs produced different lengths: %d vs %d", len(r1.Defs), len(r2.Defs))
	}
	for i := range r1.Defs {
		if r1.Defs[i].Name != r2.Defs[i].Name ||
			r1.Defs[i].Kind != r2.Defs[i].Kind ||
			r1.Defs[i].FilePath != r2.Defs[i].FilePath ||
			r1.Defs[i].Line != r2.Defs[i].Line {
			t.Errorf("run-to-run drift at idx %d:\n  r1=%+v\n  r2=%+v", i, r1.Defs[i], r2.Defs[i])
		}
	}
}

func names(defs []ContractDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}

func hasFuncNamed(defs []ContractDef, name string) bool {
	for _, d := range defs {
		if d.Kind == KindFunc && d.Name == name {
			return true
		}
	}
	return false
}

func containsSym(syms []shared.SymbolID, target string) bool {
	for _, s := range syms {
		if string(s) == target || strings.HasSuffix(string(s), "."+target) {
			return true
		}
	}
	return false
}
