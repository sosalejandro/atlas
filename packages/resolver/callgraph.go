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
func (p *Program) buildCallGraph(pkgs []*packages.Package) {
	defer func() {
		// SSA construction walks type-checked syntax that this package
		// did not produce and cannot fully vouch for. A panic here must
		// cost interface dispatch, not the scan: static resolution has
		// already been established by the type checker and stands on its
		// own. This covers ssautil.Packages, CHA and the indexing below;
		// the builder itself is covered by buildAllSSA, for the reason
		// written there.
		if r := recover(); r != nil {
			p.status.CallGraph = false
			p.invokes = map[token.Pos][]*types.Func{}
		}
	}()

	start := timeNow()
	prog, _ := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	if prog == nil {
		return
	}
	if !buildAllSSA(prog) {
		return
	}

	cg := cha.CallGraph(prog)
	p.indexInvokes(cg)
	p.status.CallGraph = true
	p.status.CallGraphDuration = timeSince(start)
}

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
func (p *Program) indexInvokes(cg *callgraph.Graph) {
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
			p.invokes[pos] = append(p.invokes[pos], callee)
		}
	}
	// CHA's node and edge iteration is map-ordered, so the candidate
	// list arrives in a different order on every run. Sorting it here
	// means the scanner emits the same edges in the same order for the
	// same tree, which docs/testing/determinism.md requires and the
	// golden snapshot would otherwise fail on at random.
	for pos, funcs := range p.invokes {
		sort.Slice(funcs, func(i, j int) bool {
			return ObjectKey(funcs[i]) < ObjectKey(funcs[j])
		})
		p.invokes[pos] = funcs
	}
	p.status.InvokeSites = len(p.invokes)
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
