package shared

// SymbolID is the stable identifier Atlas uses to reference a code symbol.
//
// Convention (Go): "ReceiverType.MethodName" for methods, "pkgName.FuncName"
// for plain functions. Convention (TS): "<file-path>::<exported-name>".
// Other languages mirror this shape — the ID must be unique within a single
// scanned project.
//
// The legacy testreg `domain.Node.ID` (string) ports 1:1 to SymbolID — the
// in-memory graph keys verbatim from the same conventions.
type SymbolID string

// FeatureID is the stable identifier for a logical feature, attached to
// symbols via @atlas:feature (or legacy @testreg) annotations.
//
// Canonical form: dot-namespaced lowercase, validated against
// `[a-z0-9_]+(\.[a-z0-9_]+)*`. Examples: "auth.login", "pantry.add-item".
type FeatureID string

// SymbolKind classifies a Symbol so audit/sprintplan can weight by layer.
//
// The set below is the closed enum recognised by Atlas v1. It supersedes
// testreg's `domain.NodeKind` and extends it with codeindex-level kinds the
// SQLite schema also stores (type, var, const, interface).
type SymbolKind string

const (
	// Legacy testreg kinds (preserved verbatim for back-compat with the
	// audit/ layer-weighting algorithm).
	KindHandler    SymbolKind = "handler"
	KindService    SymbolKind = "service"
	KindRepository SymbolKind = "repository"
	KindQuery      SymbolKind = "query"
	KindComponent  SymbolKind = "component"
	KindHook       SymbolKind = "hook"
	KindEndpoint   SymbolKind = "endpoint"
	KindExternal   SymbolKind = "external"

	// Additional kinds the SQLite schema stores (see docs/schema-v1.md
	// §5.4 — `kind IN ('type','func','method','interface','var','const')`).
	// These are emitted by codeindex/ scanners alongside the legacy kinds;
	// audit/ is free to ignore them when layer-weighting.
	KindType      SymbolKind = "type"
	KindFunc      SymbolKind = "func"
	KindMethod    SymbolKind = "method"
	KindInterface SymbolKind = "interface"
	KindVar       SymbolKind = "var"
	KindConst     SymbolKind = "const"

	// Test classification (used by codeindex/annotations and coverage/).
	KindTest SymbolKind = "test"
)

// FilePosition is the only way Atlas refers to a source location.
//
//   - Path is **repo-relative** (forward-slash, not absolute) so persisted
//     state is portable across worktrees and machines.
//   - Line is 1-based.
//   - Col is 1-based, or 0 when "unknown" — most parsers only report line.
//
// FilePosition is the canonical anchor on Symbol, Annotation, and Edge
// records throughout the codebase. Never use absolute paths or file:// URIs.
type FilePosition struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
}

// Symbol is the value-type representation of a single named code entity.
//
// This mirrors the legacy testreg `domain.Node` shape one-to-one but lives in
// the kernel so codeindex/, audit/, store/, and contract/ can all share the
// type without circular imports.
//
// JSON tags follow the docs/api/<verb>.md contract conventions: lowerCamel
// in v1; additive-only changes within a major.
type Symbol struct {
	ID        SymbolID     `json:"id"`
	Kind      SymbolKind   `json:"kind"`
	Position  FilePosition `json:"position"`
	Doc       string       `json:"doc,omitempty"`
	Signature string       `json:"signature,omitempty"`
	Package   string       `json:"package,omitempty"`
}

// AnnotationKind is the closed enum of @atlas:<kind> values the parser
// recognises. Adding a new kind is non-breaking (see docs/annotations.md
// §Forward compatibility) — older Atlas versions emit a one-time advisory
// warning and skip the annotation.
type AnnotationKind string

const (
	AnnFeature    AnnotationKind = "feature"
	AnnContract   AnnotationKind = "contract"
	AnnOwner      AnnotationKind = "owner"
	AnnDeprecated AnnotationKind = "deprecated"
	AnnSince      AnnotationKind = "since"
	// AnnAPI is the discovery-time `@api METHOD /path` annotation used by
	// the Go AST scanner to associate handler functions with HTTP endpoints
	// without a dedicated route parser. Not part of the @atlas:<kind>
	// grammar, but produced by the same parser so consumers can treat all
	// annotations uniformly.
	AnnAPI AnnotationKind = "api"

	// EDA-pattern kinds (Phase 6e). These elevate Atlas's understanding of
	// event-driven architectures: bounded contexts, aggregates, sagas,
	// stream consumers, and the emit/publish split. They are pure
	// annotation grammar extensions — Atlas does NOT autodetect these
	// patterns from source (that is Horizon 2).
	//
	// ID rules:
	//   - bc, consumer       — bare name; validated against `[a-z0-9_]+(\.[a-z0-9_]+)*`
	//   - aggregate          — dot-namespaced (e.g. `meal-prep.batch-session`)
	//   - aggregate-service  — dot-namespaced; references an aggregate id
	//   - saga               — dot-namespaced; `step=N` tag carries order
	//   - event-emit         — event name (regex-validated)
	//   - outbox-publish     — event name (regex-validated)
	AnnBC               AnnotationKind = "bc"
	AnnAggregate        AnnotationKind = "aggregate"
	AnnAggregateService AnnotationKind = "aggregate-service"
	AnnSaga             AnnotationKind = "saga"
	AnnConsumer         AnnotationKind = "consumer"
	AnnEventEmit        AnnotationKind = "event-emit"
	AnnOutboxPublish    AnnotationKind = "outbox-publish"
)

// AnnotationSource records which grammar produced the annotation.
//
//   - "atlas"  — new-style `@atlas:<kind> <id>`
//   - "testreg" — legacy `@testreg <id>` (still recognised; see
//     docs/annotations.md §Legacy reader)
//   - "api"    — `@api METHOD /path` (orthogonal to the @atlas grammar)
type AnnotationSource string

const (
	SourceAtlas   AnnotationSource = "atlas"
	SourceTestreg AnnotationSource = "testreg"
	SourceAPI     AnnotationSource = "api"
)

// Annotation is the raw record produced by the annotations parser. It is
// the input shape for the resolution pass that maps annotations to symbols
// (see docs/schema-v1.md §5.11 `annotations` table).
//
// One Annotation per @atlas:<kind> directive — multi-id annotations like
// `@atlas:feature checkout.cart checkout.shipping` produce ONE annotation
// with two IDs (and the resolver emits one feature_symbols row per ID).
type Annotation struct {
	Kind     AnnotationKind   `json:"kind"`
	IDs      []string         `json:"ids,omitempty"`
	Tags     []string         `json:"tags,omitempty"`
	Source   AnnotationSource `json:"source"`
	Position FilePosition     `json:"position"`
	// Raw is the original payload after the kind keyword, preserved for
	// migration tooling and round-trip rewriting. e.g. for
	// "// @atlas:feature auth.login #real" → Raw = "auth.login #real".
	Raw string `json:"raw,omitempty"`
	// Method + Path are set ONLY when Kind == AnnAPI. They carry the HTTP
	// method + path so the resolver can associate the annotation with a
	// handler function.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
}
