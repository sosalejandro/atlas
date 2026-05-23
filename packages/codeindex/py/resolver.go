package pyscan

import (
	"strings"

	"github.com/sosalejandro/atlas/packages/shared"
)

// externalPyStubPath is the synthetic file path attached to symbol stubs
// the Python scanner generates for unresolved edge targets (stdlib /
// third-party / unimported names referenced from Python source). The
// `external:py` prefix is reserved — no real source file path can collide
// with the leading colon because Atlas paths are always repo-relative
// forward-slash. Downstream consumers (audit, trace) may filter on this
// prefix to distinguish first-party from external symbols.
const externalPyStubPath = "external:py"

// externalSymbolStub returns a positionless symbol for an unresolved
// Python edge target. The symbol is given KindFunc as a neutral default
// (we don't know whether the unresolved name is a function, class, or
// constant — the SymbolKind enum has no "unknown" member, and KindFunc
// matches the rawKindToSymbolKind fallback already documented for unknown
// scanner.py node kinds). Line 1 satisfies the store's NOT NULL constraint
// on edges.line without lying about a real source position.
func externalSymbolStub(id shared.SymbolID) shared.Symbol {
	return shared.Symbol{
		ID:   id,
		Kind: shared.KindFunc,
		Position: shared.FilePosition{
			Path: externalPyStubPath,
			Line: 1,
		},
	}
}

// pyEdgeResolver promotes an unqualified edge target (e.g. `helper`,
// `Base`) emitted by scanner.py into a fully-qualified shared.SymbolID
// (e.g. `sample.helper`, `sample.Base`) when a same-module symbol with
// that base name exists. This is required because scanner.py emits
// best-effort callee renderings — Python's dynamic dispatch makes
// full name resolution at AST time infeasible — so the Go layer must
// fix up obvious in-module cases before edges hit the store, otherwise
// every same-file call gets dropped by the surrogate-id lookup.
//
// Resolution rules (in priority order, first match wins):
//
//  1. The raw target already matches an emitted symbol id verbatim
//     (e.g. `sample.helper` → `sample.helper`). No-op.
//  2. The target is a bare identifier and a same-module symbol with
//     that basename exists (e.g. `helper` from `sample.compute` →
//     `sample.helper`). Promote to the qualified id.
//  3. The target is a dotted access whose head is a known same-module
//     class and tail is one of its methods (e.g. `MyClass.run` from
//     `sample.compute` → `sample.MyClass.run`). Promote.
//  4. No resolution. Pass the raw target through untouched — the store
//     will skip it during ingest (unresolved targets do not pollute the
//     graph).
//
// pyEdgeResolver is stateless after construction; safe for concurrent use.
type pyEdgeResolver struct {
	// allIDs is the set of every emitted symbol id; lets rule (1)
	// short-circuit without re-walking the module index.
	allIDs map[string]struct{}

	// byModule maps a module id (e.g. "sample") → basename → qualified id.
	// One module-local name may map to multiple qualified ids only across
	// module boundaries; within a module, Python forbids legal duplication
	// (the second def silently shadows the first), so storing a single
	// qualified id per (module, basename) pair is loss-free.
	byModule map[string]map[string]string
}

// newPyEdgeResolver builds the resolution index from scanner.py's raw
// node list. Module nodes contribute the module ids; class/method/
// function/const nodes contribute the module-scoped basename index.
func newPyEdgeResolver(nodes []rawNode) *pyEdgeResolver {
	r := &pyEdgeResolver{
		allIDs:   make(map[string]struct{}, len(nodes)),
		byModule: make(map[string]map[string]string),
	}
	for _, n := range nodes {
		r.allIDs[n.ID] = struct{}{}
		modulePart, localPart := splitModuleAndLocal(n.ID, n.Kind)
		if modulePart == "" || localPart == "" {
			continue
		}
		mod, ok := r.byModule[modulePart]
		if !ok {
			mod = make(map[string]string)
			r.byModule[modulePart] = mod
		}
		// Index every prefix of the local part so a dotted access like
		// `MyClass.run` (target) resolves via the `MyClass.run` local
		// key as well as the bare `MyClass` head.
		mod[localPart] = n.ID
	}
	return r
}

// resolve returns the canonical SymbolID for target when called from
// fromID's enclosing module. Returns the raw target as SymbolID when no
// promotion applies.
func (r *pyEdgeResolver) resolve(fromID shared.SymbolID, target string) shared.SymbolID {
	if target == "" {
		return ""
	}
	// Rule (1): the raw target already matches an emitted symbol id.
	if _, ok := r.allIDs[target]; ok {
		return shared.SymbolID(target)
	}
	moduleID := moduleOfSymbol(string(fromID))
	if moduleID == "" {
		return shared.SymbolID(target)
	}
	mod, ok := r.byModule[moduleID]
	if !ok {
		return shared.SymbolID(target)
	}
	// Rule (2/3): same-module basename or dotted-head match.
	if qn, ok := mod[target]; ok {
		return shared.SymbolID(qn)
	}
	// Rule (4): no match → pass through. The store ingestor will skip
	// edges whose `to` cannot be resolved to a symbol id.
	return shared.SymbolID(target)
}

// moduleOfSymbol returns the module id portion of a symbol id. For
// `sample.helper` it returns `sample`; for `sample.MyClass.run` it
// also returns `sample`. For a bare module id (no dot) it returns
// the id itself — every symbol scanner.py emits is rooted at its
// module, so callers in module scope can still resolve in-module names.
func moduleOfSymbol(id string) string {
	if id == "" {
		return ""
	}
	if i := strings.IndexByte(id, '.'); i > 0 {
		return id[:i]
	}
	return id
}

// splitModuleAndLocal splits a scanner.py emitted symbol id into its
// module head and the locally-scoped remainder. For module-kind nodes
// the local part is empty (the symbol IS the module).
func splitModuleAndLocal(id, kind string) (string, string) {
	if kind == "module" {
		return id, ""
	}
	i := strings.IndexByte(id, '.')
	if i <= 0 {
		// Defensive: a non-module node with no dot can't be split. Skip
		// rather than mis-index — the resolver tolerates missing entries.
		return "", ""
	}
	return id[:i], id[i+1:]
}
