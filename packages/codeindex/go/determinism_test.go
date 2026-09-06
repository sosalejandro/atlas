package goscan

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sosalejandro/atlas/packages/graph"
	"github.com/sosalejandro/atlas/packages/shared"
)

// The determinism suite. See docs/testing/determinism.md for the why.
//
// Two determinism bugs reached main by hand-review only: map-iteration
// order in the fuzzy resolvers (PR #99) and LastInsertId returning a
// neighbouring row's id (#97). Nothing in CI would have caught either, and
// the moment issue #109 parallelises the scan that whole class comes back
// silently. These tests make "the same source produces the same index" an
// enforced property rather than a claim.
//
// What actually shakes the scanner here is Go's per-range map-order
// randomisation: scanContext.funcLookup, structFields and graph.Nodes are
// all maps, and every Scan call ranges over them afresh, so repeated scans
// in one process really do exercise different orders. GOMAXPROCS is varied
// on top of that as a forward guard — it buys nothing while Scan is
// single-threaded, and it is the first thing that will matter when it
// stops being.

const (
	goldenCorpusDir  = "testdata/goldencorpus"
	goldenSnapshot   = "testdata/goldencorpus/.golden/symbols_edges.txt"
	determinismRuns  = 3
	maxDiffLinesShow = 40
)

// updateGolden regenerates the checked-in snapshot instead of asserting
// against it:
//
//	go test ./packages/codeindex/go -run TestGoldenCorpus -update
var updateGolden = flag.Bool("update", false,
	"regenerate testdata/goldencorpus/.golden/symbols_edges.txt from a fresh scan")

// TestDeterminism_GoScanner_ScanIsReproducible scans the golden corpus
// repeatedly under different GOMAXPROCS settings and asserts every run
// renders byte-identical: same symbol set, same edge set, same position on
// every symbol, same warnings.
//
// Not parallel — it mutates the process-wide GOMAXPROCS.
func TestDeterminism_GoScanner_ScanIsReproducible(t *testing.T) {
	restoreProcs(t)

	cases := []struct {
		name string
		opts Options
	}{
		// The plain scan: function discovery, @api endpoints, call graph.
		{name: "default", opts: Options{}},
		// Routes exercise phase 2.5 (resolveHandlerRefs), which ranges over
		// funcLookup to find handler candidates by method-name suffix. The
		// ".Get" route below deliberately has two handler-kind candidates so
		// the ambiguous branch is covered too.
		{name: "with-routes", opts: Options{Routes: corpusRoutes()}},
		// SkipTests changes which files are walked at all; the annotated
		// _test.go symbols must drop out identically every time.
		{name: "skip-tests", opts: Options{SkipTests: true}},
		// EntryPoints switch the scanner into BuildFrom mode: a BFS seeded
		// from findMatching, which also ranges over maps.
		{name: "entry-points", opts: Options{EntryPoints: []shared.SymbolID{"Router.Handle"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want string
			var wantProcs int
			for _, procs := range []int{1, 2, 4, runtime.NumCPU()} {
				runtime.GOMAXPROCS(procs)
				for run := range determinismRuns {
					got := canonicalScan(t, goldenCorpusDir, tc.opts)
					if want == "" {
						want, wantProcs = got, procs
						continue
					}
					if got == want {
						continue
					}
					t.Fatalf("scan differs between runs (GOMAXPROCS %d vs %d, run %d):\n%s",
						wantProcs, procs, run, diffCanonical(want, got))
				}
			}
		})
	}
}

// TestDeterminism_GoScanner_IsRootRelative copies the corpus into two
// differently-named temp directories and asserts the two scans are
// byte-identical.
//
// Every position Atlas persists is repo-relative by contract
// (shared.FilePosition). A single absolute path leaking into a SymbolID,
// a signature or a position would make the store non-portable across
// worktrees and machines — and would show up here as a diff on every line.
func TestDeterminism_GoScanner_IsRootRelative(t *testing.T) {
	t.Parallel()

	first := filepath.Join(t.TempDir(), "checkout-a")
	second := filepath.Join(t.TempDir(), "some-other-name")
	copyTree(t, goldenCorpusDir, first)
	copyTree(t, goldenCorpusDir, second)

	a := canonicalScan(t, first, Options{})
	b := canonicalScan(t, second, Options{})
	if a != b {
		t.Fatalf("scan output depends on the root directory name:\n%s", diffCanonical(a, b))
	}
	if strings.Contains(a, first) {
		t.Fatalf("scan output leaks the absolute root path %q", first)
	}
}

// TestGoldenCorpus_SymbolsAndEdgesMatchSnapshot asserts the corpus scan
// against the snapshot checked in beside it.
//
// The snapshot is not a correctness oracle — parts of it record scanner
// behaviour we would like to change (SymbolIDs colliding across packages
// on Config.Validate; interface-typed calls through OrderRepository being
// dropped rather than recorded). It is a change detector: any scanner
// edit that moves a symbol, drops an edge or reclassifies a kind shows up
// as a reviewable diff in the PR that causes it, and the author either
// explains the improvement or discovers a regression.
//
// Regenerate with:
//
//	go test ./packages/codeindex/go -run TestGoldenCorpus -update
func TestGoldenCorpus_SymbolsAndEdgesMatchSnapshot(t *testing.T) {
	got := canonicalScan(t, goldenCorpusDir, Options{})

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenSnapshot), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(goldenSnapshot, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", goldenSnapshot)
		return
	}

	raw, err := os.ReadFile(goldenSnapshot)
	if err != nil {
		t.Fatalf("read golden (regenerate with `go test ./packages/codeindex/go -run TestGoldenCorpus -update`): %v", err)
	}
	if want := string(raw); got != want {
		t.Fatalf("golden corpus snapshot is stale or the scanner regressed.\n"+
			"Explain the diff in your PR, then regenerate with:\n"+
			"  go test ./packages/codeindex/go -run TestGoldenCorpus -update\n\n%s",
			diffCanonical(want, got))
	}
}

// corpusRoutes is the pre-resolved route table the "with-routes" variant
// feeds in. `orderHandler.Create` resolves to exactly one handler-kind
// symbol; `.Get` matches both OrderHandler.Get and AdminHandler.Get, so
// resolveHandlerRefs must leave that edge ambiguous rather than pick a
// winner off a map iteration.
func corpusRoutes() []Route {
	return []Route{
		{Method: "POST", Path: "/api/v1/orders", Handler: "h.orderHandler.Create",
			File: "internal/handlers/router.go", Line: 24},
		{Method: "GET", Path: "/api/v1/orders/{id}", Handler: "h.orderHandler.Get",
			File: "internal/handlers/router.go", Line: 26},
		{Method: "GET", Path: "/healthz", Handler: "h.healthHandler.Check",
			File: "internal/handlers/router.go", Line: 22},
	}
}

// canonicalScan scans root and renders the result as a stable text
// document — sorted, one record per line, every field escaped so a stray
// newline in a doc comment cannot forge a record boundary.
//
// Sorting is what makes the comparison ordering-independent without making
// it content-insensitive: duplicate edges survive as duplicate lines, so
// the multiset (and therefore the edge count) is still pinned.
func canonicalScan(t *testing.T, root string, opts Options) string {
	t.Helper()

	res, err := Scan(context.Background(), root, opts)
	if err != nil {
		t.Fatalf("Scan(%s): %v", root, err)
	}
	if res.Graph == nil {
		t.Fatalf("Scan(%s): nil graph", root)
	}
	if len(res.Symbols) == 0 {
		t.Fatalf("Scan(%s): no symbols; fixture missing?", root)
	}

	lines := make([]string, 0, len(res.Symbols)+len(res.Graph.Edges)+len(res.Warnings))
	for _, sym := range res.Symbols {
		lines = append(lines, symbolLine(sym))
	}
	for _, e := range res.Graph.Edges {
		lines = append(lines, edgeLine(e))
	}
	for _, w := range res.Warnings {
		lines = append(lines, "WARN\t"+strconv.Quote(w))
	}
	sort.Strings(lines)

	var b strings.Builder
	fmt.Fprintf(&b, "# symbols=%d edges=%d warnings=%d\n",
		len(res.Symbols), len(res.Graph.Edges), len(res.Warnings))
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

func symbolLine(s shared.Symbol) string {
	return strings.Join([]string{
		"SYM",
		string(s.ID),
		string(s.Kind),
		s.Position.Path,
		strconv.Itoa(s.Position.Line),
		strconv.Itoa(s.Position.Col),
		s.Package,
		strconv.Quote(s.Signature),
		strconv.Quote(s.Doc),
	}, "\t")
}

func edgeLine(e graph.Edge) string {
	return strings.Join([]string{
		"EDGE",
		string(e.From),
		string(e.To),
		e.Kind,
		strconv.Itoa(e.Line),
		"cycle=" + strconv.FormatBool(e.Cycle),
		"ambiguous=" + strconv.FormatBool(e.Ambiguous),
		"meta=" + e.Meta,
	}, "\t")
}

// diffCanonical renders the difference between two canonical documents as
// a unified-ish line diff. Both sides are sorted, so a linear merge finds
// exactly the lines that were added and removed — which is the whole
// message a reader needs ("this symbol moved", "this edge vanished"),
// rather than "documents are not equal".
func diffCanonical(want, got string) string {
	w := strings.Split(strings.TrimSuffix(want, "\n"), "\n")
	g := strings.Split(strings.TrimSuffix(got, "\n"), "\n")

	// The header line is a count summary, not a sorted record; compare it
	// separately so it does not float into the middle of the diff.
	var out []string
	if len(w) > 0 && len(g) > 0 && w[0] != g[0] {
		out = append(out, "- "+w[0], "+ "+g[0])
	}
	w, g = tailSorted(w), tailSorted(g)

	var i, j, shown, extra int
	emit := func(prefix, line string) {
		if shown < maxDiffLinesShow {
			out = append(out, prefix+" "+line)
			shown++
			return
		}
		extra++
	}
	for i < len(w) || j < len(g) {
		switch {
		case j >= len(g) || (i < len(w) && w[i] < g[j]):
			emit("-", w[i])
			i++
		case i >= len(w) || w[i] > g[j]:
			emit("+", g[j])
			j++
		default:
			i++
			j++
		}
	}
	if extra > 0 {
		out = append(out, fmt.Sprintf("... and %d more differing lines", extra))
	}
	if len(out) == 0 {
		return "(documents differ only in trailing whitespace)"
	}
	return strings.Join(out, "\n")
}

func tailSorted(lines []string) []string {
	if len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
		return lines[1:]
	}
	return lines
}

// copyTree mirrors src into dst so a scan can run against a differently
// named root.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

// restoreProcs pins GOMAXPROCS back to whatever the test binary started
// with, so a failing determinism case cannot leave the rest of the package
// running under GOMAXPROCS=1.
func restoreProcs(t *testing.T) {
	t.Helper()
	original := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(original) })
}
