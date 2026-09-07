package resolver

import (
	"go/token"
	"go/types"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// This file is the class-hierarchy analysis atlas used to ship, kept as
// the ORACLE the types-level dispatch index is measured against.
//
// It was production code until issue #155. The whole output of
// ssautil.Packages -> buildAllSSA -> cha.CallGraph -> indexInvokes was one
// field, map[token.Pos][]*types.Func, whose key and value are both
// go/token and go/types concepts; SSA appeared nowhere in it. CHA walks
// SSA for one reason -- to enumerate call sites -- and atlas already
// enumerates its own. typedispatch.go computes the same map from
// go/types, for an eighth of the cumulative allocation.
//
// It stays here, compiled only into the test binary, because deleting it
// would delete the only independent answer to "is the new one right".
// invokeparity_test.go runs both over this repository and the fixtures
// and compares them candidate by candidate. A pair of implementations
// that disagree is a bug in one of them; a single implementation with no
// second opinion is a bug nobody finds.

// buildCallGraph runs class-hierarchy analysis and records, for every
// interface call site, the concrete methods it can reach.
//
// CHA is the weakest of the three analyses x/tools ships (CHA, RTA, VTA)
// and it is the right one here. It answers "which methods named M could
// this interface value dispatch to", by looking only at the type
// hierarchy — no reachability, no flow. That means it over-approximates:
// a call through an OrderRepository reports every type implementing
// OrderRepository, whether or not the program ever constructs it. Atlas
// records that over-approximation as an ambiguous edge, which is the
// honest shape of the answer. RTA and VTA would narrow it using
// whole-program reachability from a main function, and atlas indexes
// libraries and half-written trees that have no main — so their extra
// precision would be bought with an assumption the corpus does not
// support.
//
// Only *invoke* sites are indexed. A static call needs no help: the type
// checker already named its callee exactly, and taking it from here
// instead would only add a second way to be wrong. A dynamic call through
// a func-typed variable is deliberately left out too: CHA resolves those
// by matching signatures across the whole program, which for a common
// signature like func(error) means every function with that shape. That
// is not resolution, it is a fan-out, and issue #87 exists to reduce
// exactly this kind of guess.
func chaInvokes(pkgs []*packages.Package) (invokes map[token.Pos][]*types.Func, ok bool) {
	invokes = map[token.Pos][]*types.Func{}
	defer func() {
		// SSA construction walks type-checked syntax that this package
		// did not produce and cannot fully vouch for. When it panics the
		// oracle has no opinion, and a parity test must skip rather than
		// compare against an empty map -- which would agree with anything
		// that also found nothing.
		if r := recover(); r != nil {
			invokes, ok = map[token.Pos][]*types.Func{}, false
		}
	}()

	// ssautil.Packages, NOT ssautil.AllPackages, and that one word is the
	// whole of issue #152's cause 3. Packages hands syntax and types.Info
	// only to the packages it was given; a dependency reached through
	// packages.Visit is created from its types alone, so it becomes an
	// ssa.Package of declarations with no code. AllPackages would build
	// bodies for every transitive dependency.
	//
	// It matters to the ORACLE too, and more than it did to production.
	// The scope of SSA fixes which concrete methods CHA can name, so a
	// parity test run against AllPackages would be comparing the types
	// index to a different question.
	// TestSSA_NoFunctionBodiesOutsideTheScannedTree is the assertion.
	prog, _ := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	if prog == nil {
		return invokes, false
	}
	if !buildAllSSA(prog) {
		return invokes, false
	}
	if ssaObserver != nil {
		ssaObserver(prog, pkgs)
	}
	return indexInvokes(cha.CallGraph(prog)), true
}

// ssaObserver, when non-nil, is handed the built ssa.Program and the
// packages it was built over, after buildAllSSA and before CHA.
//
// It exists for one assertion, and the assertion is why it is worth a hook
// in production code. The scope of SSA construction — which packages get
// bodies built for them — is a property of THIS function's arguments, and
// a test that calls ssautil.Packages itself measures its own arguments
// instead. That test passed while the production call was
// ssautil.AllPackages, which is the change it exists to catch, and the
// only fix is for it to look at the program production built.
//
// A hook rather than a field on Program, because the ssa.Program is the
// largest single thing a load constructs — 245 MB of cumulative allocation
// out of a load's 584 MB, docs/performance.md §1 — and keeping a reference
// past buildCallGraph would turn a churn cost into a residency one on
// every scan, to serve a test.
//
// Not safe for concurrent Loads. Tests that set it do not call t.Parallel.
var ssaObserver func(prog *ssa.Program, pkgs []*packages.Package)

// buildAllSSA builds every package in prog and reports whether all of
// them built.
//
// It exists instead of a call to prog.Build() because prog.Build runs
// each package on a goroutine it spawns itself and recovers nothing: a
// panic out of the SSA builder unwinds a goroutine that buildCallGraph's
// recover is not on the stack of, and takes the process down with it.
// That is the wrong failure for this program. Atlas is meant to run
// mid-edit against trees that do not compile (issue #87), and losing
// interface dispatch for a repository is a far smaller loss than a scan
// that dies. ssa.Package.Build runs inline and is documented idempotent
// and thread-safe, so doing the fan-out here keeps the parallelism and
// puts a recover back on every stack that can panic.
//
// One failure fails the whole graph rather than the one package. A
// partially built program still answers cha.CallGraph, and it answers
// short by exactly whatever the unbuilt package contained -- absences
// indistinguishable from a call site that genuinely dispatches nowhere.
// A missing call graph is reported as such by Status.CallGraph; a
// quietly incomplete one is not reportable at all.
func buildAllSSA(prog *ssa.Program) bool {
	var (
		wg     sync.WaitGroup
		failed atomic.Bool
		// The same bound x/tools uses, for the same reason: SSA
		// construction is CPU-bound and a package apiece would put one
		// goroutine per dependency on the run queue.
		sem = make(chan struct{}, runtime.GOMAXPROCS(0))
	)
	for _, pkg := range prog.AllPackages() {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					failed.Store(true)
				}
			}()
			pkg.Build()
		}()
	}
	wg.Wait()
	return !failed.Load()
}

// indexInvokes collects CHA's interface-dispatch answers keyed by the
// position of the call's opening parenthesis.
//
// The position is the join between the two halves of this package: SSA
// records a call site by the Lparen of the ast.CallExpr it came from, and
// so does the syntax tree the scanner walks. Both come from the same
// FileSet, so the token.Pos is an exact identity — no filename
// comparison, no line arithmetic, nothing that could drift.
func indexInvokes(cg *callgraph.Graph) map[token.Pos][]*types.Func {
	invokes := make(map[token.Pos][]*types.Func)
	seen := make(map[token.Pos]map[string]bool)
	for _, node := range cg.Nodes {
		for _, edge := range node.Out {
			pos, callee, ok := invokeTarget(edge)
			if !ok {
				continue
			}
			key := ObjectKey(callee)
			if seen[pos] == nil {
				seen[pos] = map[string]bool{}
			}
			if seen[pos][key] {
				continue
			}
			seen[pos][key] = true
			invokes[pos] = append(invokes[pos], callee)
		}
	}
	// CHA's node and edge iteration is map-ordered, so the candidate
	// list arrives in a different order on every run. Sorting it here
	// means the scanner emits the same edges in the same order for the
	// same tree, which docs/testing/determinism.md requires and the
	// golden snapshot would otherwise fail on at random.
	for pos, funcs := range invokes {
		sort.Slice(funcs, func(i, j int) bool {
			return ObjectKey(funcs[i]) < ObjectKey(funcs[j])
		})
		invokes[pos] = funcs
	}
	return invokes
}

// invokeTarget extracts (call site, concrete callee) from one callgraph
// edge, or reports that the edge is not an interface dispatch this
// package will vouch for.
func invokeTarget(edge *callgraph.Edge) (token.Pos, *types.Func, bool) {
	if edge == nil || edge.Site == nil || edge.Callee == nil {
		return token.NoPos, nil, false
	}
	common := edge.Site.Common()
	if common == nil || !common.IsInvoke() {
		return token.NoPos, nil, false
	}
	pos := common.Pos()
	if !pos.IsValid() {
		return token.NoPos, nil, false
	}
	fn := origin(edge.Callee.Func)
	if fn == nil {
		return token.NoPos, nil, false
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return token.NoPos, nil, false
	}
	return pos, obj.Origin(), true
}

// origin maps an SSA function back to the declaration it was written as.
//
// With generics, one source declaration produces one ssa.Function per
// instantiation, and each carries a different *types.Func. Without this
// step a call to Cache[string,*Order].Put and a call to Cache[int,X].Put
// are two callees with two ids, the symbol table grows a row per
// instantiation, and no id matches the one declaration the scanner
// actually indexed.
func origin(fn *ssa.Function) *ssa.Function {
	if fn == nil {
		return nil
	}
	if o := fn.Origin(); o != nil {
		return o
	}
	return fn
}
