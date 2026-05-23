package pyscan

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// TestResolver_CrossModule end-to-ends every cross-module resolver rule
// against the testdata/cross_module fixture. This is the integration
// test the issue acceptance criteria explicitly call for — it asserts
// resolution end-to-end through scanner.go, not just unit-level rule
// matching.
//
// Fixture shape (parent package `pkg`):
//
//	pkg/__init__.py    re-exports echo from .termui (rule 4)
//	pkg/core.py        from .termui import style;
//	                    calls echo, style, sibling_fn, deep_helper
//	pkg/termui.py      defines echo, style, _format
//	pkg/helpers.py     defines sibling_fn (no import edge from pkg.core)
//	pkg/sub/deep.py    defines deep_helper (not a direct sibling of pkg.core)
func TestResolver_CrossModule(t *testing.T) {
	t.Parallel()
	skipIfNoPython(t)
	root, err := filepath.Abs(filepath.Join("testdata", "cross_module"))
	if err != nil {
		t.Fatalf("abs testdata: %v", err)
	}
	s := NewScanner(Options{Logger: shared.NopLogger{}})
	res, err := s.Scan(context.Background(), root)
	if err != nil {
		t.Fatalf("scan cross_module: %v", err)
	}

	edgeSet := make(map[string]bool, len(res.Edges))
	for _, e := range res.Edges {
		edgeSet[string(e.From)+"->"+string(e.To)] = true
	}

	wantResolved := []struct {
		edge string
		why  string
	}{
		{"pkg.core.command->pkg.termui.echo", "rule 4: re-export from pkg/__init__.py"},
		{"pkg.core.command->pkg.termui.style", "rule 3: caller's own `from .termui import style`"},
		{"pkg.core.command->pkg.helpers.sibling_fn", "rule 5: sibling module top-level lookup"},
	}
	for _, want := range wantResolved {
		if !edgeSet[want.edge] {
			t.Errorf("expected resolved edge %q (%s); not in edges", want.edge, want.why)
		}
	}

	// Acknowledged limitation: deep_helper lives in pkg.sub.deep, which
	// shares parent package pkg.sub — NOT pkg. The sibling index is
	// keyed by parent package, so pkg.core can't see it. We confirm the
	// edge falls through to a stub so a future enhancement (e.g.
	// transitive sibling walking) has a clear regression target.
	stubbed := false
	for _, e := range res.Edges {
		if string(e.From) == "pkg.core.command" && string(e.To) == "deep_helper" {
			stubbed = true
			break
		}
	}
	if !stubbed {
		t.Errorf("expected pkg.core.command -> deep_helper to remain unqualified (limitation guard); got edges:\n%s",
			strings.Join(edgeStringList(res.Edges), "\n"))
	}
}

// TestResolver_RulePriority hits the in-process resolver directly so
// each rule's branch can be exercised without the python3 subprocess.
// We construct raw nodes/edges that mirror what scanner.py would emit
// for a contrived fixture and then assert (fromID, target) resolves
// where expected.
func TestResolver_RulePriority(t *testing.T) {
	t.Parallel()
	// Build a synthetic raw graph that exercises rules 1-5 in isolation.
	nodes := []rawNode{
		// Package pkg with __init__ re-exporting `echo` from .termui.
		{ID: "pkg", Kind: "module", File: "pkg/__init__.py"},
		{ID: "pkg.core", Kind: "module", File: "pkg/core.py"},
		{ID: "pkg.termui", Kind: "module", File: "pkg/termui.py"},
		{ID: "pkg.helpers", Kind: "module", File: "pkg/helpers.py"},

		// pkg.core has a top-level `command` and a local `inline_helper`.
		{ID: "pkg.core.command", Kind: "function", File: "pkg/core.py", Line: 10},
		{ID: "pkg.core.inline_helper", Kind: "function", File: "pkg/core.py", Line: 5},

		// pkg.termui has echo, style and a private _format.
		{ID: "pkg.termui.echo", Kind: "function", File: "pkg/termui.py", Line: 5},
		{ID: "pkg.termui.style", Kind: "function", File: "pkg/termui.py", Line: 10},
		{ID: "pkg.termui._format", Kind: "function", File: "pkg/termui.py", Line: 1},

		// pkg.helpers has sibling_fn (no import edge from pkg.core).
		{ID: "pkg.helpers.sibling_fn", Kind: "function", File: "pkg/helpers.py", Line: 1},
	}
	edges := []rawEdge{
		// pkg/__init__.py: `from .termui import echo` → re-export.
		{From: "pkg", To: ".termui.echo", Kind: "import"},
		// pkg/core.py: `from .termui import style` → caller import.
		{From: "pkg.core", To: ".termui.style", Kind: "import"},
	}

	r := newPyEdgeResolver(nodes, edges)

	type tc struct {
		name   string
		from   shared.SymbolID
		target string
		want   shared.SymbolID
	}
	cases := []tc{
		{
			name:   "rule 1: exact qualified name",
			from:   "pkg.core.command",
			target: "pkg.termui.echo",
			want:   "pkg.termui.echo",
		},
		{
			name:   "rule 2: same-module basename",
			from:   "pkg.core.command",
			target: "inline_helper",
			want:   "pkg.core.inline_helper",
		},
		{
			name:   "rule 3: caller's `from .termui import style`",
			from:   "pkg.core.command",
			target: "style",
			want:   "pkg.termui.style",
		},
		{
			name:   "rule 4: re-export from package __init__",
			from:   "pkg.core.command",
			target: "echo",
			want:   "pkg.termui.echo",
		},
		{
			name:   "rule 5: sibling-module top-level",
			from:   "pkg.core.command",
			target: "sibling_fn",
			want:   "pkg.helpers.sibling_fn",
		},
		{
			name:   "rule 6: no resolution — passthrough",
			from:   "pkg.core.command",
			target: "unknown_external_name",
			want:   "unknown_external_name",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.resolve(c.from, c.target)
			if got != c.want {
				t.Errorf("resolve(%q, %q) = %q; want %q", c.from, c.target, got, c.want)
			}
		})
	}
}

// TestResolver_SiblingTieBreak confirms the tie-break behaviour when a
// sibling name matches two distinct modules. importPopularity wins
// first; alphabetical order is the final fallback.
func TestResolver_SiblingTieBreak(t *testing.T) {
	t.Parallel()
	nodes := []rawNode{
		{ID: "pkg", Kind: "module", File: "pkg/__init__.py"},
		{ID: "pkg.a", Kind: "module", File: "pkg/a.py"},
		{ID: "pkg.b", Kind: "module", File: "pkg/b.py"},
		{ID: "pkg.consumer", Kind: "module", File: "pkg/consumer.py"},
		{ID: "pkg.popular_importer", Kind: "module", File: "pkg/popular_importer.py"},

		// Two siblings both define `dupe` at top-level.
		{ID: "pkg.a.dupe", Kind: "function", File: "pkg/a.py", Line: 1},
		{ID: "pkg.b.dupe", Kind: "function", File: "pkg/b.py", Line: 1},

		// Symbol that does the unresolved call.
		{ID: "pkg.consumer.entry", Kind: "function", File: "pkg/consumer.py", Line: 10},
	}
	edges := []rawEdge{
		// Two import edges into pkg.b.dupe — that makes it the
		// most-imported sibling.
		{From: "pkg.popular_importer", To: ".b.dupe", Kind: "import"},
		{From: "pkg", To: ".b.dupe", Kind: "import"},
	}
	r := newPyEdgeResolver(nodes, edges)
	got := r.resolve("pkg.consumer.entry", "dupe")
	if got != "pkg.b.dupe" {
		t.Errorf("expected popularity tie-break to pick pkg.b.dupe; got %q", got)
	}
}

// TestResolver_PreservesExistingBehavior protects same-module resolution
// (rule 2) from regression — the click fixture and the pre-#61
// sample_project fixture both depend on this rule firing for bare
// callee names that match a same-module symbol.
func TestResolver_PreservesExistingBehavior(t *testing.T) {
	t.Parallel()
	nodes := []rawNode{
		{ID: "sample", Kind: "module", File: "sample.py"},
		{ID: "sample.helper", Kind: "function", File: "sample.py", Line: 5},
		{ID: "sample.compute", Kind: "function", File: "sample.py", Line: 10},
		{ID: "sample.MyClass", Kind: "class", File: "sample.py", Line: 20},
		{ID: "sample.MyClass.run", Kind: "method", File: "sample.py", Line: 25},
	}
	r := newPyEdgeResolver(nodes, nil)

	if got := r.resolve("sample.compute", "helper"); got != "sample.helper" {
		t.Errorf("same-module basename: got %q, want sample.helper", got)
	}
	if got := r.resolve("sample.MyClass.run", "helper"); got != "sample.helper" {
		t.Errorf("method calling same-module helper: got %q, want sample.helper", got)
	}
	if got := r.resolve("sample.compute", "MyClass.run"); got != "sample.MyClass.run" {
		t.Errorf("dotted same-module: got %q, want sample.MyClass.run", got)
	}
	if got := r.resolve("sample.compute", "absolute.unknown"); got != "absolute.unknown" {
		t.Errorf("unknown dotted pass-through: got %q, want absolute.unknown", got)
	}
}

// TestResolver_EnclosingModule covers the longest-prefix module lookup
// directly — important because the pre-#61 behaviour was a single
// first-dot prefix, and nested-package codebases (the primary use
// case for click) need the longest-prefix variant.
func TestResolver_EnclosingModule(t *testing.T) {
	t.Parallel()
	nodes := []rawNode{
		{ID: "src", Kind: "module", File: "src/__init__.py"},
		{ID: "src.click", Kind: "module", File: "src/click/__init__.py"},
		{ID: "src.click.core", Kind: "module", File: "src/click/core.py"},
		{ID: "src.click.core.echo", Kind: "function", File: "src/click/core.py", Line: 100},
	}
	r := newPyEdgeResolver(nodes, nil)

	tests := []struct {
		in   string
		want string
	}{
		{"src.click.core.echo", "src.click.core"},
		{"src.click.something_not_in_index", "src.click"},
		{"src.unknown.deeper", "src"},
		{"floating", ""},
	}
	for _, tc := range tests {
		if got := r.enclosingModule(tc.in); got != tc.want {
			t.Errorf("enclosingModule(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolver_BoundNameAndQualifiedImport covers the scanner.py wire
// format → (boundName, qualifiedID) translation used to build the
// importAlias and reExport indices. The dot-counting + caller-package
// resolution logic is non-trivial and worth unit-isolating.
func TestResolver_BoundNameAndQualifiedImport(t *testing.T) {
	t.Parallel()
	packageInits := map[string]struct{}{
		"src.click":     {},
		"pkg":           {},
		"pkg.subpkg":    {},
	}
	tests := []struct {
		name           string
		callerModule   string
		rendered       string
		wantBoundName  string
		wantQualified  string
	}{
		{
			name:          "absolute import",
			callerModule:  "main",
			rendered:      "os",
			wantBoundName: "os",
			wantQualified: "os",
		},
		{
			name:          "from absolute import name",
			callerModule:  "main",
			rendered:      "collections.OrderedDict",
			wantBoundName: "OrderedDict",
			wantQualified: "collections.OrderedDict",
		},
		{
			name:          "relative from same package (caller is leaf)",
			callerModule:  "pkg.subpkg.leaf",
			rendered:      ".sibling.helper",
			wantBoundName: "helper",
			wantQualified: "pkg.subpkg.sibling.helper",
		},
		{
			name:          "relative from same package (caller IS init)",
			callerModule:  "pkg.subpkg",
			rendered:      ".sibling",
			wantBoundName: "sibling",
			wantQualified: "pkg.subpkg.sibling",
		},
		{
			name:          "double-dot relative — pop one package level",
			callerModule:  "pkg.subpkg.leaf",
			rendered:      "..other.helper",
			wantBoundName: "helper",
			wantQualified: "pkg.other.helper",
		},
		{
			name:          "from . import sibling (caller IS init)",
			callerModule:  "main",
			rendered:      ".sibling",
			wantBoundName: "sibling",
			wantQualified: "sibling",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bn, q := boundNameAndQualifiedImport(tc.callerModule, tc.rendered, packageInits)
			if bn != tc.wantBoundName || q != tc.wantQualified {
				t.Errorf("boundNameAndQualifiedImport(%q,%q) = (%q,%q); want (%q,%q)",
					tc.callerModule, tc.rendered, bn, q, tc.wantBoundName, tc.wantQualified)
			}
		})
	}
}

// edgeStringList renders edges for an error-output assertion.
func edgeStringList(edges []graph.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, string(e.From)+"->"+string(e.To))
	}
	sort.Strings(out)
	return out
}
