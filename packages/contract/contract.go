package contract

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sosalejandro/atlas/packages/codeindex"
	"github.com/sosalejandro/atlas/packages/shared"
)

// ContractKind narrows a ContractDef to one of the supported contract
// surfaces. It is a closed enum — adding a kind is non-breaking but every
// downstream consumer should be updated together with the parser.
type ContractKind string

const (
	// KindFunc is a Go OR TypeScript function/method signature, with no
	// HTTP / GraphQL / Huma envelope around it. The signature alone is the
	// contract.
	KindFunc ContractKind = "func"

	// KindRoute is an HTTP route discovered from a Chi/Echo/stdlib router
	// registration. Signature holds the "METHOD /path" canonical form;
	// Operation.Handler points at the handler func's SymbolID when known.
	KindRoute ContractKind = "route"

	// KindHumaOp is a Huma operation declared via
	// huma.Register(api, huma.Operation{...}, handler). The Operation
	// struct fields (Method, Path, OperationID, Summary, Tags) are
	// captured in Operation.
	KindHumaOp ContractKind = "huma-op"

	// KindGraphQL is a GraphQL Mutation/Query/Subscription operation
	// declared in a `.graphql` / `.graphqls` schema file.
	KindGraphQL ContractKind = "graphql"
)

// Language is the source language a ContractDef was extracted from. Useful
// for downstream filtering (e.g. "all backend Go contracts") without having
// to re-derive from FilePath.
type Language string

const (
	LangGo      Language = "go"
	LangTS      Language = "ts"
	LangGraphQL Language = "graphql"
)

// ContractDef is the structured contract record this package emits.
//
// One ContractDef per discovered contract; the source symbols that
// participate (handler func, helper types) are recorded in Symbols so
// the persistence layer can write `feature_symbols` rows alongside the
// `features` row.
//
// FeatureID is non-nil ONLY when the parser found an unambiguous
// @atlas:contract / @atlas:feature / @testreg annotation on or
// immediately above the contract's primary declaration.
type ContractDef struct {
	// Name is the human-readable identifier — function name for
	// KindFunc, OperationID (or "METHOD /path" if no OperationID) for
	// KindHumaOp / KindRoute, "Mutation.foo" / "Query.bar" for
	// KindGraphQL.
	Name string `json:"name"`

	// Kind narrows the shape of Operation + Signature.
	Kind ContractKind `json:"kind"`

	// Language tags the source language. Mostly useful for KindFunc,
	// which can be either Go or TS.
	Language Language `json:"language,omitempty"`

	// Signature is the canonical textual signature. For Go funcs:
	// "func (r *AuthHandler) Login(ctx context.Context) error".
	// For routes: "METHOD /path". For GraphQL: a single-line
	// "operation name(args): returnType" form.
	Signature string `json:"signature"`

	// FilePath is repo-relative (forward-slash) per shared.FilePosition
	// rules. For HTTP routes it points at the file containing the
	// router registration, NOT necessarily the handler file.
	FilePath string `json:"file_path"`

	// Line is 1-based. 0 means "unknown" (rare; usually only for
	// GraphQL operations where the legacy regex parser didn't track
	// line numbers).
	Line int `json:"line,omitempty"`

	// FeatureID, when non-nil, is the @atlas:contract / @atlas:feature
	// / @testreg ID associated with this contract. nil means "no
	// annotation found, contract was discovered purely structurally".
	FeatureID *shared.FeatureID `json:"feature_id,omitempty"`

	// Symbols lists the SymbolIDs that participate in this contract —
	// typically the handler func first, plus any helper types
	// (request/response structs) the extractor was able to associate.
	// Persistence writes one feature_symbols row per entry.
	Symbols []shared.SymbolID `json:"symbols,omitempty"`

	// Operation carries surface-specific fields (HTTP method/path,
	// Huma operation ID, GraphQL type/return). Zero value for KindFunc.
	Operation OperationDetail `json:"operation,omitempty"`

	// Source records which extractor produced the contract — useful
	// when several extractors discover the same handler from different
	// angles (e.g. a Huma handler is ALSO a Go func with a doc string).
	// The "canonical" ContractDef is the highest-fidelity one
	// (huma-op > route > func); the merge pass in Extract handles that.
	Source string `json:"source,omitempty"`
}

// OperationDetail is the contract-surface-specific payload.
//
// Only the fields relevant to a given Kind are populated. JSON omits
// empty values so the on-disk envelope stays compact.
type OperationDetail struct {
	// HTTP fields (KindRoute, KindHumaOp).
	Method      string   `json:"method,omitempty"`
	Path        string   `json:"path,omitempty"`
	Handler     string   `json:"handler,omitempty"`      // e.g. "h.authHandler.Login"
	HandlerSym  string   `json:"handler_sym,omitempty"`  // resolved SymbolID
	OperationID string   `json:"operation_id,omitempty"` // huma's stable id
	Summary     string   `json:"summary,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	// GraphQL fields.
	GraphQLType string `json:"graphql_type,omitempty"` // "Mutation", "Query", "Subscription"
	ReturnType  string `json:"return_type,omitempty"`
}

// Options configures Extract. Zero value is safe.
type Options struct {
	// Logger receives extractor-level warnings (route parse errors,
	// missing handlers). Defaults to shared.NopLogger.
	Logger shared.Logger

	// SkipGraphQL disables the GraphQL pass even when .graphqls files
	// exist under the project root. Useful in pure-backend audits.
	SkipGraphQL bool

	// SkipTS disables the TypeScript func pass. The Go / route / Huma
	// passes still run.
	SkipTS bool

	// ProjectRoot is the directory the codeindex was built against.
	// Required — extractors need it to resolve repo-relative file
	// paths back to absolute paths when re-parsing for richer
	// information (e.g. Huma operation struct literals).
	ProjectRoot string

	// AnnotationProximity caps how far above a declaration the parser
	// will look for an annotation. Defaults to 10 lines (matches the
	// codeindex/go scanner's @api rule).
	AnnotationProximity int
}

// Result is the output of Extract.
type Result struct {
	Defs     []ContractDef `json:"defs"`
	Warnings []string      `json:"warnings,omitempty"`
}

// Extractor is the top-level entry point.
//
// One Extractor per project root is the contract. Extract may be called
// multiple times against the same Extractor — it holds no per-call
// state. Per the package doc, the Extractor takes a codeindex.Index plus
// the original project root (the codeindex doesn't preserve absolute
// paths) and returns a slice of ContractDef ready to persist.
//
// Persistence is intentionally a separate step (see Persist) so callers
// that just want the extracted contracts (a diff verb, a JSON report)
// can avoid touching SQLite.
type Extractor struct {
	opts Options
}

// NewExtractor constructs an Extractor. opts.ProjectRoot must be set —
// the extractor needs it to re-read source files for the richer parses
// (Huma operations, GraphQL schemas) that codeindex.Index doesn't carry
// in structured form.
func NewExtractor(opts Options) *Extractor {
	if opts.Logger == nil {
		opts.Logger = shared.NopLogger{}
	}
	if opts.AnnotationProximity <= 0 {
		opts.AnnotationProximity = 10
	}
	return &Extractor{opts: opts}
}

// Extract is the main entry point. It runs every supported pass against
// idx + opts.ProjectRoot and returns a merged Result.
//
// The passes run in this order so the merge step (which prefers the
// richest record on conflict) sees the lower-fidelity records FIRST and
// can be overwritten by the more specific extractors that follow:
//
//  1. Go funcs       (every codeindex Go symbol with a signature)
//  2. TS funcs       (every codeindex TS symbol that looks like a func)
//  3. HTTP routes    (Chi/Echo/stdlib via re-parse)
//  4. Huma operations (huma.Register sites via re-parse)
//  5. GraphQL ops    (.graphqls walker)
//
// Errors from a single pass do NOT abort the whole extraction — they
// surface as warnings on the returned Result. The only hard error is a
// nil idx or missing ProjectRoot.
func (e *Extractor) Extract(ctx context.Context, idx *codeindex.Index) (*Result, error) {
	if idx == nil {
		return nil, errors.New("contract extract: nil index")
	}
	if e.opts.ProjectRoot == "" {
		return nil, errors.New("contract extract: ProjectRoot is required")
	}

	res := &Result{}

	// Build an annotation lookup keyed by (file, line) for fast pairing.
	annIdx := newAnnotationIndex(idx.Annotations, e.opts.AnnotationProximity)

	// 1. Go funcs.
	goDefs, goWarns := e.extractGoFuncs(idx, annIdx)
	res.Defs = append(res.Defs, goDefs...)
	res.Warnings = append(res.Warnings, goWarns...)

	// 2. TS funcs.
	if !e.opts.SkipTS {
		tsDefs, tsWarns := e.extractTSFuncs(idx, annIdx)
		res.Defs = append(res.Defs, tsDefs...)
		res.Warnings = append(res.Warnings, tsWarns...)
	}

	// 3. HTTP routes (re-parse Go files we already discovered).
	routeDefs, routeWarns := e.extractRoutes(ctx, idx, annIdx)
	res.Defs = append(res.Defs, routeDefs...)
	res.Warnings = append(res.Warnings, routeWarns...)

	// 4. Huma operations.
	humaDefs, humaWarns := e.extractHuma(ctx, idx, annIdx)
	res.Defs = append(res.Defs, humaDefs...)
	res.Warnings = append(res.Warnings, humaWarns...)

	// 5. GraphQL.
	if !e.opts.SkipGraphQL {
		gqlDefs, gqlWarns := e.extractGraphQL(ctx)
		res.Defs = append(res.Defs, gqlDefs...)
		res.Warnings = append(res.Warnings, gqlWarns...)
	}

	// Merge pass: for the same handler symbol, prefer huma-op > route > func.
	res.Defs = mergeDefs(res.Defs)

	// Stable order for tests + golden output: sort by (Kind, FilePath, Line, Name).
	sort.SliceStable(res.Defs, func(i, j int) bool {
		a, b := res.Defs[i], res.Defs[j]
		if a.Kind != b.Kind {
			return kindOrder(a.Kind) < kindOrder(b.Kind)
		}
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Name < b.Name
	})
	return res, nil
}

// kindOrder gives a stable sort key per ContractKind so golden tests don't
// flap on map-iteration order changes.
func kindOrder(k ContractKind) int {
	switch k {
	case KindHumaOp:
		return 0
	case KindRoute:
		return 1
	case KindGraphQL:
		return 2
	case KindFunc:
		return 3
	default:
		return 99
	}
}

// mergeDefs collapses overlapping records. When a handler appears as
// both a plain Go func AND a Huma operation / route, the higher-fidelity
// record wins and the func record is suppressed.
//
// Rule: if a route or huma-op has Operation.HandlerSym pointing at a
// symbol that ALSO has a KindFunc entry, the KindFunc entry is dropped.
// The route / huma-op record keeps the func's Signature in its own
// Signature field as a fallback so callers don't lose that info.
func mergeDefs(defs []ContractDef) []ContractDef {
	// Step 1: collect every handler symbol referenced by a higher-fidelity
	// record (route or huma-op).
	covered := make(map[shared.SymbolID]bool)
	for _, d := range defs {
		if d.Kind != KindRoute && d.Kind != KindHumaOp {
			continue
		}
		if d.Operation.HandlerSym != "" {
			covered[shared.SymbolID(d.Operation.HandlerSym)] = true
		}
		for _, s := range d.Symbols {
			covered[s] = true
		}
	}

	// Step 2: filter — keep all non-func defs; for KindFunc, drop if its
	// primary symbol is already covered.
	out := defs[:0]
	for _, d := range defs {
		if d.Kind == KindFunc && len(d.Symbols) > 0 && covered[d.Symbols[0]] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// annotationIndex is the in-memory data structure used to associate an
// annotation with the next-following declaration. Keyed by file path,
// values are sorted by line ascending.
//
// The byFile map holds the raw annotation list per source file. The
// owners map is built lazily on the first findFor call against a file:
// it pairs each annotation to the closest following declaration line
// passed in via knownDeclLines. Once built, lookups are O(1) per
// declaration.
//
// Why the lazy pairing instead of an eager one-shot: each extractor
// (go_funcs, http_routes, huma) discovers declaration lines for a
// different set of file types, and we don't want to force them into a
// shared "register decl line" precondition. Lazy means each extractor
// just calls findFor with the declaration line it found.
type annotationIndex struct {
	byFile    map[string][]shared.Annotation
	proximity int

	// declLines records every declaration line the extractors have
	// asked about so far, per file. Used to compute the "no other
	// declaration between annotation and target" constraint.
	declLines map[string]map[int]bool
}

func newAnnotationIndex(anns []shared.Annotation, proximity int) *annotationIndex {
	if proximity <= 0 {
		proximity = 10
	}
	idx := &annotationIndex{
		byFile:    make(map[string][]shared.Annotation),
		proximity: proximity,
		declLines: make(map[string]map[int]bool),
	}
	for _, a := range anns {
		idx.byFile[a.Position.Path] = append(idx.byFile[a.Position.Path], a)
	}
	for _, list := range idx.byFile {
		sort.SliceStable(list, func(i, j int) bool {
			return list[i].Position.Line < list[j].Position.Line
		})
	}
	return idx
}

// registerDeclLines tells the index about every known declaration line in
// filePath. Used by findFor's "annotation owns the nearest following
// declaration" rule.
//
// Idempotent — duplicate calls with the same lines are a no-op.
func (a *annotationIndex) registerDeclLines(filePath string, lines []int) {
	set, ok := a.declLines[filePath]
	if !ok {
		set = make(map[int]bool, len(lines))
		a.declLines[filePath] = set
	}
	for _, ln := range lines {
		set[ln] = true
	}
}

// findFor returns the feature-ID for the given (file, line) declaration
// position. Returns "" if no annotation lies within the proximity window
// above the declaration, OR if a closer declaration lies between the
// annotation and this one (in which case the annotation belongs to that
// closer declaration, not this one).
//
// Pairing rule: an annotation attaches to the FIRST declaration that
// follows it within `proximity` lines. The same annotation never attaches
// to two declarations. This mirrors the codeindex/go scanner's @api
// semantics + the standard godoc-comment association rule.
//
// Tie-break within a single annotation slot:
// @atlas:contract > @atlas:feature > @testreg.
func (a *annotationIndex) findFor(filePath string, declLine int) (shared.FeatureID, bool) {
	list := a.byFile[filePath]
	if len(list) == 0 {
		return "", false
	}
	// Decl-aware path: if the caller registered the file's declaration
	// lines, we can apply the strict "annotation owns nearest following
	// decl" rule. Otherwise fall back to the loose "closest within
	// proximity" rule — sub-optimal but useful when callers haven't
	// pre-registered (e.g. tests with hand-crafted indices).
	declSet := a.declLines[filePath]

	bestLine := -1
	var bestKind shared.AnnotationKind
	var bestSource shared.AnnotationSource
	var bestID string
	for _, ann := range list {
		gap := declLine - ann.Position.Line
		if gap <= 0 || gap > a.proximity {
			continue
		}
		if len(ann.IDs) == 0 {
			continue
		}
		// Strict mode: skip annotations whose nearest-following decl is
		// not this one.
		if declSet != nil {
			nearest := nearestDeclAfter(declSet, ann.Position.Line)
			if nearest != declLine {
				continue
			}
		}
		better := false
		switch {
		case ann.Position.Line > bestLine:
			better = true
		case ann.Position.Line == bestLine:
			better = annKindRank(ann) > annKindRank(shared.Annotation{Kind: bestKind, Source: bestSource})
		}
		if better {
			bestLine = ann.Position.Line
			bestKind = ann.Kind
			bestSource = ann.Source
			bestID = ann.IDs[0]
		}
	}
	if bestLine < 0 {
		return "", false
	}
	return shared.FeatureID(bestID), true
}

// nearestDeclAfter returns the smallest decl line in set that is strictly
// greater than annLine. Returns -1 if none.
func nearestDeclAfter(set map[int]bool, annLine int) int {
	nearest := -1
	for ln := range set {
		if ln <= annLine {
			continue
		}
		if nearest == -1 || ln < nearest {
			nearest = ln
		}
	}
	return nearest
}

// annKindRank scores an annotation by its kind+source for tie-breaking.
//
//	@atlas:contract → 3 (highest)
//	@atlas:feature  → 2
//	@testreg        → 1
//	other           → 0
func annKindRank(a shared.Annotation) int {
	if a.Kind == shared.AnnContract {
		return 3
	}
	if a.Kind == shared.AnnFeature {
		if a.Source == shared.SourceTestreg {
			return 1
		}
		return 2
	}
	return 0
}

// normaliseRelPath converts any path the extractor might encounter into
// the canonical forward-slash repo-relative form codeindex.IndexProject
// produces — i.e. relative to the *absolute* projectRoot.
//
// The codeindex orchestrator does `filepath.Abs(rootDir)` once and bases
// every shared.FilePosition.Path on that absolute root. To keep our
// annotation index keys aligned with codeindex's, this helper applies
// the same logic regardless of whether the caller supplied a relative
// or absolute projectRoot.
//
// Behaviour:
//   - p absolute and under absRoot      → trimmed to relative
//   - p relative and under projectRoot  → trimmed to abs-relative
//   - everything else                   → forward-slashed verbatim
func normaliseRelPath(projectRoot, p string) string {
	if p == "" {
		return ""
	}
	pSlash := filepath.ToSlash(p)
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return pSlash
	}
	absRoot = filepath.ToSlash(absRoot)

	absP := pSlash
	if !filepath.IsAbs(pSlash) {
		// Resolve relative-to-CWD then compare against absRoot.
		ap, err := filepath.Abs(p)
		if err == nil {
			absP = filepath.ToSlash(ap)
		}
	}
	if strings.HasPrefix(absP, absRoot+"/") {
		return strings.TrimPrefix(absP, absRoot+"/")
	}
	if absP == absRoot {
		return ""
	}
	return pSlash
}

