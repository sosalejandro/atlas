package pyscan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
		"annotated_comment",
		"annotated_decorator",
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
		// Comment-annotated fixture symbols
		"annotated_comment.ingest_rows",
		"annotated_comment.parse_row",
		// Decorator-annotated fixture symbols
		"annotated_decorator.ship_one",
		"annotated_decorator.BatchShipper",
		"annotated_decorator.BatchShipper.enqueue",
		"annotated_decorator.BatchShipper.flush",
	}
	for _, want := range mustHave {
		if !idSet[want] {
			t.Errorf("sample_project: missing symbol %q; got: %v", want, sortedKeys(idSet))
		}
	}

	// Kind-mapping spot checks (the SymbolKind enum is the contract surface
	// callers like `atlas codebase find` rely on).
	wantKinds := map[string]shared.SymbolKind{
		"main.helper":      shared.KindFunc,
		"main.MyClass":     shared.KindType,
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

	// Edge targets are resolved to fully-qualified symbol ids when an
	// emitted same-module symbol matches the raw target — see
	// pyEdgeResolver. `helper`, `BaseEntity`, `register`, `cached` all
	// live alongside the call/inheritance/decorator source in main.py,
	// so they promote from bare names to `main.<name>`. External targets
	// (os, collections.OrderedDict, .sibling) pass through verbatim
	// because no same-module symbol shadows them.
	wantEdges := []string{
		// Call: compute -> helper (resolved to main.helper)
		"main.compute->main.helper",
		// Call: MyClass.run -> helper (resolved to main.helper)
		"main.MyClass.run->main.helper",
		// Inheritance: MyClass -> BaseEntity (resolved to main.BaseEntity)
		"main.MyClass->main.BaseEntity",
		// Decorator: MyClass -> register (resolved to main.register)
		"main.MyClass->main.register",
		// Decorator: compute -> cached (resolved to main.cached)
		"main.compute->main.cached",
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

// TestScanner_NestedScopeCallAttribution is the wire-level regression
// test for issue #18 (call edges lose caller identity because
// `ast.walk` descended into nested function/class scopes).
//
// Asserts the call-edge attribution contract at the scanner boundary:
//
//  1. A nested closure's call attributes ONLY to the inner symbol,
//     never duplicated under the enclosing function.
//  2. A class method's nested closure's call attributes ONLY to the
//     inner symbol, never duplicated under the method.
//  3. A comprehension's call (no nameable scope) attributes to the
//     enclosing function — Python comprehensions create a scope but
//     no symbol that ends up in the symbol table, so the only
//     meaningful caller is the enclosing def.
//
// The integration test in packages/store/ingest_pyscan_integration_test.go
// covers the same contract end-to-end via the SQLite store; this test
// pins it at the wire boundary so a scanner-only regression doesn't
// have to round-trip through ingest to surface.
func TestScanner_NestedScopeCallAttribution(t *testing.T) {
	t.Parallel()
	res := runScan(t, "sample_project")

	// Index call-edge attributions: callee qualified id → set of callers.
	callers := map[string]map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != "call" {
			continue
		}
		bucket, ok := callers[string(e.To)]
		if !ok {
			bucket = map[string]bool{}
			callers[string(e.To)] = bucket
		}
		bucket[string(e.From)] = true
	}

	// AC #1: class method with nested closure.
	// `DoubleNested.method` MUST NOT call `nested_scopes.deep_helper`
	// — only its inner `closure()` does. The pre-fix scanner walked
	// `ast.walk(method)` which descended into `closure`'s body and
	// emitted `method -> deep_helper` AS WELL — the duplicate edge is
	// the smoking gun for issue #18.
	if callers["nested_scopes.deep_helper"]["nested_scopes.DoubleNested.method"] {
		t.Errorf(
			"regression: nested_scopes.DoubleNested.method attributed to "+
				"deep_helper call — issue #18 nested-closure caller-identity "+
				"collapse inside class method. All callers seen: %v",
			keysOf(callers["nested_scopes.deep_helper"]),
		)
	}
	if !callers["nested_scopes.deep_helper"]["nested_scopes.DoubleNested.method.closure"] {
		t.Errorf(
			"expected nested_scopes.DoubleNested.method.closure to call "+
				"deep_helper; callers seen: %v",
			keysOf(callers["nested_scopes.deep_helper"]),
		)
	}

	// AC #2: free function with nested closure.
	// `outer()` MUST NOT call `leaf_only` — only its inner `inside()`
	// does. Same shape as AC #1 but with module-level function as the
	// outer scope rather than a class method, so a regression that
	// special-cases method bodies still gets caught here.
	//
	// Note: the resolver does NOT promote the bare `leaf_only` target
	// for a NESTED-function caller (its byModule index is keyed on the
	// flat enclosing-module id and `nested_scopes.outer.inside`'s caller
	// module bucket only knows symbols immediately under `outer`). The
	// raw `leaf_only` therefore lands as an external stub — that's
	// orthogonal to issue #18 and is the correct downstream behaviour
	// for an unresolvable callee. The attribution we DO assert is on
	// the `from` side, which is what issue #18 is actually about.
	if callers["leaf_only"]["nested_scopes.outer"] {
		t.Errorf(
			"regression: nested_scopes.outer attributed to leaf_only call — "+
				"issue #18 caller-identity collapse on free-function nested "+
				"closures. All callers seen: %v",
			keysOf(callers["leaf_only"]),
		)
	}
	if !callers["leaf_only"]["nested_scopes.outer.inside"] {
		t.Errorf(
			"expected nested_scopes.outer.inside to be the (sole) caller of "+
				"leaf_only; callers seen: %v",
			keysOf(callers["leaf_only"]),
		)
	}

	// AC #3: comprehension scope.
	// `[x for x in items if needs(x)]` inside `DoubleNested.method`
	// MUST attribute `needs` to the method (no inner symbol exists for
	// the comprehension itself).
	if !callers["nested_scopes.needs"]["nested_scopes.DoubleNested.method"] {
		t.Errorf(
			"expected nested_scopes.DoubleNested.method to call needs() "+
				"(comprehension calls belong to the enclosing function); "+
				"callers seen: %v",
			keysOf(callers["nested_scopes.needs"]),
		)
	}
}

// keysOf renders a string-keyed set as a sorted slice for stable
// error messages in the nested-scope assertions above.
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestScanner_ImportEdgeLines is the regression test for the bug where
// every Python import edge reported line=1 regardless of where the
// import statement actually appeared (atlas-internal #17 / sosalejandro/
// atlas fix branch). The root cause was that scanner.py emitted import
// edges without a per-edge line, and the Go ingestor defaulted to the
// FROM symbol's declaration line — which is line 1 for every module.
//
// The fix wires `node.lineno` through `_Edge.line` → `rawEdge.Line` →
// `graph.Edge.Line`, and the store-side ingestor prefers that value
// when non-zero. This test exercises the wire end-to-end against the
// existing sample_project fixture, which has three imports on lines
// 26, 27, and 28 of main.py:
//
//	L26: import os
//	L27: from collections import OrderedDict
//	L28: from . import sibling
//
// Asserting that the emitted edges carry exactly those lines proves
// both (a) the Python scanner records the AST line and (b) the Go
// scanner round-trips it through `mapToResult` into `graph.Edge.Line`.
func TestScanner_ImportEdgeLines(t *testing.T) {
	t.Parallel()
	res := runScan(t, "sample_project")

	type importEdge struct {
		to   string
		line int
	}
	gotImports := make(map[string]int) // edge target -> line
	for _, e := range res.Edges {
		if e.Kind != "import" {
			continue
		}
		// Only main.py's module-level imports matter for this assertion.
		if e.From != "main" {
			continue
		}
		gotImports[string(e.To)] = e.Line
	}

	wantImports := []importEdge{
		{"os", 26},
		{"collections.OrderedDict", 27},
		{".sibling", 28},
	}
	for _, want := range wantImports {
		got, ok := gotImports[want.to]
		if !ok {
			t.Errorf("missing import edge main->%s; got: %+v", want.to, gotImports)
			continue
		}
		if got != want.line {
			t.Errorf("import edge main->%s: line = %d; want %d",
				want.to, got, want.line)
		}
	}

	// Stronger negative — no import edge in this fixture should report
	// line == 1. Pre-fix every import was anchored at line 1; the
	// regression test catches a future change that silently re-introduces
	// the old behaviour (e.g. someone drops the `line=` kwarg from
	// `_visit_import`).
	for to, line := range gotImports {
		if line <= 1 {
			t.Errorf("import edge main->%s: line = %d (≤ 1) — "+
				"regression of atlas-internal #17 (all import edges "+
				"reporting line=1)", to, line)
		}
	}

	// Distinct-line spot check: three imports on three different lines
	// must produce three distinct line values. This catches a scenario
	// where the fix records `node.lineno` correctly but a downstream
	// layer collapses them (e.g. an aggregator that keys on `from+to`
	// without `line`).
	seen := make(map[int]bool)
	for _, line := range gotImports {
		seen[line] = true
	}
	if len(seen) < 3 {
		t.Errorf("expected 3 distinct import lines for main.py; got %d (lines=%v)",
			len(seen), gotImports)
	}
}

// TestScanner_Annotations_BothModes exercises issue #53's two recognition
// modes (comment + decorator) and the class-level propagation gotcha.
//
// Layout under testdata/sample_project/:
//
//   - annotated_comment.py:
//     L13 `# @atlas:feature ingest-csv-imports` → ingest_rows  (L14)
//     L19 `# @atlas:contract ingest-csv-imports.parse-row` → parse_row (L20)
//
//   - annotated_decorator.py:
//     L21 `@atlas.feature("ship-orders")` → ship_one  (L22)
//     L27 `@atlas.feature("ship-orders.batch")` → BatchShipper  (L28)
//     Class-level propagation → __init__ (L37), enqueue (L40), flush (L44).
//
// Total expected: 2 (comment) + 2 (decorator on def/class) + 3
// (propagation) = 7 records.
func TestScanner_Annotations_BothModes(t *testing.T) {
	t.Parallel()
	res := runScan(t, "sample_project")

	type key struct {
		kind shared.AnnotationKind
		id   string
		path string
		line int
	}
	got := make(map[key]bool, len(res.Annotations))
	for _, a := range res.Annotations {
		if len(a.IDs) == 0 {
			continue
		}
		got[key{a.Kind, a.IDs[0], a.Position.Path, a.Position.Line}] = true
	}

	want := []key{
		// Comment-style hits. Anchored at the def line (one below the
		// comment-bearing line) — that's the line the scanner reports
		// via node.lineno so LookupSymbolAtOrAfterLine resolves to the
		// symbol below the comment.
		{shared.AnnFeature, "ingest-csv-imports", "annotated_comment.py", 13},
		{shared.AnnContract, "ingest-csv-imports.parse-row", "annotated_comment.py", 19},
		// Decorator-style hits at the symbol's def line.
		{shared.AnnFeature, "ship-orders", "annotated_decorator.py", 22},
		{shared.AnnFeature, "ship-orders.batch", "annotated_decorator.py", 28},
		// Class-level propagation — one record per method, anchored at
		// the method's source line.
		{shared.AnnFeature, "ship-orders.batch", "annotated_decorator.py", 37},
		{shared.AnnFeature, "ship-orders.batch", "annotated_decorator.py", 40},
		{shared.AnnFeature, "ship-orders.batch", "annotated_decorator.py", 44},
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("missing annotation %+v", k)
		}
	}

	// Stronger negative: a helper (`_read_count`) that lives in the same
	// file as the comment-annotated symbols must NOT pick up a stray
	// feature link. Asserted via "no annotation anchored at its line".
	for _, a := range res.Annotations {
		if a.Position.Path == "annotated_comment.py" && a.Position.Line == 24 {
			t.Errorf("unexpected annotation on helper @ annotated_comment.py:24: %+v", a)
		}
	}

	// Source attribution: every annotation surfaced by the AST walker
	// must carry SourceAtlas so the materialise step treats it as a
	// real annotation (testreg's legacy path is for `@testreg` only).
	for _, a := range res.Annotations {
		if a.Source != shared.SourceAtlas {
			t.Errorf("annotation %+v has Source=%q; want SourceAtlas",
				a, a.Source)
		}
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
		{"python3", true},   // relative — must be rejected
		{"", true},          // empty
		{"py;ls", true},     // shell metachar
		{"py\nthon3", true}, // newline
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
