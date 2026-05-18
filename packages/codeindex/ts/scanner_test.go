package tsscan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// atlasNodeModules returns the absolute path to atlas's own node_modules
// directory (the one populated by `npm install` at the repo root). All
// fixture tests forward this via Options.NodeModulesPaths so scanner.ts
// can resolve the `typescript` package without each fixture shipping its
// own node_modules tree.
func atlasNodeModules(t *testing.T) string {
	t.Helper()
	// scanner_test.go lives at packages/codeindex/ts/; the repo root sits
	// three directories above. We resolve relative to the source file via
	// the test's working directory (which Go sets to the package dir).
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	candidate := filepath.Join(wd, "..", "..", "..", "node_modules")
	abs, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("abs node_modules: %v", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "typescript")); err != nil {
		t.Skipf("atlas node_modules/typescript not installed (run npm install at repo root): %v", err)
	}
	return abs
}

// skipIfNoNode short-circuits a test when the Node runtime isn't on PATH;
// scanner.go itself degrades gracefully but the *_e2e tests want a real
// invocation. Mirrors the goscan testdata-availability skip pattern.
func skipIfNoNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not on PATH: %v", err)
	}
}

func runScan(t *testing.T, fixture string) *Result {
	t.Helper()
	skipIfNoNode(t)
	nm := atlasNodeModules(t)

	root, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("abs testdata: %v", err)
	}
	s := NewScanner(Options{
		NodeModulesPaths: []string{nm},
		Logger:           shared.NopLogger{},
	})
	res, err := s.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("scan %s: %v", fixture, err)
	}
	if res == nil {
		t.Fatalf("nil result for %s", fixture)
	}
	return res
}

// TestScanner_ReactRouter validates the React Router fixture produces the
// expected route → component → hook → api → endpoint chain.
func TestScanner_ReactRouter(t *testing.T) {
	t.Parallel()
	res := runScan(t, "react-router")

	idSet := make(map[string]bool, len(res.Symbols))
	for _, s := range res.Symbols {
		idSet[string(s.ID)] = true
	}

	mustHave := []string{
		"route:/login",
		"route:/dashboard",
		"LoginPage",
		"DashboardPage",
		"useLogin",
		"authApi.login",
		"POST /api/v1/auth/login",
	}
	for _, want := range mustHave {
		if !idSet[want] {
			t.Errorf("react-router: missing symbol %q; got: %v", want, sortedKeys(idSet))
		}
	}

	// At least one edge must connect the route to its component.
	hasRouteEdge := false
	for _, e := range res.Edges {
		if string(e.From) == "route:/login" && string(e.To) == "LoginPage" {
			hasRouteEdge = true
			break
		}
	}
	if !hasRouteEdge {
		t.Errorf("react-router: expected edge route:/login → LoginPage; got %d edges", len(res.Edges))
	}
}

// TestScanner_TanStack validates the file-based-route TanStack fixture.
func TestScanner_TanStack(t *testing.T) {
	t.Parallel()
	res := runScan(t, "tanstack")

	idSet := make(map[string]bool, len(res.Symbols))
	for _, s := range res.Symbols {
		idSet[string(s.ID)] = true
	}

	mustHave := []string{
		"route:/",
		"route:/dashboard",
		"useDashboard",
		"dashboardApi.getSummary",
		"GET /api/v1/dashboard/summary",
	}
	for _, want := range mustHave {
		if !idSet[want] {
			t.Errorf("tanstack: missing symbol %q; got: %v", want, sortedKeys(idSet))
		}
	}
}

// TestScanner_Expo validates the file-based-route Expo fixture (which uses
// `app/index.tsx`, `app/[id].tsx`, etc).
func TestScanner_Expo(t *testing.T) {
	t.Parallel()
	res := runScan(t, "expo")

	idSet := make(map[string]bool, len(res.Symbols))
	for _, s := range res.Symbols {
		idSet[string(s.ID)] = true
	}

	mustHave := []string{
		"route:/",
		"route:/:id",
		"usePantry",
		"pantryApi.list",
		"GET /api/v1/pantry",
	}
	for _, want := range mustHave {
		if !idSet[want] {
			t.Errorf("expo: missing symbol %q; got: %v", want, sortedKeys(idSet))
		}
	}
}

// TestScanner_NoNode degrades gracefully when node isn't on PATH.
// We simulate this by configuring a NodeBin that doesn't exist; the
// scanner must return a warning, not an error.
func TestScanner_NoNode_GracefulSkip(t *testing.T) {
	t.Parallel()
	s := NewScanner(Options{
		NodeBin: "definitely-not-node-binary-12345",
		Logger:  shared.NopLogger{},
	})
	res, err := s.Scan(context.Background(), "testdata/react-router")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Symbols) != 0 || len(res.Edges) != 0 {
		t.Fatalf("expected empty result; got %d symbols / %d edges", len(res.Symbols), len(res.Edges))
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected at least one warning about missing node binary")
	}
}

// TestValidateScannerArg confirms the shell-metacharacter rejection list.
func TestValidateScannerArg(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s       string
		wantErr bool
	}{
		{"src", false},
		{"src/routes/index.tsx", false},
		{"", true},
		{"-rf", true},
		{"--include", true},
		{"a;rm -rf /", true},
		{"a | b", true},
		{"a`whoami`", true},
		{"a$VAR", true},
		{"a\nb", true},
		{"a\x00b", true},
	}
	for _, tc := range cases {
		err := validateScannerArg(tc.s)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateScannerArg(%q) err=%v; wantErr=%v", tc.s, err, tc.wantErr)
		}
	}
}

// TestBuildScannerArgs_RejectsBadInput confirms the argv builder propagates
// validation failures rather than emitting unsafe shell args.
func TestBuildScannerArgs_RejectsBadInput(t *testing.T) {
	t.Parallel()
	_, err := buildScannerArgs("scanner.ts", "/root", Options{
		Include: []string{"src; rm -rf /"},
	})
	if err == nil {
		t.Fatal("expected error for shell-metachar in include")
	}
	if !strings.Contains(err.Error(), "shell metacharacter") {
		t.Errorf("expected shell-metacharacter error; got %v", err)
	}
}

// TestBuildScannerArgs_HappyPath confirms the argv shape used by the
// embedded scanner.ts CLI parser.
func TestBuildScannerArgs_HappyPath(t *testing.T) {
	t.Parallel()
	args, err := buildScannerArgs("/tmp/scanner.ts", "/proj", Options{
		Include: []string{"apps/web"},
		Routers: []RouterKind{TanStackRouter},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{
		"--experimental-strip-types",
		"/tmp/scanner.ts",
		"--root", "/proj",
		"--include", "apps/web",
		"--router", "tanstack",
	}
	if len(args) != len(want) {
		t.Fatalf("argv len = %d; want %d (got %v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("argv[%d] = %q; want %q", i, args[i], want[i])
		}
	}
}

// sortedKeys returns the keys of a string set in deterministic order so test
// diffs are stable. Intentionally not exported.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// simple insertion sort to avoid importing sort just for tests
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
