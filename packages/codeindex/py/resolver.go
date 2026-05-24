package pyscan

import (
	"sort"
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
// `Base`, `echo`) emitted by scanner.py into a fully-qualified
// shared.SymbolID when a first-party Python symbol can be found via
// static analysis of the scanned project.
//
// scanner.py emits best-effort callee renderings — Python's dynamic
// dispatch makes full name resolution at AST time infeasible — so the
// Go layer fixes up obvious cases before edges hit the store, otherwise
// every cross-module call gets dropped at ingest as an external stub.
//
// Resolution rules, in priority order (first match wins):
//
//  1. The raw target already matches an emitted symbol id verbatim
//     (e.g. `sample.helper` → `sample.helper`). No-op.
//  2. Suffix match against the canonical-Python-name index — a multi-
//     dot target like `mypkg.db.models.Case` that does NOT match any
//     emitted id verbatim resolves to an internal symbol whose id ENDS
//     with that target at a dot boundary, provided there is exactly one
//     such symbol. This handles `src/` layouts where atlas's
//     path-rooted symbol ids (`packages.db.src.mypkg.db.models.Case`)
//     differ from the Python import path (`mypkg.db.models.Case`) the
//     user actually wrote in `from … import …`. Issue #15.
//  3. Same-module basename or dotted-head match (e.g. `helper` from
//     `sample.compute` → `sample.helper`; `MyClass.run` →
//     `sample.MyClass.run`).
//  4. Caller's `from X import callee` — if the caller's module has an
//     import edge whose bound name matches the callee, qualify to that
//     edge's import target (e.g. `echo` from `src.click.core` with a
//     `from .termui import echo` edge → `src.click.termui.echo`).
//  5. Re-export from the caller's package `__init__.py`: if the package's
//     `__init__` re-exports the callee via `from .module import callee`,
//     qualify to `<package>.module.callee` (transitive lookup against
//     the actual definition).
//  6. Sibling-module top-level: among modules in the same parent package
//     as the caller, look for a top-level symbol whose basename matches
//     the callee. Exactly one hit → qualify. Multiple hits → pick the
//     most-imported one as a tie-breaker heuristic.
//  7. No resolution. Pass the raw target through untouched — the
//     ingestor will emit an `external:py` stub so the edge still lands.
//
// pyEdgeResolver is stateless after construction; safe for concurrent use.
//
// Out of scope (acknowledged limitations):
//
//   - Type inference for dynamic dispatch (`x.method()` where `x` is an
//     Any-typed parameter) — requires pyright-grade type analysis.
//   - super() MRO walking — `super().foo` leaves a `super().foo`
//     external stub. Resolving the MRO would require a runtime model.
type pyEdgeResolver struct {
	// allIDs is the set of every emitted symbol id; rule (1) short-
	// circuits via this set without re-walking the module index.
	allIDs map[string]struct{}

	// byModule maps a module id (e.g. "sample") → basename → qualified
	// id of an emitted symbol in that module. Rule (3).
	byModule map[string]map[string]string

	// importAlias maps a caller module id → bound name → fully-qualified
	// import target. Built from the scanner's `import` edges with the
	// scanner.py rendering reversed back into Python-import semantics
	// (relative dots resolved against the caller's package). Rule (4).
	importAlias map[string]map[string]string

	// reExport maps a package module id (i.e. the id of an
	// `__init__.py`) → bound name → the qualified id the package
	// re-exports. Rule (5) consults the caller's package; transitive
	// re-exports (a sibling `__init__` re-exporting from another
	// package) are NOT walked — the static analysis bar stays "best
	// effort, single hop".
	reExport map[string]map[string]string

	// siblingIndex maps a parent-package module id → basename → list of
	// qualified ids in sibling modules whose top-level symbol matches
	// the basename. The list is sorted to make tie-breaking deterministic
	// (rule 6 uses importPopularity as the first key, then alpha-sort).
	siblingIndex map[string]map[string][]string

	// importPopularity counts how many distinct caller modules import
	// each qualified symbol id. Used as the deterministic tie-breaker
	// when rule (6) finds multiple sibling candidates. A 0 entry means
	// no observed import — fine, ambiguous cases just fall back to
	// alphabetical order in resolveSibling.
	importPopularity map[string]int

	// packageInits is the set of module ids that are package
	// `__init__.py` files (vs leaf `.py` modules). The scanner emits
	// both as `kind=module`, indistinguishable by id alone — we record
	// the file path during construction and recover the distinction
	// here. Used by parentPackage() to decide whether the caller's
	// module IS its own package or whether to strip a dotted tail.
	packageInits map[string]struct{}

	// suffixIndex maps every dot-segmented tail of every emitted symbol
	// id (length >= 2 segments) to the list of full symbol ids that end
	// with that tail at a dot boundary. Used by rule (2) — the canonical-
	// Python-name resolver — to bridge atlas's path-rooted symbol ids
	// (e.g. `packages.db.src.mypkg.db.models.Case`) and the canonical
	// Python import paths users actually write (e.g. `mypkg.db.models.Case`)
	// when projects use a `src/` layout or a monorepo packaging convention.
	//
	// Bare 1-segment names are intentionally NOT indexed here — those
	// are resolved by rules (3) (same-module), (4) (import-alias), (5)
	// (re-export), and (6) (sibling), each of which carries enough caller
	// context to disambiguate. Indexing 1-segment names would create
	// massive collision buckets (every `Case` / `User` / `run`) and add
	// no useful resolution beyond what those rules already deliver.
	//
	// Each bucket is sorted at construction time for deterministic
	// tie-breaking when multiple symbols share the same suffix (e.g. two
	// `mypkg/db/models.py` files in different src roots). Multi-hit
	// buckets fall through to rule (7) passthrough — we never guess.
	suffixIndex map[string][]string
}

// newPyEdgeResolver builds the resolution index from scanner.py's raw
// node + edge list. Construction is O(nodes + edges).
//
// The function name keeps the historical signature (taking just nodes)
// for the old call site but the implementation now also folds in edges
// so the resolver can build the import-alias + re-export indices in one
// pass.
func newPyEdgeResolver(nodes []rawNode, edges []rawEdge) *pyEdgeResolver {
	r := &pyEdgeResolver{
		allIDs:           make(map[string]struct{}, len(nodes)),
		byModule:         make(map[string]map[string]string),
		importAlias:      make(map[string]map[string]string),
		reExport:         make(map[string]map[string]string),
		siblingIndex:     make(map[string]map[string][]string),
		importPopularity: make(map[string]int),
		packageInits:     make(map[string]struct{}),
		suffixIndex:      make(map[string][]string),
	}
	topLevelByModule := r.indexNodes(nodes)
	r.indexEdges(edges)
	r.indexSiblings(topLevelByModule)
	r.indexSuffixes(nodes)
	return r
}

// indexNodes populates allIDs, packageInits, and byModule from the raw
// scanner.py node list. Returns the per-module top-level symbol map
// the sibling-index pass needs.
func (r *pyEdgeResolver) indexNodes(nodes []rawNode) map[string]map[string]string {
	topLevelByModule := make(map[string]map[string]string)
	for _, n := range nodes {
		r.allIDs[n.ID] = struct{}{}
		if n.Kind == "module" {
			if strings.HasSuffix(n.File, "/__init__.py") || n.File == "__init__.py" {
				r.packageInits[n.ID] = struct{}{}
			}
			continue
		}
		modulePart, localPart := splitModuleAndLocal(n.ID, n.Kind)
		if modulePart == "" || localPart == "" {
			continue
		}
		mod, ok := r.byModule[modulePart]
		if !ok {
			mod = make(map[string]string)
			r.byModule[modulePart] = mod
		}
		mod[localPart] = n.ID

		if !strings.Contains(localPart, ".") {
			top, ok := topLevelByModule[modulePart]
			if !ok {
				top = make(map[string]string)
				topLevelByModule[modulePart] = top
			}
			top[localPart] = n.ID
		}
	}
	return topLevelByModule
}

// indexEdges scans import edges to build importAlias + reExport and
// increments importPopularity for resolved targets so rule (5) can
// tie-break by usage.
func (r *pyEdgeResolver) indexEdges(edges []rawEdge) {
	for _, e := range edges {
		if e.Kind != "import" {
			continue
		}
		callerModule := e.From
		boundName, qualified := boundNameAndQualifiedImport(callerModule, e.To, r.packageInits)
		if boundName == "" || qualified == "" {
			continue
		}
		alias, ok := r.importAlias[callerModule]
		if !ok {
			alias = make(map[string]string)
			r.importAlias[callerModule] = alias
		}
		alias[boundName] = qualified

		if _, isInit := r.packageInits[callerModule]; isInit {
			rex, ok := r.reExport[callerModule]
			if !ok {
				rex = make(map[string]string)
				r.reExport[callerModule] = rex
			}
			rex[boundName] = qualified
		}

		if _, ok := r.allIDs[qualified]; ok {
			r.importPopularity[qualified]++
		}
	}
}

// indexSuffixes populates r.suffixIndex with every dot-segmented tail
// of every emitted symbol id whose tail length is at least 2 segments.
//
// Why ≥2 segments: import targets in the wild that hit this index are
// always multi-dot (`mypkg.db.models.Case`, `services.api.foo.bar`).
// Bare 1-segment callees (`Case`, `helper`) are resolved by the rules
// that carry caller context (rules 3-6) where the disambiguation works
// without inducing the noise of indexing every basename in the project.
//
// Why post-construction sort: the resolver promises deterministic
// resolution (same scan → same edges) and the suffix lookup needs the
// bucket order stable for multi-hit ambiguity reporting + future
// tie-breaking work. Sort cost is O(B log B) per bucket; the suffix
// index is the only post-O(n) write so this stays cheap in aggregate.
//
// Memory note: total entries are bounded by sum-over-symbols of
// (segments - 1). For a 20k-symbol project averaging 6 segments per id
// that's ≈100k map entries — well within reasonable bounds for an
// in-process resolver. If this becomes a hot spot for very large
// codebases the index can be lazily built on first miss instead.
func (r *pyEdgeResolver) indexSuffixes(nodes []rawNode) {
	for _, n := range nodes {
		id := n.ID
		if id == "" {
			continue
		}
		// Walk suffix start positions from "skip first segment" toward
		// the end. For id="a.b.c.d" we register "b.c.d", "c.d" — i.e.
		// every multi-segment tail strictly shorter than the full id.
		// Indexing the full id is redundant with rule (1) exact match.
		i := 0
		for i < len(id) {
			j := strings.IndexByte(id[i:], '.')
			if j < 0 {
				break
			}
			i += j + 1
			suffix := id[i:]
			if !strings.Contains(suffix, ".") {
				// 1-segment tails skipped; see method doc.
				break
			}
			r.suffixIndex[suffix] = append(r.suffixIndex[suffix], id)
		}
	}
	for s := range r.suffixIndex {
		sort.Strings(r.suffixIndex[s])
	}
}

// indexSiblings builds the sibling index keyed by parent package and
// sorts each bucket so the tie-break in pickSibling is deterministic.
func (r *pyEdgeResolver) indexSiblings(topLevelByModule map[string]map[string]string) {
	for moduleID, topLevels := range topLevelByModule {
		pkg := parentPackage(moduleID, r.packageInits)
		bucket, ok := r.siblingIndex[pkg]
		if !ok {
			bucket = make(map[string][]string)
			r.siblingIndex[pkg] = bucket
		}
		for name, qn := range topLevels {
			bucket[name] = append(bucket[name], qn)
		}
	}
	for _, bucket := range r.siblingIndex {
		for name := range bucket {
			sort.Strings(bucket[name])
		}
	}
}

// resolve returns the canonical SymbolID for target when called from
// fromID's enclosing module. Returns the raw target as SymbolID when no
// promotion applies; the caller (mapToResult) then emits an external
// stub so the edge still lands in the store.
//
// The function is a fixed-order dispatch over per-rule helpers — each
// returns ("", false) when its rule doesn't fire, allowing the next
// rule to try. The first rule that produces a non-empty result wins.
func (r *pyEdgeResolver) resolve(fromID shared.SymbolID, target string) shared.SymbolID {
	if target == "" {
		return ""
	}
	// Rule (1): exact match.
	if _, ok := r.allIDs[target]; ok {
		return shared.SymbolID(target)
	}
	// Rule (2): canonical-Python-name suffix match — the import target
	// is a multi-dot path like `mypkg.db.models.Case` and exactly one
	// emitted symbol id ends with that suffix at a dot boundary. This
	// fires for `src/` layouts and monorepo packaging conventions where
	// atlas's path-rooted symbol ids drift from the importable Python
	// module path. Issue #15.
	//
	// We intentionally check this BEFORE we drop into caller-context-
	// dependent rules (3-6), because canonical-Python-name matches need
	// no caller context — they are file-to-file edges by construction.
	// Running this rule before the callerModule check also means we
	// resolve module-level (no caller) imports, which scanner.py emits
	// with from=<module_id> and to=<absolute target>.
	if qn, ok := r.resolveSuffix(target); ok {
		return shared.SymbolID(qn)
	}
	callerModule := r.callerEnclosingModule(string(fromID))
	if callerModule == "" {
		return shared.SymbolID(target)
	}

	// Rule (3): same-module basename / dotted-head match.
	if qn, ok := r.resolveSameModule(callerModule, target); ok {
		return shared.SymbolID(qn)
	}

	// Strategies 3–5 share a head/tail split — `echo` resolves bare,
	// `echo.upper` resolves via head and then applies the tail (`.upper`
	// becomes a no-op if no combined symbol exists).
	head, tail := splitHeadTail(target)
	pkg := parentPackage(callerModule, r.packageInits)

	for _, fn := range []func() (string, bool){
		func() (string, bool) { return r.resolveImportAlias(callerModule, head, tail) },
		func() (string, bool) { return r.resolveReExport(pkg, head, tail) },
		func() (string, bool) { return r.resolveSibling(pkg, head, tail, callerModule) },
	} {
		if qn, ok := fn(); ok {
			return shared.SymbolID(qn)
		}
	}

	// Rule (7): no resolution — pass through. mapToResult will stub it.
	return shared.SymbolID(target)
}

// callerEnclosingModule resolves fromID's enclosing module id. Uses the
// longest registered-module-prefix lookup when possible, falling back
// to the simple last-dot prefix when no registered module matches
// (handles orphan symbols inserted without a module node).
func (r *pyEdgeResolver) callerEnclosingModule(fromID string) string {
	if m := r.enclosingModule(fromID); m != "" {
		return m
	}
	return moduleOfSymbol(fromID)
}

// resolveSuffix implements rule (2): canonical-Python-name suffix match.
//
// For a multi-dot target like `mypkg.db.models.Case`, looks for an
// emitted symbol id that ends with that exact suffix at a dot boundary.
// Returns the symbol id only when the suffix maps to EXACTLY ONE entry —
// multiple-hit suffixes are ambiguous (two packages named the same
// across different src roots) and we refuse to guess, deferring to the
// caller's pass-through stub so the data quality issue is visible
// rather than silently mis-resolved.
//
// Targets with no dot are skipped — bare names are the province of
// rules (3) through (6) which carry caller context.
func (r *pyEdgeResolver) resolveSuffix(target string) (string, bool) {
	if !strings.Contains(target, ".") {
		return "", false
	}
	candidates := r.suffixIndex[target]
	if len(candidates) != 1 {
		return "", false
	}
	return candidates[0], true
}

// resolveSameModule implements rule (3): same-module basename or
// dotted-head match against the byModule index.
func (r *pyEdgeResolver) resolveSameModule(callerModule, target string) (string, bool) {
	mod, ok := r.byModule[callerModule]
	if !ok {
		return "", false
	}
	qn, ok := mod[target]
	return qn, ok
}

// resolveImportAlias implements rule (4): the caller's own `from X
// import Y` introduces Y into the caller's namespace; if the head
// matches a bound name we qualify to the import target.
func (r *pyEdgeResolver) resolveImportAlias(callerModule, head, tail string) (string, bool) {
	alias, ok := r.importAlias[callerModule]
	if !ok {
		return "", false
	}
	qn, ok := alias[head]
	if !ok {
		return "", false
	}
	return r.applyTail(qn, tail), true
}

// resolveReExport implements rule (5): if the caller's package
// __init__.py re-exports head via `from .module import head`, qualify
// to the re-export's target.
func (r *pyEdgeResolver) resolveReExport(pkg, head, tail string) (string, bool) {
	if pkg == "" {
		return "", false
	}
	rex, ok := r.reExport[pkg]
	if !ok {
		return "", false
	}
	qn, ok := rex[head]
	if !ok {
		return "", false
	}
	return r.applyTail(qn, tail), true
}

// resolveSibling implements rule (6): match head against top-level
// symbols of sibling modules in the same parent package, picking the
// most-imported one when multiple modules define it.
func (r *pyEdgeResolver) resolveSibling(pkg, head, tail, callerModule string) (string, bool) {
	if pkg == "" {
		return "", false
	}
	bucket, ok := r.siblingIndex[pkg]
	if !ok {
		return "", false
	}
	candidates := bucket[head]
	if len(candidates) == 0 {
		return "", false
	}
	picked := r.pickSibling(candidates, callerModule)
	if picked == "" {
		return "", false
	}
	return r.applyTail(picked, tail), true
}

// applyTail composes a resolved head qualified id with any trailing
// dotted access from the original target. `head.run` → "Cls.run" gives
// us a chance to land on a class method symbol if it exists; falling
// back to just the head qualified id if no such combined symbol exists.
//
// Returns "" only when the composed id can't be defended — i.e. when
// the head failed to resolve to anything we can attach the tail to. In
// practice the function always returns a non-empty string because head
// is already a verified qualified id when this function is reached.
func (r *pyEdgeResolver) applyTail(head, tail string) string {
	if tail == "" {
		return head
	}
	combined := head + "." + tail
	if _, ok := r.allIDs[combined]; ok {
		return combined
	}
	// The combined id doesn't exist as an emitted symbol. The most
	// helpful fallback is the head itself — we resolved the bound name
	// even if we couldn't follow the dotted access. The caller gets an
	// edge to the method's enclosing class / module, which still
	// preserves the "this call goes here" navigation that trace cares
	// about.
	return head
}

// pickSibling picks one qualified id from a list of sibling candidates
// using a deterministic two-tier ranker:
//
//  1. Most-imported by other modules in the project (importPopularity).
//  2. Alphabetical order on the qualified id.
//
// callerModule is currently unused as a tie-break input — we keep the
// signature open so a future rule can prefer same-directory siblings
// over deeper-nested ones if real-world data shows that's needed.
func (r *pyEdgeResolver) pickSibling(candidates []string, _ string) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	best := candidates[0]
	bestPop := r.importPopularity[best]
	for _, c := range candidates[1:] {
		pop := r.importPopularity[c]
		if pop > bestPop || (pop == bestPop && c < best) {
			best = c
			bestPop = pop
		}
	}
	return best
}

// moduleOfSymbol returns the module id portion of a symbol id.
//
// Pre-issue #61 this was a "first-dot-prefix" function — `sample.helper`
// → `sample`. That worked for flat fixtures but broke on nested
// packages like `src.click.core.echo` where the enclosing module is
// `src.click.core`, not `src`. The resolver now needs the longest
// registered module prefix.
//
// (*pyEdgeResolver).enclosingModule is the per-instance method that
// performs that longest-prefix lookup against r.byModule + the package
// __init__ set. moduleOfSymbol stays as the pure-string fallback used
// by tests + the top-level boundNameAndQualifiedImport helper when no
// resolver is in scope.
//
// Returns the everything-before-the-last-dot when the id contains a
// dot; otherwise the id itself.
func moduleOfSymbol(id string) string {
	if id == "" {
		return ""
	}
	if i := strings.LastIndexByte(id, '.'); i > 0 {
		return id[:i]
	}
	return id
}

// enclosingModule returns the longest module id (from the resolver's
// known module set) that is a dotted prefix of symbolID. This handles
// nested packages cleanly: `src.click.core.echo` → `src.click.core`
// when that module exists, falling back to `src.click` if not, etc.
//
// The lookup walks from longest to shortest dotted prefix, returning
// the first one that appears in either byModule (a module with
// emitted children) or packageInits (an `__init__.py`). Returns "" if
// no enclosing module is found — only happens for malformed input or
// freshly-emitted symbols whose module entry hasn't been built yet.
func (r *pyEdgeResolver) enclosingModule(symbolID string) string {
	for s := symbolID; s != ""; {
		i := strings.LastIndexByte(s, '.')
		if i <= 0 {
			break
		}
		s = s[:i]
		if _, ok := r.byModule[s]; ok {
			return s
		}
		if _, ok := r.packageInits[s]; ok {
			return s
		}
	}
	return ""
}

// splitModuleAndLocal splits a scanner.py emitted symbol id into its
// module head and the locally-scoped remainder.
//
// For class methods (`pkg.sub.MyClass.run`) the "module" is everything
// up to but not including the class name (`pkg.sub`) and the local
// part is `MyClass.run`. We figure out where the module ends by
// looking up successively shorter prefixes against the known module-id
// set; the longest matching prefix wins.
func splitModuleAndLocal(id, kind string) (string, string) {
	if kind == "module" {
		return id, ""
	}
	if !strings.ContainsRune(id, '.') {
		return "", ""
	}
	// Symbol ids are always rooted at a module id. The module id is
	// the longest dotted prefix that itself appears as a module node;
	// at construction time we don't have the full set built yet, so
	// we approximate by taking everything before the LAST dot
	// component that starts with an uppercase letter (a class) — and
	// otherwise everything before the last dot.
	//
	// A simpler heuristic that holds for scanner.py output: classes
	// are CapWords by convention, methods/funcs/consts are not. So
	// `pkg.sub.MyClass.run` splits into module `pkg.sub`, local
	// `MyClass.run`; `pkg.sub.helper` splits into module `pkg.sub`,
	// local `helper`.
	parts := strings.Split(id, ".")
	// Find the index of the first CapWord segment (class). If found,
	// everything before it is the module; from that segment forward
	// is the local part. If no CapWord, the last segment is the
	// local part.
	classIdx := -1
	for i, p := range parts {
		if isCapWord(p) {
			classIdx = i
			break
		}
	}
	if classIdx > 0 {
		return strings.Join(parts[:classIdx], "."), strings.Join(parts[classIdx:], ".")
	}
	// No class in the chain — local part is the last segment.
	return strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1]
}

// isCapWord reports whether s starts with an uppercase ASCII letter —
// the conservative-but-sufficient CapWords detector for Python class
// names emitted by scanner.py. Non-ASCII identifiers are legal Python
// but vanishingly rare in published libraries.
func isCapWord(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= 'A' && c <= 'Z'
}

// splitHeadTail splits "foo.bar.baz" into ("foo", "bar.baz") for
// dotted-access targets. A bare "foo" returns ("foo", "").
func splitHeadTail(target string) (string, string) {
	if i := strings.IndexByte(target, '.'); i > 0 {
		return target[:i], target[i+1:]
	}
	return target, ""
}

// parentPackage returns the package id that contains moduleID. For
// `src.click.core` (a leaf module) it returns `src.click`. For
// `src.click` (an `__init__.py`) it ALSO returns `src.click` because
// the package's own re-exports are visible to its own modules — the
// `__init__` IS the package, and lookups under that key should find
// re-exports the package itself defines.
//
// When the resolver can't identify a parent package (top-level
// module at the scan root), the empty string is returned.
func parentPackage(moduleID string, packageInits map[string]struct{}) string {
	if _, isInit := packageInits[moduleID]; isInit {
		// An __init__ module's "package" for re-export lookups is itself.
		// E.g. `src.click` (the __init__) re-exports symbols that
		// `src.click.core` can lookup via its parentPackage = `src.click`.
		return moduleID
	}
	if i := strings.LastIndexByte(moduleID, '.'); i > 0 {
		return moduleID[:i]
	}
	return ""
}

// boundNameAndQualifiedImport translates a scanner.py-rendered import
// edge target into (boundName, qualifiedID) where qualifiedID is the
// Atlas symbol id the bound name resolves to, with relative-import
// dots resolved against the caller's package.
//
// Examples (caller = `src.click.core`, parent package = `src.click`):
//
//	`os`                        → ("os",       "os")
//	`collections.OrderedDict`   → ("OrderedDict", "collections.OrderedDict")
//	`.termui.style`             → ("style",   "src.click.termui.style")
//	`.sibling`                  → ("sibling", "src.click.sibling")
//	`..parent_mod.helper`       → ("helper",  "src.parent_mod.helper")  (level=2)
//
// The boundName is the FINAL dotted segment of the rendered target
// (matches `from X import Y` semantics where Y is the name bound in
// the importer's scope). For bare `import X` the bound name IS X.
func boundNameAndQualifiedImport(callerModule, rendered string, packageInits map[string]struct{}) (string, string) {
	if rendered == "" {
		return "", ""
	}
	// Count leading dots — scanner.py emits one dot per `level`.
	dots := 0
	for dots < len(rendered) && rendered[dots] == '.' {
		dots++
	}
	tail := rendered[dots:]

	var qualified string
	if dots == 0 {
		qualified = tail
	} else {
		// Relative import — pop `dots-1` levels off the caller's
		// package (level=1 means "current package", level=2 means
		// "parent package", etc.). We compute the caller's package
		// first so a leaf module's `from .sibling` resolves to
		// "package.sibling", not "leaf_module.sibling".
		pkg := parentPackage(callerModule, packageInits)
		for i := 1; i < dots && pkg != ""; i++ {
			if j := strings.LastIndexByte(pkg, '.'); j > 0 {
				pkg = pkg[:j]
			} else {
				pkg = ""
				break
			}
		}
		if tail == "" {
			qualified = pkg
		} else if pkg == "" {
			qualified = tail
		} else {
			qualified = pkg + "." + tail
		}
	}

	bound := qualified
	if i := strings.LastIndexByte(bound, '.'); i > 0 {
		bound = bound[i+1:]
	}
	if bound == "" {
		return "", ""
	}
	return bound, qualified
}
