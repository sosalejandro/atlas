package pyscan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// skipIfNoPython short-circuits when python3 isn't on PATH. scanner.go
// itself degrades gracefully but the *_e2e tests want a real invocation.
// Mirrors the tsscan skipIfNoNode pattern.
func skipIfNoPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 not on PATH: %v", err)
	}
}

func runScan(t *testing.T, fixture string) *Result {
	t.Helper()
	skipIfNoPython(t)

	root, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("abs testdata: %v", err)
	}
	s := NewScanner(Options{Logger: shared.NopLogger{}})
	res, err := s.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("scan %s: %v", fixture, err)
	}
	if res == nil {
		t.Fatalf("nil result for %s", fixture)
	}
	return res
}

// TestScanner_SampleProject exercises every node + edge kind the scanner
// is contracted to emit against testdata/sample_project/.
func TestScanner_SampleProject(t *testing.T) {
	t.Parallel()
	res := runScan(t, "sample_project")

	idSet := make(map[string]bool, len(res.Symbols))
	kindByID := make(map[string]shared.SymbolKind, len(res.Symbols))
	for _, s := range res.Symbols {
		idSet[string(s.ID)] = true
		kindByID[string(s.ID)] = s.Kind
	}

	// Module ids are derived from path; the package __init__.py exists so
	// the testdata "fixture project" is named "sample_project" but the
	// scan root IS sample_project/, so the package itself is unnamed.
	// All symbols are namespaced under the file's module id, which for
	// "main.py" at the scan root is just "main", and for "__init__.py"
	// is the empty string trimmed to "".
	mustHave := []string{
		// Module-level nodes (kind=module)
		"main",
		"sibling",
		// Top-level function + decorated function
		"main.helper",
		"main.compute",
		// Class + methods
		"main.BaseEntity",
		"main.MyClass",
		"main.MyClass.run",
		"main.MyClass.classmethod_example",
		"main.MyClass.staticmethod_example",
		// UPPER_SNAKE module constant
		"main.API_VERSION",
		// Sibling module helper
		"sibling.sibling_helper",
	}
	for _, want := range mustHave {
		if !idSet[want] {
			t.Errorf("sample_project: missing symbol %q; got: %v", want, sortedKeys(idSet))
		}
	}

	// Kind-mapping spot checks (the SymbolKind enum is the contract surface
	// callers like `atlas codebase find` rely on).
	wantKinds := map[string]shared.SymbolKind{
		"main.helper":   shared.KindFunc,
		"main.MyClass":  shared.KindType,
		"main.MyClass.run": shared.KindMethod,
		"main.API_VERSION": shared.KindConst,
	}
	for id, want := range wantKinds {
		if got := kindByID[id]; got != want {
			t.Errorf("kind(%s) = %q; want %q", id, got, want)
		}
	}

	// Edge assertions — at least one of each contracted edge kind must
	// exist. We use a set keyed by "from->to" because edge order is not
	// part of the contract.
	edgeSet := make(map[string]bool, len(res.Edges))
	for _, e := range res.Edges {
		edgeSet[string(e.From)+"->"+string(e.To)] = true
	}

	wantEdges := []string{
		// Call: compute -> helper
		"main.compute->helper",
		// Call: MyClass.run -> helper
		"main.MyClass.run->helper",
		// Inheritance: MyClass -> BaseEntity
		"main.MyClass->BaseEntity",
		// Decorator: MyClass -> register
		"main.MyClass->register",
		// Decorator: compute -> cached
		"main.compute->cached",
		// Absolute import: module -> os
		"main->os",
		// from-import: module -> collections.OrderedDict
		"main->collections.OrderedDict",
		// Relative import: module -> .sibling
		"main->.sibling",
	}
	for _, want := range wantEdges {
		if !edgeSet[want] {
			t.Errorf("sample_project: missing edge %q", want)
		}
	}

	// Syntax error path — syntax_error.py must surface as a warning AND
	// in Files with a non-empty SyntaxError, AND must NOT crash the scan.
	gotSyntaxErr := false
	for _, f := range res.Files {
		if strings.HasSuffix(f.Path, "syntax_error.py") && f.SyntaxError != "" {
			gotSyntaxErr = true
			break
		}
	}
	if !gotSyntaxErr {
		t.Error("expected syntax_error.py in Files with non-empty SyntaxError")
	}
	gotSyntaxWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "syntax_error.py") {
			gotSyntaxWarn = true
			break
		}
	}
	if !gotSyntaxWarn {
		t.Errorf("expected syntax_error.py warning; got warnings=%v", res.Warnings)
	}
}

// TestScanner_NoPython_GracefulSkip verifies that when python3 isn't on
// PATH the scanner returns a warning rather than an error. We simulate
// this by configuring a PythonBin that doesn't exist; the scanner must
// return a warning, not an error.
func TestScanner_NoPython_GracefulSkip(t *testing.T) {
	t.Parallel()
	s := NewScanner(Options{
		PythonBin: "definitely-not-python3-binary-12345",
		Logger:    shared.NopLogger{},
	})
	res, err := s.Scan(context.Background(), "testdata/sample_project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Symbols) != 0 || len(res.Edges) != 0 {
		t.Fatalf("expected empty result; got %d symbols / %d edges", len(res.Symbols), len(res.Edges))
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected at least one warning about missing python3 binary")
	}
	// The warning message must be actionable — it should mention how to
	// install or skip Python.
	joined := strings.Join(res.Warnings, " | ")
	if !strings.Contains(joined, "PATH") {
		t.Errorf("expected warning to mention PATH; got %q", joined)
	}
}

// TestScanner_NoPython_EmptyPATH verifies the runtime-detection branch
// reached via an empty PATH (the production failure mode when python3
// genuinely isn't installed). Uses a t.Setenv-scoped PATH override.
func TestScanner_NoPython_EmptyPATH(t *testing.T) {
	// Cannot t.Parallel — Setenv would race with concurrent tests reading PATH.
	t.Setenv("PATH", "")
	s := NewScanner(Options{Logger: shared.NopLogger{}})
	res, err := s.Scan(context.Background(), "testdata/sample_project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected warning for empty PATH lookup")
	}
}

// TestScanner_EmptyRoot_Error confirms the rootDir contract.
func TestScanner_EmptyRoot_Error(t *testing.T) {
	t.Parallel()
	s := NewScanner(Options{Logger: shared.NopLogger{}})
	if _, err := s.Scan(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty rootDir")
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
		{"src/pkg/mod.py", false},
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
	_, err := buildScannerArgs("scanner.py", "/root", Options{
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
// embedded scanner.py CLI parser.
func TestBuildScannerArgs_HappyPath(t *testing.T) {
	t.Parallel()
	args, err := buildScannerArgs("/tmp/scanner.py", "/proj", Options{
		Include: []string{"pkg"},
		Exclude: []string{"vendor"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{
		"/tmp/scanner.py",
		"--root", "/proj",
		"--include", "pkg",
		"--exclude", "vendor",
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

// TestValidatePythonBin_AbsoluteRequired locks in the post-LookPath
// invariant: any caller that reaches newPythonCommand with a relative
// path is rejected, eliminating the "spawned via $PATH at the OS level"
// vector.
func TestValidatePythonBin_AbsoluteRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s       string
		wantErr bool
	}{
		{"/usr/bin/python3", false},
		{"python3", true},      // relative — must be rejected
		{"", true},             // empty
		{"py;ls", true},        // shell metachar
		{"py\nthon3", true},    // newline
	}
	for _, tc := range cases {
		err := validatePythonBin(tc.s)
		if (err != nil) != tc.wantErr {
			t.Errorf("validatePythonBin(%q) err=%v; wantErr=%v", tc.s, err, tc.wantErr)
		}
	}
}

// TestScannerSource_Embedded confirms //go:embed picked up scanner.py and
// the contents look like valid Python (defense against an empty embed
// silently shipping in a release).
func TestScannerSource_Embedded(t *testing.T) {
	t.Parallel()
	if ScannerSource == "" {
		t.Fatal("ScannerSource is empty — //go:embed scanner.py failed")
	}
	if !strings.Contains(ScannerSource, "import ast") {
		t.Errorf("ScannerSource missing expected `import ast`; head=%q",
			ScannerSource[:min(len(ScannerSource), 200)])
	}
}

// TestScanner_Close_Idempotent confirms calling Close more than once is
// safe (the doc-comment contract) — and that calling Close before Scan
// is also a no-op rather than a crash.
func TestScanner_Close_Idempotent(t *testing.T) {
	t.Parallel()
	s := NewScanner(Options{Logger: shared.NopLogger{}})
	if err := s.Close(); err != nil {
		t.Fatalf("Close before Scan: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestScanner_TempfileCleanup confirms the embedded scanner.py tempdir is
// removed by Close.
func TestScanner_TempfileCleanup(t *testing.T) {
	t.Parallel()
	skipIfNoPython(t)

	s := NewScanner(Options{Logger: shared.NopLogger{}})
	root, _ := filepath.Abs("testdata/sample_project")
	if _, err := s.Scan(context.Background(), root); err != nil {
		t.Fatalf("scan: %v", err)
	}
	scriptPath := s.scriptPath
	if scriptPath == "" {
		t.Fatal("scriptPath empty after Scan")
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("scanner.py missing after Scan: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("scanner.py tempdir still exists after Close: stat err=%v", err)
	}
}

// sortedKeys returns the keys of a string set in deterministic order so test
// diffs are stable. Intentionally not exported.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
