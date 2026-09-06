package cfg

import (
	"go/ast"
	"go/token"
	"strings"
)

// Confidence grades an N+1 finding. It is part of the finding, not a global
// setting, because the evidence genuinely differs per site.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Evidence records HOW the call was identified as a query. A reader deciding
// whether to act on a finding needs to know whether atlas followed a graph
// edge to a real query or matched a method name.
const (
	EvidenceGraphEdge = "the call site has a graph edge to a query symbol"
	EvidenceNaming    = "the call matches a repository/query naming convention"
)

// NPlusOneCaveat is attached to every finding. A query inside a loop is a
// smell, not a proof: the collection may be bounded at two elements, the
// query may be served from a cache, the loop may run once. Shipping the
// finding without this sentence is what turns a useful signal into noise
// somebody mutes.
const NPlusOneCaveat = "a query inside a loop is a smell, not a proof: the collection may be " +
	"tiny, the call may hit a cache, and the fix (a batched query) is not always cheaper. " +
	"Confirm against the call's real cardinality before acting."

// QueryInLoop is one N+1 candidate: a query operation reached from inside a
// loop body. Both sites are reported, because the fix is at the loop and the
// cost is at the query, and a finding that names only one of them makes the
// reader go looking for the other.
type QueryInLoop struct {
	// Symbol is filled in by the caller (the CLI) so the finding joins the
	// graph. The analysis itself has no notion of symbol ids.
	SymbolID int64  `json:"symbol_id,omitempty"`
	Func     string `json:"func"`

	LoopLine int          `json:"loop_line"`
	LoopKind DecisionKind `json:"loop_kind"`
	// OverCollection reports that the loop iterates a container rather than
	// a count. A query per element of a collection is the N+1 shape; a query
	// in a three-attempt retry loop is not.
	OverCollection bool `json:"over_collection"`

	QueryLine int    `json:"query_line"`
	QueryText string `json:"query_text"`
	// Guarded means the loop has a path that skips the query — a cache-miss
	// branch, a `continue` on a hit. Structurally still a query in a loop;
	// practically often correct, so it costs a confidence notch.
	Guarded bool `json:"guarded"`

	Confidence Confidence `json:"confidence"`
	Evidence   string     `json:"evidence"`
	Caveat     string     `json:"caveat"`
}

// QueryLoopOptions configures the detector.
type QueryLoopOptions struct {
	// KnownQueryLines are call-site lines the caller has PROVEN issue a
	// query — in atlas that means an edge from this symbol to a `sql:` node
	// in the graph. A hit here is evidence rather than a guess, and raises
	// the finding a confidence notch.
	KnownQueryLines map[int]bool
	// DisableNameHeuristic turns off the naming fallback, leaving only
	// KnownQueryLines. Use it on a repo where the graph's query edges are
	// complete and false positives cost more than misses.
	DisableNameHeuristic bool
}

// DetectQueryInLoop reports query calls reached from inside a loop body.
//
// "Inside" is computed on the CFG, not lexically: the loop's blocks are its
// natural loop (the nodes on a cycle back to the header), so a `for` whose
// body always breaks — a loop that runs once — yields nothing, which is the
// right answer and not one a lexical scan gives you.
//
// The analysis is intra-procedural. A query reached through a helper called
// from the loop is a real N+1 and this will not find it; that needs the call
// graph, and reporting a guess about it here would be indistinguishable from
// the direct case that IS proven.
func DetectQueryInLoop(fset *token.FileSet, fn *ast.FuncDecl, g *Graph, opts QueryLoopOptions) []QueryInLoop {
	if fn == nil || fn.Body == nil || g == nil {
		return nil
	}
	calls := queryCalls(fset, fn, opts)
	if len(calls) == 0 {
		return nil
	}
	var out []QueryInLoop
	for i := range g.Decisions {
		d := &g.Decisions[i]
		if d.Kind != DecisionFor && d.Kind != DecisionRange {
			continue
		}
		body, backs := naturalLoop(g, d.Block)
		if len(backs) == 0 {
			// No back edge: the body cannot repeat, so nothing in it is an
			// N+1 however query-shaped it looks.
			continue
		}
		for _, c := range calls {
			blk, ok := tightestBlock(g, body, c.line)
			if !ok {
				continue
			}
			out = append(out, QueryInLoop{
				Func:           g.Func,
				LoopLine:       d.Line,
				LoopKind:       d.Kind,
				OverCollection: d.Collection,
				QueryLine:      c.line,
				QueryText:      c.text,
				Guarded:        skippable(g, body, backs, d.Block, blk),
				Confidence:     grade(c.proven, d.Collection, skippable(g, body, backs, d.Block, blk)),
				Evidence:       c.evidence,
				Caveat:         NPlusOneCaveat,
			})
		}
	}
	return out
}

// grade turns the three signals into one confidence. Starting from the
// evidence for "this is a query", each way the shape could be innocent costs
// a notch: a loop that is not per-element, and a query the loop can skip.
func grade(proven, collection, guarded bool) Confidence {
	level := 1 // medium
	if proven {
		level = 2
	}
	if !collection {
		level--
	}
	if guarded {
		level--
	}
	switch {
	case level >= 2:
		return ConfidenceHigh
	case level == 1:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

type queryCall struct {
	line     int
	text     string
	proven   bool
	evidence string
}

// queryCalls finds the call sites in fn that look like (or are known to be)
// query operations.
func queryCalls(fset *token.FileSet, fn *ast.FuncDecl, opts QueryLoopOptions) []queryCall {
	var out []queryCall
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			// A closure is a separate flow; whether IT runs per element is
			// not something this function's CFG can say.
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		line := fset.Position(call.Lparen).Line
		if opts.KnownQueryLines[line] {
			out = append(out, queryCall{line: line, text: exprText(call.Fun), proven: true, evidence: EvidenceGraphEdge})
			return true
		}
		if opts.DisableNameHeuristic {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !queryVerb(sel.Sel.Name) || !queryReceiver(sel.X) {
			return true
		}
		out = append(out, queryCall{line: line, text: exprText(call.Fun), evidence: EvidenceNaming})
		return true
	})
	return out
}

// queryVerbs are the method-name shapes generated query layers use (sqlc,
// gorm, sqlx, hand-written repositories). The list is deliberately about
// VERBS, not about any one library, because the whole point is to work
// before packages/sqlcmap can resolve a call precisely.
var queryVerbs = []string{
	"get", "list", "find", "select", "query", "insert", "update", "delete",
	"count", "exec", "load", "fetch", "save", "create", "upsert", "scan",
}

func queryVerb(name string) bool {
	lower := strings.ToLower(name)
	for _, v := range queryVerbs {
		if strings.HasPrefix(lower, v) {
			return true
		}
	}
	return false
}

// queryReceiverHints are the identifiers a data-access handle is called in
// practice. Requiring one keeps `strings.Count` and `list.Get` out of the
// findings — without it the verb list alone fires on half a codebase.
var queryReceiverHints = []string{
	"db", "tx", "conn", "repo", "store", "queries", "querier", "dao", "client", "sql",
}

func queryReceiver(x ast.Expr) bool {
	text := strings.ToLower(exprText(x))
	// Match on the last path element (`s.db`, `r.q`, `c.store.users`) so a
	// long receiver chain still resolves to the handle at its end.
	if i := strings.LastIndex(text, "."); i >= 0 {
		text = text[i+1:]
	}
	for _, h := range queryReceiverHints {
		if text == h || strings.HasSuffix(text, h) {
			return true
		}
	}
	// A single-letter querier (`q.GetUser`) is the sqlc convention.
	return len(text) == 1 && (text == "q" || text == "r" || text == "s")
}

// naturalLoop returns the blocks of the loop headed by `header`, plus the
// blocks that carry its back edges.
//
// The set is the standard natural loop: the header, and every node that can
// reach a back-edge source without passing through the header again. Anything
// outside it is code the loop merely leads to, not code it repeats.
func naturalLoop(g *Graph, header int) (map[int]bool, []int) {
	preds := make([][]int, len(g.Blocks))
	var backs []int
	for _, e := range g.Edges {
		preds[e.To] = append(preds[e.To], e.From)
		if e.To == header && e.Kind == EdgeLoopBack {
			backs = append(backs, e.From)
		}
	}
	body := map[int]bool{header: true}
	stack := append([]int{}, backs...)
	for _, b := range backs {
		body[b] = true
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, p := range preds[cur] {
			if !body[p] {
				body[p] = true
				stack = append(stack, p)
			}
		}
	}
	return body, backs
}

// skippable reports whether the loop has a path from its header back to a
// back edge that does NOT pass through `block` — the cache-miss shape.
//
// This is the honest version of "is the query guarded": it is a question
// about paths, and answering it lexically ("is the call inside an if") misses
// the commonest real case, a `continue` on a cache hit that leaves the query
// syntactically unnested.
func skippable(g *Graph, body map[int]bool, backs []int, header, block int) bool {
	if block == header {
		return false
	}
	succ := make([][]int, len(g.Blocks))
	for _, e := range g.Edges {
		if body[e.From] && body[e.To] {
			succ[e.From] = append(succ[e.From], e.To)
		}
	}
	seen := map[int]bool{header: true, block: true}
	stack := []int{header}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, n := range succ[cur] {
			if seen[n] {
				continue
			}
			seen[n] = true
			stack = append(stack, n)
		}
	}
	for _, b := range backs {
		if b != block && seen[b] {
			return true
		}
	}
	return false
}

// tightestBlock finds the loop block whose span covers `line`, preferring the
// narrowest — blocks nest by line range, and the innermost one is the block
// the statement actually belongs to.
func tightestBlock(g *Graph, body map[int]bool, line int) (int, bool) {
	best, found := -1, false
	for i := range g.Blocks {
		b := g.Blocks[i]
		if !body[b.Index] || line < b.StartLine || line > b.EndLine {
			continue
		}
		if !found || (b.EndLine-b.StartLine) < (g.Blocks[best].EndLine-g.Blocks[best].StartLine) {
			best, found = b.Index, true
		}
	}
	return best, found
}
