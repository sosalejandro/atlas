package resolver

import (
	"go/token"
	"go/types"
	"sort"

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
		// own.
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
	prog.Build()

	cg := cha.CallGraph(prog)
	p.indexInvokes(cg)
	p.status.CallGraph = true
	p.status.CallGraphDuration = timeSince(start)
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
