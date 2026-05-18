package patterns

import (
	"fmt"
	"go/ast"
)

// matchCanonicalService reports every method whose body contains a UoW
// closure that does BOTH a repo.Save AND an outbox.Append. This is the
// nutrition-v2-go saveWithEvents shape:
//
//	func (svc *XService) saveWithEvents(...) error {
//	    return svc.uow.Run(ctx, func(ctx context.Context) error {
//	        if err := svc.repo.Save(ctx, x); err != nil {
//	            return err
//	        }
//	        return svc.outbox.AppendFromContext(ctx, x.PullEvents())
//	    })
//	}
//
// Strict requirements:
//
//   - Both a `*.Save / .Insert / .Update / .Upsert` AND a `*.Append /
//     .AppendFromContext` call must appear in the SAME closure body.
//   - The closure must be a *direct argument* of a call to a method with
//     one of cfg.UoWMethodNames (default: "Run"). The receiver of that call
//     can be anything — we don't require it to be literally named "uow".
//   - Save vs Append order is not enforced.
//
// Lenient on:
//
//   - The closure parameter name (`func(ctx context.Context) error`,
//     `func(_ context.Context) error`, anonymous arg names).
//   - The exact UoW method name (configurable via cfg.UoWMethodNames).
//   - Helper closures nested below the UoW closure are walked transitively
//     for the Save/Append calls, but only the outermost closure is
//     reported (so a helper-extracted shape stays a single match).
//
// What is intentionally NOT detected here (these are deferred to Horizon 2):
//
//   - Helper-splitting: save in one method, append in another → not a
//     match here. A separate "partial-canonical" recogniser could surface
//     it; out of scope for Phase 6f.
//   - Cross-file shapes.
//
// Confidence is fixed at 1.0 — the shape is strict enough that any match
// is almost certainly the canonical pattern.
func matchCanonicalService(cfg Config, f FileInput) []Match {
	var out []Match

	// Collect, per FuncDecl, the matches we want to surface — keyed on
	// the outer UoW call so nested closures don't double-count.
	for _, decl := range f.File.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		// Walk the body looking for `<x>.<UoWMethod>(..., func(...) error { ... })`.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !contains(cfg.UoWMethodNames, sel.Sel.Name) {
				return true
			}
			// Find a function-literal argument (closure). Any positional
			// arg position is fine — the canonical shape puts it as the
			// last arg, but lints / refactors may insert telemetry args.
			closure := findClosureArg(call.Args)
			if closure == nil {
				return true
			}
			hasSave, hasAppend := scanClosureForSaveAndAppend(cfg, closure)
			if !hasSave || !hasAppend {
				return true
			}

			sym := funcDeclSymbol(f.File, fn)
			out = append(out, Match{
				Pattern: PatternCanonicalService,
				Symbol:  sym,
				// Anchor the position on the method declaration so audit
				// reports show "open subject_aggregate_service.go:128"
				// rather than the deep-nested closure line.
				Position: posToFilePosition(f.FSet, fn.Pos(), f.RelPath),
				Detail: fmt.Sprintf(
					"%s closure has both repo.Save and outbox.Append",
					sel.Sel.Name,
				),
				Confidence: 1.0,
			})

			// Stop descending into this UoW call subtree — nested UoW
			// closures are exotic and we don't want to double-report.
			return false
		})
	}

	return out
}

// findClosureArg returns the first *ast.FuncLit argument from args, or
// nil if none of the arguments is a closure.
func findClosureArg(args []ast.Expr) *ast.FuncLit {
	for _, a := range args {
		if lit, ok := a.(*ast.FuncLit); ok {
			return lit
		}
	}
	return nil
}

// scanClosureForSaveAndAppend walks the closure body and returns whether
// at least one Save-like call AND one Append-like call were found.
// Nested closures inside the body ARE walked (so a `defer func(){...}` or
// an `errgroup.Go(func() {...})` that contains the Save or Append still
// counts toward the canonical match — this matches the nutrition pattern
// where Save and Append sometimes hide inside a helper invoked from the
// outer UoW closure).
func scanClosureForSaveAndAppend(cfg Config, lit *ast.FuncLit) (hasSave, hasAppend bool) {
	if lit == nil || lit.Body == nil {
		return false, false
	}
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		if hasSave && hasAppend {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		methodName := sel.Sel.Name
		switch {
		case contains(cfg.RepoSaveMethodNames, methodName):
			hasSave = true
		case contains(cfg.OutboxAppendMethodNames, methodName):
			hasAppend = true
		}
		return true
	})
	return hasSave, hasAppend
}
