package patterns

import (
	"go/ast"
	"go/token"

	"github.com/sosalejandro/atlas/packages/shared"
)

// matchEventRecorderEmbed reports every struct whose field list embeds a
// type whose tail name matches one of cfg.EventRecorderNames.
//
// Recognised embed shapes (all anonymous fields = no Names):
//
//	type Subject struct {
//	    sharedAgg.EventRecorder       // qualified value embed
//	    *sharedAgg.EventRecorder      // qualified pointer embed
//	    EventRecorder                 // same-package value embed
//	    *EventRecorder                // same-package pointer embed
//	}
//
// Cross-package resolution is structural: we match on the TAIL of the type
// path (the rightmost selector). This catches `sharedAgg.EventRecorder`
// regardless of how the import is aliased — the package name is irrelevant
// because EventRecorder is a unique-enough name in practice. If a project
// has its own non-aggregate type called EventRecorder, that's a low-rate
// false positive surfaced as Detail.
//
// Confidence:
//
//	1.0  — qualified embed (pkg.EventRecorder or *pkg.EventRecorder)
//	0.95 — same-package embed (just EventRecorder / *EventRecorder)
//	       — slightly lower because the struct could be a test fixture
//	       defining its own EventRecorder, but high enough to still surface
func matchEventRecorderEmbed(cfg Config, f FileInput) []Match {
	var out []Match

	for _, decl := range f.File.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, field := range st.Fields.List {
				// Embeds are anonymous: no Names. Named fields whose type
				// happens to be EventRecorder are NOT embeds and don't get
				// the type's methods, so we skip them.
				if len(field.Names) != 0 {
					continue
				}
				tail, qualified, ok := embedTail(field.Type)
				if !ok {
					continue
				}
				if !contains(cfg.EventRecorderNames, tail) {
					continue
				}

				confidence := 0.95
				detail := "embeds " + tail
				if qualified != "" {
					confidence = 1.0
					detail = "embeds " + qualified + "." + tail
				}

				out = append(out, Match{
					Pattern: PatternEventRecorderEmbed,
					// Symbol is the struct type — this is the aggregate root.
					Symbol:     shared.SymbolID(ts.Name.Name),
					Position:   posToFilePosition(f.FSet, ts.Pos(), f.RelPath),
					Detail:     detail,
					Confidence: confidence,
				})
				// One match per struct, even if it embeds multiple
				// EventRecorder variants (unusual).
				break
			}
		}
	}

	return out
}

// embedTail returns the tail name of an embedded-field type expression
// plus the qualifier (package name) if any.
//
//	EventRecorder           → ("EventRecorder", "",         true)
//	*EventRecorder          → ("EventRecorder", "",         true)
//	pkg.EventRecorder       → ("EventRecorder", "pkg",      true)
//	*pkg.EventRecorder      → ("EventRecorder", "pkg",      true)
//	deeper.path.Type        → not supported (Go embeds can't be that deep)
//	func(){}                → ("", "", false)
func embedTail(expr ast.Expr) (tail, qualifier string, ok bool) {
	if star, isStar := expr.(*ast.StarExpr); isStar {
		expr = star.X
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name, "", true
	case *ast.SelectorExpr:
		if ident, isIdent := e.X.(*ast.Ident); isIdent {
			return e.Sel.Name, ident.Name, true
		}
	}
	return "", "", false
}
