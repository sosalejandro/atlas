package patterns

import (
	"fmt"
	"go/ast"
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// matchOutboxAppend reports every call expression that LOOKS like an outbox
// append:
//
//	someExpr.outbox.Append(...)
//	someExpr.Outbox.Append(...)
//	someExpr.outbox.AppendFromContext(...)
//	outbox.Append(...)  // package-prefixed (rarer; still detected)
//
// The recogniser is intentionally syntactic. It matches when the call
// selector ends in one of the configured Append method names AND the
// directly-receiving identifier (or chained selector tail) is named
// "outbox" or "Outbox" case-insensitively. This catches:
//
//   - svc.outbox.AppendFromContext(...)           — chained: outer.outbox.Method
//   - r.outbox.Append(...)                        — same shape
//   - outbox.Append(...)                          — package/var direct call
//
// It deliberately does NOT match:
//
//   - svc.repo.Save(...)                          — wrong receiver name
//   - svc.outbox.Drain(...)                       — wrong method name
//   - someBuffer.Append(...)                      — receiver not named outbox
//
// Confidence:
//
//	1.0  — chained `x.outbox.Method(...)` (the canonical service shape)
//	0.85 — bare `outbox.Method(...)` (less specific; could be a local var
//	       called "outbox" that isn't the transactional outbox)
func matchOutboxAppend(cfg Config, f FileInput) []Match {
	var out []Match

	ast.Inspect(f.File, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		methodName := sel.Sel.Name
		if !contains(cfg.OutboxAppendMethodNames, methodName) {
			return true
		}

		var receiverDesc string
		var confidence float64
		matched := false

		switch x := sel.X.(type) {
		case *ast.SelectorExpr:
			// `outer.outbox.Method(...)` — chained selector. The tail of
			// the inner selector is what we want.
			if strings.EqualFold(x.Sel.Name, "outbox") {
				receiverDesc = x.Sel.Name + "." + methodName
				confidence = 1.0
				matched = true
			}
		case *ast.Ident:
			// `outbox.Method(...)` — bare ident. Either a package import
			// or a local variable; we treat both as a lower-confidence hit.
			if strings.EqualFold(x.Name, "outbox") {
				receiverDesc = x.Name + "." + methodName
				confidence = 0.85
				matched = true
			}
		}

		if !matched {
			return true
		}

		sym := enclosingFuncSymbol(f.File, call)
		if sym == "" {
			// Call at package scope (rare for Append — usually in init).
			// Synthesise a stable handle from the file + line so the match
			// isn't dropped.
			pos := f.FSet.Position(call.Pos())
			sym = shared.SymbolID(fmt.Sprintf("%s:L%d", f.RelPath, pos.Line))
		}

		out = append(out, Match{
			Pattern:    PatternOutboxAppend,
			Symbol:     sym,
			Position:   posToFilePosition(f.FSet, call.Pos(), f.RelPath),
			Detail:     "calls " + receiverDesc,
			Confidence: confidence,
		})
		return true
	})

	return out
}
