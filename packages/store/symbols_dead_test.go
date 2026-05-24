package store

import (
	"sort"
	"testing"

	"github.com/sosalejandro/atlas/packages/shared"
)

// TestIsTestPath locks in the lexical test-file convention FindDead
// shares with the SQL builder. Both branches MUST agree — a path the
// Go predicate flags as test must be one the SQL excludes, and vice
// versa. The cases are seeded from real shapes the Python (pytest) and
// Go (go test) ecosystems produce.
func TestIsTestPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		// pytest conventions.
		{"tests/test_foo.py", true},
		{"services/api/tests/unit/test_x.py", true},
		{"src/pkg/test_helpers.py", true},
		{"pkg/conftest.py", true},
		{"conftest.py", true},
		{"src/pkg/foo_test.py", true},

		// go test convention.
		{"internal/foo/bar_test.go", true},

		// Production files that should NOT match — including paths
		// whose basename starts with "test" (without an underscore)
		// and module names that contain "test" as a substring.
		{"src/pkg/test.py", false},
		{"src/pkg/testing.py", false},
		{"src/pkg/contestants.py", false},
		{"src/pkg/foo.py", false},
		{"pkg/util.go", false},
		{"", false},
	}
	for _, tc := range cases {
		got := IsTestPath(tc.path)
		if got != tc.want {
			t.Errorf("IsTestPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestNormalizeScopeFilter drops typos + duplicates so a CLI consumer
// can't accidentally produce an IN-clause that matches nothing.
func TestNormalizeScopeFilter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"single valid", []string{"module"}, []string{"module"}},
		{
			"all valid preserve order",
			[]string{"function", "module", "type_checking"},
			[]string{"function", "module", "type_checking"},
		},
		{
			"drop typo",
			[]string{"module", "FUNCTION", "conditional"},
			[]string{"module", "conditional"},
		},
		{
			"dedupe",
			[]string{"module", "module", "conditional"},
			[]string{"module", "conditional"},
		},
		{"all typos", []string{"x", "y"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeScopeFilter(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// seedDeadFixture seeds a small fixture with one live symbol, one dead
// symbol, an external:py stub, a test-only-used symbol, and a deferred
// import (function-scope) target. Returns the symbol qualified names
// keyed by role so individual tests can assert on the expected
// candidate set without re-deriving ids.
func seedDeadFixture(t *testing.T, s *Store) map[string]shared.SymbolID {
	t.Helper()
	ctx := t.Context()

	type sym struct {
		qn   string
		file string
		line int
	}

	mk := func(p sym) int64 {
		t.Helper()
		id, err := s.Symbols().Insert(ctx, SymbolRow{
			QualifiedName: shared.SymbolID(p.qn),
			Kind:          shared.KindFunc,
			FilePath:      p.file,
			Line:          p.line,
		})
		if err != nil {
			t.Fatalf("seed %s: %v", p.qn, err)
		}
		return id
	}

	// Live: imported at module scope from main.
	mainID := mk(sym{"pkg.main", "pkg/main.py", 1})
	usedID := mk(sym{"pkg.used.fn", "pkg/used.py", 1})

	// Dead: nobody imports orphan.
	mk(sym{"pkg.orphan.fn", "pkg/orphan.py", 1})

	// Test-only used: only a test file imports it.
	testOnlyID := mk(sym{"pkg.test_only.fn", "pkg/test_only.py", 1})
	testFileID := mk(sym{"tests.test_x", "tests/test_x.py", 1})

	// External stub — never reported as dead.
	mk(sym{"os.path.join", externalPyStubPath, 1})

	// Deferred-only target: import lives inside a function body so the
	// edge carries the function scope. Used to assert scope filtering.
	deferredID := mk(sym{"pkg.deferred.fn", "pkg/deferred.py", 1})

	// Live edge: pkg.main → pkg.used.fn (module scope).
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: mainID, ToID: usedID, Kind: EdgeKindImport,
		FilePath: "pkg/main.py", Line: 1,
		Meta: EdgeMetaImportScopeModule,
	}); err != nil {
		t.Fatalf("seed live edge: %v", err)
	}

	// Test-only edge: tests/test_x.py → pkg.test_only.fn (module scope).
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: testFileID, ToID: testOnlyID, Kind: EdgeKindImport,
		FilePath: "tests/test_x.py", Line: 1,
		Meta: EdgeMetaImportScopeModule,
	}); err != nil {
		t.Fatalf("seed test-only edge: %v", err)
	}

	// Deferred edge: pkg.main → pkg.deferred.fn (function scope).
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: mainID, ToID: deferredID, Kind: EdgeKindImport,
		FilePath: "pkg/main.py", Line: 5,
		Meta: EdgeMetaImportScopeFunction,
	}); err != nil {
		t.Fatalf("seed deferred edge: %v", err)
	}

	return map[string]shared.SymbolID{
		"main":      "pkg.main",
		"used":      "pkg.used.fn",
		"orphan":    "pkg.orphan.fn",
		"test_only": "pkg.test_only.fn",
		"test_file": "tests.test_x",
		"deferred":  "pkg.deferred.fn",
		"external":  "os.path.join",
	}
}

// qnSet helps tests assert on the candidate names regardless of order
// (FindDead orders by file_path/line, but tests should be robust to
// the seed ordering rather than coupling to it).
func qnSet(rows []DeadCodeCandidate) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[string(r.Symbol.QualifiedName)] = true
	}
	return out
}

func sortedQNs(rows []DeadCodeCandidate) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, string(r.Symbol.QualifiedName))
	}
	sort.Strings(out)
	return out
}

// TestFindDead_DefaultExcludesTests is the headline behaviour: the
// store-level default (kind=import, no scope filter, no test
// inclusion) flags the truly-orphan symbol AND the test-only-used
// symbol AND the main entrypoint (nothing imports main), while
// leaving the live symbol alone and never reporting the external:py
// stub.
func TestFindDead_DefaultExcludesTests(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	_ = seedDeadFixture(t, s)

	rows, err := s.Symbols().FindDead(t.Context(), DeadCodeFilter{
		EdgeKind: EdgeKindImport,
	})
	if err != nil {
		t.Fatalf("FindDead: %v", err)
	}
	got := qnSet(rows)

	// MUST appear: orphan + test_only + main entry point. The
	// store-level default leaves ScopeFilter empty, which means
	// "any scope counts" — so deferred is kept alive by its
	// function-scope edge.
	wantPresent := []string{
		"pkg.orphan.fn",
		"pkg.test_only.fn",
		"pkg.main",
	}
	for _, qn := range wantPresent {
		if !got[qn] {
			t.Errorf("expected %q in dead candidates; got: %v", qn, sortedQNs(rows))
		}
	}

	// MUST NOT appear: live symbol, external stub, deferred (alive
	// via the function-scope edge because the default filter
	// counts every scope).
	for _, qn := range []string{"pkg.used.fn", "os.path.join", "pkg.deferred.fn"} {
		if got[qn] {
			t.Errorf("unexpected %q in dead candidates: %v", qn, sortedQNs(rows))
		}
	}
}

// TestFindDead_IncludeTestsKeepsTestOnlyAlive verifies the toggle:
// when test importers count, the test_only symbol drops off the dead
// list because tests/test_x.py imports it.
func TestFindDead_IncludeTestsKeepsTestOnlyAlive(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	_ = seedDeadFixture(t, s)

	rows, err := s.Symbols().FindDead(t.Context(), DeadCodeFilter{
		EdgeKind:     EdgeKindImport,
		IncludeTests: true,
	})
	if err != nil {
		t.Fatalf("FindDead: %v", err)
	}
	got := qnSet(rows)

	if got["pkg.test_only.fn"] {
		t.Errorf("--include-tests should keep test_only alive; got: %v",
			sortedQNs(rows))
	}
	// Orphan still dead — its dead-ness is independent of test
	// inclusion.
	if !got["pkg.orphan.fn"] {
		t.Errorf("orphan should remain dead; got: %v", sortedQNs(rows))
	}
}

// TestFindDead_ScopeFilterDropsDeferred locks in the scope semantics
// matching the CLI default (module+conditional only): the
// deferred-only target lands on the dead list because the only edge
// pointing at it is function-scope, which the filter rejects.
func TestFindDead_ScopeFilterDropsDeferred(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	_ = seedDeadFixture(t, s)

	rows, err := s.Symbols().FindDead(t.Context(), DeadCodeFilter{
		EdgeKind: EdgeKindImport,
		ScopeFilter: []string{
			EdgeMetaImportScopeModule,
			EdgeMetaImportScopeConditional,
		},
	})
	if err != nil {
		t.Fatalf("FindDead: %v", err)
	}
	got := qnSet(rows)

	if !got["pkg.deferred.fn"] {
		t.Errorf("function-scope-only target should be dead under module+conditional filter; got: %v",
			sortedQNs(rows))
	}
	if !got["pkg.orphan.fn"] {
		t.Errorf("orphan still dead; got: %v", sortedQNs(rows))
	}
	if got["pkg.used.fn"] {
		t.Errorf("used (module-scope edge) must stay live; got: %v",
			sortedQNs(rows))
	}
}

// TestFindDead_PathPrefixNarrows checks the path-prefix predicate
// restricts the candidate set without affecting the edge predicate
// (an edge from outside the prefix still keeps a symbol live).
func TestFindDead_PathPrefixNarrows(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	_ = seedDeadFixture(t, s)

	rows, err := s.Symbols().FindDead(t.Context(), DeadCodeFilter{
		EdgeKind:   EdgeKindImport,
		PathPrefix: "pkg/orphan",
	})
	if err != nil {
		t.Fatalf("FindDead: %v", err)
	}
	got := qnSet(rows)
	if !got["pkg.orphan.fn"] {
		t.Errorf("orphan should appear under pkg/orphan prefix; got: %v",
			sortedQNs(rows))
	}
	for _, bad := range []string{"pkg.main", "pkg.used.fn", "pkg.test_only.fn"} {
		if got[bad] {
			t.Errorf("symbol outside pkg/orphan must not appear: %s in %v",
				bad, sortedQNs(rows))
		}
	}
}

// TestFindDead_KindAll counts any-kind incoming edges. A symbol with
// only a `call` edge in but no `import` edge is dead under
// kind=import but live under kind=all.
func TestFindDead_KindAll(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := t.Context()

	from, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "pkg.caller", Kind: shared.KindFunc,
		FilePath: "pkg/caller.py", Line: 1,
	})
	if err != nil {
		t.Fatalf("seed caller: %v", err)
	}
	to, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "pkg.callee", Kind: shared.KindFunc,
		FilePath: "pkg/callee.py", Line: 1,
	})
	if err != nil {
		t.Fatalf("seed callee: %v", err)
	}
	if _, err := s.Edges().Insert(ctx, EdgeRow{
		FromID: from, ToID: to, Kind: EdgeKindCall,
		FilePath: "pkg/caller.py", Line: 1,
	}); err != nil {
		t.Fatalf("seed call edge: %v", err)
	}

	// Under kind=import: callee is dead (no import edge points at it).
	importRows, err := s.Symbols().FindDead(ctx, DeadCodeFilter{EdgeKind: EdgeKindImport})
	if err != nil {
		t.Fatalf("FindDead import: %v", err)
	}
	if !qnSet(importRows)["pkg.callee"] {
		t.Errorf("callee should be dead under kind=import; got: %v",
			sortedQNs(importRows))
	}

	// Under kind="" (any): callee is alive — the call edge keeps it.
	anyRows, err := s.Symbols().FindDead(ctx, DeadCodeFilter{})
	if err != nil {
		t.Fatalf("FindDead any: %v", err)
	}
	if qnSet(anyRows)["pkg.callee"] {
		t.Errorf("callee must be live under kind=any; got: %v",
			sortedQNs(anyRows))
	}
}

// TestFindDead_ExternalStubFilteredOnLikePattern locks in the external
// stub filter — the value MUST match what codeindex/py.resolver.go
// emits. If that constant is ever renamed, this test fires.
func TestFindDead_ExternalStubFilteredOnLikePattern(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := t.Context()

	// Seed an external-stub symbol with no inbound edges. It must
	// NEVER appear in FindDead because external symbols are out of
	// scope for dead-code detection.
	if _, err := s.Symbols().Insert(ctx, SymbolRow{
		QualifiedName: "stdlib.something",
		Kind:          shared.KindFunc,
		FilePath:      externalPyStubPath,
		Line:          1,
	}); err != nil {
		t.Fatalf("seed external: %v", err)
	}

	rows, err := s.Symbols().FindDead(ctx, DeadCodeFilter{EdgeKind: EdgeKindImport})
	if err != nil {
		t.Fatalf("FindDead: %v", err)
	}
	if qnSet(rows)["stdlib.something"] {
		t.Errorf("external:py stub leaked into dead candidates: %v", sortedQNs(rows))
	}
}
