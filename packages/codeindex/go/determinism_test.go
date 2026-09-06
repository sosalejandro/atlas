package goscan

import (
	"bytes"
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
	// Diagnose a CRLF checkout before comparing, because the comparison
	// cannot. canonicalize writes "\n"; a Windows clone with
	// core.autocrlf=true (the Git for Windows installer default) hands
	// os.ReadFile "\r\n", so every one of the snapshot's lines differs and
	// the diff blames the scanner for the checkout. .gitattributes pins
	// this file to LF; this check is what says so when it has been lost.
	if bytes.Contains(raw, []byte("\r\n")) {
		t.Fatalf("%s has CRLF line endings, but the scanner renders LF, so the byte "+
			"comparison below would fail on every line for a reason that has nothing "+
			"to do with the scanner.\nThis is a checkout problem, not a scanner problem: "+
			"the repository stores this file with LF and .gitattributes pins it to LF in "+
			"the working tree. Re-checkout with `git rm --cached -r . && git reset --hard`, "+
			"or check that .gitattributes is present at the repo root.", goldenSnapshot)
	}
	if want := string(raw); got != want {
		t.Fatalf("golden corpus snapshot is stale or the scanner regressed.\n"+
			"Explain the diff in your PR, then regenerate with:\n"+
			"  go test ./packages/codeindex/go -run TestGoldenCorpus -update\n\n%s",
			diffCanonical(want, got))
	}
}

// TestGoldenCorpus_LineEndingsDoNotMoveTheSnapshot scans the corpus twice
// — once from LF sources, once from the same sources rewritten to CRLF —
// and asserts the two canonical documents are byte-identical.
//
// This is the half of the Windows question the snapshot comparison cannot
// answer for itself. Issue #143 claimed the golden corpus "bakes in path
// separators", and a guard was written to route Position.Path through a
// slash normaliser in this file. That guard was a no-op: every
// Position.Path the scanner emits has already been through
// filepath.ToSlash at construction (scanner.go, the relOrSelf call sites),
// so it could not change a byte of any real scan on any platform. It has
// been dropped rather than left standing in for a fix.
//
// What a Windows checkout DOES change is the bytes on disk: with
// core.autocrlf=true every source file in the corpus arrives as CRLF. So
// the real question is not whether the scanner emits the host separator —
// it does not — but whether reading CRLF sources moves anything the
// snapshot records. Three fields could plausibly carry a stray "\r":
// Doc (ast.CommentGroup.Text), Signature (rendered from the AST, not from
// source bytes) and the generated-header probe (a bufio.Scanner). All
// three strip it today. This test is what keeps that true, and what lets
// the docs say "the snapshot is platform-independent" as a measured
// property rather than a hope.
//
// Together with .gitattributes — which pins the snapshot file itself, the
// one place a CRLF checkout genuinely does break the comparison — this is
// the whole of the golden corpus's Windows story.
func TestGoldenCorpus_LineEndingsDoNotMoveTheSnapshot(t *testing.T) {
	t.Parallel()

	lf := filepath.Join(t.TempDir(), "lf-checkout")
	crlf := filepath.Join(t.TempDir(), "crlf-checkout")
	copyTree(t, goldenCorpusDir, lf)
	copyTreeCRLF(t, goldenCorpusDir, crlf)

	// Guard the fixture: if the rewrite silently did nothing, the
	// comparison below would pass without exercising anything.
	assertContainsCRLF(t, filepath.Join(crlf, "go.mod"))

	want := canonicalScan(t, lf, Options{})
	got := canonicalScan(t, crlf, Options{})
	if got != want {
		t.Fatalf("scanning CRLF sources produced a different canonical document, so the "+
			"golden snapshot is not valid on a Windows checkout:\n%s", diffCanonical(want, got))
	}
}

// copyTreeCRLF mirrors src into dst with every line ending rewritten to
// CRLF — what a Windows clone with core.autocrlf=true produces.
//
// Normalising to LF first makes the rewrite idempotent, so a source file
// that already had a CRLF cannot become "\r\r\n".
func copyTreeCRLF(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s -> %s as CRLF: %v", src, dst, err)
	}
}

// assertContainsCRLF fails when path has no CRLF in it, which would mean
// copyTreeCRLF produced a fixture that tests nothing.
func assertContainsCRLF(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CRLF fixture %s: %v", path, err)
	}
	if !bytes.Contains(data, []byte("\r\n")) {
		t.Fatalf("%s has no CRLF after the rewrite; the fixture does not exercise "+
			"the Windows checkout shape", path)
	}
}

// TestGoldenCorpus_TierChangeFailsSnapshot proves the snapshot can see
// a resolution-tier change.
//
// This is the test that makes #87 reviewable, and it is deliberately
// about the DETECTOR rather than about any particular edge. #146 exists
// because an edge produced by a name heuristic and the same edge
// produced by a type checker are identical in (from, to, kind, file,
// line) — so every count comparison and every set diff over that tuple
// reports "unchanged" across the exact migration that changes
// everything. If the tier ever stops being serialised, the golden file
// silently goes back to being blind, and nothing else in the suite
// would notice.
//
// It scans the real corpus, promotes one edge's tier by hand, and
// asserts the canonical document changes and the diff names the move.
// Nothing is written to disk.
func TestGoldenCorpus_TierChangeFailsSnapshot(t *testing.T) {
	t.Parallel()

	res, err := Scan(context.Background(), goldenCorpusDir, Options{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	before := canonicalize(res)

	// Pick a syntactic edge and promote it to typed — the exact
	// movement #87 is expected to produce, and the one a count-based
	// or tuple-based check cannot see.
	promoted := -1
	for i, e := range res.Graph.Edges {
		if e.Tier == graph.TierSyntactic {
			promoted = i
			break
		}
	}
	if promoted < 0 {
		t.Fatal("corpus has no syntactic edge to promote; the fixture no longer exercises the guess path")
	}
	moved := res.Graph.Edges[promoted]
	res.Graph.Edges[promoted].Tier = graph.TierTyped

	after := canonicalize(res)
	if after == before {
		t.Fatalf("promoting %s -> %s from %q to %q left the canonical document byte-identical; "+
			"the snapshot cannot see a resolver migration",
			moved.From, moved.To, graph.TierSyntactic, graph.TierTyped)
	}

	// The header counts are unchanged — same symbols, same edges. That
	// is the point: only the tier line moved, which is precisely the
	// failure mode #87's old acceptance criterion ("same symbol and
	// edge counts +/- a documented delta") could not detect.
	beforeHeader := strings.SplitN(before, "\n", 2)[0]
	afterHeader := strings.SplitN(after, "\n", 2)[0]
	if beforeHeader != afterHeader {
		t.Fatalf("edge counts moved (%q vs %q); the test perturbed more than the tier",
			beforeHeader, afterHeader)
	}

	diff := diffCanonical(before, after)
	if !strings.Contains(diff, "tier="+string(graph.TierTyped)) {
		t.Errorf("diff does not name the new tier, so a reviewer could not tell what moved:\n%s", diff)
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
	return canonicalize(res)
}

// canonicalize renders a scan result as the stable text document
// canonicalScan compares. It is split out from the scan so a test can
// perturb a result and ask what the snapshot would say about it —
// which is how TestGoldenCorpus_TierChangeFailsSnapshot proves the
// snapshot can see a tier move at all.
func canonicalize(res *Result) string {
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
		// The end line is pinned because coverage attribution charges
		// statements to a symbol by its [line, end_line] span: a scanner
		// change that silently zeroes it degrades every per-symbol coverage
		// fraction without failing any test that only reads start lines.
		strconv.Itoa(s.EndLine),
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
		// The tier is pinned because it is the ONE field a resolver
		// migration changes without changing anything else. #87
		// replaces the Go call resolver with go/packages + callgraph:
		// the same (from, to, kind, line) tuples come back out of a
		// completely different mechanism, so a snapshot over the tuple
		// alone reports "no change" for the largest change this
		// scanner has ever had. See
		// TestGoldenCorpus_TierChangeFailsSnapshot.
		"tier=" + string(e.Tier),
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
