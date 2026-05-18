package contract

import (
	"context"
	"sort"
	"strings"
	"testing"
)

func TestRoutes_ChiEchoStdlib(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/httproutes")
	e := NewExtractor(Options{ProjectRoot: "testdata/httproutes", SkipGraphQL: true, SkipTS: true})

	res, err := e.Extract(context.Background(), idx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	want := []string{
		"POST /api/v1/auth/login",
		"GET /api/v1/auth/profile",
		"GET /api/v1/admin/users",
		"GET /api/v1/admin/sessions",
		"POST /healthz", // stdlib mux.HandleFunc Go 1.22 form
		"POST /api/status",
		"GET /",
		"ANY /legacy/*",
	}

	got := collectRouteSignatures(res.Defs)
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing expected route %q in got=%v", w, got)
		}
	}
}

func TestRoutes_AnnotationPairsByLineProximity(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/httproutes")
	e := NewExtractor(Options{ProjectRoot: "testdata/httproutes", SkipGraphQL: true, SkipTS: true})
	res, _ := e.Extract(context.Background(), idx)

	for _, d := range res.Defs {
		if d.Kind != KindRoute {
			continue
		}
		if d.Signature == "POST /api/v1/auth/login" {
			if d.FeatureID == nil || *d.FeatureID != "auth.login.chi" {
				t.Errorf("expected POST /api/v1/auth/login feature_id = auth.login.chi, got %v", d.FeatureID)
			}
			return
		}
	}
	t.Errorf("did not find POST /api/v1/auth/login route in result")
}

func TestRoutes_MultiRouterInOneFile(t *testing.T) {
	t.Parallel()

	idx := indexTestProject(t, "testdata/multirouter")
	e := NewExtractor(Options{ProjectRoot: "testdata/multirouter", SkipGraphQL: true, SkipTS: true})
	res, _ := e.Extract(context.Background(), idx)

	got := collectRouteSignatures(res.Defs)
	want := []string{
		"POST /api/v1/auth/login",
		"GET /api/v1/admin/health",
		"POST /api/v1/auth/logout",
		"GET /healthz",
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("multirouter: missing %q in %v", w, got)
		}
	}
}

func TestRoutes_HandleFuncWithoutMethodPrefix(t *testing.T) {
	t.Parallel()

	// Sanity check on parseMethodPath helper — stdlib mux.HandleFunc("/p", h)
	// should produce method="" (any), path="/p" — not silently swallow "/p"
	// as the method.
	method, path := parseMethodPath("/healthz")
	if method != "" || path != "/healthz" {
		t.Errorf("parseMethodPath(/healthz) = (%q,%q), want (\"\",\"/healthz\")", method, path)
	}
	method, path = parseMethodPath("POST /healthz")
	if method != "POST" || path != "/healthz" {
		t.Errorf("parseMethodPath(POST /healthz) = (%q,%q), want (POST,/healthz)", method, path)
	}
}

// collectRouteSignatures pulls every KindRoute signature out of a Result.
func collectRouteSignatures(defs []ContractDef) []string {
	var out []string
	for _, d := range defs {
		if d.Kind == KindRoute {
			out = append(out, d.Signature)
		}
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Compile-time anchor so the strings import survives lint.
var _ = strings.Contains
