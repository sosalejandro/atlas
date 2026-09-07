package resolver

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// This file is issue #155's acceptance bar. The types-level dispatch
// index in typedispatch.go replaced class-hierarchy analysis over SSA,
// and the only thing that makes such a replacement safe is a key-by-key,
// candidate-by-candidate comparison against the algorithm it replaced,
// over trees with real shape. A benchmark showing the new one is cheaper
// says nothing about whether it is right, and a call graph that quietly
// loses edges degrades every feature built on it.
//
// The two maps are not identical, and the four assertions below are the
// reason that is acceptable rather than an excuse for it:
//
//  1. Nothing is lost. Every candidate CHA found, the types path finds.
//  2. Inside the loaded tree the two agree exactly, site for site.
//  3. Everything extra is declared OUTSIDE the loaded tree, so no scan
//     can hold a symbol for it and no edge can be emitted from it.
//  4. Wherever an edge CAN be emitted, the "more than one candidate"
//     verdict is unchanged -- so Edge.Ambiguous does not move either.
//
// Together those say: the map differs, and atlas's output cannot.
//
// The difference has one cause. CHA's universe of concrete types is
// whatever ssautil.AllFunctions reached -- package-level functions,
// exported types of syntactic packages, and every type structurally
// reachable from one that was converted to an interface, a rule x/tools
// itself calls "unprincipled" and carries a standing TODO to replace.
// That admits (*embed.file).Name, a type nothing in this repository can
// name, while omitting most of the error types in the same dependency.
// The types path enumerates every named type the loaded tree can reach
// instead, which is a definition rather than a reachability artefact, and
// which can only ever be a superset.

// invokeDiff is what one comparison of two dispatch maps found.
type invokeDiff struct {
	// missing is a candidate CHA produced and the types path did not.
	// This is the direction that matters: a lost candidate is a lost
	// edge, and #155 stops if there is one.
	missing []string
	// extra is a candidate the types path produced and CHA did not.
	extra []string
}

func (d invokeDiff) empty() bool { return len(d.missing) == 0 && len(d.extra) == 0 }

// diffInvokes compares two dispatch maps, rendering each disagreement as
// "file:line:col callee" so a failure names a call site a reader can go
// and look at. keep decides which candidates take part, so one comparison
// serves both the whole-map question and the narrower one about the
// loaded tree.
func diffInvokes(fset *token.FileSet, cha, typed map[token.Pos][]*types.Func, keep func(*types.Func) bool) invokeDiff {
	var d invokeDiff
	for _, pos := range allPositions(cha, typed) {
		at := fset.Position(pos).String()
		inCHA := candidateKeys(cha[pos], keep)
		inTyped := candidateKeys(typed[pos], keep)
		for _, k := range inCHA {
			if !contains(inTyped, k) {
				d.missing = append(d.missing, at+" "+k)
			}
		}
		for _, k := range inTyped {
			if !contains(inCHA, k) {
				d.extra = append(d.extra, at+" "+k)
			}
		}
	}
	return d
}

func allPositions(maps ...map[token.Pos][]*types.Func) []token.Pos {
	seen := map[token.Pos]bool{}
	var out []token.Pos
	for _, m := range maps {
		for pos := range m {
			if !seen[pos] {
				seen[pos] = true
				out = append(out, pos)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func candidateKeys(fns []*types.Func, keep func(*types.Func) bool) []string {
	out := make([]string, 0, len(fns))
	for _, fn := range fns {
		if keep == nil || keep(fn) {
			out = append(out, ObjectKey(fn))
		}
	}
	sort.Strings(out)
	return out
}

func contains(sorted []string, s string) bool {
	i := sort.SearchStrings(sorted, s)
	return i < len(sorted) && sorted[i] == s
}

// report renders a bounded sample from each direction with the totals. A
// divergence two thousand lines long is not a failure anyone reads.
func (d invokeDiff) report(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d candidates lost, %d candidates added\n", len(d.missing), len(d.extra))
	render := func(label string, items []string) {
		for i, item := range items {
			if i == n {
				fmt.Fprintf(&b, "  %s ... and %d more\n", label, len(items)-n)
				return
			}
			fmt.Fprintf(&b, "  %s %s\n", label, item)
		}
	}
	render("LOST", d.missing)
	render("ADDED", d.extra)
	return b.String()
}

// comparison is one tree resolved once and answered both ways.
type comparison struct {
	fset  *token.FileSet
	cha   map[token.Pos][]*types.Func
	typed map[token.Pos][]*types.Func
	// inTree reports whether a candidate is declared in a package this
	// scan type-checked -- the only candidates that can become an edge,
	// because the scanner maps a callee onto a symbol it registered and
	// it registers nothing for a dependency.
	inTree func(*types.Func) bool
}

// compareDispatch type-checks dir once and computes the dispatch map both
// ways over the same []*packages.Package, so nothing but the algorithm
// differs between them -- one FileSet, one set of *types.Func values, one
// partition into type-checked and degraded.
func compareDispatch(t *testing.T, dir string) comparison {
	t.Helper()

	checked := resolvedPackages(t, dir)
	fromCHA, ok := chaInvokes(checked)
	if !ok {
		t.Fatalf("the CHA oracle failed on %s; an empty map agrees with anything", dir)
	}

	paths := map[string]bool{}
	for _, pkg := range checked {
		paths[pkg.Types.Path()] = true
	}
	return comparison{
		fset:  checked[0].Fset,
		cha:   fromCHA,
		typed: typeInvokes(checked),
		inTree: func(fn *types.Func) bool {
			return fn.Pkg() != nil && paths[fn.Pkg().Path()]
		},
	}
}

// resolvedPackages type-checks dir the way Load does, and returns the
// packages Load would compute dispatch over.
//
// It reaches for loadMode, loadEnv, loadPatterns and Program.accept rather
// than calling Load, because Load does not hand back the package list it
// resolved and a hook existing only so a test could see it would be
// production code serving a test. What this duplicates is a Config
// literal; every decision about WHICH packages come back is production's.
func resolvedPackages(t *testing.T, dir string) []*packages.Package {
	t.Helper()

	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs %q: %v", dir, err)
	}
	cfg := &packages.Config{
		Mode:    loadMode,
		Dir:     abs,
		Tests:   true,
		Context: context.Background(),
		Env:     loadEnv(abs),
	}
	loaded, err := packages.Load(cfg, loadPatterns(abs)...)
	if err != nil {
		t.Fatalf("packages.Load(%s): %v", dir, err)
	}
	checked := (&Program{}).accept(loaded, Options{IncludeTests: true}, abs)
	if len(checked) == 0 {
		t.Fatalf("nothing in %s type-checked; the comparison would be vacuous", dir)
	}
	return checked
}

// equivalenceTrees are the trees both paths are compared over.
//
// This repository first, because it is the only one with the scale the
// question needs: forty-odd packages, generics, interfaces embedded in
// structs, dispatch into six dependencies. The corpora are here because
// they are what the rest of the suite pins its output against, and
// brokencorpus because a tree that does not compile is the case the #87
// per-package degradation exists for -- the dispatch index has to agree
// with CHA about the packages that survived, not only about a green tree.
var equivalenceTrees = map[string]string{
	"self":          selfRepo,
	"goldencorpus":  "../codeindex/go/testdata/goldencorpus",
	"brokencorpus":  "../codeindex/go/testdata/brokencorpus",
	"sampleproject": "../codeindex/go/testdata/sampleproject",
}

func TestInvokes_TypesPathIsEquivalentToCHA(t *testing.T) {
	names := make([]string, 0, len(equivalenceTrees))
	for name := range equivalenceTrees {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			c := compareDispatch(t, equivalenceTrees[name])

			t.Run("loses no candidate CHA found", func(t *testing.T) {
				d := diffInvokes(c.fset, c.cha, c.typed, nil)
				if len(d.missing) > 0 {
					t.Fatalf("the types path is LESS conservative than CHA, which is a "+
						"correctness regression and issue #155 stops here:\n%s", d.report(40))
				}
			})

			// The assertion that decides whether any edge can change.
			// The scanner keys a callee onto a symbol it registered, and
			// it registers symbols only for packages it scanned;
			// agreement here is agreement about every edge, every tier
			// and every cycle the index can contain.
			t.Run("agrees exactly inside the loaded tree", func(t *testing.T) {
				d := diffInvokes(c.fset, c.cha, c.typed, c.inTree)
				if !d.empty() {
					t.Fatalf("the two paths disagree about candidates this scan can "+
						"index, so the edge set would move:\n%s", d.report(40))
				}
			})

			t.Run("every extra candidate is outside the loaded tree", func(t *testing.T) {
				for _, pos := range allPositions(c.typed) {
					have := candidateKeys(c.cha[pos], nil)
					for _, fn := range c.typed[pos] {
						if contains(have, ObjectKey(fn)) || !c.inTree(fn) {
							continue
						}
						t.Fatalf("%s: %s is declared in the loaded tree and CHA did not "+
							"name it, so the extra candidates can no longer be argued "+
							"to be invisible to the scan",
							c.fset.Position(pos), ObjectKey(fn))
					}
				}
			})

			// packages/codeindex/go/typed.go computes Edge.Ambiguous as
			// len(Targets) > 1, BEFORE dropping the candidates it has no
			// symbol for -- deliberately, so an interface with four
			// implementations and one indexed does not read as
			// certainty. That makes the extra candidates capable of
			// moving a flag even though they can never be an edge. They
			// do not: a site with no in-tree candidate emits nothing at
			// all, and at every site that emits something the verdict is
			// unchanged.
			t.Run("ambiguity is unchanged wherever an edge can be emitted", func(t *testing.T) {
				for _, pos := range allPositions(c.cha, c.typed) {
					if len(candidateKeys(c.typed[pos], c.inTree)) == 0 {
						continue
					}
					wasAmbiguous, isAmbiguous := len(c.cha[pos]) > 1, len(c.typed[pos]) > 1
					if wasAmbiguous != isAmbiguous {
						t.Fatalf("%s: Edge.Ambiguous would flip from %v to %v at a site "+
							"that emits edges (CHA named %d candidates, types named %d)",
							c.fset.Position(pos), wasAmbiguous, isAmbiguous,
							len(c.cha[pos]), len(c.typed[pos]))
					}
				}
			})
		})
	}
}
